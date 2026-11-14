package live

import (
	"fmt"
	"strings"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/endpoint"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/engine"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/llm"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/profile"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/protocol"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/speech"
)

// Endpointing por silencio (contrato v1.2 §6.9). Solo analiza el audio del
// micrófono del usuario que manda el cliente durante su turno.

// specMinSimilarity: la decisión especulativa se usa solo si el texto final se
// parece al parcial con el que se calculó (palabras normalizadas).
const specMinSimilarity = 0.9

type chunkMark struct {
	endMs int       // fin del chunk en ms de audio del turno
	at    time.Time // cuándo llegó
}

type partialSnap struct {
	turn           int
	startMs, endMs int
	text           string
}

type specState struct {
	turn    int
	text    string // parcial con el que se calculó
	done    bool
	res     llm.FollowupResult
	err     error
	waiting bool // decide() está esperando este resultado
}

func (rt *Runtime) epOn() bool {
	return !rt.epDisabled && rt.opt.Endpoint.Mode != endpoint.Off && rt.sess.Input != "text"
}

func (rt *Runtime) endpointInfo() *protocol.EndpointInfo {
	mode := rt.opt.Endpoint.Mode
	if !rt.epOn() {
		mode = endpoint.Off
	}
	return &protocol.EndpointInfo{Enabled: rt.epOn(), Mode: mode, SilenceMs: rt.opt.Endpoint.SilenceMs}
}

// newTurnAudio arranca el estado de endpointing del turno.
func (rt *Runtime) newTurnAudio() {
	rt.cancelEndpoint()
	rt.ep, rt.marks, rt.lastPartial = nil, nil, partialSnap{}
	if rt.epOn() {
		rt.ep = endpoint.New(rt.opt.Endpoint)
	}
}

func (rt *Runtime) cancelEndpoint() {
	rt.epGen++
	if rt.epTimer != nil {
		rt.epTimer.Stop()
		rt.epTimer = nil
	}
	rt.epCand = nil
}

// onEndpointAudio pasa el audio al detector y arma un cierre si corresponde.
func (rt *Runtime) onEndpointAudio(turn int, pcm []byte) {
	if rt.ep == nil {
		return
	}
	rt.ep.Feed(pcm)
	rt.marks = append(rt.marks, chunkMark{endMs: len(rt.buf) * 1000 / bytesPerSec, at: time.Now()})
	if len(rt.marks) > 400 {
		rt.marks = rt.marks[len(rt.marks)-200:]
	}
	if rt.epCand != nil {
		if rt.ep.VoiceAfter(rt.epCand.MarkMs) {
			rt.cancelEndpoint() // volvió la voz dentro de la gracia: no se corta
		}
		return
	}
	if c, ok := rt.ep.EnergyCandidate(); ok {
		rt.armEndpoint(turn, c)
		return
	}
	if rt.ep.ProbeWanted() {
		rt.partialNow(turn) // confirmar ya con /stt en vez de esperar el próximo parcial
	}
}

// partialNow pide un parcial fuera de turno (para confirmar un silencio).
func (rt *Runtime) partialNow(turn int) {
	if rt.partialInflight {
		return
	}
	rt.stopPartials()
	rt.partialTick(turn)
}

// onPartialResult aplica la respuesta de /stt parcial al endpointing.
func (rt *Runtime) onPartialResult(turn, startMs, endMs int, res *speech.STTResult, err error) {
	if rt.ep == nil || rt.eng.State() != engine.Listening || rt.audioTurn != turn {
		return
	}
	if err != nil {
		// parcial ocupado (503) o caído: en auto, respaldo de energía
		if c, ok := rt.ep.EnergyFallback(); ok && rt.epCand == nil {
			rt.armEndpoint(turn, c)
		}
		return
	}
	rt.lastPartial = partialSnap{turn: turn, startMs: startMs, endMs: endMs, text: strings.TrimSpace(res.Text)}
	if rt.epCand != nil {
		return
	}
	se, tr := res.Endpoint()
	if c, ok := rt.ep.STTResult(startMs, endMs, se, tr, res.Text); ok {
		rt.armEndpoint(turn, c)
		return
	}
	if !rt.ep.UsesSTT() { // auto sin campos: desde ahora energía
		if c, ok := rt.ep.EnergyCandidate(); ok {
			rt.armEndpoint(turn, c)
		}
	}
}

// armEndpoint espera la ventana de gracia y, si no volvió la voz, cierra.
func (rt *Runtime) armEndpoint(turn int, c endpoint.Candidate) {
	rt.epCand = &c
	rt.epGen++
	gen := rt.epGen
	rt.epTimer = time.AfterFunc(time.Duration(rt.opt.Endpoint.GraceMs)*time.Millisecond, func() {
		rt.post(func() {
			if gen != rt.epGen || rt.quit || rt.eng.State() != engine.Listening || rt.audioTurn != turn || rt.ep == nil {
				return
			}
			rt.epTimer = nil
			if !rt.ep.Confirm(c) {
				rt.epCand = nil
				return
			}
			rt.endpointClose(turn, c)
		})
	})
}

func (rt *Runtime) endpointClose(turn int, c endpoint.Candidate) {
	rt.epCand = nil
	rt.endpointedTurn = turn
	rt.svc.M.Endpoints.WithLabelValues(c.Source).Inc()
	rt.markVoiceEnd(rt.wallAt(c.SpeechEndedMs), "endpoint")
	rt.maybeSpeculate(turn, c.SpeechEndedMs)
	rt.svc.Log.Debug("endpoint", "session_id", rt.id, "turn_id", rt.eng.CurrentTurnID(), "source", c.Source,
		"speech_ended_ms", c.SpeechEndedMs, "audio_ms", c.MarkMs)
	rt.endAudio(rt.eng.CurrentTurnID(), "endpoint")
}

// wallAt estima cuándo llegó el audio hasta ms (para medir desde el fin de voz).
func (rt *Runtime) wallAt(ms int) time.Time {
	for _, m := range rt.marks {
		if m.endMs >= ms {
			return m.at
		}
	}
	return time.Now()
}

// ---- latencia ----

func (rt *Runtime) markVoiceEnd(at time.Time, mode string) {
	rt.voiceEndAt, rt.latMode, rt.latUtt = at, mode, 0
}

// onPromptOut: la siguiente intervención después de un fin de voz.
func (rt *Runtime) onPromptOut(utt int) {
	if !rt.voiceEndAt.IsZero() && rt.latUtt == 0 {
		rt.latUtt = utt
	}
}

// onTTSSent: primer frame (o URL) de TTS de esa intervención.
func (rt *Runtime) onTTSSent(utt int) {
	if rt.voiceEndAt.IsZero() || rt.latUtt != utt {
		return
	}
	rt.svc.M.EndpointToTTS.WithLabelValues(rt.latMode, rt.prov.ID()).Observe(time.Since(rt.voiceEndAt).Seconds())
	rt.voiceEndAt, rt.latUtt = time.Time{}, 0
}

// ---- decisión de repregunta (con especulación) ----

func (rt *Runtime) followupPayload(extra string) (llm.FollowupPayload, string) {
	in := rt.eng.DecisionInput()
	hist := make([]llm.QA, 0, len(in.History))
	for _, h := range in.History {
		hist = append(hist, llm.QA{Q: h[0], A: h[1]})
	}
	answers := in.Answers
	if extra != "" {
		answers = append(answers, extra)
	}
	p := llm.FollowupPayload{Lang: rt.sess.Lang, Question: in.Question.Text, Answers: answers, Probes: in.Question.Probes,
		AskedProbes: in.AskedProbes, FollowupsDone: in.FollowupsDone, MaxFollowups: in.MaxFollowups, History: hist}
	if rt.sess.Mode == "story" {
		if pr, _, err := rt.svc.Profile(rt.ctx); err == nil && pr != nil {
			p.Profile = profile.Summary(pr)
		}
	}
	return p, llm.FollowupSystem(rt.sess.Lang, rt.cfg.Style)
}

// maybeSpeculate lanza la decisión con el último parcial mientras corre el STT
// final. Solo si el parcial cubre todo el turno (ventana desde 0) hasta el fin
// de voz y quedan repreguntas disponibles.
func (rt *Runtime) maybeSpeculate(turn, speechEndMs int) {
	rt.spec = nil
	lp := rt.lastPartial
	if !rt.opt.Speculative || lp.turn != turn || lp.startMs != 0 || lp.endMs < speechEndMs || lp.text == "" {
		return
	}
	in := rt.eng.DecisionInput()
	if in.FollowupsDone >= in.MaxFollowups {
		return
	}
	payload, system := rt.followupPayload(lp.text)
	st := &specState{turn: turn, text: lp.text}
	rt.spec = st
	go func() {
		start := time.Now()
		res, err := llm.Call[llm.FollowupResult](rt.ctx, rt.prov, llm.TaskFollowup, system, payload, 0.3)
		d := time.Since(start)
		rt.post(func() {
			st.done, st.res, st.err = true, res, err
			if err == nil {
				rt.svc.M.TurnStage.WithLabelValues("llm_followup").Observe(d.Seconds())
			}
			if st.waiting && rt.spec == st {
				rt.spec = nil
				if rt.quit || rt.eng.State() != engine.Processing || rt.eng.CurrentTurnNo() != turn {
					return
				}
				if err != nil {
					rt.svc.M.Speculative.WithLabelValues("error").Inc()
					rt.decideNow()
					return
				}
				rt.svc.M.Speculative.WithLabelValues("hit").Inc()
				rt.applyFollowup(res, "")
			}
		})
	}()
}

// useSpec decide si la decisión especulativa sirve para el texto final. true
// si ya se aplicó o se va a aplicar cuando llegue.
func (rt *Runtime) useSpec(turn int, final string) bool {
	st := rt.spec
	if st == nil {
		return false
	}
	if st.turn != turn || endpoint.Similarity(st.text, final) < specMinSimilarity {
		rt.spec = nil
		rt.svc.M.Speculative.WithLabelValues("discarded").Inc()
		return false
	}
	if !st.done {
		st.waiting = true
		return true
	}
	rt.spec = nil
	if st.err != nil {
		rt.svc.M.Speculative.WithLabelValues("error").Inc()
		return false
	}
	rt.svc.M.Speculative.WithLabelValues("hit").Inc()
	rt.applyFollowup(st.res, "")
	return true
}

func (rt *Runtime) applyFollowup(res llm.FollowupResult, degraded string) {
	if degraded != "" {
		rt.llmErrs = append(rt.llmErrs, fmt.Sprintf("repregunta %s: %s; se usó la lógica local", rt.eng.CurrentTurnID(), degraded))
		rt.svc.M.LLMFallbacks.WithLabelValues(rt.prov.ID()).Inc()
		rt.sendErr(protocol.ErrLLMDegraded, "El proveedor LLM falló; se usa la lógica local de repreguntas.", false, nil)
	}
	action := strings.ToLower(res.Action)
	rt.emit(rt.eng.ApplyDecision(engine.Decision{Followup: action == "followup", Text: res.Question, Probe: res.Probe}))
}

// decideNow consulta al LLM con la respuesta final (respaldo local si falla).
func (rt *Runtime) decideNow() {
	turn := rt.eng.CurrentTurnNo()
	payload, system := rt.followupPayload("")
	go func() {
		start := time.Now()
		res, err := llm.Call[llm.FollowupResult](rt.ctx, rt.prov, llm.TaskFollowup, system, payload, 0.3)
		d := time.Since(start)
		degraded := ""
		if err != nil {
			degraded = err.Error()
			res = llm.MockFollowup(payload)
		}
		rt.post(func() {
			if degraded == "" {
				rt.svc.M.TurnStage.WithLabelValues("llm_followup").Observe(d.Seconds())
			}
			if rt.quit || rt.eng.State() != engine.Processing || rt.eng.CurrentTurnNo() != turn {
				return
			}
			rt.applyFollowup(res, degraded)
		})
	}()
}
