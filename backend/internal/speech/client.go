// Package speech es el cliente HTTP del servicio de voz (/speech) según
// docs/contract.md §6: POST /stt, POST /tts, GET /health y GET /voices.
package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/resilience"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/secret"
)

// ContentTypePCM16 es el formato base del contrato (PCM16LE 16 kHz mono).
const ContentTypePCM16 = "audio/L16; rate=16000; channels=1"

// FillerPrompt le muestra a Whisper que conserve las muletillas.
var FillerPrompt = map[string]string{
	"es": "Eh, bueno, este... o sea, digamos que, mmm, trabajé en backend.",
	"en": "Um, uh, well, like, you know, I mean, I worked on the backend.",
}

type Word struct {
	Word       string   `json:"word"`
	StartS     float64  `json:"start_s"`
	EndS       float64  `json:"end_s"`
	Confidence *float64 `json:"confidence"`
	IsFiller   *bool    `json:"is_filler,omitempty"` // extensión de /speech
}

// Filler es una muletilla detectada por /speech (con contexto de pausas).
type Filler struct {
	Text   string  `json:"text"`
	StartS float64 `json:"start_s"`
	EndS   float64 `json:"end_s"`
}

// Pause es un hueco ≥ umbral (0,6 s por defecto en /speech).
type Pause struct {
	StartS    float64 `json:"start_s"`
	EndS      float64 `json:"end_s"`
	DurationS float64 `json:"duration_s"`
}

type Segment struct {
	StartS     float64  `json:"start_s"`
	EndS       float64  `json:"end_s"`
	Text       string   `json:"text"`
	Confidence *float64 `json:"confidence"`
	Words      []Word   `json:"words"`
}

type STTResult struct {
	Text         string    `json:"text"`
	Language     string    `json:"language"`
	DurationS    float64   `json:"duration_s"`
	Confidence   *float64  `json:"confidence"`
	Segments     []Segment `json:"segments"`
	Engine       string    `json:"engine"`
	Model        string    `json:"model"`
	ProcessingMs int       `json:"processing_ms"`
	// Extensiones de /speech (opcionales; nil si el servicio no las manda).
	Fillers     []Filler `json:"fillers,omitempty"`
	FillerCount *int     `json:"filler_count,omitempty"`
	Pauses      []Pause  `json:"pauses,omitempty"`
	// Endpointing (v1.2, opcionales, sobre todo en parciales): fin de la voz en
	// ms relativo al inicio del audio mandado y silencio final en ms.
	SpeechEndedMs     *float64 `json:"speech_ended_ms,omitempty"`
	TrailingSilenceMs *float64 `json:"trailing_silence_ms,omitempty"`
}

// Endpoint devuelve los campos de endpointing redondeados a ms (nil si no vinieron).
func (r *STTResult) Endpoint() (speechEnded, trailing *int) {
	conv := func(f *float64) *int {
		if f == nil || *f < 0 {
			return nil
		}
		v := int(*f + 0.5)
		return &v
	}
	return conv(r.SpeechEndedMs), conv(r.TrailingSilenceMs)
}

// FillerTexts devuelve las muletillas detectadas por /speech, o nil si no vinieron.
func (r *STTResult) FillerTexts() []string {
	if r.FillerCount == nil && r.Fillers == nil {
		return nil
	}
	out := make([]string, 0, len(r.Fillers))
	for _, f := range r.Fillers {
		out = append(out, strings.ToLower(strings.TrimSpace(f.Text)))
	}
	return out
}

// LowConfidence aplica la regla de interview-sim: confianza < 0.5 y largo > 2.
func (r *STTResult) LowConfidence() []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range r.Segments {
		for _, w := range s.Words {
			if w.Confidence == nil || *w.Confidence >= 0.5 {
				continue
			}
			t := strings.Trim(strings.TrimSpace(w.Word), ".,;:!?¿¡")
			if len([]rune(t)) > 2 && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

type TTSRequest struct {
	Text       string  `json:"text"`
	Language   string  `json:"language"`
	Voice      string  `json:"voice,omitempty"`
	Format     string  `json:"format"`
	SampleRate int     `json:"sample_rate,omitempty"`
	Speed      float64 `json:"speed"`
}

type TTSResult struct {
	Audio       []byte
	ContentType string
	Format      string
	DurationMs  int
	SampleRate  int
	Voice       string
	Engine      string
}

type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Mode    string `json:"mode"`
	STT     struct {
		Ready        bool     `json:"ready"`
		Engine       string   `json:"engine"`
		Model        string   `json:"model"`
		Languages    []string `json:"languages"`
		InputFormats []string `json:"input_formats"`
	} `json:"stt"`
	TTS struct {
		Ready         bool     `json:"ready"`
		Engine        string   `json:"engine"`
		Fallback      string   `json:"fallback"`
		OutputFormats []string `json:"output_formats"`
	} `json:"tts"`
}

type Voice struct {
	ID       string `json:"id"`
	Language string `json:"language"`
	Engine   string `json:"engine"`
	Default  bool   `json:"default"`
}

// Error es un error del servicio de voz (problem+json o de transporte).
type Error struct {
	Op      string
	Status  int
	Code    string
	Detail  string
	Timeout bool
	retry   bool
}

func (e *Error) Error() string {
	s := fmt.Sprintf("speech %s: ", e.Op)
	if e.Status > 0 {
		s += fmt.Sprintf("HTTP %d ", e.Status)
	}
	if e.Code != "" {
		s += e.Code + " "
	}
	return strings.TrimSpace(s + e.Detail)
}

func (e *Error) Retryable() bool { return e.retry }

// IsTimeout dice si el error fue por timeout.
func IsTimeout(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Timeout
}

// ErrPartialSkipped: el parcial se descartó (servicio ocupado o caído). No es un error para el usuario.
var ErrPartialSkipped = errors.New("parcial descartado")

// IsNoAudio dice si el servicio rechazó el audio por vacío/corto.
func IsNoAudio(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == "NO_AUDIO"
}

// Observer recibe métricas de cada llamada.
type Observer interface {
	SpeechCall(op, outcome string, d time.Duration)
	SpeechError(op, code string)
}

type nop struct{}

func (nop) SpeechCall(string, string, time.Duration) {}
func (nop) SpeechError(string, string)               {}

// Client habla con el servicio de voz con los timeouts y reintentos del contrato.
type Client struct {
	Base    string
	Token   secret.String
	HC      *http.Client
	Obs     Observer
	Backoff resilience.Backoff
	Connect time.Duration

	sttBreaker, ttsBreaker *resilience.Breaker
}

func New(base string, token secret.String, obs Observer) *Client {
	if obs == nil {
		obs = nop{}
	}
	return &Client{
		Base: strings.TrimRight(base, "/"), Token: token, Obs: obs,
		HC:         &http.Client{Transport: &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second}},
		Backoff:    resilience.Backoff{Base: 300 * time.Millisecond, Jitter: 100 * time.Millisecond},
		sttBreaker: resilience.NewBreaker(5, 30*time.Second),
		ttsBreaker: resilience.NewBreaker(5, 30*time.Second),
	}
}

// BreakerStates expone el estado de los breakers (para /v1/ready y métricas).
func (c *Client) BreakerStates() (stt, tts resilience.State) {
	return c.sttBreaker.State(), c.ttsBreaker.State()
}

// STTTimeout es min(60 s, 10 s + 0,5 × duración del audio).
func STTTimeout(audioSeconds float64) time.Duration {
	return time.Duration(math.Min(60, 10+0.5*audioSeconds) * float64(time.Second))
}

func (c *Client) do(ctx context.Context, op string, req *http.Request) (*http.Response, error) {
	if c.Token.IsSet() {
		req.Header.Set("Authorization", "Bearer "+c.Token.Reveal())
	}
	if rid, ok := ctx.Value(requestIDKey{}).(string); ok && rid != "" {
		req.Header.Set("X-Request-Id", rid)
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		timeout := errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded
		return nil, &Error{Op: op, Detail: "sin conexión", Timeout: timeout, retry: !errors.Is(err, context.Canceled)}
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer resp.Body.Close()
	var p struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
		Title  string `json:"title"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = json.Unmarshal(data, &p)
	if p.Detail == "" {
		p.Detail = p.Title
	}
	e := &Error{Op: op, Status: resp.StatusCode, Code: p.Code, Detail: p.Detail,
		retry: resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504}
	e.Timeout = resp.StatusCode == 504
	return nil, e
}

type requestIDKey struct{}

// WithRequestID propaga X-Request-Id al servicio de voz.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// STTOptions son los parámetros de /stt.
type STTOptions struct {
	ContentType string
	Language    string
	Prompt      string
	Partial     bool
	AudioSecs   float64
	MockText    string // solo para tests contra el mock (X-Mock-Transcript)
}

// STT transcribe. Final: 1 reintento ante conexión/502/503/504. Parcial: 5 s y sin reintento.
func (c *Client) STT(ctx context.Context, audio []byte, o STTOptions) (*STTResult, error) {
	if o.ContentType == "" {
		o.ContentType = ContentTypePCM16
	}
	q := url.Values{"language": {o.Language}, "word_confidence": {"true"}}
	if o.Prompt != "" {
		q.Set("prompt", o.Prompt)
	}
	if o.Partial {
		q.Set("partial", "true")
	}
	retries, timeout := 1, STTTimeout(o.AudioSecs)
	if o.Partial {
		retries, timeout = 0, 5*time.Second
	}
	var out STTResult
	start := time.Now()
	br := c.sttBreaker
	if o.Partial {
		br = nil // los parciales no cuentan para el circuit breaker
	}
	err := resilience.Do(ctx, br, retries, c.Backoff, func(int) time.Duration { return timeout }, func(actx context.Context) error {
		req, _ := http.NewRequestWithContext(actx, http.MethodPost, c.Base+"/stt?"+q.Encode(), bytes.NewReader(audio))
		req.Header.Set("Content-Type", o.ContentType)
		if o.MockText != "" {
			req.Header.Set("X-Mock-Transcript", url.QueryEscape(o.MockText))
		}
		resp, err := c.do(actx, "stt", req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
			return &Error{Op: "stt", Detail: "respuesta inválida"}
		}
		return nil
	})
	if o.Partial && err != nil {
		c.Obs.SpeechCall("stt_partial", "skipped", time.Since(start))
		return nil, ErrPartialSkipped
	}
	if o.Partial {
		c.Obs.SpeechCall("stt_partial", "ok", time.Since(start))
		return &out, nil
	}
	c.observe("stt", start, err)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// TTS sintetiza con 1 reintento y 15 s de timeout.
func (c *Client) TTS(ctx context.Context, r TTSRequest) (*TTSResult, error) {
	if r.Format == "" {
		r.Format = "wav"
	}
	if r.Speed == 0 {
		r.Speed = 1
	}
	body, _ := json.Marshal(r)
	var out TTSResult
	start := time.Now()
	err := resilience.Do(ctx, c.ttsBreaker, 1, c.Backoff, func(int) time.Duration { return 15 * time.Second }, func(actx context.Context) error {
		req, _ := http.NewRequestWithContext(actx, http.MethodPost, c.Base+"/tts", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.do(actx, "tts", req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		audio, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			return &Error{Op: "tts", Detail: "lectura incompleta", retry: true}
		}
		out = TTSResult{Audio: audio, ContentType: resp.Header.Get("Content-Type"), Format: r.Format,
			Voice: resp.Header.Get("X-Voice"), Engine: resp.Header.Get("X-Engine")}
		out.DurationMs, _ = strconv.Atoi(resp.Header.Get("X-Audio-Duration-Ms"))
		out.SampleRate, _ = strconv.Atoi(resp.Header.Get("X-Sample-Rate"))
		return nil
	})
	c.observe("tts", start, err)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) observe(op string, start time.Time, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
		code := "UNAVAILABLE"
		var e *Error
		switch {
		case errors.As(err, &e) && e.Code != "":
			code = e.Code
		case IsTimeout(err):
			code = "TIMEOUT"
		case errors.Is(err, resilience.ErrOpen):
			code = "BREAKER_OPEN"
		}
		c.Obs.SpeechError(op, code)
	}
	c.Obs.SpeechCall(op, outcome, time.Since(start))
}

// Health consulta GET /health (2 s, sin reintento). 503 devuelve el cuerpo igual.
func (c *Client) Health(ctx context.Context) (*Health, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/health", nil)
	if c.Token.IsSet() {
		req.Header.Set("Authorization", "Bearer "+c.Token.Reveal())
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, &Error{Op: "health", Detail: "sin conexión"}
	}
	defer resp.Body.Close()
	var h Health
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&h); err != nil {
		return nil, &Error{Op: "health", Status: resp.StatusCode, Detail: "respuesta inválida"}
	}
	return &h, nil
}

// Voices consulta GET /voices.
func (c *Client) Voices(ctx context.Context) ([]Voice, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/voices", nil)
	resp, err := c.do(ctx, "voices", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Items []Voice `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, &Error{Op: "voices", Detail: "respuesta inválida"}
	}
	return out.Items, nil
}
