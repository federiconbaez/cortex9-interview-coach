"""Utilidades de audio sin dependencias nativas (solo numpy + wave).

read_wav viene del CLI interview-sim: decodifica WAV PCM16 a float32 mono 16 kHz
para pasarle el array directo a faster-whisper sin depender de ffmpeg.
"""

from __future__ import annotations

import io
import wave

import numpy as np

SAMPLE_RATE = 16000


def is_wav(data: bytes) -> bool:
    return len(data) >= 12 and data[:4] == b"RIFF" and data[8:12] == b"WAVE"


def read_wav_bytes(data: bytes, target_sr: int = SAMPLE_RATE) -> np.ndarray:
    """WAV PCM16 (mono o estéreo) -> float32 mono a target_sr."""
    with wave.open(io.BytesIO(data), "rb") as w:
        sr, ch, width = w.getframerate(), w.getnchannels(), w.getsampwidth()
        raw = w.readframes(w.getnframes())
    if width != 2:
        raise ValueError(f"solo WAV PCM de 16 bits (tiene {8 * width} bits)")
    audio = np.frombuffer(raw, dtype="<i2").astype(np.float32) / 32768.0
    if ch > 1:
        audio = audio.reshape(-1, ch).mean(axis=1)
    if sr != target_sr and audio.size:
        n = int(round(audio.size * target_sr / sr))
        audio = np.interp(np.linspace(0, audio.size - 1, n), np.arange(audio.size), audio)
    return audio.astype(np.float32)


def read_pcm16le(data: bytes, sample_rate: int = SAMPLE_RATE, target_sr: int = SAMPLE_RATE) -> np.ndarray:
    """PCM16LE mono crudo (formato base del WS del backend) -> float32 mono a target_sr."""
    if len(data) % 2:
        raise ValueError("PCM16LE con cantidad impar de bytes")
    audio = np.frombuffer(data, dtype="<i2").astype(np.float32) / 32768.0
    if sample_rate != target_sr and audio.size:
        n = int(round(audio.size * target_sr / sample_rate))
        audio = np.interp(np.linspace(0, audio.size - 1, n), np.arange(audio.size), audio)
    return audio.astype(np.float32)


def wav_info(data: bytes) -> tuple[int, int, int]:
    """(sample_rate, canales, frames)."""
    with wave.open(io.BytesIO(data), "rb") as w:
        return w.getframerate(), w.getnchannels(), w.getnframes()


def wav_to_pcm16le(data: bytes, target_sr: int | None = None) -> tuple[bytes, int]:
    """WAV -> (PCM16LE mono crudo, sample_rate)."""
    sr = wav_info(data)[0]
    out_sr = target_sr or sr
    audio = read_wav_bytes(data, out_sr)
    return (np.clip(audio, -1.0, 1.0) * 32767).astype("<i2").tobytes(), out_sr


def wav_duration_s(data: bytes) -> float:
    with wave.open(io.BytesIO(data), "rb") as w:
        return w.getnframes() / float(w.getframerate() or 1)


def to_wav_bytes(audio: np.ndarray, sr: int = SAMPLE_RATE) -> bytes:
    pcm = (np.clip(audio, -1.0, 1.0) * 32767).astype("<i2")
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(sr)
        w.writeframes(pcm.tobytes())
    return buf.getvalue()


def tone(seconds: float = 0.5, freq: float = 440.0, sr: int = 22050) -> bytes:
    """WAV de prueba (tono puro)."""
    t = np.arange(int(seconds * sr)) / sr
    return to_wav_bytes(0.2 * np.sin(2 * np.pi * freq * t), sr)


def parse_content_type(value: str | None) -> tuple[str, dict[str, str]]:
    """'audio/L16; rate=16000; channels=1' -> ('audio/l16', {'rate': '16000', 'channels': '1'})."""
    if not value:
        return "", {}
    parts = [p.strip() for p in value.split(";")]
    params = {}
    for p in parts[1:]:
        k, _, v = p.partition("=")
        if k:
            params[k.strip().lower()] = v.strip().strip('"').lower()
    return parts[0].lower(), params


def decode_container(data: bytes, target_sr: int = SAMPLE_RATE, max_s: float = 600.0) -> np.ndarray:
    """Ogg/Opus (u otro contenedor) -> float32 mono a target_sr, con PyAV (dep. de faster-whisper)."""
    import av  # type: ignore

    chunks: list[np.ndarray] = []
    total = 0
    limit = int(max_s * target_sr * 1.01) + target_sr // 10
    resampler = av.AudioResampler(format="s16", layout="mono", rate=target_sr)
    with av.open(io.BytesIO(data), mode="r") as container:
        for frame in container.decode(audio=0):
            for out in resampler.resample(frame):
                arr = out.to_ndarray().reshape(-1)
                chunks.append(arr)
                total += arr.size
                if total > limit:  # no decodificar audio infinito
                    raise ValueError("audio demasiado largo")
        for out in resampler.resample(None):
            chunks.append(out.to_ndarray().reshape(-1))
    if not chunks:
        return np.zeros(0, np.float32)
    return (np.concatenate(chunks).astype(np.float32) / 32768.0).astype(np.float32)
