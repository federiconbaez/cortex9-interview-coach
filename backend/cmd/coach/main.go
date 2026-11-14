// Comando coach: backend del CORTEX-9 Interview Coach (simulador de PRÁCTICA).
//
//	coach                 sirve la API (igual que "coach serve")
//	coach hash-password   lee una contraseña por stdin e imprime el hash argon2id para CX9_PASSWORD_HASH
//	coach speech-mock     levanta el servicio de voz mock en memoria (dev/tests), en CX9_SPEECH_MOCK_ADDR (:8090)
//	coach healthcheck     GET /v1/health local (para HEALTHCHECK de Docker; sale 0 si responde 200)
//	coach version
package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/app"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/auth"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/config"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/speech/speechmock"
)

var version = "1.0.0"

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		os.Exit(serve())
	case "hash-password":
		os.Exit(hashPassword())
	case "speech-mock":
		addr := os.Getenv("CX9_SPEECH_MOCK_ADDR")
		if addr == "" {
			addr = "127.0.0.1:8090"
		}
		fmt.Fprintln(os.Stderr, "speech mock en", addr)
		srv := &http.Server{Addr: addr, Handler: speechmock.Handler(), ReadHeaderTimeout: 10 * time.Second}
		if err := srv.ListenAndServe(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "healthcheck":
		os.Exit(healthcheck())
	case "version", "-v", "--version":
		fmt.Println(version)
	default:
		fmt.Fprintln(os.Stderr, "uso: coach [serve|hash-password|speech-mock|healthcheck|version]")
		os.Exit(2)
	}
}

func serve() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 2
	}
	if os.Getenv("CX9_VERSION") == "" {
		cfg.Version = version
	}
	log := app.NewLogger(cfg, os.Stdout)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg, log); err != nil {
		log.Error("terminó con error", "error", err.Error())
		return 1
	}
	return 0
}

func hashPassword() int {
	fmt.Fprint(os.Stderr, "Contraseña (se lee de stdin, no se muestra en logs): ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	pw := strings.TrimRight(line, "\r\n")
	if (err != nil && pw == "") || pw == "" {
		fmt.Fprintln(os.Stderr, "\ncontraseña vacía")
		return 1
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(h)
	return 0
}

func healthcheck() int {
	addr := os.Getenv("CX9_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	port := addr[strings.LastIndex(addr, ":")+1:]
	c := &http.Client{Timeout: 3 * time.Second}
	res, err := c.Get("http://127.0.0.1:" + port + "/v1/health")
	if err != nil {
		return 1
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		return 1
	}
	return 0
}
