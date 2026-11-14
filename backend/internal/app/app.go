// Package app arma el servidor: config, base, migraciones, proveedores,
// métricas, API, WebSocket, worker de informes y apagado ordenado.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/api"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/coach"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/config"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/live"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/llm"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/metrics"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/speech"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/store"
)

// NewLogger arma el logger slog (JSON por defecto).
func NewLogger(cfg *config.Config, w io.Writer) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

// App es el servidor armado.
type App struct {
	Cfg     *config.Config
	Svc     *coach.Service
	Hub     *live.Hub
	Handler http.Handler
	Store   *store.Store
	Log     *slog.Logger
}

// Build arma todo sin escuchar (los tests usan httptest con Handler).
func Build(ctx context.Context, cfg *config.Config, log *slog.Logger, opt live.Options) (*App, error) {
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("base de datos: %w", err)
	}
	applied, err := st.Migrate(ctx)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("migraciones: %w", err)
	}
	if len(applied) > 0 {
		log.Info("migraciones aplicadas", "versiones", applied)
	}
	m := metrics.New()
	reg := llm.NewRegistry(cfg.LLM, &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}, m)
	sp := speech.New(cfg.SpeechURL, cfg.SpeechToken, m)
	svc, err := coach.New(cfg, st, reg, sp, m, log)
	if err != nil {
		st.Close()
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Warn("no se pudo crear CX9_DATA_DIR", "error", err.Error())
	}
	if err := svc.Recover(ctx); err != nil {
		log.Warn("recuperación al arrancar", "error", err.Error())
	}
	svc.StartScoring(2)
	hub := live.NewHub(svc, opt)
	return &App{Cfg: cfg, Svc: svc, Hub: hub, Handler: api.New(svc, hub), Store: st, Log: log}, nil
}

// Shutdown: termina sesiones en vivo, espera informes y cierra la base.
func (a *App) Shutdown(ctx context.Context) {
	a.Hub.Shutdown(ctx)
	a.Svc.StopScoring(ctx)
	_ = a.Store.Close()
}

// Run escucha hasta que se cancela ctx (SIGINT/SIGTERM) y apaga ordenado.
func Run(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	opt := live.DefaultOptions()
	opt.Grace, opt.MaxTurnMs = cfg.ReconnectGrace, cfg.MaxTurnMs
	opt.PartialInterval, opt.PartialWindow = cfg.PartialInterval, cfg.PartialWindow
	a, err := Build(ctx, cfg, log, opt)
	if err != nil {
		return err
	}
	if cfg.DevNoAuth {
		log.Warn("CX9_DEV_NO_AUTH=1: autenticación desactivada (solo loopback)")
	} else if !cfg.PasswordHash.IsSet() {
		log.Error("CX9_PASSWORD_HASH no definido: nadie va a poder iniciar sesión (generalo con: coach hash-password)")
	}
	var configured []string
	for _, p := range a.Svc.LLM.List() {
		if p.Configured {
			configured = append(configured, p.ID)
		}
	}
	log.Info("arrancando", "addr", cfg.HTTPAddr, "env", cfg.Env, "db", dbKind(cfg.DatabaseURL), "llm_default", a.Svc.LLM.Default(),
		"llm_configurados", configured, "speech", cfg.SpeechURL, "version", cfg.Version)
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: a.Handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second,
		BaseContext: func(net.Listener) context.Context { return context.Background() }}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		a.Shutdown(context.Background())
		return err
	case <-ctx.Done():
	}
	log.Info("apagando", "timeout", cfg.ShutdownTimeout.String())
	sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	// primero las sesiones en vivo (mandan session.end y cierran 1001), después HTTP
	a.Hub.Shutdown(sctx)
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn("shutdown http", "error", err.Error())
	}
	a.Svc.StopScoring(sctx)
	_ = a.Store.Close()
	log.Info("apagado")
	return nil
}

func dbKind(url string) string {
	if strings.HasPrefix(url, "sqlite") {
		return "sqlite"
	}
	return "postgres"
}
