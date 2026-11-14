#!/usr/bin/env bash
# Levanta speech, backend y frontend sin Docker. Ctrl+C los detiene a todos.
# Postgres: usá uno local (pg_isready) o CX9_SKIP_DB=1. Prometheus/Grafana solo vienen en el modo Docker.
# shellcheck source=common.sh
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
load_env

LOGS="$ROOT/deploy/local/logs"
mkdir -p "$LOGS"
pids=()
cleanup() {
    for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
    wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

if [[ "${CX9_SKIP_DB:-0}" != "1" ]]; then
    if command -v pg_isready >/dev/null 2>&1 && pg_isready -h 127.0.0.1 -p "${POSTGRES_PORT:-5432}" -q; then
        echo "postgres OK en 127.0.0.1:${POSTGRES_PORT:-5432}"
    else
        echo "Aviso: no hay postgres en 127.0.0.1:${POSTGRES_PORT:-5432}; el backend puede no arrancar." >&2
    fi
fi

start() {
    local name="$1"
    "$ROOT/deploy/local/run-$name.sh" >"$LOGS/$name.log" 2>&1 &
    pids+=("$!")
    echo "$name iniciado (log: deploy/local/logs/$name.log)"
}

start speech
for _ in $(seq 1 60); do
    curl -fsS "http://127.0.0.1:${CX9_SPEECH_PORT:-8090}/healthz" >/dev/null 2>&1 && break
    sleep 1
done
[[ -d "$ROOT/backend" ]] && start backend
[[ -d "$ROOT/frontend" ]] && start frontend

echo "Listo. Abrí http://127.0.0.1:5173 (Ctrl+C para salir)."
wait -n "${pids[@]}" || true
echo "Un proceso terminó; deteniendo el resto. Revisá deploy/local/logs/." >&2
