"""Configuración por variables de entorno (prefijo SPEECH_).

No se loguean valores: algunas rutas pueden contener datos del usuario.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field

# Idiomas soportados: código corto -> (idioma de whisper, clave de voz piper).
LANGUAGES: dict[str, tuple[str, str]] = {
    "es": ("es", "es_AR"),
    "es-ar": ("es", "es_AR"),
    "es_ar": ("es", "es_AR"),
    "en": ("en", "en_US"),
    "en-us": ("en", "en_US"),
    "en_us": ("en", "en_US"),
}

# Prompt inicial que "muestra" a Whisper que queremos las muletillas tal cual.
DEFAULT_FILLER_PROMPT = {
    "es": "Eh, este... o sea, um, mmm, bueno, digamos que, eh, trabajé en el backend.",
    "en": "Um, uh, er, like, you know, I mean, um, I worked on the backend.",
}


def normalize_language(lang: str | None) -> str:
    """Devuelve 'es' o 'en'. Lanza ValueError si no está soportado."""
    key = (lang or "").strip().lower()
    if key not in LANGUAGES:
        raise ValueError(f"idioma no soportado: {lang!r} (usá es, es-AR, en o en-US)")
    return LANGUAGES[key][0]


def voice_key(lang: str) -> str:
    return LANGUAGES[normalize_language(lang)][1]


def _int(name: str, default: int) -> int:
    raw = os.environ.get(name)
    return int(raw) if raw not in (None, "") else default


def _float(name: str, default: float) -> float:
    raw = os.environ.get(name)
    return float(raw) if raw not in (None, "") else default


def _bool(name: str, default: bool) -> bool:
    raw = os.environ.get(name)
    if raw in (None, ""):
        return default
    return raw.strip().lower() in ("1", "true", "yes", "si", "sí", "on")


@dataclass
class Settings:
    mode: str = "real"  # real | mock (CX9_SPEECH_MODE)
    token: str = ""  # CX9_SPEECH_TOKEN: Bearer opcional
    stt_backend: str = "faster-whisper"  # faster-whisper | mock
    tts_backend: str = "auto"  # auto (piper -> espeak) | piper | espeak | mock
    whisper_model: str = "small"  # nombre (tiny/base/small) o ruta local a un modelo CT2
    whisper_device: str = "cpu"
    whisper_compute: str = "int8"
    whisper_beam_size: int = 5
    whisper_partial_beam_size: int = 1  # re-transcripciones parciales (partial=true)
    whisper_cpu_threads: int = 2
    whisper_download_root: str = "models/whisper"
    whisper_local_only: bool = True  # no descarga modelos en runtime
    filler_prompt: dict[str, str] = field(default_factory=lambda: dict(DEFAULT_FILLER_PROMPT))
    low_confidence_threshold: float = 0.5
    pause_threshold_s: float = 0.6
    piper_bin: str = "piper"
    piper_voices: dict[str, str] = field(default_factory=dict)  # es_AR / en_US -> .onnx
    espeak_voices: dict[str, str] = field(default_factory=lambda: {"es": "es-419", "en": "en-us"})
    max_upload_bytes: int = 20 * 1024 * 1024
    max_audio_s: float = 600.0
    min_audio_s: float = 0.1
    max_tts_chars: int = 2000
    stt_timeout_s: float = 60.0
    tts_timeout_s: float = 30.0
    max_concurrency: int = 1
    queue_timeout_s: float = 10.0
    preload: bool = True
    # VAD (Silero, incluido en faster-whisper) para decodificar y para el endpointing
    vad_filter: bool = True  # SPEECH_VAD_FILTER: vad_filter de faster-whisper en parciales y finales
    vad_threshold: float = 0.5
    vad_min_speech_ms: int = 100
    vad_min_silence_ms: int = 1000  # finales: no partir por pausas de vacilación
    vad_partial_min_silence_ms: int = 500
    vad_speech_pad_ms: int = 300
    endpoint_min_silence_ms: int = 200  # granularidad de speech_ended_ms / trailing_silence_ms
    vad_energy_dbfs: float = -45.0  # VAD por energía (mock y respaldo)
    mock_trailing_silence_ms: int | None = None  # SPEECH_MOCK_TRAILING_SILENCE_MS: fija el valor en mock
    mock_text: str = ""  # SPEECH_MOCK_TEXT: texto fijo del mock si no viene X-Mock-Transcript (extensión, apagada por defecto)

    @classmethod
    def from_env(cls) -> Settings:
        mode = os.environ.get("CX9_SPEECH_MODE", "real").strip().lower() or "real"
        if mode not in ("real", "mock"):
            raise ValueError(f"CX9_SPEECH_MODE inválido: {mode} (real | mock)")
        return cls(
            mode=mode,
            token=os.environ.get("CX9_SPEECH_TOKEN", ""),
            stt_backend="mock"
            if mode == "mock"
            else os.environ.get("SPEECH_STT_BACKEND", "faster-whisper").lower(),
            tts_backend="mock" if mode == "mock" else os.environ.get("SPEECH_TTS_BACKEND", "auto").lower(),
            whisper_model=os.environ.get("SPEECH_WHISPER_MODEL", "small"),
            whisper_device=os.environ.get("SPEECH_WHISPER_DEVICE", "cpu"),
            whisper_compute=os.environ.get("SPEECH_WHISPER_COMPUTE", "int8"),
            whisper_beam_size=_int("SPEECH_WHISPER_BEAM_SIZE", 5),
            whisper_partial_beam_size=_int("SPEECH_WHISPER_PARTIAL_BEAM_SIZE", 1),
            whisper_cpu_threads=_int("SPEECH_WHISPER_CPU_THREADS", 2),
            whisper_download_root=os.environ.get("SPEECH_WHISPER_DOWNLOAD_ROOT", "models/whisper"),
            whisper_local_only=_bool("SPEECH_WHISPER_LOCAL_ONLY", True),
            filler_prompt={
                "es": os.environ.get("SPEECH_FILLER_PROMPT_ES", DEFAULT_FILLER_PROMPT["es"]),
                "en": os.environ.get("SPEECH_FILLER_PROMPT_EN", DEFAULT_FILLER_PROMPT["en"]),
            },
            low_confidence_threshold=_float("SPEECH_LOW_CONFIDENCE", 0.5),
            pause_threshold_s=_float("SPEECH_PAUSE_THRESHOLD_S", 0.6),
            piper_bin=os.environ.get("SPEECH_PIPER_BIN", "piper"),
            piper_voices={
                k: v
                for k, v in {
                    "es_AR": os.environ.get("SPEECH_PIPER_VOICE_ES_AR", ""),
                    "en_US": os.environ.get("SPEECH_PIPER_VOICE_EN_US", ""),
                }.items()
                if v
            },
            espeak_voices={
                "es": os.environ.get("SPEECH_ESPEAK_VOICE_ES", "es-419"),
                "en": os.environ.get("SPEECH_ESPEAK_VOICE_EN", "en-us"),
            },
            max_upload_bytes=int(_float("SPEECH_MAX_UPLOAD_MB", 20) * 1024 * 1024),
            max_audio_s=_float("SPEECH_MAX_AUDIO_S", 600.0),
            max_tts_chars=_int("SPEECH_MAX_TTS_CHARS", 2000),
            stt_timeout_s=_float("SPEECH_STT_TIMEOUT_S", 60.0),
            tts_timeout_s=_float("SPEECH_TTS_TIMEOUT_S", 30.0),
            max_concurrency=max(1, _int("SPEECH_MAX_CONCURRENCY", 1)),
            queue_timeout_s=_float("SPEECH_QUEUE_TIMEOUT_S", 10.0),
            preload=_bool("SPEECH_PRELOAD", True),
            vad_filter=_bool("SPEECH_VAD_FILTER", True),
            vad_threshold=_float("SPEECH_VAD_THRESHOLD", 0.5),
            vad_min_speech_ms=_int("SPEECH_VAD_MIN_SPEECH_MS", 100),
            vad_min_silence_ms=_int("SPEECH_VAD_MIN_SILENCE_MS", 1000),
            vad_partial_min_silence_ms=_int("SPEECH_VAD_PARTIAL_MIN_SILENCE_MS", 500),
            vad_speech_pad_ms=_int("SPEECH_VAD_SPEECH_PAD_MS", 300),
            endpoint_min_silence_ms=_int("SPEECH_ENDPOINT_MIN_SILENCE_MS", 200),
            vad_energy_dbfs=_float("SPEECH_VAD_ENERGY_DBFS", -45.0),
            mock_trailing_silence_ms=(
                _int("SPEECH_MOCK_TRAILING_SILENCE_MS", 0)
                if os.environ.get("SPEECH_MOCK_TRAILING_SILENCE_MS")
                else None
            ),
            mock_text=os.environ.get("SPEECH_MOCK_TEXT", ""),
        )
