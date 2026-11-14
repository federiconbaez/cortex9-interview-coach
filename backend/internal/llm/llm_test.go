package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/resilience"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/secret"
)

const key = "sk-test-NO-DEBE-FILTRARSE"

func TestOpenAICompatibleFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key {
			t.Errorf("path/auth: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["response_format"].(map[string]any)["type"] != "json_object" || body["model"] != "m1" {
			t.Errorf("body %v", body)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"action\":\"next\"}"}}]}`)
	}))
	defer srv.Close()
	p := NewOpenAICompatible("xai", srv.URL+"/v1", secret.New(key), "m1", srv.Client())
	got, err := Call[FollowupResult](context.Background(), p, TaskFollowup, "s", FollowupPayload{}, 0.1)
	if err != nil || got.Action != "next" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAnthropicAndOllamaFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/messages":
			if r.Header.Get("x-api-key") != key || r.Header.Get("anthropic-version") == "" {
				t.Error("headers anthropic")
			}
			_, _ = io.WriteString(w, "{\"content\":[{\"type\":\"text\",\"text\":\"```json\\n{\\\"mejorada\\\":\\\"ok\\\"}\\n```\"}]}")
		case "/api/chat":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["format"] != "json" || body["stream"] != false {
				t.Errorf("ollama body %v", body)
			}
			_, _ = io.WriteString(w, `{"message":{"content":"{\"mejorada\":\"ok2\"}"}}`)
		}
	}))
	defer srv.Close()
	a, err := Call[ImproveResult](context.Background(), NewAnthropic(srv.URL, secret.New(key), "c", srv.Client()), TaskImprove, "s", nil, 0)
	if err != nil || a.Mejorada != "ok" {
		t.Fatalf("%v %v", a, err)
	}
	o, err := Call[ImproveResult](context.Background(), NewOllama(srv.URL, "llama", srv.Client()), TaskImprove, "s", nil, 0)
	if err != nil || o.Mejorada != "ok2" {
		t.Fatalf("%v %v", o, err)
	}
}

func TestResilientRetriesAndBreakerAndNoKeyInErrors(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, "sobrecargado")
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{}"}}]}`)
	}))
	defer srv.Close()
	r := NewResilient(NewOpenAICompatible("openai", srv.URL, secret.New(key), "m", srv.Client()), time.Second, 2, nil)
	r.Backoff = resilience.Backoff{Base: time.Millisecond}
	if _, err := r.Complete(context.Background(), Request{}); err != nil || n.Load() != 3 {
		t.Fatalf("err=%v n=%d", err, n.Load())
	}
	// 4xx: no se reintenta y el error no trae la clave
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":"invalid key"}`)
	}))
	defer bad.Close()
	r2 := NewResilient(NewOpenAICompatible("openai", bad.URL, secret.New(key), "m", bad.Client()), time.Second, 2, nil)
	_, err := r2.Complete(context.Background(), Request{})
	if err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("err=%v", err)
	}
	// breaker abre tras 5 fallos retryables
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer down.Close()
	r3 := NewResilient(NewOpenAICompatible("openai", down.URL, secret.New(key), "m", down.Client()), time.Second, 0, nil)
	for i := 0; i < 5; i++ {
		_, _ = r3.Complete(context.Background(), Request{})
	}
	if _, err := r3.Complete(context.Background(), Request{}); !errors.Is(err, resilience.ErrOpen) {
		t.Fatalf("breaker: %v", err)
	}
}

func TestResilientTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	r := NewResilient(NewOllama(slow.URL, "m", slow.Client()), 50*time.Millisecond, 1, nil)
	r.Backoff = resilience.Backoff{Base: time.Millisecond}
	start := time.Now()
	_, err := r.Complete(context.Background(), Request{})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("timeout no aplicado: %v %v", err, time.Since(start))
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry(ProviderConfig{Default: "openai", AnthropicKey: secret.New(key), AnthropicModel: "c", Timeout: time.Second}, nil, nil)
	if r.Default() != "mock" {
		t.Fatal("default no configurado cae a mock")
	}
	if _, err := r.Get("openai"); err == nil {
		t.Fatal("openai no configurado")
	}
	if _, err := r.Get("anthropic"); err != nil {
		t.Fatal(err)
	}
	l := r.List()
	if len(l) != 5 || l[0].ID != "openai" || l[4].ID != "mock" || !l[1].Configured || l[0].Configured {
		t.Fatalf("%+v", l)
	}
	j, _ := json.Marshal(l)
	if strings.Contains(string(j), key) {
		t.Fatal("la lista filtró una clave")
	}
}

func TestMockTasks(t *testing.T) {
	m := NewMock()
	f, _ := Call[FollowupResult](context.Background(), m, TaskFollowup, "", FollowupPayload{Lang: "es", Answers: []string{"Trabajé en backend."}, Probes: []string{"numbers", "why"}, MaxFollowups: 2}, 0)
	if f.Action != "followup" || f.Probe != "numbers" {
		t.Fatalf("%+v", f)
	}
	f, _ = Call[FollowupResult](context.Background(), m, TaskFollowup, "", FollowupPayload{Lang: "es", Answers: []string{"x"}, FollowupsDone: 2, MaxFollowups: 2}, 0)
	if f.Action != "next" {
		t.Fatal("límite")
	}
	imp, _ := Call[ImproveResult](context.Background(), m, TaskImprove, "", ImprovePayload{Lang: "es", Answers: []string{"Trabajé en backend con Go."}}, 0)
	if !strings.Contains(imp.Mejorada, "[FALTA: número concreto]") || imp.Faltantes[0] != "[FALTA: número concreto]" {
		t.Fatalf("%+v", imp)
	}
	ex, _ := Call[Extraction](context.Background(), m, TaskProfile, "", ProfilePayload{Answer: "Trabajé en Telecom como desarrollador backend desde 2021 hasta 2024. Usaba go y redis. Logré bajar la latencia."}, 0)
	if len(ex.Experiences) != 1 || ex.Experiences[0].Company != "Telecom" || ex.Experiences[0].Period != "2021-2024" || len(ex.Experiences[0].Achievements) != 1 {
		t.Fatalf("%+v", ex)
	}
	en := MockEnglish("i think people is happy")
	if len(en.Grammar) != 2 {
		t.Fatalf("%+v", en)
	}
	if _, err := m.Complete(context.Background(), Request{Task: "x"}); err == nil {
		t.Fatal("tarea desconocida")
	}
}
