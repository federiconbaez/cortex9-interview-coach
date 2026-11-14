package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/profile"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/scenario"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/store"
)

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	p, etag, err := s.svc.Profile(r.Context())
	if err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudo leer el perfil")
		return
	}
	if p == nil {
		problem(w, r, 404, "NOT_FOUND", "todavía no hay perfil")
		return
	}
	w.Header().Set("ETag", etag)
	writeJSON(w, 200, p)
}

// decodeProfile lee ProfileInput en modo estricto (lo escrito a mano se guarda tal cual).
func decodeProfile(w http.ResponseWriter, r *http.Request, data []byte) (*profile.Profile, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var in profile.Input
	if err := dec.Decode(&in); err != nil {
		problem(w, r, 422, "VALIDATION_FAILED", "perfil inválido: "+err.Error())
		return nil, false
	}
	p := profile.FromInput(in)
	if errs := p.Validate(); len(errs) > 0 {
		var fe []scenario.FieldError
		for _, e := range errs {
			fe = append(fe, fieldErr(e, "obligatorio o demasiado corto"))
		}
		problem(w, r, 422, "VALIDATION_FAILED", "el perfil no es válido", fe...)
		return nil, false
	}
	return p, true
}

func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request, p *profile.Profile, ifMatch string, create bool, status int) {
	etag, err := s.svc.SaveProfile(r.Context(), p, ifMatch, create)
	switch {
	case errors.Is(err, store.ErrConflict):
		problem(w, r, 409, "CONFLICT", "el perfil ya existe; usá PUT o PATCH")
	case errors.Is(err, store.ErrPrecondition):
		problem(w, r, 412, "PRECONDITION_FAILED", "If-Match no coincide con el ETag actual")
	case err != nil:
		problem(w, r, 500, "INTERNAL", "no se pudo guardar el perfil")
	default:
		w.Header().Set("ETag", etag)
		if create {
			w.Header().Set("Location", "/v1/profile")
		}
		writeJSON(w, status, p)
	}
}

func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r, maxBody)
	if !ok {
		return
	}
	if p, ok := decodeProfile(w, r, b); ok {
		s.saveProfile(w, r, p, "", true, 201)
	}
}

func (s *Server) putProfile(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r, maxBody)
	if !ok {
		return
	}
	p, ok := decodeProfile(w, r, b)
	if !ok {
		return
	}
	ifMatch := r.Header.Get("If-Match")
	if ifMatch != "" {
		if cur, _, _ := s.svc.Profile(r.Context()); cur == nil {
			problem(w, r, 412, "PRECONDITION_FAILED", "If-Match sobre un perfil inexistente")
			return
		}
	}
	s.saveProfile(w, r, p, ifMatch, false, 200)
}

// mergePatch aplica RFC 7396.
func mergePatch(target map[string]any, patch map[string]any) {
	for k, v := range patch {
		if v == nil {
			delete(target, k)
			continue
		}
		if pm, ok := v.(map[string]any); ok {
			tm, ok := target[k].(map[string]any)
			if !ok {
				tm = map[string]any{}
			}
			mergePatch(tm, pm)
			target[k] = tm
			continue
		}
		target[k] = v
	}
}

func (s *Server) patchProfile(w http.ResponseWriter, r *http.Request) {
	if mediaType(r) != "application/merge-patch+json" {
		problem(w, r, 415, "UNSUPPORTED_MEDIA_TYPE", "PATCH usa application/merge-patch+json")
		return
	}
	b, ok := readBody(w, r, maxBody)
	if !ok {
		return
	}
	cur, etag, err := s.svc.Profile(r.Context())
	if err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudo leer el perfil")
		return
	}
	if cur == nil {
		problem(w, r, 404, "NOT_FOUND", "todavía no hay perfil")
		return
	}
	if im := r.Header.Get("If-Match"); im != "" && im != etag {
		problem(w, r, 412, "PRECONDITION_FAILED", "If-Match no coincide con el ETag actual")
		return
	}
	var patch map[string]any
	if err := json.Unmarshal(b, &patch); err != nil {
		problem(w, r, 422, "VALIDATION_FAILED", "merge patch inválido")
		return
	}
	for k := range patch {
		if k != "experiences" && k != "education" && k != "skills" {
			problem(w, r, 422, "VALIDATION_FAILED", "campo no editable", fieldErr("/"+k, "desconocido"))
			return
		}
	}
	curJSON, _ := json.Marshal(profile.Input{Experiences: cur.Experiences, Education: cur.Education, Skills: cur.Skills})
	var target map[string]any
	_ = json.Unmarshal(curJSON, &target)
	mergePatch(target, patch)
	merged, _ := json.Marshal(target)
	p, ok := decodeProfile(w, r, merged)
	if !ok {
		return
	}
	p.Version = cur.Version
	s.saveProfile(w, r, p, etag, false, 200)
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Store.DeleteProfile(r.Context()); err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudo borrar el perfil")
		return
	}
	w.WriteHeader(204)
}

func (s *Server) profileGaps(w http.ResponseWriter, r *http.Request) {
	lang := r.URL.Query().Get("lang")
	if lang == "" {
		lang = "es"
	}
	if lang != "es" && lang != "en" {
		problem(w, r, 400, "BAD_REQUEST", "lang debe ser es o en")
		return
	}
	p, _, err := s.svc.Profile(r.Context())
	if err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudo leer el perfil")
		return
	}
	if p == nil {
		p = profile.Empty()
	}
	writeJSON(w, 200, map[string]any{"items": profile.Gaps(p, lang)})
}
