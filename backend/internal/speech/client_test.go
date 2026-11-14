package speech

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/resilience"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/secret"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/speech/speechmock"
)

func newMock(t *testing.T) (*Client, *httptest.Server) {
	srv := httptest.NewServer(speechmock.Handler())
	t.Cleanup(srv.Close)
	c := New(srv.URL, secret.String{}, nil)
	c.Backoff = resilience.Backoff{Base: time.Millisecond}
	return c, srv
}

func TestSTTMockDeterministic(t *testing.T) {
	c, _ := newMock(t)
	audio := bytes.Repeat([]byte{1, 0}, 16000) // 1 s
	a, err := c.STT(context.Background(), audio, STTOptions{Language: "es", AudioSecs: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := c.STT(context.Background(), audio, STTOptions{Language: "es", AudioSecs: 1})
	if a.Text != b.Text || a.DurationS != 1 || a.Text[:19] != "respuesta simulada " {
		t.Fatalf("%+v", a)
	}
	r, err := c.STT(context.Background(), audio, STTOptions{Language: "es", MockText: "trabajé en ?Telecom con Go"})
	// no dependemos de si el mock deja o no el "?" en text: solo de la confianza por palabra
	if err != nil || !strings.Contains(r.Text, "Telecom") {
		t.Fatalf("%v %+v", err, r)
	}
	if lc := r.LowConfidence(); len(lc) != 1 || lc[0] != "Telecom" {
		t.Fatalf("low conf %v", lc)
	}
	if _, err := c.STT(context.Background(), []byte{0, 0}, STTOptions{Language: "es"}); !IsNoAudio(err) {
		t.Fatalf("no audio: %v", err)
	}
}

func TestTTSMockDeterministic(t *testing.T) {
	c, _ := newMock(t)
	a, err := c.TTS(context.Background(), TTSRequest{Text: "hola qué tal", Language: "es"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := c.TTS(context.Background(), TTSRequest{Text: "hola qué tal", Language: "es"})
	if sha256.Sum256(a.Audio) != sha256.Sum256(b.Audio) || a.DurationMs != 480 || a.Engine != "mock" || a.ContentType != "audio/wav" {
		t.Fatalf("%+v", a)
	}
	if len(a.Audio) != 44+16000*480/1000*2 {
		t.Fatalf("len %d", len(a.Audio))
	}
	if _, err := c.TTS(context.Background(), TTSRequest{Text: "x", Language: "es", Voice: "nope"}); err == nil {
		t.Fatal("voz desconocida")
	}
	h, err := c.Health(context.Background())
	if err != nil || h.Mode != "mock" {
		t.Fatal(err)
	}
	v, err := c.Voices(context.Background())
	if err != nil || len(v) != 2 {
		t.Fatal(err)
	}
}

func TestRetryOn503AndNotOn4xx(t *testing.T) {
	var n atomic.Int32
	mock := speechmock.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			r.Header.Set("X-Mock-Fail", "503")
		}
		mock.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := New(srv.URL, secret.New("tok"), nil)
	c.Backoff = resilience.Backoff{Base: time.Millisecond}
	if _, err := c.TTS(context.Background(), TTSRequest{Text: "hola", Language: "es"}); err != nil || n.Load() != 2 {
		t.Fatalf("err=%v n=%d", err, n.Load())
	}
	n.Store(10)
	if _, err := c.TTS(context.Background(), TTSRequest{Text: "", Language: "es"}); err == nil || n.Load() != 11 {
		t.Fatalf("4xx no reintenta: n=%d", n.Load())
	}
}

func TestPartialTimeoutNoRetry(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()
	c := New(srv.URL, secret.String{}, nil)
	for i := 0; i < 10; i++ {
		_, err := c.STT(context.Background(), make([]byte, 3200), STTOptions{Language: "es", Partial: true})
		if err != ErrPartialSkipped {
			t.Fatalf("parcial ocupado se descarta: %v", err)
		}
	}
	if n.Load() != 10 {
		t.Fatalf("parcial no reintenta: %d", n.Load())
	}
	if st, _ := c.BreakerStates(); st != resilience.Closed {
		t.Fatal("los parciales no abren el breaker")
	}
	if STTTimeout(1) != 10500*time.Millisecond || STTTimeout(1000) != 60*time.Second {
		t.Fatal("timeout STT")
	}
}

func TestExtensionsTolerated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"eh bueno sí","language":"es","duration_s":2,"confidence":0.8,"segments":[{"start_s":0,"end_s":2,"text":"eh bueno sí","confidence":0.8,"words":[{"word":"eh","start_s":0,"end_s":0.2,"confidence":0.9,"is_filler":true}]}],"engine":"x","model":"y","processing_ms":3,"fillers":[{"text":"Eh","start_s":0,"end_s":0.2}],"filler_count":1,"pauses":[{"start_s":0.2,"end_s":1.0,"duration_s":0.8}],"campo_futuro":{"a":1}}`))
	}))
	defer srv.Close()
	c := New(srv.URL, secret.String{}, nil)
	r, err := c.STT(context.Background(), make([]byte, 32000), STTOptions{Language: "es"})
	if err != nil || *r.FillerCount != 1 || r.FillerTexts()[0] != "eh" || len(r.Pauses) != 1 || !*r.Segments[0].Words[0].IsFiller {
		t.Fatalf("%v %+v", err, r)
	}
	r.FillerCount, r.Fillers = nil, nil
	if r.FillerTexts() != nil {
		t.Fatal("sin extensión = nil")
	}
}

func TestSTTPartialEndpointFieldsFromMock(t *testing.T) {
	c, _ := newMock(t)
	// 1 s de "voz" (onda cuadrada de amplitud 4000) + 0,8 s de silencio
	audio := append(bytes.Repeat([]byte{0xa0, 0x0f, 0x60, 0xf0}, 8000), make([]byte, 25600)...)
	r, err := c.STT(context.Background(), audio, STTOptions{Language: "es", Partial: true, AudioSecs: 1.8})
	if err != nil {
		t.Fatal(err)
	}
	se, tr := r.Endpoint()
	if se == nil || tr == nil || *se != 1000 || *tr != 800 {
		t.Fatalf("%v %v", se, tr)
	}
	f, err := c.STT(context.Background(), audio, STTOptions{Language: "es", AudioSecs: 1.8})
	if err != nil {
		t.Fatal(err)
	}
	if se, tr := f.Endpoint(); se != nil || tr != nil {
		t.Fatal("el final no trae campos de endpointing en el mock")
	}
	neg := -1.0
	if se, _ := (&STTResult{SpeechEndedMs: &neg}).Endpoint(); se != nil {
		t.Fatal("negativo = ausente")
	}
}
