#!/usr/bin/env bash
# Descarga EXPLÍCITA y opcional de modelos públicos a models/ (ignorado por git).
# Es la única operación de red de /speech y /deploy aparte del proveedor LLM que elijas,
# y solo baja archivos: no envía audio, texto ni datos tuyos.
#
#   deploy/local/fetch-models.sh [tiny|base|small]   # por defecto: small
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
need curl "instalá curl"

SIZE="${1:-small}"
MODELS="${CX9_MODELS_DIR_LOCAL:-$ROOT/models}"
HF="https://huggingface.co"
mkdir -p "$MODELS/whisper" "$MODELS/piper"

PY="${SPEECH_VENV:-$ROOT/speech/.venv}/bin/python"
[[ -x "$PY" ]] || PY=python3
echo "Whisper '$SIZE' -> $MODELS/whisper"
"$PY" - "$SIZE" "$MODELS/whisper" <<'PYEOF'
import sys
from huggingface_hub import snapshot_download  # viene con faster-whisper
size, root = sys.argv[1], sys.argv[2]
snapshot_download(f"Systran/faster-whisper-{size}", cache_dir=root,
                  allow_patterns=["config.json", "model.bin", "tokenizer.json", "vocabulary.*", "preprocessor_config.json"])
PYEOF

voice() {  # voice <ruta en rhasspy/piper-voices> <archivo>
    for ext in onnx onnx.json; do
        local out="$MODELS/piper/$2.$ext"
        [[ -s "$out" ]] && continue
        echo "Piper $2.$ext"
        curl -fL --proto '=https' --tlsv1.2 -o "$out.part" "$HF/rhasspy/piper-voices/resolve/main/$1/$2.$ext"
        mv "$out.part" "$out"
    done
}
voice es/es_AR/daniela/high es_AR-daniela-high
voice en/en_US/lessac/medium en_US-lessac-medium
echo "Modelos listos en $MODELS"
