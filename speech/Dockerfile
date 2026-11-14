# Servicio de voz del coach de entrevistas CORTEX-9.
# Los modelos (whisper, voces piper) NO van en la imagen: se montan en /models.
FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 \
    SPEECH_HOST=0.0.0.0 SPEECH_PORT=8090 \
    SPEECH_WHISPER_DOWNLOAD_ROOT=/models/whisper SPEECH_WHISPER_LOCAL_ONLY=true \
    HF_HUB_OFFLINE=1

# espeak-ng: respaldo de TTS cuando piper o la voz no están.
RUN apt-get update \
    && apt-get install -y --no-install-recommends espeak-ng \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY requirements.txt requirements-models.txt ./
ARG WITH_MODELS=true
RUN pip install -r requirements.txt \
    && if [ "$WITH_MODELS" = "true" ]; then pip install -r requirements-models.txt; fi

COPY pyproject.toml README.md ./
COPY coach_speech ./coach_speech
RUN pip install --no-deps . \
    && useradd --system --uid 10001 --home /nonexistent speech \
    && mkdir -p /models && chown speech /models

USER speech
EXPOSE 8090
HEALTHCHECK --interval=15s --timeout=3s --start-period=60s --retries=3 \
    CMD python -c "import urllib.request,sys; urllib.request.urlopen('http://127.0.0.1:8090/healthz', timeout=2)" || exit 1
CMD ["python", "-m", "coach_speech"]
