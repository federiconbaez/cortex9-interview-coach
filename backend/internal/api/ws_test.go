package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/config"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/live"
)

// playTTS consume tts.audio + frames, verifica sha256 y manda tts.played.
func (w *wsClient) playTTS(utt string) {
	w.t.Helper()
	m := w.expect("tts.audio")
	d := m.data(w.t)
	if d["utterance_id"] != utt {
		w.t.Fatalf("tts de %v, esperaba %s", d["utterance_id"], utt)
	}
	var audio []byte
	n := int(d["chunks"].(float64))
	for i := 0; i < n; i++ {
		f := w.expect("frame")
		if int(f.Header.ChunkIndex) != i || f.Header.Last != (i == n-1) || f.Header.Codec != 0x03 || f.Seq != m.Seq+uint32(i)+1 {
			w.t.Fatalf("frame %d: %+v", i, f.Header)
		}
		audio = append(audio, f.Binary...)
	}
	sum := sha256.Sum256(audio)
	if hex.EncodeToString(sum[:]) != d["sha256"] || len(audio) != int(d["bytes"].(float64)) {
		w.t.Fatal("sha256/bytes del TTS")
	}
	w.send("tts.played", map[string]any{"utterance_id": utt})
}

func checkSeqs(t *testing.T, w *wsClient) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	var last uint32
	for _, m := range w.all {
		if m.Seq == 0 {
			if m.Type != "ack" {
				t.Fatalf("solo ack va con seq 0: %s", m.Type)
			}
			continue
		}
		if m.Seq != last+1 {
			t.Fatalf("hueco de seq: %d después de %d (%s)", m.Seq, last, m.Type)
		}
		last = m.Seq
	}
}

func TestLiveFullVoiceSession(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	sid := e.createSession(map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia", "num_questions": 2})
	// sin números ni ejemplo → el mock repregunta (máx. 2)
	e.speech.say("Eh, bueno, trabajé en backend.", "Hice APIs.", "Fueron 3 años y 40 % menos latencia.", "Uso Go.", "Con Go.", "Go y Node.")
	w, _, err := e.dial(sid)
	if err != nil {
		t.Fatal(err)
	}
	w.hello(nil)
	ready := w.expect("session.ready").data(t)
	if ready["state"] != "ready" || ready["heartbeat_ms"].(float64) != 15000 || ready["max_inflight_bytes"].(float64) != 96000 ||
		ready["audio_format"].(map[string]any)["codec"] != "pcm16le" || ready["tts_format"] != "wav" || ready["question_count"].(float64) != 2 {
		t.Fatal(ready)
	}
	if r := e.do("GET", "/v1/sessions/"+sid, nil); r.json(t)["status"] != "live" {
		t.Fatal("status live")
	}
	if r := e.do("GET", "/v1/sessions/"+sid+"/report", nil); r.status != 409 {
		t.Fatal("sin devolución en vivo")
	}
	followups := map[string]int{}
	turns := 0
	for {
		typ, d, _ := w.prompt()
		if typ == "session.end" {
			if d["reason"] != "completed" || d["report_url"] != "/v1/sessions/"+sid+"/report" {
				t.Fatal(d)
			}
			sum := d["summary"].(map[string]any)
			if sum["questions_asked"].(float64) != 2 || sum["report_status"] != "scoring" {
				t.Fatal(sum)
			}
			break
		}
		utt := d["utterance_id"].(string)
		kind, _ := d["kind"].(string)
		w.playTTS(utt)
		if kind == "intro" || kind == "closing" {
			continue
		}
		if typ == "followup" {
			followups[d["question_id"].(string)]++
			if fi := d["followup_index"].(float64); fi < 1 || fi > 2 {
				t.Fatal("followup_index")
			}
		}
		tid := d["turn_id"].(string)
		w.answer(tid, 1500)
		fin := w.expect("transcript.final").data(t)
		if fin["turn_id"] != tid || fin["source"] != "stt" || fin["duration_ms"].(float64) != 1500 {
			t.Fatal(fin)
		}
		te := w.expect("turn.end").data(t)
		if te["turn_id"] != tid || te["max_followups"].(float64) != 2 {
			t.Fatal(te)
		}
		turns++
	}
	if code := w.waitClose(); code != websocket.StatusNormalClosure {
		t.Fatalf("cierre %d", code)
	}
	for q, n := range followups {
		if n > 2 {
			t.Fatalf("%s tuvo %d repreguntas", q, n)
		}
	}
	if followups["recorrido"] == 0 && len(followups) == 0 {
		t.Fatal("se esperaban repreguntas")
	}
	checkSeqs(t, w)
	if w.ack() == 0 {
		t.Fatal("el servidor no mandó ack")
	}
	rep := waitReport(t, e, sid)
	if len(rep["blocks"].([]any)) != 2 || len(rep["top3"].([]any)) != 3 {
		t.Fatal(rep)
	}
	det := e.do("GET", "/v1/sessions/"+sid, nil).json(t)
	if len(det["turns"].([]any)) != turns || det["status"] != "completed" || det["overall_score"] == nil {
		t.Fatal(det)
	}
}

func TestLiveTextSessionRepeatSkipEnd(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	sid := e.createSession(map[string]any{"lang": "en", "mode": "practice", "scenario_id": "behavioral-star", "input": "text", "num_questions": 3, "max_followups": 0})
	w, _, err := e.dial(sid)
	if err != nil {
		t.Fatal(err)
	}
	w.hello(map[string]any{"tts_delivery": []string{"none"}})
	if r := w.expect("session.ready").data(t); r["tts_delivery"] != "none" {
		t.Fatal(r)
	}
	// intro (si hay) pasa sola: con none no se espera tts.played
	_, q, _ := w.prompt()
	for q["kind"] == "intro" {
		_, q, _ = w.prompt()
	}
	w.send("control.repeat", map[string]any{"turn_id": q["turn_id"]})
	m := w.expect("question")
	rep := m.data(t)
	if rep["question_id"] != q["question_id"] || rep["repeat"] != true || rep["turn_id"] != q["turn_id"] || rep["utterance_id"] == q["utterance_id"] {
		t.Fatal(rep)
	}
	w.mu.Lock()
	for _, x := range w.all {
		if x.Type == "turn.end" || x.Type == "tts.audio" {
			t.Fatal("repeat no manda turn.end ni TTS con none")
		}
	}
	w.mu.Unlock()
	w.send("answer.text", map[string]any{"turn_id": q["turn_id"], "text": "In my last job I led a migration to Go and we cut latency by 40 percent."})
	fin := w.expect("transcript.final").data(t)
	if fin["source"] != "text" {
		t.Fatal(fin)
	}
	if te := w.expect("turn.end").data(t); te["next"] != "question" || te["reason"] != "text" {
		t.Fatal(te)
	}
	_, q2, _ := w.prompt()
	w.send("control.skip", map[string]any{"turn_id": q2["turn_id"]})
	if te := w.expect("turn.end").data(t); te["reason"] != "skip" {
		t.Fatal(te)
	}
	w.prompt()
	w.send("control.pause", map[string]any{})
	if p := w.expect("pause").data(t); p["reason"] != "user" || p["max_pause_ms"].(float64) != 1800000 {
		t.Fatal(p)
	}
	w.send("control.resume", map[string]any{})
	w.expect("resume")
	w.send("control.resume", map[string]any{})
	if er := w.expect("error").data(t); er["code"] != "STATE_CONFLICT" || er["fatal"] != false {
		t.Fatal(er)
	}
	w.send("ping", map[string]any{"nonce": "c1"})
	if p := w.expect("pong").data(t); p["nonce"] != "c1" {
		t.Fatal(p)
	}
	w.send("control.end", map[string]any{})
	if d := w.expect("session.end").data(t); d["reason"] != "user" {
		t.Fatal(d)
	}
	w.waitClose()
	checkSeqs(t, w)
	r := waitReport(t, e, sid)
	if r["ended_by_user"] != true {
		t.Fatal(r["ended_by_user"])
	}
}

func TestLiveResumeReplaysWithOriginalSeq(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	sid := e.createSession(map[string]any{"lang": "es", "mode": "practice", "scenario_id": "backend-tecnica", "num_questions": 2})
	w, _, _ := e.dial(sid)
	w.hello(nil)
	w.expect("session.ready")
	var q map[string]any
	var qm msg
	for {
		_, d, m := w.prompt()
		w.playTTS(d["utterance_id"].(string))
		if d["kind"] != "intro" {
			q, qm = d, m
			break
		}
	}
	time.Sleep(100 * time.Millisecond)
	_ = w.c.CloseNow()
	waitStatus(t, e, sid, "detached")

	w2, _, err := e.dial(sid)
	if err != nil {
		t.Fatal(err)
	}
	w2.seq = w.seq
	w2.hello(map[string]any{"resume": map[string]any{"last_server_seq": qm.Seq - 1}})
	ready := w2.expect("session.ready")
	rd := ready.data(t)
	if rd["resumed"] != true || uint32(rd["last_client_seq"].(float64)) != w.seq {
		t.Fatal(rd)
	}
	if ready.Seq <= qm.Seq {
		t.Fatal("session.ready lleva un seq nuevo")
	}
	re := w2.expect("question")
	if re.Seq != qm.Seq || re.data(t)["turn_id"] != q["turn_id"] {
		t.Fatalf("reenvío con seq original: %d vs %d", re.Seq, qm.Seq)
	}
	w2.expect("resume")
	waitStatus(t, e, sid, "live")
	// duplicado del cliente: se ignora sin error
	w2.sendRaw("ping", w.seq, map[string]any{"nonce": "dup"})
	w2.send("ping", map[string]any{"nonce": "nuevo"})
	if p := w2.expect("pong").data(t); p["nonce"] != "nuevo" {
		t.Fatal("el duplicado no debía responderse")
	}
	// hueco de seq
	w2.sendRaw("ping", w2.seq+5, map[string]any{"nonce": "x"})
	er := w2.expect("error").data(t)
	if er["code"] != "SEQ_OUT_OF_ORDER" || uint32(er["ref_seq"].(float64)) != w2.seq+1 {
		t.Fatal(er)
	}
	// una conexión nueva reemplaza a la vieja (4009)
	w3, _, _ := e.dial(sid)
	w3.seq = w2.seq
	w3.hello(map[string]any{"resume": map[string]any{"last_server_seq": ready.Seq + 100}})
	if er := w2.expect("error").data(t); er["code"] != "ALREADY_CONNECTED" {
		t.Fatal(er)
	}
	if code := w2.waitClose(); code != 4009 {
		t.Fatalf("cierre %d", code)
	}
	w3.expect("session.ready")
	w3.send("control.end", map[string]any{})
	w3.expect("session.end")
}

func waitStatus(t *testing.T, e *env, sid, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m := e.do("GET", "/v1/sessions/"+sid, nil).json(t)
		if m["status"] == want {
			return m
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("la sesión no llegó a %s", want)
	return nil
}

func TestLiveResumeGapAndGraceExpiry(t *testing.T) {
	e := newEnv(t, func(_ *config.Config, o *live.Options) { o.ReplayMax = 2; o.Grace = 400 * time.Millisecond })
	e.login()
	sid := e.createSession(map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia", "num_questions": 1})
	w, _, _ := e.dial(sid)
	w.hello(nil)
	w.expect("session.ready")
	_, d, _ := w.prompt()
	w.playTTS(d["utterance_id"].(string))
	_ = w.c.CloseNow()
	waitStatus(t, e, sid, "detached")
	w2, _, _ := e.dial(sid)
	w2.hello(map[string]any{"resume": map[string]any{"last_server_seq": 1}})
	if er := w2.expect("error").data(t); er["code"] != "RESUME_GAP" || er["fatal"] != false {
		t.Fatal(er)
	}
	if rd := w2.expect("session.ready").data(t); rd["resumed"] != false {
		t.Fatal(rd)
	}
	if _, q, _ := w2.prompt(); q["repeat"] != true {
		t.Fatal("se reenvía el estado actual")
	}
	_ = w2.c.CloseNow()
	s := waitStatus(t, e, sid, "completed")
	if s["ended_reason"] != "disconnected" {
		t.Fatal(s)
	}
}

func TestLiveAcksKeepTenSecondsFlowingWithoutPartials(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	sid := e.createSession(map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia", "num_questions": 1, "max_followups": 0})
	w, _, _ := e.dial(sid)
	w.hello(map[string]any{"partials": false, "tts_delivery": []string{"none"}})
	w.expect("session.ready")
	_, q, _ := w.prompt()
	for q["kind"] == "intro" {
		_, q, _ = w.prompt()
	}
	tid := q["turn_id"].(string)
	var turn int
	fmtSscan(tid, &turn)
	const window = 96000
	sizes := map[uint32]int{}
	start := time.Now()
	for i := 0; i < 100; i++ { // 10 s de audio en chunks de 100 ms
		for {
			ack := w.ack()
			inflight := 0
			for s, n := range sizes {
				if s > ack {
					inflight += n
				}
			}
			if inflight+3200 <= window {
				break
			}
			if time.Since(start) > 8*time.Second {
				t.Fatalf("el envío se frenó: inflight=%d ack=%d", inflight, ack)
			}
			time.Sleep(2 * time.Millisecond)
		}
		w.frame(uint32(turn), uint32(i), silence(100))
		sizes[w.seq] = 3200
	}
	w.send("audio.end", map[string]any{"turn_id": tid, "chunks": 100, "bytes": 320000, "duration_ms": 10000})
	if fin := w.expect("transcript.final").data(t); fin["duration_ms"].(float64) != 10000 {
		t.Fatal(fin)
	}
	if time.Since(start) > 8*time.Second {
		t.Fatal("demasiado lento")
	}
	acks := 0
	w.mu.Lock()
	for _, m := range w.all {
		if m.Type == "ack" {
			acks++
		}
	}
	w.mu.Unlock()
	if acks == 0 {
		t.Fatal("sin mensajes ack")
	}
	checkSeqs(t, w)
}

func TestLivePartialsAndBusySkip(t *testing.T) {
	e := newEnv(t, func(_ *config.Config, o *live.Options) { o.PartialInterval = 40 * time.Millisecond })
	e.login()
	sid := e.createSession(map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia", "num_questions": 1, "max_followups": 0})
	w, _, _ := e.dial(sid)
	w.hello(map[string]any{"partials": true, "tts_delivery": []string{"none"}})
	w.expect("session.ready")
	_, q, _ := w.prompt()
	for q["kind"] == "intro" {
		_, q, _ = w.prompt()
	}
	var turn int
	fmtSscan(q["turn_id"].(string), &turn)
	for i := 0; i < 5; i++ {
		w.frame(uint32(turn), uint32(i), silence(100))
		time.Sleep(30 * time.Millisecond)
	}
	p := w.expect("transcript.partial").data(t)
	if p["revision"].(float64) != 1 || p["text"] == "" {
		t.Fatal(p)
	}
	e.speech.mu.Lock()
	e.speech.partialBusy = true
	before := e.speech.partials
	e.speech.mu.Unlock()
	for i := 5; i < 15; i++ {
		w.frame(uint32(turn), uint32(i), silence(100))
		time.Sleep(30 * time.Millisecond)
	}
	e.speech.mu.Lock()
	after := e.speech.partials
	e.speech.mu.Unlock()
	if after <= before {
		t.Fatal("se esperaban parciales ocupados")
	}
	w.send("audio.end", map[string]any{"turn_id": q["turn_id"], "chunks": 15, "bytes": 48000, "duration_ms": 1500})
	w.expect("transcript.final")
	w.mu.Lock()
	for _, m := range w.all {
		if m.Type == "error" {
			t.Fatalf("un parcial ocupado no es error: %s", m.Data)
		}
	}
	w.mu.Unlock()
	if st, _ := e.app.Svc.Speech.BreakerStates(); st != 0 {
		t.Fatal("los parciales no cuentan para el breaker")
	}
}

func TestLiveSTTFailureAsksToRepeat(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	sid := e.createSession(map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia", "num_questions": 1, "max_followups": 0})
	w, _, _ := e.dial(sid)
	w.hello(map[string]any{"tts_delivery": []string{"none"}})
	w.expect("session.ready")
	_, q, _ := w.prompt()
	for q["kind"] == "intro" {
		_, q, _ = w.prompt()
	}
	e.speech.mu.Lock()
	e.speech.sttFail = []string{"503", "503"}
	e.speech.mu.Unlock()
	w.answer(q["turn_id"].(string), 500)
	if er := w.expect("error").data(t); er["code"] != "STT_UNAVAILABLE" || er["retryable"] != true {
		t.Fatal(er)
	}
	if te := w.expect("turn.end").data(t); te["next"] != "repeat" {
		t.Fatal(te)
	}
	_, q2, _ := w.prompt()
	if q2["repeat"] != true || q2["turn_id"] == q["turn_id"] {
		t.Fatal(q2)
	}
	// silencio corto → NO_AUDIO y se repite
	w.answer(q2["turn_id"].(string), 50)
	if er := w.expect("error").data(t); er["code"] != "NO_AUDIO" {
		t.Fatal(er)
	}
	if te := w.expect("turn.end").data(t); te["reason"] != "no_audio" || te["next"] != "repeat" {
		t.Fatal(te)
	}
}

func TestLiveProtocolErrors(t *testing.T) {
	e := newEnv(t, func(_ *config.Config, o *live.Options) { o.HelloTimeout = 200 * time.Millisecond })
	e.login()
	sid := e.createSession(map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia"})

	// sin hello a tiempo
	w, _, _ := e.dial(sid)
	if er := w.expect("error").data(t); er["code"] != "HELLO_REQUIRED" || er["fatal"] != true || er["close_code"].(float64) != 4400 {
		t.Fatal(er)
	}
	if code := w.waitClose(); code != 4400 {
		t.Fatalf("cierre %d", code)
	}
	// 3 tipos desconocidos seguidos → 4400
	w, _, _ = e.dial(sid)
	w.hello(nil)
	w.expect("session.ready")
	for i := 0; i < 3; i++ {
		w.send("listen.interviewer", map[string]any{})
	}
	if er := w.expect("error").data(t); er["code"] != "UNSUPPORTED_MESSAGE" {
		t.Fatal(er)
	}
	if code := w.waitClose(); code != 4400 {
		t.Fatalf("cierre %d", code)
	}
	// formato de audio no aceptable
	w, _, _ = e.dial(sid)
	w.hello(map[string]any{"audio_formats": []map[string]any{{"codec": "opus", "sample_rate": 48000, "channels": 1}}})
	if er := w.expect("error").data(t); er["code"] != "UNSUPPORTED_FORMAT" {
		t.Fatal(er)
	}

	// errores antes del upgrade
	tok := e.token
	e.token = ""
	if _, res, err := e.dial(sid); err == nil || res.StatusCode != 401 {
		t.Fatal("401")
	}
	e.token = tok
	if _, res, err := e.dial(sid, "Origin", "https://evil.example"); err == nil || res.StatusCode != 403 {
		t.Fatal("403")
	}
	if _, res, err := e.dial("01900000-0000-7000-8000-000000000000"); err == nil || res.StatusCode != 404 {
		t.Fatal("404")
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/sessions/"+sid+"/live", nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != 426 {
		t.Fatalf("426: %d", res.StatusCode)
	}
	// ticket de un solo uso
	tk := e.do("POST", "/v1/sessions/"+sid+"/live-ticket", nil).json(t)["ticket"].(string)
	e.token = ""
	u := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/v1/sessions/" + sid + "/live?ticket=" + tk
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{Subprotocols: []string{"cx9.live.v1"}})
	if err != nil {
		t.Fatal(err)
	}
	c.CloseNow()
	if _, res, err := websocket.Dial(ctx, u, &websocket.DialOptions{Subprotocols: []string{"cx9.live.v1"}}); err == nil || res.StatusCode != 401 {
		t.Fatal("ticket reutilizado")
	}
	e.token = tok
	e.do("POST", "/v1/sessions/"+sid+"/end", nil)
	if _, res, err := e.dial(sid); err == nil || res.StatusCode != 409 {
		t.Fatal("409 sesión terminada")
	}
}

func TestLiveRESTEndAndShutdown(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	sid := e.createSession(map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia"})
	w, _, _ := e.dial(sid)
	w.hello(map[string]any{"tts_delivery": []string{"url"}})
	w.expect("session.ready")
	_, d, _ := w.prompt()
	tu := w.expect("tts.url").data(t)
	if tu["utterance_id"] != d["utterance_id"] || tu["format"] != "wav" {
		t.Fatal(tu)
	}
	if r := e.do("GET", tu["url"].(string), nil); r.status != 200 || r.header.Get("Content-Type") != "audio/wav" || r.header.Get("X-Audio-Duration-Ms") == "" {
		t.Fatalf("tts url %d", r.status)
	}
	r := e.do("POST", "/v1/sessions/"+sid+"/end", nil)
	if r.status != 200 || r.json(t)["status"] == "live" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if d := w.expect("session.end").data(t); d["reason"] != "user" {
		t.Fatal(d)
	}
	w.waitClose()

	sid2 := e.createSession(map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia"})
	w2, _, _ := e.dial(sid2)
	w2.hello(nil)
	w2.expect("session.ready")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e.app.Hub.Shutdown(ctx)
	if d := w2.expect("session.end").data(t); d["reason"] != "error" {
		t.Fatal(d)
	}
	if code := w2.waitClose(); code != websocket.StatusGoingAway {
		t.Fatalf("cierre %d", code)
	}
	if e.app.Hub.Active() != 0 {
		t.Fatal("quedaron sesiones activas")
	}
}

func TestLiveStoryModeLearnsProfileWithEvidence(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	sid := e.createSession(map[string]any{"lang": "es", "mode": "story", "input": "text", "max_followups": 0})
	w, _, _ := e.dial(sid)
	w.hello(map[string]any{"tts_delivery": []string{"none"}})
	w.expect("session.ready")
	_, q, _ := w.prompt()
	for q["kind"] == "intro" {
		_, q, _ = w.prompt()
	}
	w.send("answer.text", map[string]any{"turn_id": q["turn_id"], "text": "Trabajé en Telecom tres años con Go y PostgreSQL."})
	w.expect("transcript.final")
	w.send("control.end", map[string]any{})
	w.expect("session.end")
	rep := waitReport(t, e, sid)
	if rep["mode"] != "story" {
		t.Fatal(rep["mode"])
	}
	r := e.do("GET", "/v1/profile", nil)
	if r.status != 200 || !strings.Contains(string(r.body), "Telecom") || !strings.Contains(string(r.body), sid) {
		t.Fatalf("perfil con evidencia: %d %s", r.status, r.body)
	}
	if len(rep["profile_changes"].([]any)) == 0 {
		t.Fatal("profile_changes")
	}
}
