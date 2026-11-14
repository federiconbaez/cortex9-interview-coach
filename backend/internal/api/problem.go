package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/scenario"
)

// Problem es RFC 9457 + code estable.
type Problem struct {
	Type      string                `json:"type"`
	Title     string                `json:"title"`
	Status    int                   `json:"status"`
	Detail    string                `json:"detail,omitempty"`
	Instance  string                `json:"instance,omitempty"`
	Code      string                `json:"code"`
	RequestID string                `json:"request_id,omitempty"`
	Retryable bool                  `json:"retryable"`
	Errors    []scenario.FieldError `json:"errors,omitempty"`
}

var titles = map[string]string{
	"BAD_REQUEST":             "Petición mal formada",
	"VALIDATION_FAILED":       "La petición no pasó la validación",
	"UNAUTHENTICATED":         "Falta autenticación o es inválida",
	"FORBIDDEN":               "Prohibido",
	"ORIGIN_NOT_ALLOWED":      "Origen no permitido",
	"READ_ONLY":               "Recurso de solo lectura",
	"NOT_FOUND":               "No encontrado",
	"CONFLICT":                "Conflicto",
	"STATE_CONFLICT":          "El estado actual no permite la operación",
	"IDEMPOTENCY_IN_PROGRESS": "La petición original sigue en curso",
	"SESSION_NOT_ENDED":       "La sesión no terminó",
	"PRECONDITION_FAILED":     "If-Match no coincide",
	"PAYLOAD_TOO_LARGE":       "Cuerpo demasiado grande",
	"UNSUPPORTED_MEDIA_TYPE":  "Content-Type no soportado",
	"IDEMPOTENCY_KEY_REUSED":  "Idempotency-Key reutilizada con otro cuerpo",
	"RATE_LIMITED":            "Demasiados intentos",
	"INTERNAL":                "Error interno",
	"DEPENDENCY_UNAVAILABLE":  "Dependencia no disponible",
	"DEPENDENCY_TIMEOUT":      "Dependencia sin respuesta",
}

func problem(w http.ResponseWriter, r *http.Request, status int, code, detail string, errs ...scenario.FieldError) {
	p := Problem{Type: "urn:cx9:problem:" + strings.ToLower(strings.ReplaceAll(code, "_", "-")), Title: titles[code], Status: status,
		Detail: detail, Instance: r.URL.Path, Code: code, RequestID: w.Header().Get("X-Request-Id"),
		Retryable: status == 429 || status == 503 || status == 504 || code == "IDEMPOTENCY_IN_PROGRESS", Errors: errs}
	if p.Title == "" {
		p.Title = http.StatusText(status)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
