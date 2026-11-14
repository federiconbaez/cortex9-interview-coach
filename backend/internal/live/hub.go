// Package live implementa el WebSocket /v1/sessions/{id}/live (cx9.live.v1):
// un Runtime por sesión (una goroutine que es dueña del motor), conexiones con
// cola de salida acotada, buffer de reenvío para resume y heartbeat.
//
// Solo práctica: el servidor hace de entrevistador. No existe ningún modo que
// escuche a un entrevistador real ni que sugiera respuestas en vivo.
package live

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/coach"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/endpoint"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/engine"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/protocol"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/store"
)

// Options son los tiempos del protocolo (configurables para tests).
type Options struct {
	HelloTimeout     time.Duration
	Heartbeat        time.Duration
	IdleTimeout      time.Duration
	Grace            time.Duration
	MaxPause         time.Duration
	MaxTurnMs        int
	MaxInflightBytes int
	PartialInterval  time.Duration
	PartialWindow    time.Duration
	TTSPlayedGrace   time.Duration   // espera de tts.played además de duration_ms (CX9_TTS_PLAYED_GRACE_MS)
	TTSPlayedTimeout time.Duration   // espera de tts.played si la duración no se conoce (CX9_TTS_PLAYED_TIMEOUT_MS)
	Endpoint         endpoint.Config // endpointing por silencio (CX9_ENDPOINT_*)
	Speculative      bool            // decisión especulativa de repregunta con el último parcial
	ReplayMax        int
	ReplayAge        time.Duration
}

// DefaultOptions son los valores del contrato.
func DefaultOptions() Options {
	return Options{HelloTimeout: 5 * time.Second, Heartbeat: protocol.HeartbeatMs * time.Millisecond,
		IdleTimeout: protocol.IdleTimeoutMs * time.Millisecond, Grace: protocol.ReconnectGraceMs * time.Millisecond,
		MaxPause: protocol.MaxPauseMs * time.Millisecond, MaxTurnMs: 300000, MaxInflightBytes: 96000,
		PartialInterval: 1500 * time.Millisecond, PartialWindow: 12 * time.Second, TTSPlayedGrace: 3 * time.Second, TTSPlayedTimeout: 20 * time.Second,
		Endpoint: endpoint.Default(), Speculative: true,
		ReplayMax: 512, ReplayAge: 60 * time.Second}
}

var ErrClosed = errors.New("el servidor se está apagando")

// Hub tiene los runtimes de las sesiones en vivo.
type Hub struct {
	svc    *coach.Service
	opt    Options
	mu     sync.Mutex
	rts    map[string]*Runtime
	wg     sync.WaitGroup
	closed bool
}

func NewHub(svc *coach.Service, opt Options) *Hub {
	return &Hub{svc: svc, opt: opt, rts: map[string]*Runtime{}}
}

// runtime devuelve (o crea) el runtime de la sesión.
func (h *Hub) runtime(ctx context.Context, id string) (*Runtime, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	if rt, ok := h.rts[id]; ok {
		return rt, nil
	}
	sess, err := h.svc.Store.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	switch sess.Status {
	case coach.StCreated, coach.StLive, coach.StDetached:
	default:
		return nil, errSessionEnded
	}
	cfg, err := coach.ParseConfig(sess.Config)
	if err != nil {
		return nil, err
	}
	rt := newRuntime(h, sess, cfg)
	h.rts[id] = rt
	h.svc.M.SessionsActive.Inc()
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		rt.loop()
	}()
	return rt, nil
}

var errSessionEnded = errors.New("la sesión ya terminó")

func (h *Hub) remove(id string) {
	h.mu.Lock()
	if _, ok := h.rts[id]; ok {
		delete(h.rts, id)
		h.svc.M.SessionsActive.Dec()
	}
	h.mu.Unlock()
}

func (h *Hub) get(id string) *Runtime {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rts[id]
}

// Active cuenta las sesiones en vivo.
func (h *Hub) Active() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.rts)
}

// End termina una sesión en vivo (POST /end). false si no había runtime.
func (h *Hub) End(ctx context.Context, id, reason string) bool {
	rt := h.get(id)
	if rt == nil {
		return false
	}
	rt.post(func() { rt.emit(rt.eng.End(reason)) })
	select {
	case <-rt.done:
	case <-ctx.Done():
	}
	return true
}

// Shutdown termina todas las sesiones en vivo (reason=error, cierre 1001) y
// espera a que cierren o venza ctx.
func (h *Hub) Shutdown(ctx context.Context) {
	h.mu.Lock()
	h.closed = true
	rts := make([]*Runtime, 0, len(h.rts))
	for _, rt := range h.rts {
		rts = append(rts, rt)
	}
	h.mu.Unlock()
	for _, rt := range rts {
		rt := rt
		rt.post(func() {
			rt.closeCode = protocol.CloseGoingAway
			rt.emit(rt.eng.End("error"))
		})
	}
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// configFor arma la config del motor.
func engineConfig(sess *store.Session, cfg *coach.SessionConfig) engine.Config {
	return engine.Config{Lang: sess.Lang, Questions: cfg.Questions, MaxFollowups: sess.MaxFollowups,
		Intro: cfg.Intro, Closing: cfg.Closing, ThinkPauseMs: cfg.ThinkPauseMs}
}
