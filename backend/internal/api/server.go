// Package api expone la API REST /v1 (docs/openapi.yaml) y el handshake del
// WebSocket en vivo. Solo práctica: no hay rutas para escuchar a un
// entrevistador real ni para sugerir respuestas en vivo.
package api

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/auth"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/coach"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/live"
)

const (
	CookieName  = "cx9_session"
	maxBody     = 1 << 20
	maxScenario = 256 << 10
)

// Server es el handler HTTP.
type Server struct {
	svc     *coach.Service
	hub     *live.Hub
	limiter *auth.Limiter
	mux     *http.ServeMux
	log     *slog.Logger
	cors    map[string]bool
}

func New(svc *coach.Service, hub *live.Hub) *Server {
	s := &Server{svc: svc, hub: hub, limiter: auth.NewLimiter(), mux: http.NewServeMux(), log: svc.Log, cors: map[string]bool{}}
	for _, o := range svc.Cfg.CORSOrigins {
		s.cors[o] = true
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /v1/health", s.health)
	m.HandleFunc("GET /v1/ready", s.ready)
	m.Handle("GET /metrics", s.svc.M.Handler())
	m.HandleFunc("POST /v1/auth/login", s.write(s.login))
	m.HandleFunc("POST /v1/auth/logout", s.write(s.logout))
	m.HandleFunc("GET /v1/auth/me", s.authed(s.me))
	m.HandleFunc("GET /v1/languages", s.authed(s.languages))
	m.HandleFunc("GET /v1/scenarios", s.authed(s.listScenarios))
	m.HandleFunc("POST /v1/scenarios", s.authed(s.write(s.idempotent(s.createScenario))))
	m.HandleFunc("POST /v1/scenarios/validate", s.authed(s.write(s.validateScenario)))
	m.HandleFunc("GET /v1/scenarios/{scenarioId}", s.authed(s.getScenario))
	m.HandleFunc("PUT /v1/scenarios/{scenarioId}", s.authed(s.write(s.putScenario)))
	m.HandleFunc("DELETE /v1/scenarios/{scenarioId}", s.authed(s.write(s.deleteScenario)))
	m.HandleFunc("GET /v1/profile", s.authed(s.getProfile))
	m.HandleFunc("POST /v1/profile", s.authed(s.write(s.createProfile)))
	m.HandleFunc("PUT /v1/profile", s.authed(s.write(s.putProfile)))
	m.HandleFunc("PATCH /v1/profile", s.authed(s.write(s.patchProfile)))
	m.HandleFunc("DELETE /v1/profile", s.authed(s.write(s.deleteProfile)))
	m.HandleFunc("GET /v1/profile/gaps", s.authed(s.profileGaps))
	m.HandleFunc("GET /v1/sessions", s.authed(s.listSessions))
	m.HandleFunc("POST /v1/sessions", s.authed(s.write(s.idempotent(s.createSession))))
	m.HandleFunc("GET /v1/sessions/{sessionId}", s.authed(s.getSession))
	m.HandleFunc("DELETE /v1/sessions/{sessionId}", s.authed(s.write(s.deleteSession)))
	m.HandleFunc("POST /v1/sessions/{sessionId}/end", s.authed(s.write(s.endSession)))
	m.HandleFunc("GET /v1/sessions/{sessionId}/report", s.authed(s.report))
	m.HandleFunc("POST /v1/sessions/{sessionId}/live-ticket", s.authed(s.write(s.liveTicket)))
	m.HandleFunc("GET /v1/sessions/{sessionId}/live", s.liveWS)
	m.HandleFunc("GET /v1/sessions/{sessionId}/tts/{utteranceId}", s.authed(s.ttsAudio))
	m.HandleFunc("GET /v1/llm/providers", s.authed(s.providers))
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		problem(w, r, http.StatusNotFound, "NOT_FOUND", "ruta inexistente")
	})
}

// ServeHTTP aplica los middlewares comunes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rid := r.Header.Get("X-Request-Id")
	if !ridRe.MatchString(rid) {
		rid = newRequestID()
	}
	w.Header().Set("X-Request-Id", rid)
	sw := &statusWriter{ResponseWriter: w, status: 200}
	defer func() {
		if rec := recover(); rec != nil {
			s.log.Error("panic", "request_id", rid, "panic", fmt.Sprint(rec))
			if !sw.wrote {
				problem(sw, r, 500, "INTERNAL", "error interno")
			}
		}
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		if i := strings.IndexByte(route, ' '); i >= 0 {
			route = route[i+1:]
		}
		d := time.Since(start)
		s.svc.M.HTTPRequests.WithLabelValues(r.Method, route, strconv.Itoa(sw.status)).Inc()
		s.svc.M.HTTPDuration.WithLabelValues(r.Method, route).Observe(d.Seconds())
		if route != "/metrics" && route != "/v1/health" {
			s.log.Info("http", "request_id", rid, "method", r.Method, "route", route, "status", sw.status, "duration_ms", d.Milliseconds())
		}
	}()
	if s.corsHeaders(sw, r) {
		return
	}
	s.mux.ServeHTTP(sw, r)
}

var ridRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// corsHeaders aplica CORS en dev. true si respondió un preflight.
func (s *Server) corsHeaders(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if s.svc.Cfg.Prod() || origin == "" || !s.cors[strings.TrimRight(origin, "/")] {
		return false
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Credentials", "true")
	h.Add("Vary", "Origin")
	h.Set("Access-Control-Expose-Headers", "ETag, Location, Retry-After, X-Request-Id, Idempotent-Replayed")
	if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, If-Match, X-Request-Id")
		h.Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

// originAllowed: sin Origin (curl), mismo host, o lista de CORS en dev.
func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	o := strings.TrimRight(origin, "/")
	if !s.svc.Cfg.Prod() && s.cors[o] {
		return true
	}
	if i := strings.Index(o, "://"); i >= 0 && o[i+3:] == r.Host {
		return true
	}
	return false
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(c int) {
	if !w.wrote {
		w.status, w.wrote = c, true
	}
	w.ResponseWriter.WriteHeader(c)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijack no soportado")
	}
	w.status, w.wrote = http.StatusSwitchingProtocols, true
	return h.Hijack()
}

// ---- auth ----

func bearer(r *http.Request) string {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		return c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func (s *Server) checkAuth(r *http.Request) (time.Time, bool) {
	if s.svc.Cfg.DevNoAuth {
		return s.svc.Now().Add(s.svc.Cfg.TokenTTL).UTC(), true
	}
	tok := bearer(r)
	if tok == "" {
		return time.Time{}, false
	}
	exp, err := s.svc.Store.TokenExpiry(r.Context(), auth.HashToken(tok), s.svc.Now())
	return exp, err == nil
}

func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		exp, ok := s.checkAuth(r)
		if !ok {
			problem(w, r, 401, "UNAUTHENTICATED", "falta cookie cx9_session o Authorization: Bearer válidos")
			return
		}
		h(w, r.WithContext(auth.WithUser(r.Context(), exp)))
	}
}

// write aplica las reglas de escritura: Origin (CSRF) y Content-Type.
func (s *Server) write(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.originAllowed(r) {
			problem(w, r, 403, "ORIGIN_NOT_ALLOWED", "Origin no permitido")
			return
		}
		if r.ContentLength != 0 && r.Method != http.MethodDelete {
			mt := mediaType(r)
			switch mt {
			case "application/json", "application/yaml", "application/merge-patch+json":
			case "":
				if r.ContentLength > 0 {
					problem(w, r, 415, "UNSUPPORTED_MEDIA_TYPE", "falta Content-Type")
					return
				}
			default:
				problem(w, r, 415, "UNSUPPORTED_MEDIA_TYPE", "Content-Type no soportado: "+mt)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		h(w, r)
	}
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) || int64(len(b)) > limit {
		problem(w, r, 413, "PAYLOAD_TOO_LARGE", fmt.Sprintf("máximo %d bytes", limit))
		return nil, false
	}
	if err != nil {
		problem(w, r, 400, "BAD_REQUEST", "no se pudo leer el cuerpo")
		return nil, false
	}
	return b, true
}

func mediaType(r *http.Request) string {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return mt
}
