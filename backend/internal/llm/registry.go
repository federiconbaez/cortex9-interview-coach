package llm

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/resilience"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/secret"
)

// IDs en el orden del contrato.
var IDs = []string{"openai", "anthropic", "xai", "ollama", "mock"}

// ProviderConfig sale solo del entorno (ver internal/config).
type ProviderConfig struct {
	OpenAIKey, AnthropicKey, XAIKey               secret.String
	OpenAIModel, AnthropicModel, XAIModel         string
	OpenAIBase, AnthropicBase, XAIBase, OllamaURL string
	OllamaModel                                   string
	Default                                       string
	Timeout                                       time.Duration
	Retries                                       int
}

// Status es lo que expone GET /v1/llm/providers. Nunca incluye claves.
type Status struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Configured   bool    `json:"configured"`
	Available    bool    `json:"available"`
	Local        bool    `json:"local"`
	DefaultModel string  `json:"default_model"`
	Breaker      string  `json:"breaker"`
	Reason       *string `json:"reason"`
}

type entry struct {
	name   string
	local  bool
	model  string
	reason string
	p      *Resilient
}

// Registry tiene un proveedor resiliente por id configurado.
type Registry struct {
	entries map[string]*entry
	def     string
}

func NewRegistry(c ProviderConfig, hc *http.Client, obs Observer) *Registry {
	if hc == nil {
		hc = &http.Client{}
	}
	r := &Registry{entries: map[string]*entry{}, def: c.Default}
	add := func(id, name string, local bool, model string, p Provider, reason string) {
		e := &entry{name: name, local: local, model: model, reason: reason}
		if p != nil {
			e.p = NewResilient(p, c.Timeout, c.Retries, obs)
		}
		r.entries[id] = e
	}
	if c.OpenAIKey.IsSet() {
		add("openai", "OpenAI", false, c.OpenAIModel, NewOpenAICompatible("openai", c.OpenAIBase, c.OpenAIKey, c.OpenAIModel, hc), "")
	} else {
		add("openai", "OpenAI", false, c.OpenAIModel, nil, "OPENAI_API_KEY no definida")
	}
	if c.AnthropicKey.IsSet() {
		add("anthropic", "Anthropic", false, c.AnthropicModel, NewAnthropic(c.AnthropicBase, c.AnthropicKey, c.AnthropicModel, hc), "")
	} else {
		add("anthropic", "Anthropic", false, c.AnthropicModel, nil, "ANTHROPIC_API_KEY no definida")
	}
	if c.XAIKey.IsSet() {
		add("xai", "xAI", false, c.XAIModel, NewOpenAICompatible("xai", c.XAIBase, c.XAIKey, c.XAIModel, hc), "")
	} else {
		add("xai", "xAI", false, c.XAIModel, nil, "XAI_API_KEY no definida")
	}
	if c.OllamaURL != "" {
		add("ollama", "Ollama (local)", true, c.OllamaModel, NewOllama(c.OllamaURL, c.OllamaModel, hc), "")
	} else {
		add("ollama", "Ollama (local)", true, c.OllamaModel, nil, "CX9_OLLAMA_URL vacía")
	}
	add("mock", "Mock determinista", true, "mock", NewMock(), "")
	if e, ok := r.entries[r.def]; !ok || e.p == nil {
		r.def = "mock"
	}
	return r
}

// Default es el proveedor por defecto (cae a mock si el pedido no está configurado).
func (r *Registry) Default() string { return r.def }

// Get devuelve el proveedor configurado o error.
func (r *Registry) Get(id string) (Provider, error) {
	e, ok := r.entries[id]
	if !ok {
		return nil, fmt.Errorf("proveedor desconocido %q", id)
	}
	if e.p == nil {
		return nil, fmt.Errorf("proveedor %s no configurado: %s", id, e.reason)
	}
	return e.p, nil
}

// List devuelve el estado de los 5 proveedores en el orden del contrato.
func (r *Registry) List() []Status {
	out := make([]Status, 0, len(IDs))
	for _, id := range IDs {
		e := r.entries[id]
		st := Status{ID: id, Name: e.name, Configured: e.p != nil, Local: e.local, DefaultModel: e.model, Breaker: resilience.Closed.String()}
		if e.p != nil {
			bs := e.p.Breaker.State()
			st.Breaker = bs.String()
			st.Available = bs != resilience.Open
			if !st.Available {
				reason := "circuit breaker abierto"
				st.Reason = &reason
			}
		} else {
			reason := e.reason
			st.Reason = &reason
		}
		out = append(out, st)
	}
	sort.SliceStable(out, func(i, j int) bool { return false })
	return out
}
