package api_test

import (
	"testing"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/config"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/endpoint"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/live"
)

// waitMetric espera a que la métrica llegue a want (se observa justo después
// de mandar el primer frame, así que puede ir un poco detrás del cliente).
func (e *env) waitMetric(name string, want float64) {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		v := e.metric(name)
		if v == want {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("%s = %v, esperaba %v", name, v, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func endpointSession(t *testing.T, e *env, hello map[string]any) (*wsClient, string, uint32) {
	t.Helper()
	e.login()
	sid := e.createSession(map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia", "num_questions": 1, "max_followups": 1})
	w, _, err := e.dial(sid)
	if err != nil {
		t.Fatal(err)
	}
	h := map[string]any{"partials": false, "tts_delivery": []string{"binary"}}
	for k, v := range hello {
		h[k] = v
	}
	w.hello(h)
	tid, turn := w.firstAnswerTurn()
	return w, tid, turn
}

func TestLiveEndpointClosesTurnBySilenceWithoutAudioEnd(t *testing.T) {
	e := newEnv(t, fastEndpoint(endpoint.Auto))
	const final = "Trabajé tres años en backend."
	e.speech.say(final)
	e.speech.partialText = final // el parcial coincide: se usa la decisión especulativa
	w, tid, turn := endpointSession(t, e, nil)
	idx := w.stream(turn, 0, voice(2000), 2)
	w.stream(turn, idx, silence(1200), 2) // sin audio.end
	fin := w.expect("transcript.final").data(t)
	if fin["turn_id"] != tid || fin["text"] != final {
		t.Fatal(fin)
	}
	te := w.expect("turn.end").data(t)
	if te["reason"] != "endpoint" || te["next"] != "followup" {
		t.Fatal(te)
	}
	fu := w.expect("followup").data(t)
	w.receiveTTS(fu["utterance_id"].(string))
	w.noErrors()
	e.waitMetric(`cx9_endpoints_total{source="stt"}`, 1)
	e.waitMetric(`cx9_endpoint_to_followup_tts_seconds_count{mode="endpoint",provider="mock"}`, 1)
	e.waitMetric(`cx9_speculative_followups_total{result="hit"}`, 1)
	for _, st := range []string{"stt_final", "llm_followup", "tts_first_byte"} {
		if v := e.metric(`cx9_turn_stage_seconds_count{stage="` + st + `"}`); v < 1 {
			t.Fatalf("etapa %s: %v", st, v)
		}
	}
	checkSeqs(t, w)
}

func TestLiveEndpointEnergyFallbackWithoutSTTFields(t *testing.T) {
	e := newEnv(t, fastEndpoint(endpoint.Auto))
	e.speech.noVAD = true // servicio sin speech_ended_ms/trailing_silence_ms
	e.speech.say("Hice APIs en Go.")
	w, _, turn := endpointSession(t, e, nil)
	idx := w.stream(turn, 0, voice(1500), 2)
	w.stream(turn, idx, silence(1200), 2)
	te := w.expect("turn.end").data(t)
	if te["reason"] != "endpoint" {
		t.Fatal(te)
	}
	w.noErrors()
	e.waitMetric(`cx9_endpoints_total{source="energy"}`, 1)
}

func TestLiveEndpointVoiceThatReturnsDoesNotCut(t *testing.T) {
	e := newEnv(t, func(c *config.Config, o *live.Options) {
		fastEndpoint(endpoint.Energy)(c, o)
		o.Endpoint.GraceMs = 800
	})
	e.speech.say("Primero hice esto y después lo otro.")
	w, _, turn := endpointSession(t, e, nil)
	idx := w.stream(turn, 0, voice(1000), 2)
	idx = w.stream(turn, idx, silence(800), 2) // candidato de cierre
	idx = w.stream(turn, idx, voice(400), 2)   // vuelve la voz dentro de la gracia
	w.mu.Lock()
	for _, m := range w.all {
		if m.Type == "turn.end" || m.Type == "transcript.final" {
			t.Fatal("cortó el turno con voz que volvió")
		}
	}
	w.mu.Unlock()
	w.stream(turn, idx, silence(1000), 2)
	fin := w.expect("transcript.final").data(t)
	if d := fin["duration_ms"].(float64); d < 2200 {
		t.Fatalf("el turno se cortó antes: %v ms", d)
	}
	if te := w.expect("turn.end").data(t); te["reason"] != "endpoint" {
		t.Fatal(te)
	}
	w.noErrors()
}

func TestLiveEndpointDisabledByClientUsesExplicitEnd(t *testing.T) {
	e := newEnv(t, fastEndpoint(endpoint.Auto))
	e.speech.say("Trabajé tres años en backend.")
	e.login()
	sid := e.createSession(map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia", "num_questions": 1, "max_followups": 1})
	w, _, _ := e.dial(sid)
	w.hello(map[string]any{"partials": false, "tts_delivery": []string{"binary"}, "endpointing": false})
	ready := w.expect("session.ready").data(t)
	if ep := ready["endpointing"].(map[string]any); ep["enabled"] != false || ep["mode"] != "off" {
		t.Fatal(ep)
	}
	// session.ready ya se consumió: saltear la intro a mano
	var tid string
	var turn uint32
	for {
		_, d, _ := w.prompt()
		w.expect("tts.audio")
		w.send("tts.played", map[string]any{"utterance_id": d["utterance_id"]})
		if d["kind"] != "intro" {
			tid = d["turn_id"].(string)
			var n int
			_, _ = fmtSscan(tid, &n)
			turn = uint32(n)
			break
		}
	}
	idx := w.stream(turn, 0, voice(1000), 4)
	idx = w.stream(turn, idx, silence(1500), 4)
	time.Sleep(600 * time.Millisecond)
	w.mu.Lock()
	for _, m := range w.all {
		if m.Type == "turn.end" {
			t.Fatal("con endpointing apagado no se cierra por silencio")
		}
	}
	w.mu.Unlock()
	w.send("audio.end", map[string]any{"turn_id": tid, "chunks": idx, "bytes": int(idx) * 3200, "duration_ms": int(idx) * 100})
	if te := w.expect("turn.end").data(t); te["reason"] != "audio_end" {
		t.Fatal(te)
	}
	fu := w.expect("followup").data(t)
	w.receiveTTS(fu["utterance_id"].(string))
	e.waitMetric(`cx9_endpoint_to_followup_tts_seconds_count{mode="explicit",provider="mock"}`, 1)
	w.noErrors()
}
