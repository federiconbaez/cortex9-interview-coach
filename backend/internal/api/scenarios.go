package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/coach"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/scenario"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/store"
)

func fieldErr(p, m string) scenario.FieldError { return scenario.FieldError{Pointer: p, Message: m} }

func (s *Server) listScenarios(w http.ResponseWriter, r *http.Request) {
	lang := r.URL.Query().Get("lang")
	if lang != "" && lang != "es" && lang != "en" {
		problem(w, r, 400, "BAD_REQUEST", "lang debe ser es o en")
		return
	}
	scs, err := s.svc.Scenarios(r.Context())
	if err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudieron leer los escenarios")
		return
	}
	items := []map[string]any{}
	for _, sc := range scs {
		var title any = sc.Title
		if lang != "" {
			title = sc.Title.Get(lang)
		}
		items = append(items, map[string]any{"id": sc.ID, "title": title, "style": sc.Style, "builtin": sc.Builtin,
			"languages": sc.Languages, "question_count": len(sc.Questions), "max_followups": sc.MaxF()})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// parseScenario lee YAML o JSON según Content-Type.
func parseScenario(w http.ResponseWriter, r *http.Request) (*scenario.Scenario, bool) {
	body, ok := readBody(w, r, maxScenario)
	if !ok {
		return nil, false
	}
	var sc *scenario.Scenario
	var err error
	switch mediaType(r) {
	case "application/yaml":
		sc, err = scenario.ParseYAML(body)
	case "application/json":
		sc, err = scenario.ParseJSON(body)
	default:
		problem(w, r, 415, "UNSUPPORTED_MEDIA_TYPE", "usá application/yaml o application/json")
		return nil, false
	}
	var ve *scenario.ValidationError
	if errors.As(err, &ve) {
		problem(w, r, 422, "VALIDATION_FAILED", "el escenario no es válido", ve.Errors...)
		return nil, false
	}
	if err != nil {
		problem(w, r, 400, "BAD_REQUEST", "cuerpo inválido")
		return nil, false
	}
	return sc, true
}

func (s *Server) validateScenario(w http.ResponseWriter, r *http.Request) {
	if sc, ok := parseScenario(w, r); ok {
		writeJSON(w, 200, sc)
	}
}

func (s *Server) saveRow(sc *scenario.Scenario) store.ScenarioRow {
	sc.Builtin = false
	js, _ := json.Marshal(sc)
	y := sc.ToYAML()
	now := s.svc.Now().UTC().Truncate(time.Second)
	sc.UpdatedAt = &now
	return store.ScenarioRow{ID: sc.ID, YAML: string(y), JSON: string(js), ETag: coach.ETag(js), UpdatedAt: now}
}

func (s *Server) createScenario(w http.ResponseWriter, r *http.Request) {
	sc, ok := parseScenario(w, r)
	if !ok {
		return
	}
	if _, builtin := s.svc.Builtin[sc.ID]; builtin {
		problem(w, r, 409, "CONFLICT", "ya existe un escenario incluido con ese id")
		return
	}
	row := s.saveRow(sc)
	if err := s.svc.Store.InsertScenario(r.Context(), row); errors.Is(err, store.ErrConflict) {
		problem(w, r, 409, "CONFLICT", "ya existe un escenario con ese id")
		return
	} else if err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudo guardar")
		return
	}
	w.Header().Set("Location", "/v1/scenarios/"+sc.ID)
	w.Header().Set("ETag", row.ETag)
	writeJSON(w, 201, sc)
}

func wantsYAML(r *http.Request) bool {
	a := r.Header.Get("Accept")
	return strings.Contains(a, "application/yaml") && !strings.Contains(a, "application/json")
}

func (s *Server) getScenario(w http.ResponseWriter, r *http.Request) {
	sc, y, etag, err := s.svc.Scenario(r.Context(), r.PathValue("scenarioId"))
	if errors.Is(err, coach.ErrNotFound) {
		problem(w, r, 404, "NOT_FOUND", "escenario inexistente")
		return
	}
	if err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudo leer el escenario")
		return
	}
	w.Header().Set("ETag", etag)
	if wantsYAML(r) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.WriteHeader(200)
		_, _ = w.Write(y)
		return
	}
	writeJSON(w, 200, sc)
}

func (s *Server) putScenario(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("scenarioId")
	if _, builtin := s.svc.Builtin[id]; builtin {
		problem(w, r, 403, "READ_ONLY", "los escenarios incluidos son de solo lectura; copialo con otro id")
		return
	}
	sc, ok := parseScenario(w, r)
	if !ok {
		return
	}
	if sc.ID != id {
		problem(w, r, 422, "VALIDATION_FAILED", "el id del cuerpo no coincide con la ruta", fieldErr("/id", "debe ser "+id))
		return
	}
	row := s.saveRow(sc)
	err := s.svc.Store.UpdateScenario(r.Context(), row, r.Header.Get("If-Match"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		problem(w, r, 404, "NOT_FOUND", "escenario inexistente")
	case errors.Is(err, store.ErrPrecondition):
		problem(w, r, 412, "PRECONDITION_FAILED", "If-Match no coincide con el ETag actual")
	case err != nil:
		problem(w, r, 500, "INTERNAL", "no se pudo guardar")
	default:
		w.Header().Set("ETag", row.ETag)
		writeJSON(w, 200, sc)
	}
}

func (s *Server) deleteScenario(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("scenarioId")
	if _, builtin := s.svc.Builtin[id]; builtin {
		problem(w, r, 403, "READ_ONLY", "los escenarios incluidos son de solo lectura")
		return
	}
	if err := s.svc.Store.DeleteScenario(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		problem(w, r, 404, "NOT_FOUND", "escenario inexistente")
		return
	} else if err != nil {
		problem(w, r, 500, "INTERNAL", "no se pudo borrar")
		return
	}
	w.WriteHeader(204)
}
