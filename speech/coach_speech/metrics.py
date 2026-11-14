"""Métricas Prometheus (registro propio por app, para tests aislados).

Nunca se etiqueta con texto del usuario: solo endpoint, estado, idioma y motor.
"""

from __future__ import annotations

from prometheus_client import CollectorRegistry, Counter, Gauge, Histogram


class Metrics:
    def __init__(self) -> None:
        self.registry = CollectorRegistry()
        self.requests = Counter(
            "speech_requests_total",
            "Solicitudes HTTP",
            ["endpoint", "method", "status"],
            registry=self.registry,
        )
        self.latency = Histogram(
            "speech_request_duration_seconds",
            "Latencia HTTP",
            ["endpoint"],
            buckets=(0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120),
            registry=self.registry,
        )
        self.stt_audio_seconds = Counter(
            "speech_stt_audio_seconds_total",
            "Segundos de audio transcriptos",
            ["language"],
            registry=self.registry,
        )
        self.stt_fillers = Counter(
            "speech_stt_fillers_total", "Muletillas detectadas", ["language"], registry=self.registry
        )
        self.upload_bytes = Histogram(
            "speech_upload_bytes",
            "Tamaño de audio subido",
            buckets=(1e4, 1e5, 5e5, 1e6, 2.5e6, 5e6, 1e7, 2.5e7),
            registry=self.registry,
        )
        self.tts_requests = Counter(
            "speech_tts_synth_total", "Síntesis por motor", ["engine", "language"], registry=self.registry
        )
        self.tts_fallbacks = Counter(
            "speech_tts_fallback_total", "Veces que se usó el motor de respaldo", registry=self.registry
        )
        self.timeouts = Counter(
            "speech_timeouts_total", "Operaciones cortadas por timeout", ["op"], registry=self.registry
        )
        self.rejected = Counter(
            "speech_rejected_total", "Solicitudes rechazadas", ["reason"], registry=self.registry
        )
        self.vad_requests = Counter(
            "speech_vad_endpoint_total",
            "Cálculos de fin de voz por motor",
            ["engine"],
            registry=self.registry,
        )
        self.in_flight = Gauge("speech_in_flight", "Operaciones STT/TTS en curso", registry=self.registry)
