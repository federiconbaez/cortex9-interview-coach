package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/secret"
)

const maxBody = 4 << 20

func postJSON(ctx context.Context, hc *http.Client, provider, url string, headers map[string]string, payload any, out any) error {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return &Error{Provider: provider, Msg: "URL inválida"}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return transportErr(provider, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode/100 != 2 {
		return httpErr(provider, resp.StatusCode, data)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &Error{Provider: provider, Msg: "respuesta no es JSON"}
	}
	return nil
}

// OpenAICompatible sirve para OpenAI y xAI (misma API de chat completions).
type OpenAICompatible struct {
	id, model, base string
	key             secret.String
	hc              *http.Client
}

func NewOpenAICompatible(id, base string, key secret.String, model string, hc *http.Client) *OpenAICompatible {
	return &OpenAICompatible{id: id, model: model, base: strings.TrimRight(base, "/"), key: key, hc: hc}
}

func (p *OpenAICompatible) ID() string    { return p.id }
func (p *OpenAICompatible) Model() string { return p.model }

func (p *OpenAICompatible) Complete(ctx context.Context, r Request) (string, error) {
	payload := map[string]any{
		"model":       modelOr(r.Model, p.model),
		"temperature": r.Temperature,
		"messages": []map[string]string{
			{"role": "system", "content": r.System},
			{"role": "user", "content": r.User},
		},
	}
	if r.JSON {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := postJSON(ctx, p.hc, p.id, p.base+"/chat/completions", map[string]string{"Authorization": "Bearer " + p.key.Reveal()}, payload, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", &Error{Provider: p.id, Msg: "respuesta sin choices"}
	}
	return out.Choices[0].Message.Content, nil
}

// Anthropic usa la API de mensajes.
type Anthropic struct {
	model, base string
	key         secret.String
	hc          *http.Client
}

func NewAnthropic(base string, key secret.String, model string, hc *http.Client) *Anthropic {
	return &Anthropic{model: model, base: strings.TrimRight(base, "/"), key: key, hc: hc}
}

func (p *Anthropic) ID() string    { return "anthropic" }
func (p *Anthropic) Model() string { return p.model }

func (p *Anthropic) Complete(ctx context.Context, r Request) (string, error) {
	sys := r.System
	if r.JSON {
		sys += "\nRespondé únicamente con el objeto JSON, sin texto adicional."
	}
	payload := map[string]any{
		"model": modelOr(r.Model, p.model), "max_tokens": 1500, "system": sys, "temperature": r.Temperature,
		"messages": []map[string]string{{"role": "user", "content": r.User}},
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	h := map[string]string{"x-api-key": p.key.Reveal(), "anthropic-version": "2023-06-01"}
	if err := postJSON(ctx, p.hc, "anthropic", p.base+"/v1/messages", h, payload, &out); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String(), nil
}

// Ollama usa /api/chat sin streaming.
type Ollama struct {
	model, base string
	hc          *http.Client
}

func NewOllama(base, model string, hc *http.Client) *Ollama {
	return &Ollama{model: model, base: strings.TrimRight(base, "/"), hc: hc}
}

func (p *Ollama) ID() string    { return "ollama" }
func (p *Ollama) Model() string { return p.model }

func (p *Ollama) Complete(ctx context.Context, r Request) (string, error) {
	payload := map[string]any{
		"model": modelOr(r.Model, p.model), "stream": false,
		"options":  map[string]any{"temperature": r.Temperature},
		"messages": []map[string]string{{"role": "system", "content": r.System}, {"role": "user", "content": r.User}},
	}
	if r.JSON {
		payload["format"] = "json"
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := postJSON(ctx, p.hc, "ollama", p.base+"/api/chat", nil, payload, &out); err != nil {
		return "", err
	}
	return out.Message.Content, nil
}

func modelOr(req, def string) string {
	if req != "" {
		return req
	}
	return def
}
