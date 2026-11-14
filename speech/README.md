# Servicio de voz (`/speech`)

Servicio FastAPI local del coach de entrevistas CORTEX-9: transcripción con faster-whisper que **conserva las muletillas** (con confianza por palabra) y síntesis con piper (voces `es_AR` y `en_US`) con respaldo espeak-ng. Implementa el contrato de `docs/contract.md` §6; detalles en [`API.md`](API.md).

Reutiliza la lógica de STT/TTS del CLI `interview-sim` (prompt de muletillas, lectura de WAV sin ffmpeg, argumentos de piper y espeak), adaptada a un servicio HTTP con backends detrás de interfaces (`STTBackend`, `TTSBackend`) para poder testear con mocks.

## Uso rápido

```bash
python -m venv .venv && . .venv/bin/activate
pip install -r requirements-dev.txt -e .
pytest                                  # todo offline, con mocks
CX9_SPEECH_MODE=mock python -m coach_speech   # http://127.0.0.1:8090/health
```

Modo real:

```bash
pip install -r requirements-models.txt
../deploy/local/fetch-models.sh         # descarga explícita y opcional de modelos a ../models
SPEECH_WHISPER_MODEL=small SPEECH_WHISPER_DOWNLOAD_ROOT=../models/whisper \
SPEECH_PIPER_VOICE_ES_AR=../models/piper/es_AR-daniela-high.onnx \
SPEECH_PIPER_VOICE_EN_US=../models/piper/en_US-lessac-medium.onnx \
python -m coach_speech
```

## Recursos

- RAM aproximada medida en local: proceso con whisper `tiny` + piper ≈ 255 MB RSS. `small` int8 necesita bastante más; en máquinas justas usá `tiny`/`base`.
- `SPEECH_MAX_CONCURRENCY=1` por defecto para no duplicar el pico de memoria.

## Privacidad

El audio se procesa en memoria (o en un archivo temporal que se borra para Ogg/Opus) y no se guarda. No se loguea texto transcripto ni URLs con `prompt`. El servicio no abre conexiones salientes: `SPEECH_WHISPER_LOCAL_ONLY=true` impide descargas en runtime.
