package resilience

import (
	"context"
	"errors"
	"testing"
	"time"
)

type rErr struct{ r bool }

func (e rErr) Error() string   { return "x" }
func (e rErr) Retryable() bool { return e.r }

func TestBreakerCycle(t *testing.T) {
	now := time.Unix(0, 0)
	b := NewBreaker(3, 30*time.Second)
	b.Now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if err := b.Allow(); err != nil {
			t.Fatal(err)
		}
		b.Record(false)
	}
	if b.State() != Open || b.Allow() != ErrOpen {
		t.Fatal("debía abrir")
	}
	now = now.Add(31 * time.Second)
	if b.State() != HalfOpen {
		t.Fatal("half open")
	}
	if b.Allow() != nil {
		t.Fatal("una prueba debe pasar")
	}
	if b.Allow() != ErrOpen {
		t.Fatal("solo una prueba a la vez")
	}
	b.Record(false)
	if b.State() != Open {
		t.Fatal("prueba fallida reabre")
	}
	now = now.Add(31 * time.Second)
	_ = b.Allow()
	b.Record(true)
	if b.State() != Closed {
		t.Fatal("prueba ok cierra")
	}
}

func TestDoRetries(t *testing.T) {
	calls := 0
	err := Do(context.Background(), nil, 2, Backoff{Base: time.Millisecond}, nil, func(context.Context) error {
		calls++
		return rErr{true}
	})
	if calls != 3 || err == nil {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	calls = 0
	_ = Do(context.Background(), nil, 2, Backoff{Base: time.Millisecond}, nil, func(context.Context) error {
		calls++
		return rErr{false}
	})
	if calls != 1 {
		t.Fatal("no retryable no reintenta")
	}
	calls = 0
	err = Do(context.Background(), nil, 2, Backoff{Base: time.Millisecond}, func(int) time.Duration { return 5 * time.Millisecond }, func(ctx context.Context) error {
		calls++
		if calls < 2 {
			<-ctx.Done()
			return rErr{true}
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("timeout por intento: calls=%d err=%v", calls, err)
	}
}

func TestDoBreakerStops(t *testing.T) {
	b := NewBreaker(1, time.Hour)
	_ = Do(context.Background(), b, 0, Backoff{}, nil, func(context.Context) error { return rErr{true} })
	err := Do(context.Background(), b, 0, Backoff{}, nil, func(context.Context) error { t.Fatal("no debía llamar"); return nil })
	if !errors.Is(err, ErrOpen) {
		t.Fatalf("got %v", err)
	}
}

func TestBackoff(t *testing.T) {
	bo := Backoff{Base: 100 * time.Millisecond, Max: 300 * time.Millisecond}
	if bo.Delay(0) != 100*time.Millisecond || bo.Delay(5) != 300*time.Millisecond {
		t.Fatal("backoff")
	}
}
