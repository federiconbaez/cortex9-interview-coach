package api_test

import (
	"testing"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/config"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/live"
)

// Contrato v1.2 §6.10: espera de tts.played.

func ttsSession(t *testing.T, e *env, delivery string) *wsClient {
	t.Helper()
	e.login()
	sid := e.createSession(map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia", "num_questions": 1, "max_followups": 0})
	w, _, err := e.dial(sid)
	if err != nil {
		t.Fatal(err)
	}
	w.hello(map[string]any{"partials": false, "tts_delivery": []string{delivery}})
	w.expect("session.ready")
	return w
}

func TestTTSPlayedNoneAdvancesWithoutWaiting(t *testing.T) {
	e := newEnv(t, func(_ *config.Config, o *live.Options) {
		o.TTSPlayedTimeout = time.Minute
		o.TTSPlayedGrace = time.Minute
	})
	e.speech.say("Hice APIs en Go.")
	w := ttsSession(t, e, "none")
	start := time.Now()
	_, intro, _ := w.prompt()
	_, q, _ := w.prompt() // sin mandar tts.played de la intro
	if intro["kind"] != "intro" || q["kind"] != "main" {
		t.Fatal(intro, q)
	}
	w.answer(q["turn_id"].(string), 500) // escucha enseguida, sin tts.played
	w.expect("transcript.final")
	if time.Since(start) > 5*time.Second {
		t.Fatal("con none no se espera nada")
	}
	w.mu.Lock()
	for _, m := range w.all {
		if m.Type == "tts.audio" || m.Type == "tts.url" {
			t.Fatal("none no manda audio")
		}
	}
	w.mu.Unlock()
	w.noErrors()
	if v := e.metric("cx9_tts_played_timeouts_total"); v != 0 {
		t.Fatal(v)
	}
}

func TestTTSPlayedTimeoutAdvancesAnyway(t *testing.T) {
	e := newEnv(t, func(_ *config.Config, o *live.Options) { o.TTSPlayedGrace = 100 * time.Millisecond })
	e.speech.say("Hice APIs en Go.")
	w := ttsSession(t, e, "binary")
	_, intro, _ := w.prompt()
	ia := w.expect("tts.audio")
	at := time.Now()
	dur := time.Duration(ia.data(t)["duration_ms"].(float64)) * time.Millisecond
	_, q, _ := w.prompt() // nunca mandamos tts.played
	if waited := time.Since(at); waited < dur {
		t.Fatalf("avanzó antes de duration_ms: %v < %v", waited, dur)
	}
	if intro["kind"] != "intro" || q["kind"] != "main" {
		t.Fatal(intro, q)
	}
	w.expect("tts.audio")
	// tampoco tts.played de la pregunta: después de duration_ms + gracia escucha igual
	e.waitMetric("cx9_tts_played_timeouts_total", 2)
	w.answer(q["turn_id"].(string), 500)
	if fin := w.expect("transcript.final").data(t); fin["turn_id"] != q["turn_id"] {
		t.Fatal(fin)
	}
	w.noErrors()
}

func TestTTSPlayedDuplicateIgnored(t *testing.T) {
	e := newEnv(t, func(_ *config.Config, o *live.Options) { o.TTSPlayedGrace = time.Minute })
	e.speech.say("Hice APIs en Go.")
	w := ttsSession(t, e, "binary")
	_, intro, _ := w.prompt()
	w.receiveTTS(intro["utterance_id"].(string))
	w.send("tts.played", map[string]any{"utterance_id": intro["utterance_id"]})
	w.send("tts.played", map[string]any{"utterance_id": intro["utterance_id"]}) // duplicado
	_, q, _ := w.prompt()
	w.receiveTTS(q["utterance_id"].(string))
	// el duplicado de la intro no adelantó la pregunta: sin su tts.played todavía no escucha
	var n int
	_, _ = fmtSscan(q["turn_id"].(string), &n)
	w.frame(uint32(n), 0, silence(100))
	if d := w.expect("error").data(t); d["code"] != "STATE_CONFLICT" {
		t.Fatal(d)
	}
	w.send("tts.played", map[string]any{"utterance_id": q["utterance_id"]})
	w.send("tts.played", map[string]any{"utterance_id": q["utterance_id"]})     // duplicado
	w.send("tts.played", map[string]any{"utterance_id": intro["utterance_id"]}) // viejo
	w.answer(q["turn_id"].(string), 500)
	fin := w.expect("transcript.final").data(t)
	if fin["turn_id"] != q["turn_id"] {
		t.Fatal(fin)
	}
	w.mu.Lock()
	errs := 0
	for _, m := range w.all {
		if m.Type == "error" {
			errs++
		}
	}
	w.mu.Unlock()
	if errs != 1 { // solo el STATE_CONFLICT provocado; los duplicados no son error
		t.Fatalf("errores: %d", errs)
	}
	if v := e.metric("cx9_tts_played_timeouts_total"); v != 0 {
		t.Fatal(v)
	}
}
