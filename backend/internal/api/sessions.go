package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/auth"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/coach"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/protocol"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/store"
)

func sessionJSON(x *store.Session) map[string]any {
	var report any
	switch x.Status {
	case coach.StEnded, coach.StScoring, coach.StCompleted, coach.StFailed:
		report = "/v1/sessions/" + x.ID + "/report"
	}
	return map[string]any{"id": x.ID, "status": x.Status, "mode": x.Mode, "scenario_id": x.ScenarioID, "scenario_title": x.ScenarioTitle,
		"lang": x.Lang, "input": x.Input, "llm_provider": x.LLMProvider, "llm_model": x.LLMModel, "max_followups": x.MaxFollowups,
		"question_count": x.QuestionCount, "questions_answered": x.QuestionsAnswered, "ended_reason": x.EndedReason,
		"overall_score": x.OverallScore, "created_at": x.CreatedAt.UTC(), "started_at": utcPtr(x.StartedAt), "ended_at": utcPtr(x.EndedAt),
		"live_url": "/v1/sessions/" + x.ID + "/live", "report_url": report}
}

func utcPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

var validStatus = map[string]bool{"created": true, "live": true, "detached": true, "ended": true, "scoring": true, "completed": true, "failed": true}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.Filter{Status: q.Get("status"), ScenarioID: q.Get("scenario_id"), Lang: q.Get("lang"), Mode: q.Get("mode"), Limit: 20, Cursor: q.Get("cursor")}
	bad := func(m string) { problem(w, r, 400, "BAD_REQUEST", m) }
	if f.Status != "" && !validStatus[f.Status] {
		bad("status inválido")
		return
	}
	if f.Lang != "" && f.Lang != "es" && f.Lang != "en" {
		bad("lang inválido")
		return
	}
	if f.Mode != "" && f.Mode != "practice" && f.Mode != "story" {
		bad("mode inválido")
		return
	}
	for _, p := range []struct {
		k   string
		dst **time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := q.Get(p.k); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				bad(p.k + " debe ser RFC 3339")
				return
			}
			*p.dst = &t
		}
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			bad("limit entre 1 y 100")
			return
		}
		f.Limit = n
	}
	switch q.Get("sort") {
	case "", "created_at_desc":
	case "created_at_asc":
		f.Asc = true
	default:
		bad("sort: created_at_desc o created_at_asc")
		return
	}
	items, next, total, err := s.svc.Store.ListSessions(r.Context(), f)
	if errors.Is(err, store.ErrBadCursor) {
		bad("cursor inválido")
		return
	}
	if err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudo leer el historial")
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, x := range items {
		out = append(out, sessionJSON(x))
	}
	writeJSON(w, 200, map[string]any{"items": out, "next_cursor": next, "total": total})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r, maxBody)
	if !ok {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var req coach.CreateRequest
	if err := dec.Decode(&req); err != nil {
		problem(w, r, 422, "VALIDATION_FAILED", "cuerpo inválido: "+err.Error())
		return
	}
	sess, err := s.svc.CreateSession(r.Context(), req)
	var ve *coach.ValidationError
	switch {
	case errors.As(err, &ve):
		problem(w, r, 422, "VALIDATION_FAILED", ve.Error(), ve.Errors...)
	case errors.Is(err, coach.ErrNotFound):
		problem(w, r, 404, "NOT_FOUND", "escenario inexistente")
	case errors.Is(err, coach.ErrConflict):
		problem(w, r, 409, "CONFLICT", err.Error())
	case err != nil:
		s.log.Error("crear sesión", "error", err.Error())
		problem(w, r, 500, "INTERNAL", "no se pudo crear la sesión")
	default:
		w.Header().Set("Location", "/v1/sessions/"+sess.ID)
		writeJSON(w, 201, sessionJSON(sess))
	}
}

func (s *Server) loadSession(w http.ResponseWriter, r *http.Request) (*store.Session, bool) {
	sess, err := s.svc.Store.GetSession(r.Context(), r.PathValue("sessionId"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, r, 404, "NOT_FOUND", "sesión inexistente")
		return nil, false
	}
	if err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudo leer la sesión")
		return nil, false
	}
	return sess, true
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.loadSession(w, r)
	if !ok {
		return
	}
	turns, err := s.svc.Store.Turns(r.Context(), sess.ID)
	if err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudieron leer los turnos")
		return
	}
	out := sessionJSON(sess)
	out["turns"] = turns
	out["llm_errors"] = sess.LLMErrors
	writeJSON(w, 200, out)
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.loadSession(w, r)
	if !ok {
		return
	}
	if sess.Status == coach.StLive || sess.Status == coach.StDetached {
		problem(w, r, 409, "STATE_CONFLICT", "la sesión está en vivo; terminala primero")
		return
	}
	if err := s.svc.Store.DeleteSession(r.Context(), sess.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		problem(w, r, 500, "INTERNAL", "no se pudo borrar")
		return
	}
	s.svc.TTS.DropSession(sess.ID)
	_ = os.RemoveAll(filepath.Join(s.svc.Cfg.DataDir, "audio", sess.ID))
	w.WriteHeader(204)
}

func (s *Server) endSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.loadSession(w, r)
	if !ok {
		return
	}
	reason := "user"
	if b, ok := readBody(w, r, 4096); !ok {
		return
	} else if len(bytes.TrimSpace(b)) > 0 {
		var body struct {
			Reason string `json:"reason"`
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if dec.Decode(&body) != nil || (body.Reason != "" && body.Reason != "user") {
			problem(w, r, 422, "VALIDATION_FAILED", "reason solo admite \"user\"", fieldErr("/reason", "debe ser user"))
			return
		}
	}
	switch sess.Status {
	case coach.StLive, coach.StDetached, coach.StCreated:
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		ended := s.hub.End(ctx, sess.ID, reason)
		cancel()
		if !ended {
			now := s.svc.Now().UTC()
			sess.Status, sess.EndedReason, sess.EndedAt = coach.StEnded, &reason, &now
			if err := s.svc.Store.SaveSession(r.Context(), sess); err != nil {
				problem(w, r, 500, "INTERNAL", "no se pudo terminar")
				return
			}
			s.svc.M.Sessions.WithLabelValues(sess.Mode, sess.Lang, reason).Inc()
			s.svc.Enqueue(sess.ID)
		}
		if sess, ok = s.loadSession(w, r); !ok {
			return
		}
	}
	writeJSON(w, 200, sessionJSON(sess))
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.loadSession(w, r)
	if !ok {
		return
	}
	switch sess.Status {
	case coach.StCreated, coach.StLive, coach.StDetached:
		problem(w, r, 409, "SESSION_NOT_ENDED", "el informe se arma cuando termina la sesión; no hay devolución en vivo")
		return
	case coach.StFailed:
		s.svc.Enqueue(sess.ID)
		fallthrough
	case coach.StEnded, coach.StScoring:
		w.Header().Set("Retry-After", "2")
		writeJSON(w, 202, sessionJSON(sess))
		return
	}
	data, md, err := s.svc.Store.GetReport(r.Context(), sess.ID)
	if err != nil {
		s.svc.Enqueue(sess.ID)
		w.Header().Set("Retry-After", "2")
		writeJSON(w, 202, sessionJSON(sess))
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/markdown") {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(md))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(data))
}

func (s *Server) liveTicket(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.loadSession(w, r)
	if !ok {
		return
	}
	if sess.Status != coach.StCreated && sess.Status != coach.StLive && sess.Status != coach.StDetached {
		problem(w, r, 409, "STATE_CONFLICT", "la sesión ya terminó")
		return
	}
	tok, hash, err := auth.NewToken()
	if err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudo generar el ticket")
		return
	}
	exp := s.svc.Now().Add(60 * time.Second).UTC().Truncate(time.Second)
	id := sess.ID
	if err := s.svc.Store.CreateToken(r.Context(), hash, "ticket", &id, exp); err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudo guardar el ticket")
		return
	}
	writeJSON(w, 201, map[string]any{"ticket": tok, "expires_at": exp, "url": "/v1/sessions/" + id + "/live?ticket=" + tok})
}

func hasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, p := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(p), token) {
				return true
			}
		}
	}
	return false
}

// liveWS valida antes del upgrade: 401, 403, 404, 409, 426 (en ese orden).
func (s *Server) liveWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sessionId")
	authOK := false
	if t := r.URL.Query().Get("ticket"); t != "" {
		used, err := s.svc.Store.UseTicket(r.Context(), auth.HashToken(t), id, s.svc.Now())
		authOK = err == nil && used
	} else {
		_, authOK = s.checkAuth(r)
	}
	if !authOK {
		problem(w, r, 401, "UNAUTHENTICATED", "cookie cx9_session o ticket válidos requeridos")
		return
	}
	if !s.originAllowed(r) {
		problem(w, r, 403, "ORIGIN_NOT_ALLOWED", "Origin no permitido")
		return
	}
	sess, ok := s.loadSession(w, r)
	if !ok {
		return
	}
	if sess.Status != coach.StCreated && sess.Status != coach.StLive && sess.Status != coach.StDetached {
		problem(w, r, 409, "STATE_CONFLICT", "la sesión ya terminó")
		return
	}
	if !hasToken(r.Header, "Upgrade", "websocket") || !hasToken(r.Header, "Sec-WebSocket-Protocol", protocol.Subprotocol) {
		w.Header().Set("Upgrade", "websocket")
		problem(w, r, 426, "BAD_REQUEST", "se requiere WebSocket con subprotocolo "+protocol.Subprotocol)
		return
	}
	s.hub.Serve(w, r, id)
}

func (s *Server) ttsAudio(w http.ResponseWriter, r *http.Request) {
	a, ok := s.svc.TTS.Get(r.PathValue("sessionId"), r.PathValue("utteranceId"))
	if !ok {
		problem(w, r, 404, "NOT_FOUND", "audio inexistente o vencido")
		return
	}
	w.Header().Set("Content-Type", a.ContentType)
	w.Header().Set("X-Audio-Duration-Ms", strconv.Itoa(a.DurationMs))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(200)
	_, _ = w.Write(a.Audio)
}
