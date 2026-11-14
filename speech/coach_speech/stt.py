"""Backends de transcripción detrás de una interfaz común (contrato §6.1).

FasterWhisperSTT conserva las muletillas: desactiva la supresión de tokens,
no usa VAD (que recorta vacilaciones) y pasa un initial_prompt con muletillas
para que Whisper no "limpie" la transcripción. Basado en interview_sim.stt.
"""

from __future__ import annotations

import hashlib
import os
import tempfile
import threading
from dataclasses import dataclass, field
from typing import Protocol

import numpy as np

from .config import Settings


@dataclass
class Word:
    word: str
    start_s: float
    end_s: float
    confidence: float


@dataclass
class Segment:
    start_s: float
    end_s: float
    text: str
    words: list[Word] = field(default_factory=list)
    avg_logprob: float | None = None


@dataclass
class STTRequest:
    body: bytes  # cuerpo crudo recibido (el mock lo hashea)
    duration_s: float  # duración real del audio recibido
    language: str | None  # "es" | "en" | None (= auto)
    samples: np.ndarray | None = None  # float32 mono 16 kHz (L16 / WAV)
    container: bytes | None = None  # ogg/opus: lo decodifica faster-whisper (PyAV)
    prompt: str | None = None
    partial: bool = False
    mock_transcript: str | None = None


@dataclass
class Transcript:
    text: str
    language: str
    segments: list[Segment]
    engine: str
    model: str


class STTBackend(Protocol):
    name: str
    model: str

    def transcribe(self, req: STTRequest) -> Transcript: ...

    def ready(self) -> tuple[bool, str]: ...


class FasterWhisperSTT:
    name = "faster-whisper"

    def __init__(self, settings: Settings):
        self.s = settings
        self.model = os.path.basename(settings.whisper_model.rstrip("/")) or settings.whisper_model
        self._model = None
        self._lock = threading.Lock()
        self.loading = False
        self.load_error: str | None = None

    def load(self):
        with self._lock:
            if self._model is None:
                self.loading = True
                try:
                    from faster_whisper import WhisperModel  # type: ignore

                    self._model = WhisperModel(
                        self.s.whisper_model,
                        device=self.s.whisper_device,
                        compute_type=self.s.whisper_compute,
                        cpu_threads=self.s.whisper_cpu_threads,
                        download_root=self.s.whisper_download_root,
                        local_files_only=self.s.whisper_local_only,
                    )
                    self.load_error = None
                except Exception as e:
                    self.load_error = type(e).__name__
                    raise
                finally:
                    self.loading = False
        return self._model

    def ready(self) -> tuple[bool, str]:
        if self._model is not None:
            return True, f"modelo {self.model} cargado"
        if self.loading:
            return False, "cargando modelo"
        if self.load_error:
            return False, f"no se pudo cargar el modelo ({self.load_error})"
        try:
            import faster_whisper  # type: ignore  # noqa: F401
        except ImportError:
            return False, "faster-whisper no instalado"
        return True, "el modelo se carga en el primer uso"

    def decode_options(self, language: str | None, prompt: str | None, partial: bool = False) -> dict:
        default_prompt = self.s.filler_prompt.get(language or "es")
        return {
            "language": language,
            "task": "transcribe",
            "beam_size": self.s.whisper_partial_beam_size if partial else self.s.whisper_beam_size,
            "word_timestamps": True,
            "initial_prompt": prompt if prompt else default_prompt,
            # Sin supresión: por defecto Whisper suprime símbolos y blancos y
            # tiende a borrar "um", "eh", "este"...
            "suppress_tokens": [],
            "suppress_blank": False,
            "vad_filter": False,
            "condition_on_previous_text": False,
        }

    def transcribe(self, req: STTRequest) -> Transcript:
        model = self.load()
        opts = self.decode_options(req.language, req.prompt, req.partial)
        tmp = None
        try:
            if req.samples is not None:
                src = req.samples
            else:
                fd, tmp = tempfile.mkstemp(prefix="coach-stt-")
                with os.fdopen(fd, "wb") as f:
                    f.write(req.container or b"")
                src = tmp
            raw_segments, info = model.transcribe(src, **opts)
            raw_segments = list(raw_segments)
        finally:
            if tmp:
                os.unlink(tmp)
        segments = [
            Segment(
                start_s=round(float(s.start), 3),
                end_s=round(float(s.end), 3),
                text=s.text.strip(),
                words=[
                    Word(
                        w.word.strip(),
                        round(float(w.start), 3),
                        round(float(w.end), 3),
                        round(float(w.probability), 4),
                    )
                    for w in (s.words or [])
                ],
                avg_logprob=float(s.avg_logprob),
            )
            for s in raw_segments
        ]
        return Transcript(
            text=" ".join(s.text for s in segments).strip(),
            language=getattr(info, "language", None) or req.language or "es",
            segments=segments,
            engine=self.name,
            model=self.model,
        )


MOCK_PREFIX = {"es": "respuesta simulada", "en": "simulated answer"}


class MockSTT:
    """Mock determinista del contrato §6.7: misma entrada -> mismos bytes de salida."""

    name = "mock"
    model = "mock"

    def __init__(self, default_text: str = ""):
        # default_text (SPEECH_MOCK_TEXT) reemplaza al texto con hash cuando no viene
        # X-Mock-Transcript: sirve para pruebas de integración a través del backend,
        # que no reenvía headers X-Mock-*. Vacío = comportamiento exacto del contrato §6.7.
        self.default_text = default_text
        self.calls: list[STTRequest] = []

    def ready(self) -> tuple[bool, str]:
        return True, "mock"

    def transcribe(self, req: STTRequest) -> Transcript:
        self.calls.append(req)
        lang = req.language or "es"
        if req.mock_transcript is not None:
            raw = req.mock_transcript
        elif self.default_text:
            raw = self.default_text
        else:
            raw = f"{MOCK_PREFIX.get(lang, MOCK_PREFIX['es'])} {hashlib.sha256(req.body).hexdigest()[:8]}"
        tokens = raw.split()
        dur = req.duration_s
        step = dur / len(tokens) if tokens else 0.0
        words = []
        for i, tok in enumerate(tokens):
            low = tok.startswith("?") and len(tok) > 1
            words.append(
                Word(
                    tok[1:] if low else tok, round(i * step, 3), round((i + 1) * step, 3), 0.3 if low else 0.9
                )
            )
        text = " ".join(w.word for w in words)
        return Transcript(text, lang, [Segment(0.0, dur, text, words)], self.name, self.model)


def make_stt(settings: Settings) -> STTBackend:
    kind = settings.stt_backend
    if kind in ("faster-whisper", "whisper", "local"):
        return FasterWhisperSTT(settings)
    if kind == "mock":
        return MockSTT(settings.mock_text)
    raise ValueError(f"SPEECH_STT_BACKEND desconocido: {kind} (faster-whisper | mock)")
