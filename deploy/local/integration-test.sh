#!/usr/bin/env bash
# Prueba de integración sin Docker: servicio de voz + backend (SQLite, LLM mock) + cliente
# que recorre el flujo del contrato por REST y WebSocket (deploy/local/integration_client.py).
#
# Uso: deploy/local/integration-test.sh [mock|real]
#   mock (por defecto): CX9_SPEECH_MODE=mock con SPEECH_MOCK_TEXT (muletillas y una palabra dudosa).
#   real: faster-whisper (modelo SPEECH_WHISPER_MODEL, por defecto tiny, ya descargado en
#         SPEECH_WHISPER_DOWNLOAD_ROOT) + Piper/espeak-ng; la respuesta del candidato se sintetiza
#         con el propio /tts del servicio y la transcribe Whisper.
#
# Variables: PYTHON (intérprete con speech, httpx y websockets instalados; por defecto python3),
# CX9_COACH_BIN (binario ya compilado; si falta se compila con go build en un directorio temporal),
# CX9_IT_OUT (resumen JSON), CX9_IT_KEEP=1 (no borrar el directorio temporal).
# Puertos libres elegidos al azar en 127.0.0.1; nada escucha fuera de loopback.
set -euo pipefail

MODE="${1:-mock}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PY="${PYTHON:-python3}"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/cx9-it.XXXXXX")"
PIDS=()

cleanup() {
    local rc=$?
    for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
    wait 2>/dev/null || true
    if [[ $rc -ne 0 ]]; then
        for f in "$WORK"/*.log; do
            [[ -f "$f" ]] || continue
            echo "----- $(basename "$f") (últimas 40 líneas) -----" >&2
            tail -n 40 "$f" >&2
        done
    fi
    if [[ "${CX9_IT_KEEP:-0}" == "1" ]]; then echo "Directorio de trabajo conservado: $WORK"; else rm -rf "$WORK"; fi
    exit "$rc"
}
trap cleanup EXIT INT TERM

free_port() {
    "$PY" -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

wait_http() { # url, segundos, pid
    local url=$1 secs=$2 pid=$3 i
    for ((i = 0; i < secs * 4; i++)); do
        if ! kill -0 "$pid" 2>/dev/null; then echo "El proceso de $url terminó antes de estar listo" >&2; return 1; fi
        if "$PY" - "$url" <<'PYEOF' 2>/dev/null; then return 0; fi
import sys, urllib.request
with urllib.request.urlopen(sys.argv[1], timeout=2) as r:
    sys.exit(0 if r.status == 200 else 1)
PYEOF
        sleep 0.25
    done
    echo "Tiempo agotado esperando $url" >&2
    return 1
}

case "$MODE" in mock | real) ;; *) echo "Modo desconocido: $MODE (mock|real)" >&2; exit 2 ;; esac
"$PY" -c 'import coach_speech, httpx, websockets' || {
    echo "Faltan dependencias en $PY: pip install -r speech/requirements.txt -r deploy/local/requirements-it.txt -e speech" >&2
    exit 1
}

# 1) Backend: binario compilado desde backend/ (la raíz del módulo Go es backend/, no la del repo).
COACH="${CX9_COACH_BIN:-}"
if [[ -z "$COACH" ]]; then
    command -v go >/dev/null || { echo "Falta Go (versión en backend/go.mod)" >&2; exit 1; }
    COACH="$WORK/coach"
    (cd "$ROOT/backend" && go build -trimpath -o "$COACH" ./cmd/coach)
fi
"$COACH" version

SPEECH_PORT_IT="$(free_port)"
BACKEND_PORT_IT="$(free_port)"
PASSWORD="it-$("$PY" -c 'import secrets; print(secrets.token_hex(8))')"
HASH="$(printf '%s\n' "$PASSWORD" | "$COACH" hash-password 2>/dev/null)"

# 2) Servicio de voz.
CLIENT_ARGS=(--test-repeat)
if [[ "$MODE" == "mock" ]]; then
    export CX9_SPEECH_MODE=mock
    export SPEECH_MOCK_TEXT="${SPEECH_MOCK_TEXT:-Eh, este... o sea, trabajé tres años en backend con ?Kafka, um, y bajé la latencia un 40 por ciento.}"
    CLIENT_ARGS+=(--expect-fillers --expect-low-conf Kafka --realtime "${CX9_IT_REALTIME:-2}")
    READY_SECS=30
else
    export CX9_SPEECH_MODE=real
    export SPEECH_WHISPER_MODEL="${SPEECH_WHISPER_MODEL:-tiny}"
    export SPEECH_WHISPER_DOWNLOAD_ROOT="${SPEECH_WHISPER_DOWNLOAD_ROOT:-$ROOT/models/whisper}"
    export SPEECH_WHISPER_LOCAL_ONLY="${SPEECH_WHISPER_LOCAL_ONLY:-1}"
    CLIENT_ARGS+=(--speech-url "http://127.0.0.1:${SPEECH_PORT_IT}" --realtime "${CX9_IT_REALTIME:-1}" --timeout 180)
    READY_SECS=180
fi
(cd "$WORK" && SPEECH_HOST=127.0.0.1 SPEECH_PORT="$SPEECH_PORT_IT" exec "$PY" -m coach_speech) >"$WORK/speech.log" 2>&1 &
PIDS+=($!)
wait_http "http://127.0.0.1:${SPEECH_PORT_IT}/health" "$READY_SECS" "${PIDS[-1]}"

# 3) Backend con SQLite y LLM mock; autenticación real (hash Argon2id del binario).
mkdir -p "$WORK/data"
(cd "$WORK" && exec env \
    CX9_HTTP_ADDR="127.0.0.1:${BACKEND_PORT_IT}" \
    CX9_DATABASE_URL="sqlite://$WORK/data/cx9.db" \
    CX9_DATA_DIR="$WORK/data" \
    CX9_PASSWORD_HASH="$HASH" \
    CX9_LLM_PROVIDER=mock \
    CX9_SPEECH_URL="http://127.0.0.1:${SPEECH_PORT_IT}" \
    CX9_LOG_FORMAT=text \
    "$COACH" serve) >"$WORK/backend.log" 2>&1 &
PIDS+=($!)
wait_http "http://127.0.0.1:${BACKEND_PORT_IT}/v1/health" 30 "${PIDS[-1]}"
CX9_HTTP_ADDR="127.0.0.1:${BACKEND_PORT_IT}" "$COACH" healthcheck

# 4) Flujo completo.
echo "== Integración ($MODE): voz :$SPEECH_PORT_IT, backend :$BACKEND_PORT_IT =="
"$PY" "$ROOT/deploy/local/integration_client.py" \
    --base "http://127.0.0.1:${BACKEND_PORT_IT}" --password "$PASSWORD" \
    --out "${CX9_IT_OUT:-$WORK/result.json}" "${CLIENT_ARGS[@]}"
