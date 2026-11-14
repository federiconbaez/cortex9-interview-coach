package llm

import (
	"context"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/resilience"
)

// Observer recibe métricas de cada llamada (lo implementa el paquete metrics).
type Observer interface {
	LLMCall(provider, outcome string, d time.Duration)
	LLMBreaker(provider string, state resilience.State)
}

type nopObserver struct{}

func (nopObserver) LLMCall(string, string, time.Duration) {}
func (nopObserver) LLMBreaker(string, resilience.State)   {}

// Resilient envuelve un proveedor con timeout por intento, reintentos con
// backoff exponencial con jitter y circuit breaker.
type Resilient struct {
	P       Provider
	Timeout time.Duration
	Retries int
	Backoff resilience.Backoff
	Breaker *resilience.Breaker
	Obs     Observer
}

func NewResilient(p Provider, timeout time.Duration, retries int, obs Observer) *Resilient {
	if obs == nil {
		obs = nopObserver{}
	}
	br := resilience.NewBreaker(5, 30*time.Second)
	br.OnChange = func(s resilience.State) { obs.LLMBreaker(p.ID(), s) }
	return &Resilient{P: p, Timeout: timeout, Retries: retries, Obs: obs, Breaker: br,
		Backoff: resilience.Backoff{Base: 500 * time.Millisecond, Max: 4 * time.Second, Jitter: 150 * time.Millisecond}}
}

func (r *Resilient) ID() string    { return r.P.ID() }
func (r *Resilient) Model() string { return r.P.Model() }

func (r *Resilient) Complete(ctx context.Context, req Request) (string, error) {
	var out string
	start := time.Now()
	err := resilience.Do(ctx, r.Breaker, r.Retries, r.Backoff, func(int) time.Duration { return r.Timeout }, func(c context.Context) error {
		s, err := r.P.Complete(c, req)
		if err != nil && c.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			err = &Error{Provider: r.P.ID(), Msg: "timeout", retry: true}
		}
		out = s
		return err
	})
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	r.Obs.LLMCall(r.P.ID(), outcome, time.Since(start))
	return out, err
}
