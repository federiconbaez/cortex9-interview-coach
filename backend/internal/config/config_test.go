package config

import (
	"strings"
	"testing"
)

func TestDefaultsAndValidation(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-secreta")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" || c.DatabaseURL != "sqlite://cx9.db" || c.LLM.Default != "mock" || c.LLM.OllamaURL != "http://localhost:11434" {
		t.Fatalf("%+v", c)
	}
	if c.LLM.OpenAIKey.String() != "[REDACTED]" || c.LLM.OpenAIKey.Reveal() != "sk-secreta" {
		t.Fatal("clave")
	}
	t.Setenv("CX9_OLLAMA_URL", "http://host.docker.internal:11434")
	t.Setenv("CX9_LLM_PROVIDER", "nope")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CX9_LLM_PROVIDER") {
		t.Fatal("proveedor inválido")
	}
	t.Setenv("CX9_LLM_PROVIDER", "ollama")
	c, _ = Load()
	if c.LLM.OllamaURL != "http://host.docker.internal:11434" {
		t.Fatal("CX9_OLLAMA_URL")
	}
	t.Setenv("CX9_DEV_NO_AUTH", "1")
	if _, err := Load(); err == nil {
		t.Fatal("dev sin auth exige loopback")
	}
	t.Setenv("CX9_HTTP_ADDR", "127.0.0.1:8080")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CX9_LLM_TIMEOUT", "x")
	if _, err := Load(); err == nil {
		t.Fatal("duración inválida")
	}
	if IsLoopback(":8080") || !IsLoopback("[::1]:1") || !IsLoopback("localhost:1") {
		t.Fatal("loopback")
	}
}
