package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/app"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/auth"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/config"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/live"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/protocol"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/secret"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/speech/speechmock"
)

const testPassword = "prueba"

var testHash string

func init() {
	h, err := auth.HashPassword(testPassword)
	if err != nil {
		panic(err)
	}
	testHash = h
}

// fakeSpeech envuelve el mock del contrato: inyecta transcripciones y fallas.
type fakeSpeech struct {
	mu          sync.Mutex
	transcripts []string
	sttFail     []string // X-Mock-Fail para los próximos /stt finales
	partialBusy bool
	partials    int
	finals      int
	h           http.Handler
}

func (f *fakeSpeech) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/stt" {
		f.mu.Lock()
		if r.URL.Query().Get("partial") == "true" {
			f.partials++
			busy := f.partialBusy
			f.mu.Unlock()
			if busy {
				w.Header().Set("Content-Type", "application/problem+json")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"type":"urn:cx9:problem:dependency-unavailable","title":"busy","status":503,"code":"DEPENDENCY_UNAVAILABLE"}`))
				return
			}
			r.Header.Set("X-Mock-Transcript", "parcial en curso")
			f.h.ServeHTTP(w, r)
			return
		}
		f.finals++
		if len(f.sttFail) > 0 {
			r.Header.Set("X-Mock-Fail", f.sttFail[0])
			f.sttFail = f.sttFail[1:]
		} else if len(f.transcripts) > 0 {
			r.Header.Set("X-Mock-Transcript", url.QueryEscape(f.transcripts[0]))
			f.transcripts = f.transcripts[1:]
		}
		f.mu.Unlock()
	}
	f.h.ServeHTTP(w, r)
}

func (f *fakeSpeech) say(ts ...string) {
	f.mu.Lock()
	f.transcripts = append(f.transcripts, ts...)
	f.mu.Unlock()
}

type env struct {
	t      *testing.T
	app    *app.App
	srv    *httptest.Server
	speech *fakeSpeech
	token  string
}

func newEnv(t *testing.T, mut func(*config.Config, *live.Options)) *env {
	t.Helper()
	fs := &fakeSpeech{h: speechmock.Handler()}
	sp := httptest.NewServer(fs)
	t.Cleanup(sp.Close)
	t.Setenv("CX9_DATABASE_URL", "sqlite://:memory:")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SpeechURL = sp.URL
	cfg.PasswordHash = secret.New(testHash)
	cfg.DataDir = t.TempDir()
	cfg.DatabaseURL = "sqlite://" + cfg.DataDir + "/test.db"
	opt := live.DefaultOptions()
	opt.ListenSlack = 200 * time.Millisecond
	if mut != nil {
		mut(cfg, &opt)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := app.Build(context.Background(), cfg, log, opt)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler)
	e := &env{t: t, app: a, srv: srv, speech: fs}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		a.Shutdown(ctx)
		srv.Close()
	})
	return e
}

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("json: %v: %s", err, r.body)
	}
	return m
}

func (e *env) do(method, path string, body any, hdr ...string) resp {
	e.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
		ct = "application/yaml"
	case []byte:
		rd = bytes.NewReader(b)
		ct = "application/json"
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
		ct = "application/json"
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, res.Header, b}
}

func (e *env) login() {
	e.t.Helper()
	r := e.do("POST", "/v1/auth/login", map[string]string{"password": testPassword})
	if r.status != 200 {
		e.t.Fatalf("login %d %s", r.status, r.body)
	}
	e.token = r.json(e.t)["token"].(string)
}

func (e *env) createSession(body map[string]any) string {
	e.t.Helper()
	r := e.do("POST", "/v1/sessions", body)
	if r.status != 201 {
		e.t.Fatalf("crear sesión %d %s", r.status, r.body)
	}
	return r.json(e.t)["id"].(string)
}

// ---- cliente WS ----

type msg struct {
	Type   string          `json:"type"`
	Seq    uint32          `json:"seq"`
	Ack    *uint32         `json:"ack"`
	Data   json.RawMessage `json:"data"`
	Binary []byte          `json:"-"`
	Header protocol.FrameHeader
}

func (m msg) data(t *testing.T) map[string]any {
	var d map[string]any
	if err := json.Unmarshal(m.Data, &d); err != nil {
		t.Fatalf("%s: %v", m.Type, err)
	}
	return d
}

type wsClient struct {
	t      *testing.T
	e      *env
	c      *websocket.Conn
	sid    string
	seq    uint32
	in     chan msg
	closed chan websocket.StatusCode
	mu     sync.Mutex
	maxAck uint32
	all    []msg
}

func (e *env) dial(sid string, extra ...string) (*wsClient, *http.Response, error) {
	h := http.Header{}
	if e.token != "" {
		h.Set("Authorization", "Bearer "+e.token)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		h.Set(extra[i], extra[i+1])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/v1/sessions/" + sid + "/live"
	c, res, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: h, Subprotocols: []string{protocol.Subprotocol}})
	if err != nil {
		return nil, res, err
	}
	c.SetReadLimit(1 << 20)
	w := &wsClient{t: e.t, e: e, c: c, sid: sid, in: make(chan msg, 4096), closed: make(chan websocket.StatusCode, 1)}
	go w.reader()
	e.t.Cleanup(func() { _ = c.CloseNow() })
	return w, res, nil
}

func (w *wsClient) reader() {
	for {
		typ, data, err := w.c.Read(context.Background())
		if err != nil {
			w.closed <- websocket.CloseStatus(err)
			close(w.in)
			return
		}
		var m msg
		if typ == websocket.MessageBinary {
			h, p, err := protocol.DecodeFrame(data)
			if err != nil {
				w.t.Errorf("frame del servidor inválido: %v", err)
			}
			m = msg{Type: "frame", Seq: h.Seq, Binary: p, Header: h}
		} else if err := json.Unmarshal(data, &m); err != nil {
			w.t.Errorf("json del servidor inválido: %s", data)
		}
		w.mu.Lock()
		if m.Ack != nil && *m.Ack > w.maxAck {
			w.maxAck = *m.Ack
		}
		w.all = append(w.all, m)
		w.mu.Unlock()
		w.in <- m
	}
}

func (w *wsClient) ack() uint32 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.maxAck
}

func (w *wsClient) sendRaw(typ string, seq uint32, data any) {
	w.t.Helper()
	b, _ := json.Marshal(map[string]any{"v": 1, "type": typ, "seq": seq, "ts": protocol.Now(time.Now()), "session_id": w.sid, "data": data})
	if err := w.c.Write(context.Background(), websocket.MessageText, b); err != nil {
		w.t.Fatalf("write %s: %v", typ, err)
	}
}

func (w *wsClient) send(typ string, data any) uint32 {
	w.t.Helper()
	w.seq++
	w.sendRaw(typ, w.seq, data)
	return w.seq
}

func (w *wsClient) hello(extra map[string]any) {
	w.t.Helper()
	d := map[string]any{"client": "test/1", "audio_formats": []map[string]any{{"codec": "pcm16le", "sample_rate": 16000, "channels": 1, "frame_ms": 100}},
		"tts_delivery": []string{"binary", "url"}, "tts_formats": []string{"wav"}, "partials": false, "resume": nil}
	for k, v := range extra {
		d[k] = v
	}
	w.sendRaw("hello", 0, d)
}

func (w *wsClient) frame(turn, idx uint32, pcm []byte) {
	w.t.Helper()
	w.seq++
	b, err := protocol.EncodeFrame(protocol.FrameHeader{Kind: protocol.KindAudioChunk, Seq: w.seq, TurnNo: turn, ChunkIndex: idx, Codec: protocol.CodecPCM16}, pcm)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.c.Write(context.Background(), websocket.MessageBinary, b); err != nil {
		w.t.Fatalf("frame: %v", err)
	}
}

// expect espera un mensaje del tipo (descarta los demás, salvo errores no esperados que registra).
func (w *wsClient) expect(typ string) msg {
	w.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m, ok := <-w.in:
			if !ok {
				w.t.Fatalf("conexión cerrada esperando %s", typ)
			}
			if m.Type == typ {
				return m
			}
		case <-timeout:
			w.mu.Lock()
			var seen []string
			for _, m := range w.all {
				if m.Type != "frame" {
					seen = append(seen, m.Type+":"+string(m.Data))
				}
			}
			w.mu.Unlock()
			w.t.Fatalf("timeout esperando %s; recibidos: %v", typ, seen)
		}
	}
}

func (w *wsClient) waitClose() websocket.StatusCode {
	w.t.Helper()
	for {
		select {
		case _, ok := <-w.in:
			if !ok {
				return <-w.closed
			}
		case <-time.After(10 * time.Second):
			w.t.Fatal("timeout esperando cierre")
		}
	}
}

func silence(ms int) []byte { return make([]byte, ms*32) }

// answer manda audio (chunks de 100 ms) y audio.end para el turno.
func (w *wsClient) answer(turnID string, ms int) {
	w.t.Helper()
	var n int
	_, _ = fmtSscan(turnID, &n)
	pcm := silence(ms)
	chunks := 0
	for i := 0; i < len(pcm); i += 3200 {
		end := i + 3200
		if end > len(pcm) {
			end = len(pcm)
		}
		w.frame(uint32(n), uint32(chunks), pcm[i:end])
		chunks++
	}
	w.send("audio.end", map[string]any{"turn_id": turnID, "chunks": chunks, "bytes": len(pcm), "duration_ms": ms})
}

func fmtSscan(turnID string, n *int) (int, error) {
	var x int
	for _, c := range strings.TrimPrefix(turnID, "t-") {
		x = x*10 + int(c-'0')
	}
	*n = x
	return 1, nil
}

// prompt espera la siguiente question/followup (ignora tts) y devuelve su data.
func (w *wsClient) prompt() (string, map[string]any, msg) {
	w.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m, ok := <-w.in:
			if !ok {
				w.t.Fatal("cerrada esperando pregunta")
			}
			if m.Type == "question" || m.Type == "followup" {
				return m.Type, m.data(w.t), m
			}
			if m.Type == "session.end" {
				return "session.end", m.data(w.t), m
			}
		case <-timeout:
			w.t.Fatal("timeout esperando pregunta")
		}
	}
}

func degradedSpeech(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"degraded","version":"x","mode":"real","stt":{"ready":true,"engine":"faster-whisper","model":"small","languages":["es"]},"tts":{"ready":true,"engine":"espeak-ng"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
