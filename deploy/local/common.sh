#!/usr/bin/env bash
# Funciones comunes del modo sin Docker. Se usa con: source deploy/local/common.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export ROOT
ENV_FILE="${CX9_ENV_FILE:-$ROOT/deploy/.env}"

load_env() {
    if [[ -f "$ENV_FILE" ]]; then
        set -a
        # shellcheck disable=SC1090
        source "$ENV_FILE"
        set +a
    else
        echo "Aviso: no existe $ENV_FILE (copiá deploy/.env.example); uso valores por defecto." >&2
    fi
    # Sin Docker todo escucha solo en loopback.
    export SPEECH_HOST=127.0.0.1
    export SPEECH_PORT="${CX9_SPEECH_PORT:-8090}"
    export CX9_SPEECH_URL="http://127.0.0.1:${SPEECH_PORT}"
    export CX9_HTTP_ADDR="127.0.0.1:${CX9_BACKEND_PORT:-8080}"
    export CX9_ENV="${CX9_ENV_LOCAL:-dev}"
    if [[ -n "${POSTGRES_PASSWORD:-}" ]]; then
        export CX9_DATABASE_URL="${CX9_DATABASE_URL:-postgres://${POSTGRES_USER:-cx9}:${POSTGRES_PASSWORD}@127.0.0.1:${POSTGRES_PORT:-5432}/${POSTGRES_DB:-cx9}?sslmode=disable}"
    fi
}

need() {
    command -v "$1" >/dev/null 2>&1 || { echo "Falta '$1' en el PATH: $2" >&2; exit 1; }
}
