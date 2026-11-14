"""Backends de síntesis de voz, todos locales (contrato §6.2).

Piper (voces es_AR / en_US, rutas configurables) es el motor principal;
espeak-ng/espeak queda como respaldo si piper o la voz no están o fallan.
Basado en interview_sim.tts, pero devuelve bytes WAV en vez de reproducir.
"""

from __future__ import annotations

import logging
import shutil
import subprocess  # noqa: S404 - binarios locales, argumentos fijos, sin shell
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol

import numpy as np

from .audio import to_wav_bytes
from .config import Settings, normalize_language, voice_key

log = logging.getLogger("coach_speech.tts")


class TTSError(RuntimeError):
    pass


@dataclass
class Speech:
    audio: bytes  # WAV PCM16 mono
    engine: str
    voice: str


@dataclass
class Voice:
    id: str
    language: str
    engine: str


class TTSBackend(Protocol):
    name: str

    def synthesize(
        self, text: str, language: str, speed: float = 1.0, voice: str | None = None
    ) -> Speech: ...

    def voices(self, language: str) -> list[Voice]: ...

    def available(self, language: str | None = None) -> tuple[bool, str]: ...


class PiperTTS:
    name = "piper"

    def __init__(self, settings: Settings):
        self.s = settings

    def binary(self) -> str | None:
        b = self.s.piper_bin
        return b if Path(b).is_file() else shutil.which(b)

    def voice_path(self, language: str) -> Path | None:
        p = self.s.piper_voices.get(voice_key(language))
        if not p:
            return None
        path = Path(p).expanduser()
        return path if path.is_file() else None

    def voices(self, language: str) -> list[Voice]:
        path = self.voice_path(language)
        if not path or not self.binary():
            return []
        return [Voice(path.name.removesuffix(".onnx"), normalize_language(language), self.name)]

    def available(self, language: str | None = None) -> tuple[bool, str]:
        if not self.binary():
            return False, "binario piper no encontrado"
        langs = [language] if language else ["es", "en"]
        missing = [voice_key(lang) for lang in langs if self.voice_path(lang) is None]
        if missing:
            return False, f"faltan voces piper: {', '.join(missing)}"
        return True, "ok"

    def args(self, language: str, speed: float, out: Path) -> list[str]:
        binary, voice = self.binary(), self.voice_path(language)
        if not binary or not voice:
            raise TTSError(self.available(language)[1])
        scale = f"{1.0 / speed:.3f}"  # length_scale > 1 = más lento
        return [binary, "--model", str(voice), "--length_scale", scale, "--output_file", str(out)]

    def synthesize(self, text: str, language: str, speed: float = 1.0, voice: str | None = None) -> Speech:
        with tempfile.TemporaryDirectory(prefix="coach-tts-") as d:
            out = Path(d) / "out.wav"
            cmd = self.args(language, speed, out)
            try:
                subprocess.run(  # noqa: S603
                    cmd,
                    input=text.encode("utf-8"),
                    check=True,
                    capture_output=True,
                    timeout=self.s.tts_timeout_s,
                )
            except (subprocess.SubprocessError, OSError) as e:
                raise TTSError(f"piper falló: {type(e).__name__}") from e
            if not out.is_file() or out.stat().st_size == 0:
                raise TTSError("piper no generó audio")
            return Speech(out.read_bytes(), self.name, self.voices(language)[0].id)


class EspeakTTS:
    def __init__(self, settings: Settings):
        self.s = settings

    def binary(self) -> str | None:
        return shutil.which("espeak-ng") or shutil.which("espeak")

    @property
    def name(self) -> str:
        b = self.binary()
        return Path(b).name if b else "espeak-ng"

    def voices(self, language: str) -> list[Voice]:
        if not self.binary():
            return []
        lang = normalize_language(language)
        return [Voice(f"{self.name}-{self.s.espeak_voices.get(lang, lang)}", lang, self.name)]

    def available(self, language: str | None = None) -> tuple[bool, str]:
        return (True, "ok") if self.binary() else (False, "espeak-ng/espeak no encontrado")

    def args(self, language: str, speed: float) -> list[str]:
        binary = self.binary()
        if not binary:
            raise TTSError("espeak-ng/espeak no encontrado")
        lang = normalize_language(language)
        wpm = int({"es": 160, "en": 160}.get(lang, 160) * speed)
        return [binary, "-v", self.s.espeak_voices.get(lang, lang), "-s", str(wpm), "--stdout"]

    def synthesize(self, text: str, language: str, speed: float = 1.0, voice: str | None = None) -> Speech:
        cmd = self.args(language, speed)
        try:
            # El texto va por stdin para que nunca se interprete como opción.
            r = subprocess.run(  # noqa: S603
                cmd,
                input=text.encode("utf-8"),
                check=True,
                capture_output=True,
                timeout=self.s.tts_timeout_s,
            )
        except (subprocess.SubprocessError, OSError) as e:
            raise TTSError(f"espeak falló: {type(e).__name__}") from e
        if not r.stdout:
            raise TTSError("espeak no generó audio")
        return Speech(r.stdout, self.name, self.voices(language)[0].id)


class FallbackTTS:
    """Prueba los motores en orden y usa el primero que funcione."""

    def __init__(self, engines: list):
        self.engines = engines
        self.fallbacks = 0

    @property
    def name(self) -> str:
        return self.engines[0].name

    @property
    def fallback(self) -> str | None:
        return self.engines[1].name if len(self.engines) > 1 else None

    def voices(self, language: str) -> list[Voice]:
        return [v for e in self.engines for v in e.voices(language)]

    def primary_ok(self) -> bool:
        return self.engines[0].available()[0]

    def available(self, language: str | None = None) -> tuple[bool, str]:
        notes = []
        for e in self.engines:
            ok, why = e.available(language)
            if ok:
                return True, f"{e.name} disponible"
            notes.append(f"{e.name}: {why}")
        return False, "; ".join(notes)

    def synthesize(self, text: str, language: str, speed: float = 1.0, voice: str | None = None) -> Speech:
        order = list(self.engines)
        if voice:  # el motor dueño de la voz pedida va primero
            order.sort(key=lambda e: 0 if any(v.id == voice for v in e.voices(language)) else 1)
        errors = []
        for e in order:
            if not e.available(language)[0]:
                errors.append(f"{e.name}: no disponible")
                continue
            try:
                sp = e.synthesize(text, language, speed, voice)
                if e is not self.engines[0]:
                    self.fallbacks += 1
                return sp
            except TTSError as err:
                log.warning("motor TTS %s falló, probando respaldo", e.name)
                errors.append(f"{e.name}: {err}")
        raise TTSError("ningún motor TTS disponible (" + "; ".join(errors) + ")")


class MockTTS:
    """Mock determinista §6.7: silencio PCM16 a 16 kHz, 300 + 60 ms por palabra."""

    name = "mock"
    fallback = None
    SAMPLE_RATE = 16000

    def __init__(self):
        self.calls: list[dict] = []

    def voices(self, language: str) -> list[Voice]:
        lang = normalize_language(language)
        return [Voice(f"mock-{lang}", lang, self.name)]

    def available(self, language: str | None = None) -> tuple[bool, str]:
        return True, "mock"

    @staticmethod
    def duration_ms(text: str) -> int:
        return 300 + 60 * len(text.split())

    def synthesize(self, text: str, language: str, speed: float = 1.0, voice: str | None = None) -> Speech:
        self.calls.append({"text": text, "language": language, "speed": speed, "voice": voice})
        n = self.SAMPLE_RATE * self.duration_ms(text) // 1000
        return Speech(
            to_wav_bytes(np.zeros(n, np.float32), self.SAMPLE_RATE),
            self.name,
            voice or self.voices(language)[0].id,
        )


def make_tts(settings: Settings):
    kind = settings.tts_backend
    if kind == "auto":
        return FallbackTTS([PiperTTS(settings), EspeakTTS(settings)])
    if kind == "piper":
        return FallbackTTS([PiperTTS(settings)])
    if kind in ("espeak", "espeak-ng"):
        return FallbackTTS([EspeakTTS(settings)])
    if kind == "mock":
        return MockTTS()
    raise ValueError(f"SPEECH_TTS_BACKEND desconocido: {kind} (auto | piper | espeak | mock)")
