#!/usr/bin/env bash
# Servicio de voz sin Docker: venv propio en speech/.venv (ignorado por git), escucha en 127.0.0.1.
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
load_env
need python3 "instalá Python 3.10+"

cd "$ROOT/speech"
VENV="${SPEECH_VENV:-$ROOT/speech/.venv}"
if [[ ! -x "$VENV/bin/python" ]]; then
    python3 -m venv "$VENV"
fi
"$VENV/bin/pip" install -q -r requirements.txt
"$VENV/bin/pip" install -q --no-deps -e .
if [[ "${CX9_SPEECH_MODE:-real}" != "mock" ]]; then
    "$VENV/bin/pip" install -q -r requirements-models.txt
    MODELS="${CX9_MODELS_DIR_LOCAL:-$ROOT/models}"
    export SPEECH_WHISPER_DOWNLOAD_ROOT="${SPEECH_WHISPER_DOWNLOAD_ROOT:-$MODELS/whisper}"
    export SPEECH_PIPER_BIN="${SPEECH_PIPER_BIN:-$VENV/bin/piper}"
    export SPEECH_PIPER_VOICE_ES_AR="${SPEECH_PIPER_VOICE_ES_AR:-$MODELS/piper/${SPEECH_PIPER_VOICE_ES_AR_FILE:-es_AR-daniela-high.onnx}}"
    export SPEECH_PIPER_VOICE_EN_US="${SPEECH_PIPER_VOICE_EN_US:-$MODELS/piper/${SPEECH_PIPER_VOICE_EN_US_FILE:-en_US-lessac-medium.onnx}}"
fi
echo "speech en http://${SPEECH_HOST}:${SPEECH_PORT} (modo ${CX9_SPEECH_MODE:-real})"
exec "$VENV/bin/python" -m coach_speech
