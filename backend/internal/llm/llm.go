// Package llm define la interfaz de proveedores LLM intercambiables (openai,
// anthropic, xai, ollama, mock), el envoltorio resiliente (timeouts,
// reintentos con backoff, circuit breaker) y los prompts del entrevistador.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Task identifica qué le pedimos al LLM. El mock responde según la tarea.
type Task string

const (
	TaskFollowup Task = "followup"
	TaskScore    Task = "score"
	TaskImprove  Task = "improve"
	TaskProfile  Task = "profile"
	TaskEnglish  Task = "english"
)

// Request es un pedido de completado. User es JSON con los datos de la tarea.
type Request struct {
	Task        Task
	System      string
	User        string
	JSON        bool
	Temperature float64
	Model       string // vacío = modelo por defecto del proveedor
}

// Provider es un proveedor LLM. Complete devuelve el texto crudo.
type Provider interface {
	ID() string
	Model() string
	Complete(ctx context.Context, req Request) (string, error)
}

// Error es un error de proveedor. Nunca contiene la clave ni los headers.
type Error struct {
	Provider string
	Status   int
	Msg      string
	retry    bool
}

func (e *Error) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.Status, e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.Provider, e.Msg)
}

func (e *Error) Retryable() bool { return e.retry }

func httpErr(provider string, status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return &Error{Provider: provider, Status: status, Msg: msg, retry: status == 429 || status >= 500}
}

func transportErr(provider string, err error) error {
	// Errores de red y timeouts se reintentan; una cancelación del llamador no.
	var ne net.Error
	_ = errors.As(err, &ne)
	retry := !errors.Is(err, context.Canceled)
	return &Error{Provider: provider, Msg: "no se pudo conectar: " + redactURL(err.Error()), retry: retry}
}

// redactURL saca querystrings de mensajes de error (por si alguna URL lleva credenciales).
func redactURL(s string) string {
	if i := strings.Index(s, "?"); i >= 0 {
		if j := strings.IndexAny(s[i:], "\" "); j > 0 {
			return s[:i] + s[i+j:]
		}
		return s[:i]
	}
	return s
}

// ParseJSON extrae el primer objeto JSON del texto (tolera ```json ... ```).
func ParseJSON(text string, v any) error {
	t := strings.TrimSpace(text)
	i := strings.Index(t, "{")
	j := strings.LastIndex(t, "}")
	if i < 0 || j < i {
		return fmt.Errorf("la respuesta del LLM no trae JSON")
	}
	if err := json.Unmarshal([]byte(t[i:j+1]), v); err != nil {
		return fmt.Errorf("JSON inválido del LLM: %w", err)
	}
	return nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---- payloads y resultados tipados de cada tarea ----

type QA struct {
	Q string `json:"q"`
	A string `json:"a"`
}

type FollowupPayload struct {
	Lang          string   `json:"lang"`
	Question      string   `json:"question"`
	Answers       []string `json:"answers"`
	Probes        []string `json:"probes"`
	AskedProbes   []string `json:"asked_probes"`
	FollowupsDone int      `json:"followups_done"`
	MaxFollowups  int      `json:"max_followups"`
	Profile       string   `json:"profile"`
	History       []QA     `json:"history"`
}

type FollowupResult struct {
	Action   string `json:"action"`
	Question string `json:"question"`
	Probe    string `json:"probe"`
	Reason   string `json:"reason"`
}

type ScorePayload struct {
	Lang     string   `json:"lang"`
	Question string   `json:"question"`
	Answer   string   `json:"answer"`
	Keywords []string `json:"keywords"`
}

type ScoreResult struct {
	Claridad   any    `json:"claridad"`
	Estructura any    `json:"estructura"`
	Relevancia any    `json:"relevancia"`
	Comentario string `json:"comentario"`
}

type ImprovePayload struct {
	Lang              string   `json:"lang"`
	Question          string   `json:"question"`
	Answers           []string `json:"answers"`
	Star              bool     `json:"star"`
	ForbiddenNewTerms []string `json:"forbidden_new_terms"`
}

type ImproveResult struct {
	Mejorada  string   `json:"mejorada"`
	Faltantes []string `json:"faltantes"`
}

type ProfilePayload struct {
	Answer         string   `json:"answer"`
	Question       string   `json:"question"`
	KnownCompanies []string `json:"known_companies"`
}

type ExtractedExperience struct {
	Company         string   `json:"company"`
	Role            string   `json:"role"`
	Period          string   `json:"period"`
	Technologies    []string `json:"technologies"`
	Frameworks      []string `json:"frameworks"`
	Achievements    []string `json:"achievements"`
	Problems        []string `json:"problems"`
	ReasonForChange string   `json:"reason_for_change"`
	Evidence        string   `json:"evidence"`
}

type ExtractedEducation struct {
	Institution string `json:"institution"`
	Degree      string `json:"degree"`
	Status      string `json:"status"`
	Evidence    string `json:"evidence"`
}

type Extraction struct {
	Experiences    []ExtractedExperience `json:"experiences"`
	Education      []ExtractedEducation  `json:"education"`
	Skills         []string              `json:"skills"`
	SkillsEvidence string                `json:"skills_evidence"`
}

type EnglishPayload struct {
	Answer string `json:"answer"`
}

type Grammar struct {
	Original    string `json:"original"`
	Corrected   string `json:"corrected"`
	Explanation string `json:"explanation"`
}

type EnglishResult struct {
	Grammar []Grammar `json:"grammar"`
	Tips    []string  `json:"tips"`
}

// Call arma el pedido de una tarea, lo ejecuta y decodifica el JSON.
func Call[T any](ctx context.Context, p Provider, task Task, system string, payload any, temperature float64) (T, error) {
	var out T
	txt, err := p.Complete(ctx, Request{Task: task, System: system, User: mustJSON(payload), JSON: true, Temperature: temperature})
	if err != nil {
		return out, err
	}
	err = ParseJSON(txt, &out)
	return out, err
}

type withModel struct {
	Provider
	model string
}

// WithModel fija el modelo pedido por la sesión (llm_model). Vacío = el por defecto.
func WithModel(p Provider, model string) Provider {
	if model == "" || model == p.Model() {
		return p
	}
	return &withModel{p, model}
}

func (w *withModel) Model() string { return w.model }

func (w *withModel) Complete(ctx context.Context, r Request) (string, error) {
	r.Model = w.model
	return w.Provider.Complete(ctx, r)
}
