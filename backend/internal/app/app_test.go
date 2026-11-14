package app

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/config"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestRunServesAndShutsDownWithoutLeakingKeys(t *testing.T) {
	const key = "sk-test-no-debe-aparecer"
	t.Setenv("OPENAI_API_KEY", key)
	t.Setenv("CX9_DATABASE_URL", "sqlite://"+filepath.Join(t.TempDir(), "cx9.db"))
	t.Setenv("CX9_DATA_DIR", t.TempDir())
	addr := freeAddr(t)
	t.Setenv("CX9_HTTP_ADDR", addr)
	t.Setenv("CX9_SPEECH_URL", "http://127.0.0.1:1")
	t.Setenv("CX9_SHUTDOWN_TIMEOUT", "5s")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	var out syncBuf
	log := NewLogger(cfg, &out)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, log) }()

	var res *http.Response
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		res, err = http.Get(fmt.Sprintf("http://%s/v1/health", addr))
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("health %d", res.StatusCode)
	}
	res, err = http.Get(fmt.Sprintf("http://%s/metrics", addr))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("metrics %v", err)
	}
	res.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run no terminó")
	}
	logs := out.String()
	if strings.Contains(logs, key) {
		t.Fatal("la clave apareció en el log")
	}
	for _, want := range []string{"arrancando", "apagando", "apagado", "CX9_PASSWORD_HASH"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("falta %q en el log:\n%s", want, logs)
		}
	}
}

func TestRunFailsOnBadDatabaseURL(t *testing.T) {
	t.Setenv("CX9_DATABASE_URL", "mysql://nope")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	var out syncBuf
	if err := Run(context.Background(), cfg, NewLogger(cfg, &out)); err == nil || !strings.Contains(err.Error(), "base de datos") {
		t.Fatalf("esperaba error de base: %v", err)
	}
}

func TestDBKind(t *testing.T) {
	if dbKind("sqlite://x.db") != "sqlite" || dbKind("postgres://h/db") != "postgres" {
		t.Fatal("dbKind")
	}
}
