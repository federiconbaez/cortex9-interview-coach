// Package config lee la configuración SOLO del entorno. Las claves se guardan
// como secret.String para que nunca salgan en logs ni respuestas.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/llm"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/secret"
)

type Config struct {
	HTTPAddr        string
	Env             string
	DatabaseURL     string
	PasswordHash    secret.String
	DevNoAuth       bool
	CORSOrigins     []string
	SpeechURL       string
	SpeechToken     secret.String
	LLM             llm.ProviderConfig
	TokenTTL        time.Duration
	LogLevel        string
	LogFormat       string
	LogTranscripts  bool
	DataDir         string
	ShutdownTimeout time.Duration
	MaxTurnMs       int
	PartialInterval time.Duration
	PartialWindow   time.Duration
	ReconnectGrace  time.Duration
	Version         string
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envBool(k string) bool {
	v := strings.ToLower(env(k, ""))
	return v == "1" || v == "true" || v == "yes"
}

func envDur(k string, def time.Duration) (time.Duration, error) {
	v := env(k, "")
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: duración inválida %q (ej. 30s, 2m)", k, v)
	}
	return d, nil
}

func envInt(k string, def int) (int, error) {
	v := env(k, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: entero inválido %q", k, v)
	}
	return n, nil
}

// Load lee y valida el entorno.
func Load() (*Config, error) {
	c := &Config{
		HTTPAddr:       env("CX9_HTTP_ADDR", ":8080"),
		Env:            env("CX9_ENV", "dev"),
		DatabaseURL:    env("CX9_DATABASE_URL", "sqlite://cx9.db"),
		PasswordHash:   secret.New(env("CX9_PASSWORD_HASH", "")),
		DevNoAuth:      envBool("CX9_DEV_NO_AUTH"),
		SpeechURL:      env("CX9_SPEECH_URL", "http://127.0.0.1:8090"),
		SpeechToken:    secret.New(env("CX9_SPEECH_TOKEN", "")),
		LogLevel:       env("CX9_LOG_LEVEL", "info"),
		LogFormat:      env("CX9_LOG_FORMAT", "json"),
		LogTranscripts: envBool("CX9_LOG_TRANSCRIPTS"),
		DataDir:        env("CX9_DATA_DIR", "data"),
		Version:        env("CX9_VERSION", "1.0.0"),
	}
	origins := env("CX9_CORS_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173")
	for _, o := range strings.Split(origins, ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			c.CORSOrigins = append(c.CORSOrigins, o)
		}
	}
	var err error
	if c.TokenTTL, err = envDur("CX9_TOKEN_TTL", 12*time.Hour); err != nil {
		return nil, err
	}
	if c.ShutdownTimeout, err = envDur("CX9_SHUTDOWN_TIMEOUT", 20*time.Second); err != nil {
		return nil, err
	}
	if c.PartialInterval, err = envDur("CX9_PARTIAL_INTERVAL", 1500*time.Millisecond); err != nil {
		return nil, err
	}
	if c.PartialWindow, err = envDur("CX9_PARTIAL_WINDOW", 12*time.Second); err != nil {
		return nil, err
	}
	if c.ReconnectGrace, err = envDur("CX9_RECONNECT_GRACE", 120*time.Second); err != nil {
		return nil, err
	}
	if c.MaxTurnMs, err = envInt("CX9_MAX_TURN_MS", 300000); err != nil {
		return nil, err
	}
	lc := llm.ProviderConfig{
		Default:        env("CX9_LLM_PROVIDER", "mock"),
		OpenAIKey:      secret.New(env("OPENAI_API_KEY", "")),
		OpenAIModel:    env("OPENAI_MODEL", "gpt-4o-mini"),
		OpenAIBase:     env("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		AnthropicKey:   secret.New(env("ANTHROPIC_API_KEY", "")),
		AnthropicModel: env("ANTHROPIC_MODEL", "claude-3-5-haiku-latest"),
		AnthropicBase:  env("ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
		XAIKey:         secret.New(env("XAI_API_KEY", "")),
		XAIModel:       env("XAI_MODEL", "grok-3-mini"),
		XAIBase:        env("XAI_BASE_URL", "https://api.x.ai/v1"),
		OllamaURL:      env("CX9_OLLAMA_URL", env("OLLAMA_URL", "http://localhost:11434")),
		OllamaModel:    env("OLLAMA_MODEL", "llama3.1"),
	}
	if lc.Timeout, err = envDur("CX9_LLM_TIMEOUT", 30*time.Second); err != nil {
		return nil, err
	}
	if lc.Retries, err = envInt("CX9_LLM_RETRIES", 2); err != nil {
		return nil, err
	}
	valid := false
	for _, id := range llm.IDs {
		if id == lc.Default {
			valid = true
		}
	}
	if !valid {
		return nil, fmt.Errorf("CX9_LLM_PROVIDER inválido %q (openai|anthropic|xai|ollama|mock)", lc.Default)
	}
	c.LLM = lc
	if c.DevNoAuth && !IsLoopback(c.HTTPAddr) {
		return nil, fmt.Errorf("CX9_DEV_NO_AUTH=1 solo se permite si CX9_HTTP_ADDR escucha en 127.0.0.1 o ::1 (es %q)", c.HTTPAddr)
	}
	return c, nil
}

// IsLoopback dice si la dirección de escucha es solo local.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Prod dice si corre en producción (CORS apagado). El Secure de la cookie depende del Host, no de esto.
func (c *Config) Prod() bool { return c.Env == "prod" }
