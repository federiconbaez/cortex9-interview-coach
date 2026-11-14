package config

import (
	"strings"
	"testing"
	"time"
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

func TestEndpointAndTTSPlayedEnv(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint.Mode != "auto" || c.Endpoint.SilenceMs != 700 || c.Endpoint.RMS != 500 || !c.SpeculativeFollowup ||
		c.TTSPlayedGrace != 3*time.Second || c.TTSPlayedTimeout != 20*time.Second {
		t.Fatalf("%+v", c)
	}
	t.Setenv("CX9_ENDPOINT_MODE", "ENERGY")
	t.Setenv("CX9_ENDPOINT_SILENCE_MS", "900")
	t.Setenv("CX9_ENDPOINT_RMS", "800.5")
	t.Setenv("CX9_SPECULATIVE_FOLLOWUP", "false")
	t.Setenv("CX9_TTS_PLAYED_GRACE_MS", "1500")
	t.Setenv("CX9_TTS_PLAYED_TIMEOUT_MS", "8000")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint.Mode != "energy" || c.Endpoint.SilenceMs != 900 || c.Endpoint.RMS != 800.5 || c.SpeculativeFollowup ||
		c.TTSPlayedGrace != 1500*time.Millisecond || c.TTSPlayedTimeout != 8*time.Second {
		t.Fatalf("%+v", c)
	}
	for k, v := range map[string]string{"CX9_ENDPOINT_MODE": "vad", "CX9_ENDPOINT_SILENCE_MS": "5", "CX9_ENDPOINT_RMS": "x",
		"CX9_TTS_PLAYED_GRACE_MS": "-1", "CX9_TTS_PLAYED_TIMEOUT_MS": "10"} {
		t.Run(k, func(t *testing.T) {
			t.Setenv(k, v)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%s debería fallar", k, v)
			}
		})
	}
}
