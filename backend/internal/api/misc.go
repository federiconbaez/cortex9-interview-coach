package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/auth"
)

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok", "version": s.svc.Cfg.Version})
}

type check struct {
	Status    string `json:"status"`
	LatencyMs *int64 `json:"latency_ms,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// ready: db y voz obligatorios (down → 503); degraded del servicio de voz es usable.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	checks := map[string]check{}
	ms := func(t time.Time) *int64 { v := time.Since(t).Milliseconds(); return &v }

	t := time.Now()
	if err := s.svc.Store.DB.PingContext(ctx); err != nil {
		checks["db"] = check{Status: "down", Detail: "sin conexión a la base"}
	} else {
		checks["db"] = check{Status: "ok", LatencyMs: ms(t)}
	}
	t = time.Now()
	if h, err := s.svc.Speech.Health(ctx); err != nil {
		checks["speech"] = check{Status: "down", Detail: "el servicio de voz no responde"}
	} else {
		st := h.Status
		if st != "ok" && st != "degraded" {
			st = "down"
		}
		checks["speech"] = check{Status: st, LatencyMs: ms(t), Detail: "stt=" + h.STT.Engine + "/" + h.STT.Model + " tts=" + h.TTS.Engine + " mode=" + h.Mode}
	}
	def := s.svc.LLM.Default()
	lc := check{Status: "ok"}
	if def == "ollama" {
		t = time.Now()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.svc.Cfg.LLM.OllamaURL, "/")+"/api/tags", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lc = check{Status: "down", Detail: "Ollama no responde; se usará mock"}
		} else {
			resp.Body.Close()
			lc.LatencyMs = ms(t)
		}
	}
	for _, p := range s.svc.LLM.List() {
		if p.ID == def && p.Breaker == "open" {
			lc = check{Status: "degraded", Detail: "circuit breaker abierto; se usa la lógica local"}
		}
	}
	checks["llm:"+def] = lc
	if !s.svc.Cfg.DevNoAuth && !s.svc.Cfg.PasswordHash.IsSet() {
		checks["auth"] = check{Status: "degraded", Detail: "CX9_PASSWORD_HASH no definido: no se puede iniciar sesión"}
	}
	status, code := "ok", 200
	for name, c := range checks {
		switch {
		case c.Status == "down" && (name == "db" || name == "speech"):
			status, code = "down", 503
		case c.Status != "ok" && status == "ok":
			status = "degraded"
		}
	}
	writeJSON(w, code, map[string]any{"status": status, "version": s.svc.Cfg.Version, "checks": checks})
}

// ---- auth ----

func isLocalHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, tok string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		MaxAge: maxAge, Secure: !isLocalHost(r.Host)})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if d := s.limiter.Blocked(); d > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(d.Seconds()+0.999)))
		problem(w, r, 429, "RATE_LIMITED", "demasiados intentos fallidos; esperá un minuto")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	b, ok := readBody(w, r, 4096)
	if !ok {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Password == "" {
		problem(w, r, 422, "VALIDATION_FAILED", "falta password", fieldErr("/password", "obligatorio"))
		return
	}
	good := s.svc.Cfg.DevNoAuth
	if !good {
		if !s.svc.Cfg.PasswordHash.IsSet() {
			s.log.Warn("login sin CX9_PASSWORD_HASH configurado")
		} else if ok, err := auth.VerifyPassword(s.svc.Cfg.PasswordHash.Reveal(), body.Password); err != nil {
			s.log.Error("CX9_PASSWORD_HASH inválido", "error", err.Error())
		} else {
			good = ok
		}
	}
	if !good {
		s.limiter.Fail()
		problem(w, r, 401, "UNAUTHENTICATED", "contraseña incorrecta")
		return
	}
	s.limiter.Reset()
	tok, hash, err := auth.NewToken()
	if err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudo generar el token")
		return
	}
	exp := s.svc.Now().Add(s.svc.Cfg.TokenTTL).UTC().Truncate(time.Second)
	if err := s.svc.Store.CreateToken(r.Context(), hash, "session", nil, exp); err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudo guardar el token")
		return
	}
	_ = s.svc.Store.PurgeExpired(r.Context(), s.svc.Now())
	s.setCookie(w, r, tok, int(s.svc.Cfg.TokenTTL.Seconds()))
	writeJSON(w, 200, map[string]any{"token": tok, "expires_at": exp})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	tok := bearer(r)
	if !s.svc.Cfg.DevNoAuth {
		if _, ok := s.checkAuth(r); !ok {
			problem(w, r, 401, "UNAUTHENTICATED", "no hay sesión")
			return
		}
	}
	if tok != "" {
		_ = s.svc.Store.DeleteToken(r.Context(), auth.HashToken(tok))
	}
	s.setCookie(w, r, "", -1)
	w.WriteHeader(204)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	exp, _ := auth.Expiry(r.Context())
	writeJSON(w, 200, map[string]any{"user": "local", "expires_at": exp.UTC().Truncate(time.Second)})
}

// ---- idiomas y proveedores ----

var langNames = map[string]string{"es": "Español", "en": "English"}

func (s *Server) languages(w http.ResponseWriter, r *http.Request) {
	scs, err := s.svc.Scenarios(r.Context())
	if err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudieron leer los escenarios")
		return
	}
	h, herr := s.svc.Speech.Health(r.Context())
	voices, verr := s.svc.Speech.Voices(r.Context())
	items := []map[string]any{}
	for _, code := range []string{"es", "en"} {
		n := 0
		for _, sc := range scs {
			for _, l := range sc.Languages {
				if l == code {
					n++
				}
			}
		}
		stt := false
		if herr == nil && h.STT.Ready {
			for _, l := range h.STT.Languages {
				if l == code || l == "auto" {
					stt = true
				}
			}
		}
		vs := []string{}
		var def *string
		if verr == nil {
			for _, v := range voices {
				if v.Language == code {
					vs = append(vs, v.ID)
					if v.Default {
						id := v.ID
						def = &id
					}
				}
			}
		}
		items = append(items, map[string]any{"code": code, "name": langNames[code], "stt": stt, "tts_voices": vs, "default_voice": def,
			"slow_speech": code == "en", "english_feedback": code == "en", "scenarios": n})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) providers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"default": s.svc.LLM.Default(), "items": s.svc.LLM.List()})
}

// ---- idempotencia ----

var idemRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

type captureWriter struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (c *captureWriter) WriteHeader(s int) { c.status = s; c.ResponseWriter.WriteHeader(s) }
func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = 200
	}
	c.buf.Write(b)
	return c.ResponseWriter.Write(b)
}

func (s *Server) idempotent(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			h(w, r)
			return
		}
		if !idemRe.MatchString(key) {
			problem(w, r, 400, "BAD_REQUEST", "Idempotency-Key: 8-128 caracteres [A-Za-z0-9_-]")
			return
		}
		body, ok := readBody(w, r, maxScenario)
		if !ok {
			return
		}
		sum := sha256.Sum256(append([]byte(mediaType(r)+"\n"), body...))
		hash := hex.EncodeToString(sum[:])
		scope := r.Method + " " + r.URL.Path + " " + key
		rec, created, err := s.svc.Store.IdemBegin(r.Context(), scope, hash, s.svc.Now())
		if err != nil {
			problem(w, r, 500, "INTERNAL", "idempotencia no disponible")
			return
		}
		if !created {
			switch {
			case rec.BodyHash != hash:
				problem(w, r, 422, "IDEMPOTENCY_KEY_REUSED", "la misma Idempotency-Key se usó con otro cuerpo")
			case rec.State != "done":
				w.Header().Set("Retry-After", "1")
				problem(w, r, 409, "IDEMPOTENCY_IN_PROGRESS", "la petición original sigue en curso")
			default:
				for k, v := range rec.Headers {
					w.Header().Set(k, v)
				}
				w.Header().Set("Idempotent-Replayed", "true")
				w.WriteHeader(rec.Status)
				_, _ = w.Write([]byte(rec.Body))
			}
			return
		}
		r.Body = readCloser{bytes.NewReader(body)}
		r.ContentLength = int64(len(body))
		cw := &captureWriter{ResponseWriter: w}
		h(cw, r)
		if cw.status >= 500 || cw.status == 0 {
			_ = s.svc.Store.IdemRelease(context.WithoutCancel(r.Context()), scope)
			return
		}
		hdr := map[string]string{}
		for _, k := range []string{"Content-Type", "Location", "ETag"} {
			if v := w.Header().Get(k); v != "" {
				hdr[k] = v
			}
		}
		_ = s.svc.Store.IdemComplete(context.WithoutCancel(r.Context()), scope, cw.status, hdr, cw.buf.String())
	}
}

type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }
