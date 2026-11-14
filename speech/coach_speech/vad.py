"""Detección de voz para el endpointing del backend y para filtrar la decodificación.

- Silero VAD (incluido en faster-whisper, ONNX, sin descargas) en modo real.
- VAD por energía (RMS por tramos de 30 ms) como respaldo y en modo mock:
  determinista, sin modelos.

``endpoint()`` devuelve los campos opcionales de /stt con ``partial=true``
(contrato v1.2): ``speech_ended_ms`` (fin de la última voz, relativo al inicio
de la ventana enviada; ``None`` si no hubo voz) y ``trailing_silence_ms``
(silencio desde ese punto hasta el final de la ventana).
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .config import Settings

SAMPLE_RATE = 16000


@dataclass
class Endpoint:
    speech_ended_ms: int | None
    trailing_silence_ms: int
    engine: str  # silero | energy | mock-header

    def fields(self) -> dict:
        return {"speech_ended_ms": self.speech_ended_ms, "trailing_silence_ms": self.trailing_silence_ms}


def energy_segments(samples: np.ndarray, threshold_dbfs: float, frame_ms: int = 30) -> list[tuple[int, int]]:
    """Tramos con voz [inicio, fin) en ms según la energía RMS por tramo."""
    n = int(SAMPLE_RATE * frame_ms / 1000)
    if samples is None or len(samples) == 0:
        return []
    frames = len(samples) // n
    rest = len(samples) - frames * n
    out: list[tuple[int, int]] = []
    start = None
    limit = 10 ** (threshold_dbfs / 20)
    for i in range(frames + (1 if rest else 0)):
        chunk = samples[i * n : (i + 1) * n]
        voiced = float(np.sqrt(np.mean(np.square(chunk, dtype=np.float64)))) >= limit if len(chunk) else False
        t = i * frame_ms
        if voiced and start is None:
            start = t
        elif not voiced and start is not None:
            out.append((start, t))
            start = None
    if start is not None:
        out.append((start, int(round(len(samples) * 1000 / SAMPLE_RATE))))
    return out


def silero_segments(samples: np.ndarray, s: Settings) -> list[tuple[int, int]]:
    from faster_whisper.vad import VadOptions, get_speech_timestamps  # type: ignore

    opts = VadOptions(
        threshold=s.vad_threshold,
        min_speech_duration_ms=s.vad_min_speech_ms,
        min_silence_duration_ms=s.endpoint_min_silence_ms,
        speech_pad_ms=0,  # sin relleno: el fin de voz tiene que ser el real
    )
    ts = get_speech_timestamps(samples, vad_options=opts, sampling_rate=SAMPLE_RATE)
    return [(int(t["start"] * 1000 / SAMPLE_RATE), int(t["end"] * 1000 / SAMPLE_RATE)) for t in ts]


def from_segments(segments: list[tuple[int, int]], duration_ms: int, engine: str) -> Endpoint:
    if not segments:
        return Endpoint(None, duration_ms, engine)
    end = min(segments[-1][1], duration_ms)
    return Endpoint(end, max(0, duration_ms - end), engine)


def endpoint(samples: np.ndarray, s: Settings, use_silero: bool) -> Endpoint:
    duration_ms = int(round(len(samples) * 1000 / SAMPLE_RATE))
    if use_silero:
        try:
            return from_segments(silero_segments(samples, s), duration_ms, "silero")
        except ImportError:
            pass
    return from_segments(energy_segments(samples, s.vad_energy_dbfs), duration_ms, "energy")


def whisper_vad_parameters(s: Settings, partial: bool) -> dict:
    """vad_parameters de faster-whisper para decodificar (parciales y finales).

    Relleno generoso y silencio mínimo largo para no recortar vacilaciones
    ("eh", "este") que suelen venir pegadas a pausas.
    """
    return {
        "threshold": s.vad_threshold,
        "min_speech_duration_ms": s.vad_min_speech_ms,
        "min_silence_duration_ms": s.vad_partial_min_silence_ms if partial else s.vad_min_silence_ms,
        "speech_pad_ms": s.vad_speech_pad_ms,
    }
