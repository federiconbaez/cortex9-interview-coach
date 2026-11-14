#!/usr/bin/env bash
# Backend (Go) sin Docker, escuchando en 127.0.0.1. Comando configurable con CX9_BACKEND_CMD.
# shellcheck source=common.sh
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
load_env
need go "instalá Go (ver backend/go.mod)"

cd "$ROOT/backend" || exit 1
if [[ -n "${CX9_BACKEND_CMD:-}" ]]; then
    exec bash -c "$CX9_BACKEND_CMD"
fi
main_pkg="$(find ./cmd -mindepth 2 -maxdepth 2 -name main.go -print -quit 2>/dev/null || true)"
if [[ -z "$main_pkg" ]]; then
    echo "No encontré backend/cmd/*/main.go; definí CX9_BACKEND_CMD (p. ej. 'go run ./cmd/server')." >&2
    exit 1
fi
echo "backend en http://${CX9_HTTP_ADDR} (voz: ${CX9_SPEECH_URL})"
exec go run "$(dirname "$main_pkg")"
