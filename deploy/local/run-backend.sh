#!/usr/bin/env bash
# Backend (Go) sin Docker, escuchando en 127.0.0.1. Comando configurable con CX9_BACKEND_CMD.
# shellcheck source=common.sh
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
load_env
need go "instalá Go (ver backend/go.mod)"

# La raíz del módulo Go es backend/ (no la del repo): `go run ./backend/cmd/coach` falla desde la raíz.
cd "$ROOT/backend" || exit 1
if [[ -n "${CX9_BACKEND_CMD:-}" ]]; then
    exec bash -c "$CX9_BACKEND_CMD"
fi
if [[ -z "${CX9_PASSWORD_HASH:-}" && "${CX9_DEV_NO_AUTH:-0}" != "1" ]]; then
    echo "Aviso: sin CX9_PASSWORD_HASH ni CX9_DEV_NO_AUTH=1 el login falla (ver deploy/README.md)." >&2
fi
echo "backend en http://${CX9_HTTP_ADDR} (voz: ${CX9_SPEECH_URL}, base: ${CX9_DATABASE_URL%%://*})"
exec go run ./cmd/coach serve
