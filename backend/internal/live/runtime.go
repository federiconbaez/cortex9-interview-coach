package live

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/analysis"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/coach"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/endpoint"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/engine"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/llm"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/protocol"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/speech"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/store"
)

const (
	bytesPerSec = 32000 // PCM16LE 16 kHz mono
	ackEvery    = 250 * time.Millisecond
	ackBytes    = 32 * 1024
)

// Runtime es la sesión en vivo. Todo su estado lo toca solo loop(); el resto
// de las goroutines le mandan closures por events.
type Runtime struct {
	hub  *Hub
	svc  *coach.Service
	opt  Options
	id   string
	sess *store.Session
	cfg  *coach.SessionConfig
	eng  *engine.Engine
	prov llm.Provider

	events chan func()
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	quit   bool

	conn      *Conn
	serverSeq atomic.Uint32
	ring      []outMsg

	lastClient  uint32
	ackSent     uint32
	ackBytes    int
	ackTimer    *time.Timer
	gapReported uint32
	invalid     int

	audioFmt    protocol.AudioFormat
	ttsDelivery string
	ttsFormat   string
	ttsURL      bool // el cliente acepta url (para cuando la cola está cargada)
	partials    bool
	started     bool
	detachedAt  time.Time

	audioTurn   int
	buf         []byte
	nextChunk   uint32
	tooLongTurn int
	conflictFor int

	partialTimer    *time.Timer
	partialInflight bool
	partialRev      int
	partialAt       int

	listenTimer *time.Timer
	lastPlayed  int // último utterance con tts.played (duplicados y viejos se ignoran)

	// endpointing por silencio (endpointing.go)
	epDisabled     bool
	endpointedTurn int
	ep             *endpoint.Detector
	epCand         *endpoint.Candidate
	epTimer        *time.Timer
	epGen          int
	marks          []chunkMark
	lastPartial    partialSnap
	spec           *specState
	voiceEndAt     time.Time
	latMode        string
	latUtt         int
	sttStart       time.Time
	thinkGen       int
	pauseGen       int
	graceGen       int
	persisted      int
	closeCode      int

	learnWG sync.WaitGroup
	learnMu sync.Mutex
	learned []string
	llmErrs []string
}

func newRuntime(h *Hub, sess *store.Session, cfg *coach.SessionConfig) *Runtime {
	ctx, cancel := context.WithCancel(context.Background())
	prov, _ := h.svc.Provider(sess)
	rt := &Runtime{hub: h, svc: h.svc, opt: h.opt, id: sess.ID, sess: sess, cfg: cfg, eng: engine.New(engineConfig(sess, cfg)),
		prov: prov, events: make(chan func(), 256), done: make(chan struct{}), ctx: ctx, cancel: cancel,
		closeCode: protocol.CloseNormal, llmErrs: append([]string(nil), sess.LLMErrors...)}
	return rt
}

func (rt *Runtime) loop() {
	defer close(rt.done)
	defer rt.cancel()
	for f := range rt.events {
		f()
		if rt.quit {
			return
		}
	}
}

// post encola trabajo para la goroutine del runtime (descarta si ya terminó).
func (rt *Runtime) post(f func()) {
	select {
	case rt.events <- f:
	case <-rt.done:
	}
}

// ---- salida ----

func (rt *Runtime) nextSeq() uint32 { return rt.serverSeq.Add(1) }

func (rt *Runtime) remember(m outMsg) {
	rt.ring = append(rt.ring, m)
	cut := 0
	now := time.Now()
	for cut < len(rt.ring) && (len(rt.ring)-cut > rt.opt.ReplayMax || now.Sub(rt.ring[cut].at) > rt.opt.ReplayAge) {
		cut++
	}
	if cut > 0 {
		rt.ring = append([]outMsg(nil), rt.ring[cut:]...)
	}
}

func (rt *Runtime) deliver(m outMsg) {
	if rt.conn == nil {
		return
	}
	if !rt.conn.enqueue(m) {
		// la cola se llenó con mensajes que no se descartan: cierre 4008 y el cliente reconecta con resume
		rt.svc.M.WSBackpressure.WithLabelValues("close").Inc()
		rt.conn.closeNow(protocol.CloseBackpressure, "cola de salida llena")
	}
}

// send manda un mensaje JSON (consume seq y queda en el buffer de reenvío).
func (rt *Runtime) send(typ string, data any) {
	seq := rt.nextSeq()
	m := outMsg{seq: seq, typ: typ, data: envelope(typ, seq, rt.id, rt.lastClient, data), at: time.Now()}
	rt.ackSent, rt.ackBytes = rt.lastClient, 0
	rt.remember(m)
	rt.svc.M.WSMessages.WithLabelValues("out", typ).Inc()
	rt.deliver(m)
}

// sendAck manda un ack liviano: seq 0 (fuera de la secuencia, no se reenvía).
func (rt *Runtime) sendAck() {
	rt.ackSent, rt.ackBytes = rt.lastClient, 0
	if rt.conn == nil {
		return
	}
	rt.conn.enqueue(outMsg{seq: 0, typ: protocol.TAck, data: envelope(protocol.TAck, 0, rt.id, rt.lastClient,
		protocol.Ack{LastClientSeq: rt.lastClient}), at: time.Now()})
	rt.svc.M.WSMessages.WithLabelValues("out", protocol.TAck).Inc()
}

// ackTick: cada 250 ms mientras se escucha, si hay seq nuevos sin confirmar.
func (rt *Runtime) armAck() {
	if rt.ackTimer != nil {
		return
	}
	rt.ackTimer = time.AfterFunc(ackEvery, func() {
		rt.post(func() {
			rt.ackTimer = nil
			if rt.quit {
				return
			}
			if rt.lastClient > rt.ackSent {
				rt.sendAck()
			}
			if rt.eng.State() == engine.Listening {
				rt.armAck()
			}
		})
	})
}

func (rt *Runtime) sendErr(code, msg string, retry bool, ref *uint32) {
	rt.send(protocol.TError, errData(code, msg, false, retry, ref, 0))
}

// fatal manda un error fatal a una conexión y la cierra. Consume seq pero no
// entra al buffer de reenvío (no se re-envía a la conexión nueva).
func (rt *Runtime) fatal(c *Conn, code, msg string, retry bool, closeCode int) {
	if c == nil {
		return
	}
	seq := rt.nextSeq()
	c.enqueue(outMsg{seq: seq, typ: protocol.TError, data: envelope(protocol.TError, seq, rt.id, rt.lastClient,
		errData(code, msg, true, retry, nil, closeCode)), at: time.Now()})
	c.closeAfterFlush(closeCode, code)
	rt.svc.M.WSMessages.WithLabelValues("out", protocol.TError).Inc()
}

func (rt *Runtime) sendFrame(utt, idx int, last bool, codec byte, payload []byte) {
	seq := rt.nextSeq()
	b, _ := protocol.EncodeFrame(protocol.FrameHeader{Kind: protocol.KindTTSAudio, Seq: seq, TurnNo: uint32(utt),
		ChunkIndex: uint32(idx), Codec: codec, Last: last}, payload)
	m := outMsg{seq: seq, typ: "tts.frame", binary: true, data: b, at: time.Now()}
	rt.remember(m)
	rt.deliver(m)
}

// ---- conexión ----

type helloMsg struct {
	env   protocol.Envelope
	hello protocol.Hello
}

func (rt *Runtime) negotiate(h protocol.Hello) bool {
	ok := false
	for _, f := range h.AudioFormats {
		if f.Codec == "pcm16le" && f.SampleRate == 16000 && f.Channels == 1 {
			ok = true
			rt.audioFmt = protocol.AudioFormat{Codec: "pcm16le", SampleRate: 16000, Channels: 1, FrameMs: f.FrameMs}
			if rt.audioFmt.FrameMs == 0 {
				rt.audioFmt.FrameMs = 100
			}
		}
	}
	// opus no se elige en v1 (ver backend/docs/decisiones.md)
	rt.ttsDelivery, rt.ttsURL = "", false
	for _, d := range h.TTSDelivery {
		if d == "url" {
			rt.ttsURL = true
		}
		if rt.ttsDelivery == "" && (d == "binary" || d == "url" || d == "none") {
			rt.ttsDelivery = d
		}
	}
	if rt.ttsDelivery == "" {
		rt.ttsDelivery = "binary"
	}
	rt.ttsFormat = "wav"
	if len(h.TTSFormats) > 0 {
		rt.ttsFormat = ""
		for _, f := range h.TTSFormats {
			if rt.ttsFormat == "" && (f == "wav" || f == "pcm16le") {
				rt.ttsFormat = f
			}
		}
		if rt.ttsFormat == "" {
			rt.ttsFormat = "wav"
		}
	}
	if rt.ttsDelivery == "url" {
		rt.ttsFormat = "wav"
	}
	rt.partials = h.Partials == nil || *h.Partials
	rt.epDisabled = h.Endpointing != nil && !*h.Endpointing
	return ok
}

func (rt *Runtime) attach(c *Conn, hm helloMsg) {
	if rt.quit {
		rt.fatal(c, protocol.ErrSessionEnded, "La sesión ya terminó.", false, protocol.CloseSessionEnded)
		return
	}
	if !rt.negotiate(hm.hello) {
		rt.fatal(c, protocol.ErrUnsupportedFormat, "Ningún formato de audio aceptable: se requiere pcm16le 16 kHz mono.", false, protocol.CloseProtocolError)
		return
	}
	if old := rt.conn; old != nil && old != c {
		rt.fatal(old, protocol.ErrAlreadyConnected, "Otra conexión tomó esta sesión.", false, protocol.CloseReplaced)
	}
	// hello no consume seq (va con seq 0). Sin resume es un cliente nuevo (p. ej.
	// recargó la página) y su contador arranca de nuevo; con resume se descartan
	// solo los reenvíos con seq <= last_client_seq.
	if hm.hello.Resume == nil {
		rt.lastClient, rt.ackSent = 0, 0
	}
	rt.gapReported, rt.invalid = 0, 0
	wasDetached := !rt.detachedAt.IsZero()
	rt.conn = c
	rt.detachedAt = time.Time{}
	rt.graceGen++
	rt.setStatus(coach.StLive)

	resumed, gap := false, false
	var replay []outMsg
	if r := hm.hello.Resume; r != nil && rt.started {
		last := rt.serverSeq.Load()
		switch {
		case r.LastServerSeq >= last:
			resumed = true
		case len(rt.ring) > 0 && rt.ring[0].seq <= r.LastServerSeq+1:
			resumed = true
			for _, m := range rt.ring {
				if m.seq > r.LastServerSeq {
					replay = append(replay, m)
				}
			}
		default:
			gap = true
		}
	}
	if gap {
		rt.sendErr(protocol.ErrResumeGap, "No se pudo reenviar desde last_server_seq; se reenvía el estado actual.", false, nil)
	}
	state := string(rt.eng.State())
	rt.send(protocol.TSessionReady, protocol.SessionReady{State: state, AudioFormat: rt.audioFmt, TTSDelivery: rt.ttsDelivery,
		TTSFormat: rt.ttsFormat, HeartbeatMs: int(rt.opt.Heartbeat / time.Millisecond), IdleTimeoutMs: int(rt.opt.IdleTimeout / time.Millisecond),
		MaxInflightBytes: rt.opt.MaxInflightBytes, MaxTurnMs: rt.opt.MaxTurnMs, Resumed: resumed, LastClientSeq: rt.lastClient,
		QuestionIndex: rt.eng.QuestionIndex(), QuestionCount: rt.eng.QuestionCount(), Endpointing: rt.endpointInfo()})
	for _, m := range replay {
		rt.deliver(m)
	}
	if !rt.started {
		rt.started = true
		rt.emit(rt.eng.Start())
		return
	}
	if wasDetached {
		rt.send(protocol.TResume, protocol.ResumeMsg{State: string(rt.eng.State())})
	}
	if !resumed {
		rt.resetAudio()
		rt.emit(rt.eng.CurrentPrompt())
	}
}

func (rt *Runtime) detach(c *Conn) {
	if rt.conn != c || rt.quit {
		return
	}
	rt.conn = nil
	rt.detachedAt = time.Now()
	rt.setStatus(coach.StDetached)
	if rt.started {
		rt.send(protocol.TPause, protocol.Pause{Reason: "reconnect", MaxPauseMs: int(rt.opt.Grace / time.Millisecond)})
	}
	rt.stopPartials()
	rt.graceGen++
	gen := rt.graceGen
	time.AfterFunc(rt.opt.Grace, func() {
		rt.post(func() {
			if rt.graceGen == gen && rt.conn == nil {
				rt.emit(rt.eng.End("disconnected"))
			}
		})
	})
}

func (rt *Runtime) setStatus(st string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if st == coach.StLive && rt.sess.StartedAt == nil {
		now := time.Now().UTC()
		rt.sess.StartedAt = &now
		rt.sess.Status = st
		_ = rt.svc.Store.SaveSession(ctx, rt.sess)
		return
	}
	rt.sess.Status = st
	_, _ = rt.svc.Store.SetStatus(ctx, rt.id, st, coach.StCreated, coach.StLive, coach.StDetached)
}

// ---- entrada ----

// accept aplica las reglas de seq: duplicados se ignoran, huecos piden reenvío.
func (rt *Runtime) accept(seq uint32) bool {
	if seq <= rt.lastClient {
		return false
	}
	if seq != rt.lastClient+1 {
		if rt.gapReported != rt.lastClient+1 {
			rt.gapReported = rt.lastClient + 1
			ref := rt.lastClient + 1
			rt.sendErr(protocol.ErrSeqOutOfOrder, fmt.Sprintf("Falta el seq %d; reenviá desde ahí.", ref), true, &ref)
		}
		return false
	}
	rt.lastClient = seq
	return true
}

func (rt *Runtime) bad(code, msg string, ref *uint32) {
	rt.invalid++
	if rt.invalid >= 3 {
		rt.fatal(rt.conn, code, msg+" (3 seguidos)", false, protocol.CloseProtocolError)
		return
	}
	rt.sendErr(code, msg, false, ref)
}

func (rt *Runtime) onText(c *Conn, data []byte) {
	if c != rt.conn || rt.quit {
		return
	}
	rt.svc.M.WSMessages.WithLabelValues("in", "text").Inc()
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil || env.Type == "" {
		rt.bad(protocol.ErrInvalidMessage, "JSON inválido o sin type.", nil)
		return
	}
	if env.Type == protocol.THello {
		rt.bad(protocol.ErrInvalidMessage, "hello ya recibido en esta conexión.", nil)
		return
	}
	if env.Seq == 0 {
		rt.bad(protocol.ErrInvalidMessage, "Falta seq (solo hello va con seq 0).", nil)
		return
	}
	if !rt.accept(env.Seq) {
		return
	}
	ref := env.Seq
	if env.V != 1 || env.SessionID != rt.id {
		rt.bad(protocol.ErrInvalidMessage, "v o session_id no coinciden.", &ref)
		return
	}
	if !protocol.ClientTypes[env.Type] {
		rt.bad(protocol.ErrUnsupportedMessage, "Tipo de mensaje no soportado.", &ref)
		return
	}
	decode := func(v any) bool {
		if len(env.Data) == 0 || json.Unmarshal(env.Data, v) != nil {
			rt.bad(protocol.ErrInvalidMessage, "data inválido para "+env.Type+".", &ref)
			return false
		}
		return true
	}
	rt.svc.M.WSMessages.WithLabelValues("in", env.Type).Inc()
	switch env.Type {
	case protocol.TPing:
		var n protocol.Nonce
		if !decode(&n) {
			return
		}
		rt.send(protocol.TPong, n)
	case protocol.TPong:
	case protocol.TAudioChunk:
		var a protocol.AudioChunkJSON
		if !decode(&a) {
			return
		}
		pcm, err := base64.StdEncoding.DecodeString(a.AudioB64)
		n, ok := parseNo(a.TurnID, "t-")
		if err != nil || !ok || a.ChunkIndex < 0 || len(pcm) > protocol.MaxPayload {
			rt.bad(protocol.ErrInvalidMessage, "audio.chunk inválido.", &ref)
			return
		}
		rt.invalid = 0
		rt.audio(n, uint32(a.ChunkIndex), pcm)
		return
	case protocol.TAudioEnd:
		var a protocol.AudioEnd
		if !decode(&a) || a.TurnID == "" {
			return
		}
		if rt.eng.State() == engine.Listening && a.TurnID == rt.eng.CurrentTurnID() {
			rt.markVoiceEnd(time.Now(), "explicit")
		}
		rt.endAudio(a.TurnID, "audio_end")
	case protocol.TAnswerText:
		var a protocol.AnswerText
		if !decode(&a) || a.TurnID == "" {
			return
		}
		if len([]rune(a.Text)) > 8000 {
			rt.bad(protocol.ErrInvalidMessage, "answer.text supera 8000 caracteres.", &ref)
			return
		}
		rt.answerText(a)
	case protocol.TControlPause:
		outs := rt.eng.Pause()
		if rt.eng.State() == engine.Paused {
			rt.resetAudio()
			rt.stopListenTimer()
		}
		rt.emit(outs)
	case protocol.TControlResume:
		rt.emit(rt.eng.Resume())
	case protocol.TControlSkip:
		var ctl protocol.Control
		_ = json.Unmarshal(env.Data, &ctl)
		rt.resetAudio()
		rt.emit(rt.eng.Skip(ctl.TurnID))
	case protocol.TControlRepeat:
		var ctl protocol.Control
		_ = json.Unmarshal(env.Data, &ctl)
		outs := rt.eng.Repeat(ctl.TurnID)
		if rt.eng.State() == engine.Asking {
			rt.resetAudio()
		}
		rt.emit(outs)
	case protocol.TControlEnd:
		rt.emit(rt.eng.End("user"))
	case protocol.TTTSPlayed:
		var p protocol.TTSPlayed
		if !decode(&p) {
			return
		}
		if n, ok := parseNo(p.UtteranceID, "u-"); ok {
			if n <= rt.lastPlayed {
				break // duplicado o de una intervención vieja: se ignora sin error
			}
			rt.lastPlayed = n
			if n == rt.eng.UtteranceNo() {
				rt.stopListenTimer()
			}
			rt.emit(rt.eng.PromptDelivered(n))
		}
	}
	rt.invalid = 0
}

func (rt *Runtime) onFrame(c *Conn, h protocol.FrameHeader, payload []byte, err error) {
	defer c.inflight.Add(-int64(len(payload)))
	if c != rt.conn || rt.quit {
		return
	}
	rt.svc.M.WSMessages.WithLabelValues("in", "audio.frame").Inc()
	if err != nil {
		rt.bad(protocol.ErrInvalidMessage, "Frame binario mal formado.", nil)
		return
	}
	if !rt.accept(h.Seq) {
		return
	}
	ref := h.Seq
	if h.Kind != protocol.KindAudioChunk || h.Codec != protocol.CodecPCM16 || len(payload)%2 != 0 {
		rt.bad(protocol.ErrInvalidMessage, "Frame con kind/codec no negociado.", &ref)
		return
	}
	rt.invalid = 0
	rt.audio(int(h.TurnNo), h.ChunkIndex, payload)
}

func parseNo(id, prefix string) (int, bool) {
	if !strings.HasPrefix(id, prefix) {
		return 0, false
	}
	n, err := strconv.Atoi(id[len(prefix):])
	return n, err == nil && n >= 0
}

// ---- audio ----

func (rt *Runtime) resetAudio() {
	rt.buf, rt.nextChunk, rt.audioTurn = nil, 0, 0
	rt.stopPartials()
	rt.cancelEndpoint()
	rt.ep = nil
}

func (rt *Runtime) conflictOnce(turn int, msg string) {
	if rt.conflictFor == turn {
		return
	}
	rt.conflictFor = turn
	rt.sendErr(protocol.ErrStateConflict, msg, false, nil)
}

func (rt *Runtime) audio(turnNo int, idx uint32, pcm []byte) {
	if (rt.tooLongTurn == turnNo || rt.endpointedTurn == turnNo) && turnNo != 0 {
		// chunks posteriores a max_turn_ms o al cierre por endpoint (el cliente
		// todavía no vio turn.end) se descartan sin error
		return
	}
	if rt.sess.Input == "text" {
		rt.conflictOnce(turnNo, "Sesión de texto: mandá answer.text.")
		return
	}
	if !rt.eng.AcceptAudio(turnNo) {
		rt.conflictOnce(turnNo, "Audio fuera de listening o de otro turno.")
		return
	}
	if rt.audioTurn != turnNo {
		rt.buf, rt.nextChunk, rt.audioTurn = nil, 0, turnNo
		rt.partialRev, rt.partialAt = 0, 0
		rt.newTurnAudio()
	}
	if idx < rt.nextChunk {
		return
	}
	if idx > rt.nextChunk {
		rt.bad(protocol.ErrInvalidMessage, fmt.Sprintf("chunk_index con hueco: se esperaba %d.", rt.nextChunk), nil)
		return
	}
	rt.nextChunk++
	rt.buf = append(rt.buf, pcm...)
	rt.ackBytes += len(pcm)
	if rt.ackBytes >= ackBytes {
		rt.sendAck()
	}
	rt.armAck()
	maxBytes := rt.opt.MaxTurnMs * bytesPerSec / 1000
	if len(rt.buf) >= maxBytes {
		rt.buf = rt.buf[:maxBytes]
		rt.tooLongTurn = turnNo
		rt.sendErr(protocol.ErrAudioTooLong, "La respuesta superó el máximo por turno; se cierra el turno.", false, nil)
		rt.endAudio(rt.eng.CurrentTurnID(), "max_duration")
		return
	}
	rt.onEndpointAudio(turnNo, pcm)
	if rt.eng.State() != engine.Listening || rt.audioTurn != turnNo {
		return // el endpointing cerró el turno
	}
	if (rt.partials || (rt.ep != nil && rt.ep.UsesSTT())) && rt.partialTimer == nil && !rt.partialInflight {
		turn := turnNo
		rt.partialTimer = time.AfterFunc(rt.opt.PartialInterval, func() { rt.post(func() { rt.partialTick(turn) }) })
	}
}

func (rt *Runtime) stopPartials() {
	if rt.partialTimer != nil {
		rt.partialTimer.Stop()
		rt.partialTimer = nil
	}
}

// partialTick re-transcribe la ventana de los últimos ~12 s del turno.
func (rt *Runtime) partialTick(turn int) {
	rt.partialTimer = nil
	if rt.quit || rt.eng.State() != engine.Listening || rt.audioTurn != turn {
		return
	}
	resched := func() {
		rt.partialTimer = time.AfterFunc(rt.opt.PartialInterval, func() { rt.post(func() { rt.partialTick(turn) }) })
	}
	if rt.partialInflight || len(rt.buf) == rt.partialAt || (rt.conn != nil && rt.conn.heavy()) {
		resched()
		return
	}
	win := int(rt.opt.PartialWindow.Seconds() * bytesPerSec)
	start := 0
	if len(rt.buf) > win {
		start = (len(rt.buf) - win) &^ 1
	}
	audio := append([]byte(nil), rt.buf[start:]...)
	total := len(rt.buf)
	startMs, endMs := start*1000/bytesPerSec, total*1000/bytesPerSec
	rt.partialAt = total
	rt.partialInflight = true
	go func() {
		res, err := rt.svc.Speech.STT(rt.ctx, audio, speech.STTOptions{Language: rt.sess.Lang, Prompt: speech.FillerPrompt[rt.sess.Lang], Partial: true,
			AudioSecs: float64(len(audio)) / bytesPerSec})
		rt.post(func() {
			rt.partialInflight = false
			rt.onPartialResult(turn, startMs, endMs, res, err)
			if rt.partials && err == nil && rt.eng.State() == engine.Listening && rt.audioTurn == turn && strings.TrimSpace(res.Text) != "" {
				rt.partialRev++
				rt.send(protocol.TTranscriptPartial, protocol.TranscriptPartial{TurnID: rt.eng.CurrentTurnID(), Text: strings.TrimSpace(res.Text),
					Revision: rt.partialRev, AudioMs: total * 1000 / bytesPerSec})
			}
		})
	}()
	resched()
}

func (rt *Runtime) endAudio(tid, reason string) {
	if outs := rt.eng.EndOfAudio(tid, reason); outs != nil {
		rt.emit(outs)
		return
	}
	rt.stopPartials()
	rt.stopListenTimer()
	rt.cancelEndpoint()
	rt.ep = nil
	turn := rt.eng.CurrentTurnNo()
	audio := rt.buf
	if rt.audioTurn != turn {
		audio = nil
	}
	rt.buf, rt.nextChunk = nil, 0
	if rt.cfg.SaveAudio && len(audio) > 0 {
		rt.saveAudio(tid, audio)
	}
	if len(audio) < bytesPerSec/10 {
		rt.finalize(engine.Answer{TurnID: tid, Source: "stt"})
		return
	}
	secs := float64(len(audio)) / bytesPerSec
	rt.sttStart = time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(rt.ctx, 2*speech.STTTimeout(secs)+2*time.Second)
		defer cancel()
		res, err := rt.svc.Speech.STT(ctx, audio, speech.STTOptions{Language: rt.sess.Lang, Prompt: speech.FillerPrompt[rt.sess.Lang], AudioSecs: secs})
		rt.post(func() { rt.sttDone(tid, turn, secs, res, err) })
	}()
}

func (rt *Runtime) sttDone(tid string, turn int, secs float64, res *speech.STTResult, err error) {
	if rt.quit || rt.eng.State() != engine.Processing || rt.eng.CurrentTurnNo() != turn {
		return
	}
	if err == nil {
		rt.svc.M.TurnStage.WithLabelValues("stt_final").Observe(time.Since(rt.sttStart).Seconds())
	}
	if err != nil {
		if speech.IsNoAudio(err) {
			rt.finalize(engine.Answer{TurnID: tid, Source: "stt"})
			return
		}
		code, msg := protocol.ErrSTTUnavailable, "El servicio de voz no está disponible. Repetí tu respuesta."
		if speech.IsTimeout(err) {
			code, msg = protocol.ErrSTTTimeout, "El servicio de voz no respondió a tiempo. Repetí tu respuesta."
		}
		rt.svc.Log.Warn("stt final falló", "session_id", rt.id, "turn_id", tid, "error", err.Error())
		rt.sendErr(code, msg, true, nil)
		rt.emit(rt.eng.Retry(tid))
		return
	}
	dur := res.DurationS
	if dur <= 0 {
		dur = secs
	}
	a := engine.Answer{TurnID: tid, Text: strings.TrimSpace(res.Text), DurationS: dur, Confidence: res.Confidence,
		LowConfidence: res.LowConfidence(), Source: "stt", Fillers: res.FillerTexts()}
	if res.Pauses != nil {
		ps := &engine.PauseStats{}
		for _, p := range res.Pauses {
			ps.Count++
			ps.TotalS += p.DurationS
			if p.DurationS > ps.LongestS {
				ps.LongestS = p.DurationS
			}
		}
		ps.TotalS = analysis.Round(ps.TotalS, 2)
		ps.LongestS = analysis.Round(ps.LongestS, 2)
		a.Pauses = ps
	}
	if rt.svc.Cfg.LogTranscripts {
		rt.svc.Log.Debug("transcripción", "session_id", rt.id, "turn_id", tid, "text", a.Text)
	}
	rt.finalize(a)
}

func (rt *Runtime) answerText(a protocol.AnswerText) {
	if outs := rt.eng.AcceptText(a.TurnID); outs != nil {
		rt.emit(outs)
		return
	}
	rt.stopListenTimer()
	rt.resetAudio()
	text := strings.TrimSpace(a.Text)
	rt.finalize(engine.Answer{TurnID: a.TurnID, Text: text, DurationS: analysis.EstimateSeconds(text, rt.sess.Lang), Estimated: true, Source: "text"})
}

func (rt *Runtime) finalize(a engine.Answer) {
	outs, need := rt.eng.Final(a)
	rt.persistTurns()
	if t := rt.eng.LastTurn(); t != nil && t.ID == a.TurnID && t.Answer != "" && rt.cfg.LearnProfile {
		rt.learn(*t)
	}
	// Primero transcript.final (y lo que siga); después la decisión, que puede
	// aplicarse en el acto si la especulativa ya está lista.
	rt.emit(outs)
	if need && !rt.quit && rt.eng.State() == engine.Processing {
		rt.decide()
	}
}

// decide consulta al LLM si repreguntar (con respaldo local si falla). Si hay
// una decisión especulativa para un parcial igual al texto final, se usa esa.
func (rt *Runtime) decide() {
	in := rt.eng.DecisionInput()
	final := ""
	if len(in.Answers) > 0 {
		final = in.Answers[len(in.Answers)-1]
	}
	if rt.useSpec(rt.eng.CurrentTurnNo(), final) {
		return
	}
	rt.decideNow()
}

func (rt *Runtime) learn(t engine.Turn) {
	rt.learnWG.Add(1)
	go func() {
		defer rt.learnWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		changes, rejected, err := rt.svc.LearnProfile(ctx, rt.prov, rt.id, t.ID, t.Question, t.Answer)
		rt.learnMu.Lock()
		rt.learned = append(rt.learned, changes...)
		rt.learned = append(rt.learned, rejected...)
		rt.learnMu.Unlock()
		if err != nil {
			rt.svc.Log.Warn("aprendizaje de perfil", "session_id", rt.id, "error", err.Error())
		}
	}()
}

func (rt *Runtime) persistTurns() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for ; rt.persisted < len(rt.eng.Turns); rt.persisted++ {
		t := rt.eng.Turns[rt.persisted]
		b, _ := json.Marshal(t)
		if err := rt.svc.Store.PutTurn(ctx, rt.id, rt.persisted, t.ID, b); err != nil {
			rt.svc.Log.Error("guardar turno", "session_id", rt.id, "error", err.Error())
			return
		}
	}
}

func (rt *Runtime) saveAudio(tid string, pcm []byte) {
	dir := filepath.Join(rt.svc.Cfg.DataDir, "audio", rt.id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, tid+".wav"), wav(pcm, 16000), 0o600)
}

func wav(pcm []byte, rate int) []byte {
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	return append(h, pcm...)
}

// ---- salida del motor ----

func (rt *Runtime) emit(outs []engine.Out) {
	for _, o := range outs {
		if rt.quit {
			return
		}
		switch d := o.Data.(type) {
		case protocol.Question:
			rt.send(o.Type, d)
			if d.Kind != "closing" {
				if n, ok := parseNo(d.UtteranceID, "u-"); ok {
					rt.onPromptOut(n)
				}
			}
			if d.Kind == "closing" {
				rt.ttsSync(d.Text, d.UtteranceID)
			} else {
				rt.prompt(d.Text, d.UtteranceID)
			}
		case protocol.Followup:
			rt.svc.M.Followups.WithLabelValues(d.Probe).Inc()
			rt.send(o.Type, d)
			if n, ok := parseNo(d.UtteranceID, "u-"); ok {
				rt.onPromptOut(n)
			}
			rt.prompt(d.Text, d.UtteranceID)
		case protocol.Pause:
			rt.send(o.Type, d)
			rt.stopListenTimer()
			if d.Reason == "think_time" && d.ResumeAfterMs != nil {
				rt.thinkGen++
				gen := rt.thinkGen
				time.AfterFunc(time.Duration(*d.ResumeAfterMs)*time.Millisecond, func() {
					rt.post(func() {
						if gen == rt.thinkGen {
							rt.emit(rt.eng.ThinkElapsed())
						}
					})
				})
			} else {
				rt.pauseGen++
				gen := rt.pauseGen
				time.AfterFunc(rt.opt.MaxPause, func() {
					rt.post(func() {
						if gen == rt.pauseGen && rt.eng.State() == engine.Paused {
							rt.emit(rt.eng.End("timeout"))
						}
					})
				})
			}
		case protocol.ResumeMsg:
			rt.pauseGen++
			rt.thinkGen++
			rt.send(o.Type, d)
		case protocol.SessionEnd:
			rt.finish(d)
		default:
			rt.send(o.Type, o.Data)
		}
	}
}

// armPlayedTimeout: el servidor espera tts.played de la intervención. Si no
// llega en duration_ms + CX9_TTS_PLAYED_GRACE_MS (o CX9_TTS_PLAYED_TIMEOUT_MS si
// la duración no se conoce) avanza igual. No es un error: métrica
// cx9_tts_played_timeouts_total y log warn.
func (rt *Runtime) armPlayedTimeout(utt, durationMs int) {
	rt.stopListenTimer()
	wait := rt.opt.TTSPlayedTimeout
	if durationMs > 0 {
		wait = time.Duration(durationMs)*time.Millisecond + rt.opt.TTSPlayedGrace
	}
	rt.listenTimer = time.AfterFunc(wait, func() {
		rt.post(func() {
			st := rt.eng.State()
			if rt.quit || rt.eng.UtteranceNo() != utt || (st != engine.Intro && st != engine.Asking) {
				return
			}
			rt.listenTimer = nil
			rt.svc.M.TTSPlayedTimeouts.Inc()
			rt.svc.Log.Warn("tts.played no llegó a tiempo; se avanza igual", "session_id", rt.id,
				"utterance_id", fmt.Sprintf("u-%d", utt), "espera_ms", wait.Milliseconds())
			rt.emit(rt.eng.PromptDelivered(utt))
		})
	})
}

func (rt *Runtime) stopListenTimer() {
	if rt.listenTimer != nil {
		rt.listenTimer.Stop()
		rt.listenTimer = nil
	}
}

func (rt *Runtime) ttsRequest(text string) speech.TTSRequest {
	speed := 1.0
	if rt.sess.Lang == "en" {
		speed = 0.85
	}
	return speech.TTSRequest{Text: text, Language: rt.sess.Lang, Voice: rt.cfg.Voice, Format: rt.ttsFormat, Speed: speed}
}

// prompt sintetiza la intervención y la entrega según tts_delivery.
func (rt *Runtime) prompt(text, uttID string) {
	utt, _ := parseNo(uttID, "u-")
	if rt.ttsDelivery == "none" {
		rt.emit(rt.eng.PromptDelivered(utt))
		return
	}
	req := rt.ttsRequest(text)
	go func() {
		ctx, cancel := context.WithTimeout(rt.ctx, 40*time.Second)
		defer cancel()
		start := time.Now()
		res, err := rt.svc.Speech.TTS(ctx, req)
		rt.post(func() {
			if rt.quit || rt.eng.UtteranceNo() != utt {
				return // intervención vieja (repetida o saltada)
			}
			if err != nil {
				rt.sendErr(protocol.ErrTTSUnavailable, "No hay audio para esta intervención; se muestra el texto.", true, nil)
				rt.emit(rt.eng.PromptDelivered(utt))
				return
			}
			rt.svc.M.TurnStage.WithLabelValues("tts_first_byte").Observe(time.Since(start).Seconds())
			if rt.deliverTTS(uttID, res) {
				rt.armPlayedTimeout(utt, res.DurationMs)
			}
		})
	}()
}

// ttsSync se usa para el cierre (va antes de session.end).
func (rt *Runtime) ttsSync(text, uttID string) {
	if rt.ttsDelivery == "none" {
		return
	}
	ctx, cancel := context.WithTimeout(rt.ctx, 15*time.Second)
	defer cancel()
	res, err := rt.svc.Speech.TTS(ctx, rt.ttsRequest(text))
	if err != nil {
		rt.sendErr(protocol.ErrTTSUnavailable, "No hay audio para el cierre.", true, nil)
		return
	}
	rt.deliverTTS(uttID, res)
}

// deliverTTS manda el audio (frames o URL). false si no se mandó audio (solo
// texto): en ese caso ya se avanzó y no hay que esperar tts.played.
func (rt *Runtime) deliverTTS(uttID string, res *speech.TTSResult) bool {
	utt, _ := parseNo(uttID, "u-")
	format := res.Format
	if format == "" {
		format = rt.ttsFormat
	}
	useURL := rt.ttsDelivery == "url" || (rt.conn != nil && rt.conn.heavy() && rt.ttsURL && format == "wav")
	if useURL {
		ct := res.ContentType
		if ct == "" {
			ct = "audio/wav"
		}
		exp := rt.svc.TTS.Put(rt.id, uttID, coach.TTSAudio{Audio: res.Audio, ContentType: ct, DurationMs: res.DurationMs})
		rt.send(protocol.TTTSURL, protocol.TTSURL{UtteranceID: uttID, URL: "/v1/sessions/" + rt.id + "/tts/" + uttID, Format: "wav",
			DurationMs: res.DurationMs, ExpiresAt: exp.UTC().Format(time.RFC3339)})
		rt.onTTSSent(utt)
		return true
	}
	if rt.conn != nil && rt.conn.heavy() {
		rt.svc.M.WSBackpressure.WithLabelValues("tts_paused").Inc()
		rt.sendErr(protocol.ErrTTSUnavailable, "Conexión cargada: la intervención va solo como texto.", true, nil)
		rt.emit(rt.eng.PromptDelivered(utt))
		return false
	}
	codec := protocol.CodecWAV
	if format == "pcm16le" {
		codec = protocol.CodecPCM16
	}
	chunks := (len(res.Audio) + protocol.MaxPayload - 1) / protocol.MaxPayload
	sum := sha256.Sum256(res.Audio)
	sr := res.SampleRate
	if sr == 0 {
		sr = 16000
	}
	rt.send(protocol.TTTSAudio, protocol.TTSAudio{UtteranceID: uttID, Format: format, SampleRate: sr, DurationMs: res.DurationMs,
		Chunks: chunks, Bytes: len(res.Audio), SHA256: hex.EncodeToString(sum[:])})
	for i := 0; i < chunks; i++ {
		end := (i + 1) * protocol.MaxPayload
		if end > len(res.Audio) {
			end = len(res.Audio)
		}
		rt.sendFrame(utt, i, i == chunks-1, codec, res.Audio[i*protocol.MaxPayload:end])
		if i == 0 {
			rt.onTTSSent(utt)
		}
	}
	return true
}

// finish cierra la sesión: guarda, manda session.end, cierra y encola el informe.
func (rt *Runtime) finish(d protocol.SessionEnd) {
	rt.stopPartials()
	rt.stopListenTimer()
	rt.cancelEndpoint()
	rt.persistTurns()
	waitTimeout(&rt.learnWG, 15*time.Second)
	rt.learnMu.Lock()
	rt.cfg.ProfileChanges = append(rt.cfg.ProfileChanges, rt.learned...)
	rt.learnMu.Unlock()
	sum := rt.eng.Summary()
	rt.cfg.Summary = &coach.SummarySnapshot{QuestionsAsked: sum.QuestionsAsked, FollowupsAsked: sum.FollowupsAsked, Skipped: sum.Skipped, TotalAnswerMs: sum.TotalAnswerMs}
	raw, _ := json.Marshal(rt.cfg)
	now := time.Now().UTC()
	reason := d.Reason
	rt.sess.Status, rt.sess.EndedReason, rt.sess.EndedAt = coach.StEnded, &reason, &now
	rt.sess.QuestionsAnswered, rt.sess.Config, rt.sess.LLMErrors = sum.QuestionsAnswered, raw, rt.llmErrs
	if rt.sess.LLMErrors == nil {
		rt.sess.LLMErrors = []string{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := rt.svc.Store.SaveSession(ctx, rt.sess); err != nil {
		rt.svc.Log.Error("guardar sesión", "session_id", rt.id, "error", err.Error())
	}
	cancel()
	d.ReportURL = "/v1/sessions/" + rt.id + "/report"
	rt.send(protocol.TSessionEnd, d)
	if rt.conn != nil {
		rt.conn.closeAfterFlush(rt.closeCode, "session.end")
	}
	rt.quit = true
	rt.hub.remove(rt.id)
	rt.svc.M.Sessions.WithLabelValues(rt.sess.Mode, rt.sess.Lang, reason).Inc()
	rt.svc.Enqueue(rt.id)
	rt.svc.Log.Info("sesión terminada", "session_id", rt.id, "reason", reason, "turnos", len(rt.eng.Turns))
}

func waitTimeout(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}
