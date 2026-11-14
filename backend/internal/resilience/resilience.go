// Package resilience tiene el circuit breaker y los reintentos con backoff
// que usan los clientes de LLM y del servicio de voz.
package resilience

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"
)

type State int

const (
	Closed State = iota
	HalfOpen
	Open
)

func (s State) String() string {
	switch s {
	case HalfOpen:
		return "half_open"
	case Open:
		return "open"
	}
	return "closed"
}

// ErrOpen se devuelve cuando el breaker está abierto y no deja pasar.
var ErrOpen = errors.New("circuit breaker abierto")

// Breaker abre tras Threshold fallos seguidos y deja pasar una prueba
// (half-open) cuando pasa Cooldown.
type Breaker struct {
	Threshold int
	Cooldown  time.Duration
	Now       func() time.Time
	OnChange  func(State)

	mu       sync.Mutex
	state    State
	fails    int
	openedAt time.Time
	probing  bool
}

func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{Threshold: threshold, Cooldown: cooldown, Now: time.Now}
}

func (b *Breaker) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Breaker) setState(s State) {
	if b.state != s {
		b.state = s
		if b.OnChange != nil {
			b.OnChange(s)
		}
	}
}

// State devuelve el estado actual (pasa a half_open si venció el cooldown).
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == Open && b.now().Sub(b.openedAt) >= b.Cooldown {
		return HalfOpen
	}
	return b.state
}

// Allow dice si una llamada puede pasar.
func (b *Breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case Open:
		if b.now().Sub(b.openedAt) < b.Cooldown {
			return ErrOpen
		}
		b.setState(HalfOpen)
		b.probing = true
		return nil
	case HalfOpen:
		if b.probing {
			return ErrOpen
		}
		b.probing = true
	}
	return nil
}

// Record registra el resultado de una llamada permitida.
func (b *Breaker) Record(ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
	if ok {
		b.fails = 0
		b.setState(Closed)
		return
	}
	b.fails++
	if b.state == HalfOpen || b.fails >= b.Threshold {
		b.openedAt = b.now()
		b.setState(Open)
	}
}

// Retryable marca errores que vale la pena reintentar.
type Retryable interface{ Retryable() bool }

// IsRetryable es true para errores que implementan Retryable() == true.
func IsRetryable(err error) bool {
	var r Retryable
	return errors.As(err, &r) && r.Retryable()
}

// Backoff es exponencial con jitter: base·2^n ± jitter, con tope Max.
type Backoff struct {
	Base, Max, Jitter time.Duration
}

func (b Backoff) Delay(attempt int) time.Duration {
	d := b.Base << attempt
	if b.Max > 0 && d > b.Max {
		d = b.Max
	}
	if b.Jitter > 0 {
		d += time.Duration(rand.Int64N(int64(2*b.Jitter))) - b.Jitter
	}
	if d < 0 {
		d = 0
	}
	return d
}

// Do ejecuta fn con breaker y hasta retries reintentos para errores
// retryables. Cada intento recibe su propio contexto con timeout.
func Do(ctx context.Context, br *Breaker, retries int, bo Backoff, timeout func(attempt int) time.Duration, fn func(ctx context.Context) error) error {
	var err error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			t := time.NewTimer(bo.Delay(attempt - 1))
			select {
			case <-ctx.Done():
				t.Stop()
				return errors.Join(err, ctx.Err())
			case <-t.C:
			}
		}
		if br != nil {
			if e := br.Allow(); e != nil {
				return errors.Join(e, err)
			}
		}
		actx, cancel := ctx, context.CancelFunc(func() {})
		if timeout != nil {
			actx, cancel = context.WithTimeout(ctx, timeout(attempt))
		}
		err = fn(actx)
		cancel()
		if br != nil {
			// Un 4xx no es culpa de la dependencia: no cuenta como fallo.
			br.Record(err == nil || !IsRetryable(err))
		}
		if err == nil || !IsRetryable(err) || ctx.Err() != nil {
			return err
		}
	}
	return err
}
