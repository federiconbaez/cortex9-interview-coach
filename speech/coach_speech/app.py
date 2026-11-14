"""API HTTP del servicio de voz según docs/contract.md §6 (backend -> /speech).

Rutas: POST /stt (cuerpo binario crudo), POST /tts, GET /health, GET /voices,
GET /metrics. Además /healthz (liveness) y /readyz (= /health) para
orquestadores. Errores RFC 9457 (application/problem+json).
"""

from __future__ import annotations

import asyncio
import hmac
import logging
import math
import re
import threading
import time
import uuid
from contextlib import asynccontextmanager
from typing import Literal

from fastapi import FastAPI, Query, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse, Response
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest
from pydantic import BaseModel, ConfigDict, Field
from starlette.concurrency import run_in_threadpool

from . import __version__
from .audio import (
    SAMPLE_RATE,
    decode_container,
    parse_content_type,
    read_pcm16le,
    read_wav_bytes,
    to_wav_bytes,
    wav_info,
    wav_to_pcm16le,
)
from .config import Settings
from .fillers import find_pauses, mark_fillers
from .limits import BodySizeLimitMiddleware
from .metrics import Metrics
from .stt import STTBackend, STTRequest, make_stt
from .tts import TTSError, make_tts
from .vad import Endpoint
from .vad import endpoint as vad_endpoint

log = logging.getLogger("coach_speech")

PROBLEM = "application/problem+json"
TITLES = {
    400: "Petición mal formada",
    401: "No autenticado",
    413: "Audio o texto demasiado grande",
    415: "Tipo de audio no soportado",
    422: "La petición no pasó la validación",
    500: "Error interno",
    503: "Servicio de voz no disponible",
    504: "El servicio de voz no respondió a tiempo",
}
WAV_TYPES = {"audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave"}
L16_FORMAT = "audio/L16;rate=16000;channels=1"
REQUEST_ID = re.compile(r"^[A-Za-z0-9-]{8,64}$")
LANGS = ("es", "en")


class APIError(Exception):
    def __init__(
        self,
        status: int,
        code: str,
        detail: str,
        retryable: bool = False,
        headers: dict | None = None,
        errors: list | None = None,
    ):
        self.status, self.code, self.detail = status, code, detail
        self.retryable, self.headers, self.errors = retryable, headers or {}, errors


def problem(request: Request, e: APIError) -> JSONResponse:
    body = {
        "type": "urn:cx9:problem:" + e.code.lower().replace("_", "-"),
        "title": TITLES.get(e.status, "Error"),
        "status": e.status,
        "detail": e.detail,
        "instance": request.url.path,
        "code": e.code,
        "request_id": getattr(request.state, "request_id", None),
        "retryable": e.retryable,
    }
    if e.errors:
        body["errors"] = e.errors
    return JSONResponse(body, status_code=e.status, headers=e.headers, media_type=PROBLEM)


class TTSRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")

    text: str = Field(..., min_length=1, max_length=2000)
    language: str
    voice: str | None = None
    format: Literal["wav", "pcm16le", "opus"] = "wav"
    sample_rate: int | None = Field(None, ge=8000, le=48000)
    speed: float = Field(1.0, ge=0.5, le=2.0)


def _weighted_confidence(words: list[dict], simple: bool = False) -> float | None:
    """Promedio de confianza ponderado por duración (§6.1); simple en el mock (§6.7)."""
    if not words:
        return None
    if simple:
        return round(sum(w["confidence"] for w in words) / len(words), 4)
    weights = [max(w["end_s"] - w["start_s"], 0.001) for w in words]
    return round(sum(w["confidence"] * k for w, k in zip(words, weights, strict=True)) / sum(weights), 4)


def opus_supported() -> bool:
    try:
        import av  # type: ignore  # noqa: F401
    except ImportError:
        return False
    return True


def create_app(settings: Settings | None = None, stt: STTBackend | None = None, tts=None) -> FastAPI:
    s = settings or Settings.from_env()
    mock = s.mode == "mock"
    stt_backend = stt or make_stt(s)
    tts_backend = tts or make_tts(s)
    metrics = Metrics()
    sem = asyncio.Semaphore(s.max_concurrency)
    accept_opus = not mock and stt_backend.name == "faster-whisper" and opus_supported()
    input_formats = [L16_FORMAT, "audio/wav"] + (["audio/ogg;codecs=opus"] if accept_opus else [])

    @asynccontextmanager
    async def lifespan(app: FastAPI):
        if s.preload and not mock and hasattr(stt_backend, "load"):
            # Se carga en segundo plano: mientras tanto /stt responde 503 MODEL_LOADING.
            stt_backend.loading = True

            def _load():
                try:
                    stt_backend.load()
                except Exception as e:  # noqa: BLE001 - se informa en /health
                    log.error("no se pudo cargar el modelo STT: %s", type(e).__name__)
                try:  # calienta Silero (la primera llamada carga el ONNX, ~150 ms)
                    import numpy as np

                    vad_endpoint(np.zeros(SAMPLE_RATE // 2, dtype=np.float32), s, True)
                except Exception as e:  # noqa: BLE001 - sin Silero se usa el VAD por energía
                    log.warning("VAD Silero no disponible: %s", type(e).__name__)

            threading.Thread(target=_load, name="stt-preload", daemon=True).start()
        yield

    app = FastAPI(
        title="CORTEX-9 Interview Coach - Speech",
        version=__version__,
        lifespan=lifespan,
        docs_url="/docs",
        redoc_url=None,
    )
    app.state.settings, app.state.stt, app.state.tts, app.state.metrics = s, stt_backend, tts_backend, metrics
    app.add_middleware(
        BodySizeLimitMiddleware,
        max_bytes=s.max_upload_bytes,
        on_reject=lambda: metrics.rejected.labels("too_large").inc(),
    )

    @app.middleware("http")
    async def observe(request: Request, call_next):
        rid = request.headers.get("x-request-id", "")
        request.state.request_id = rid if REQUEST_ID.match(rid) else str(uuid.uuid4())
        t0 = time.perf_counter()
        response = await call_next(request)
        response.headers["X-Request-Id"] = request.state.request_id
        endpoint = getattr(request.scope.get("route"), "path", "unmatched")
        metrics.requests.labels(endpoint, request.method, str(response.status_code)).inc()
        if endpoint != "/metrics":
            metrics.latency.labels(endpoint).observe(time.perf_counter() - t0)
        return response

    @app.exception_handler(APIError)
    async def api_error(request: Request, e: APIError):
        return problem(request, e)

    @app.exception_handler(RequestValidationError)
    async def validation_error(request: Request, e: RequestValidationError):
        errs = e.errors()
        if any(err.get("type") == "json_invalid" for err in errs):
            return problem(request, APIError(400, "BAD_REQUEST", "JSON mal formado"))
        errors = [
            {"pointer": "/" + "/".join(str(p) for p in err.get("loc", [])[1:]), "message": err.get("msg", "")}
            for err in errs
        ]
        return problem(
            request, APIError(422, "VALIDATION_FAILED", "la petición no pasó la validación", errors=errors)
        )

    def check_auth(request: Request) -> None:
        if not s.token:
            return
        got = request.headers.get("authorization", "")
        if not hmac.compare_digest(got.encode(), f"Bearer {s.token}".encode()):
            raise APIError(401, "UNAUTHENTICATED", "falta o es inválido el token Bearer")

    async def mock_fail(request: Request) -> None:
        """X-Mock-Fail: 503|504|timeout (solo CX9_SPEECH_MODE=mock)."""
        if not mock:
            return
        how = request.headers.get("x-mock-fail", "").strip().lower()
        if how == "503":
            raise APIError(
                503, "DEPENDENCY_UNAVAILABLE", "fallo simulado (X-Mock-Fail)", True, {"Retry-After": "1"}
            )
        if how == "504":
            raise APIError(504, "DEPENDENCY_TIMEOUT", "timeout simulado (X-Mock-Fail)", True)
        if how == "timeout":
            deadline = time.monotonic() + 600
            while time.monotonic() < deadline and not await request.is_disconnected():
                await asyncio.sleep(0.05)
            raise APIError(504, "DEPENDENCY_TIMEOUT", "timeout simulado (X-Mock-Fail)", True)

    async def guarded(op: str, timeout: float, wait: bool, fn, *args):
        try:
            if wait:
                await asyncio.wait_for(sem.acquire(), timeout=s.queue_timeout_s)
            elif sem.locked():
                raise asyncio.TimeoutError
            else:
                await sem.acquire()
        except asyncio.TimeoutError:
            metrics.rejected.labels("busy").inc()
            raise APIError(
                503, "DEPENDENCY_UNAVAILABLE", "servicio de voz ocupado", True, {"Retry-After": "1"}
            ) from None
        metrics.in_flight.inc()
        try:
            return await asyncio.wait_for(run_in_threadpool(fn, *args), timeout=timeout)
        except asyncio.TimeoutError:
            metrics.timeouts.labels(op).inc()
            raise APIError(504, "DEPENDENCY_TIMEOUT", f"{op} superó {timeout:g} s", True) from None
        finally:
            metrics.in_flight.dec()
            sem.release()

    def tts_status() -> dict:
        ok, _ = tts_backend.available()
        engines = getattr(tts_backend, "engines", [tts_backend])
        usable = [e.name for e in engines if e.available()[0]]
        return {
            "ready": ok,
            "engine": usable[0] if usable else tts_backend.name,
            "fallback": usable[1] if len(usable) > 1 else None,
            "output_formats": ["wav", "pcm16le"],
            "degraded": bool(usable) and usable[0] != engines[0].name,
        }

    def health_body() -> tuple[dict, int, dict]:
        stt_ok, stt_why = stt_backend.ready()
        tts = tts_status()
        degraded = tts.pop("degraded")
        status = "down" if not stt_ok else ("degraded" if degraded or not tts["ready"] else "ok")
        body = {
            "status": status,
            "version": __version__,
            "mode": s.mode,
            "stt": {
                "ready": stt_ok,
                "engine": stt_backend.name,
                "model": stt_backend.model,
                "languages": list(LANGS),
                "input_formats": input_formats,
                "detail": stt_why,
            },
            "tts": tts,
        }
        headers = {"Retry-After": "5"} if getattr(stt_backend, "loading", False) else {}
        return body, (503 if status == "down" else 200), headers

    @app.get("/health")
    @app.get("/readyz", include_in_schema=False)
    async def health():
        body, code, headers = health_body()
        return JSONResponse(body, status_code=code, headers=headers)

    @app.get("/healthz")
    async def healthz():
        return {"status": "ok", "version": __version__}

    @app.get("/metrics")
    async def prom():
        return Response(generate_latest(metrics.registry), media_type=CONTENT_TYPE_LATEST)

    @app.get("/voices")
    async def voices(request: Request):
        check_auth(request)
        items = []
        for lang in LANGS:
            for i, v in enumerate(tts_backend.voices(lang)):
                items.append({"id": v.id, "language": v.language, "engine": v.engine, "default": i == 0})
        return {"items": items}

    def decode_audio(body: bytes, content_type: str | None) -> tuple[float, object, object]:
        """-> (duración_s, samples float32 16 kHz | None, contenedor | None)."""
        mime, params = parse_content_type(content_type)
        if mime == "audio/l16":
            try:
                rate = int(params.get("rate", SAMPLE_RATE))
                channels = int(params.get("channels", "1"))
            except ValueError:
                raise APIError(400, "BAD_REQUEST", "parámetros de audio/L16 inválidos") from None
            if channels != 1 or not 8000 <= rate <= 48000:
                raise APIError(415, "UNSUPPORTED_MEDIA_TYPE", "audio/L16 debe ser mono, 8-48 kHz")
            if len(body) % 2:
                raise APIError(400, "BAD_REQUEST", "PCM16 con cantidad impar de bytes")
            return len(body) / (2 * rate), body, ("l16", rate)
        if mime in WAV_TYPES:
            try:
                rate, _, frames = wav_info(body)
                return frames / float(rate or 1), body, ("wav", rate)
            except Exception:
                raise APIError(415, "UNSUPPORTED_MEDIA_TYPE", "WAV inválido o no PCM") from None
        if mime == "audio/ogg" and params.get("codecs", "opus") == "opus" and accept_opus:
            return -1.0, body, ("ogg", 48000)
        raise APIError(
            415,
            "UNSUPPORTED_MEDIA_TYPE",
            f"Content-Type no soportado; usá uno de: {', '.join(input_formats)}",
        )

    def to_samples(kind: tuple, body: bytes):
        if kind[0] == "l16":
            return read_pcm16le(body, kind[1])
        if kind[0] == "wav":
            try:
                return read_wav_bytes(body)
            except ValueError as e:
                raise APIError(415, "UNSUPPORTED_MEDIA_TYPE", str(e)) from None
        return decode_container(body, max_s=s.max_audio_s)

    @app.post("/stt")
    async def transcribe(
        request: Request,
        language: str = Query(...),
        word_confidence: bool = Query(True),
        prompt: str | None = Query(None, max_length=500),
        partial: bool = Query(False),
        vad: bool | None = Query(None),
    ):
        check_auth(request)
        await mock_fail(request)
        lang = language.strip().lower()
        if lang not in (*LANGS, "auto"):
            raise APIError(422, "UNSUPPORTED_LANGUAGE", "language debe ser es, en o auto")
        body = await request.body()
        if len(body) > s.max_upload_bytes:
            raise APIError(413, "PAYLOAD_TOO_LARGE", f"más de {s.max_upload_bytes // (1024 * 1024)} MB")
        if not body:
            raise APIError(422, "NO_AUDIO", "el cuerpo está vacío")
        duration, _, kind = decode_audio(body, request.headers.get("content-type"))
        samples = None
        # Endpointing (contrato v1.2): con partial=true (o vad=true) se informan
        # speech_ended_ms y trailing_silence_ms; vad=false lo apaga.
        want_endpoint = partial if vad is None else vad
        mock_forced = mock and (
            request.headers.get("x-mock-trailing-silence") is not None
            or request.headers.get("x-mock-speech-ended") is not None
            or s.mock_trailing_silence_ms is not None
        )
        if not mock or kind[0] == "ogg" or (want_endpoint and not mock_forced):
            if getattr(stt_backend, "loading", False):
                raise APIError(
                    503, "MODEL_LOADING", "cargando el modelo de transcripción", True, {"Retry-After": "5"}
                )
            try:
                samples = await run_in_threadpool(to_samples, kind, body)
            except APIError:
                raise
            except Exception:
                raise APIError(415, "UNSUPPORTED_MEDIA_TYPE", "no se pudo decodificar el audio") from None
            if kind[0] == "ogg":
                duration = len(samples) / SAMPLE_RATE
        duration = round(duration, 3)
        if duration < s.min_audio_s:
            raise APIError(422, "NO_AUDIO", f"audio de menos de {int(s.min_audio_s * 1000)} ms")
        if duration > s.max_audio_s:
            raise APIError(413, "PAYLOAD_TOO_LARGE", f"más de {s.max_audio_s:g} s de audio")
        metrics.upload_bytes.observe(len(body))
        ep = None
        if want_endpoint:
            if mock_forced:
                ep = mock_endpoint(request, duration)
            else:
                ep = await run_in_threadpool(vad_endpoint, samples, s, not mock)
            metrics.vad_requests.labels(ep.engine).inc()
        mock_text = request.headers.get("x-mock-transcript") if mock else None
        if mock_text is not None:
            from urllib.parse import unquote

            mock_text = unquote(mock_text)
        req = STTRequest(
            body=body,
            duration_s=duration,
            language=None if lang == "auto" else lang,
            samples=samples,
            prompt=prompt or None,
            partial=partial,
            mock_transcript=mock_text,
            no_speech=bool(ep and ep.engine == "silero" and ep.speech_ended_ms is None),
        )
        t0 = time.perf_counter()
        try:
            tr = await guarded("stt", s.stt_timeout_s, not partial, stt_backend.transcribe, req)
        except APIError:
            raise
        except Exception as e:
            # Sin detalles: el mensaje podría incluir rutas locales o texto del usuario.
            log.error("error de STT: %s", type(e).__name__)
            raise APIError(500, "INTERNAL", f"falló la transcripción ({type(e).__name__})") from None
        processing_ms = 0 if mock else int((time.perf_counter() - t0) * 1000)

        all_words: list[dict] = []
        segments = []
        for seg in tr.segments:
            words = [
                {"word": w.word, "start_s": w.start_s, "end_s": w.end_s, "confidence": w.confidence}
                for w in seg.words
            ]
            all_words.extend(words)
            conf = _weighted_confidence(words, simple=tr.engine == "mock")
            if conf is None and seg.avg_logprob is not None:
                conf = round(math.exp(seg.avg_logprob), 4)
            segments.append(
                {
                    "start_s": seg.start_s,
                    "end_s": seg.end_s,
                    "text": seg.text,
                    "confidence": conf,
                    "words": words,
                }
            )
        fillers = mark_fillers(all_words, tr.language, s.pause_threshold_s)
        pauses = find_pauses(all_words, s.pause_threshold_s)
        confidence = _weighted_confidence(all_words, simple=tr.engine == "mock")
        if not word_confidence:
            for seg in segments:
                seg["words"] = []
        metrics.stt_audio_seconds.labels(tr.language).inc(duration)
        metrics.stt_fillers.labels(tr.language).inc(len(fillers))
        out = {
            "text": tr.text,
            "language": tr.language,
            "duration_s": duration,
            "confidence": confidence,
            "segments": segments,
            "engine": tr.engine,
            "model": tr.model,
            "processing_ms": processing_ms,
            # Extensiones (los clientes ignoran campos desconocidos, §2.1):
            "fillers": fillers,
            "filler_count": len(fillers),
            "pauses": pauses,
        }
        if ep is not None:
            out.update(ep.fields())
        return out

    def mock_endpoint(request: Request, duration_s: float) -> Endpoint:
        """Mock: X-Mock-Trailing-Silence / X-Mock-Speech-Ended (ms) o SPEECH_MOCK_TRAILING_SILENCE_MS.

        Sin ninguno se usa la energía del PCM (vad.endpoint), también determinista.
        """
        dur = int(round(duration_s * 1000))
        h_tr = request.headers.get("x-mock-trailing-silence")
        h_end = request.headers.get("x-mock-speech-ended")
        try:
            tr = int(h_tr) if h_tr not in (None, "") else None
            end = int(h_end) if h_end not in (None, "") else None
        except ValueError:
            raise APIError(
                400, "BAD_REQUEST", "X-Mock-Trailing-Silence / X-Mock-Speech-Ended: ms enteros"
            ) from None
        if tr is None and end is None:
            tr = s.mock_trailing_silence_ms or 0
        if end is None:
            end = max(0, dur - tr)
        if tr is None:
            tr = max(0, dur - end)
        return Endpoint(end, tr, "mock-header")

    @app.post("/tts")
    async def synthesize(request: Request, req: TTSRequest):
        check_auth(request)
        await mock_fail(request)
        lang = req.language.strip().lower()
        if lang not in LANGS:
            raise APIError(422, "UNSUPPORTED_LANGUAGE", "language debe ser es o en")
        text = req.text.strip()
        if not text or len(text) > s.max_tts_chars:
            raise APIError(
                422,
                "VALIDATION_FAILED",
                f"text debe tener 1-{s.max_tts_chars} caracteres",
                errors=[{"pointer": "/text", "message": "longitud inválida"}],
            )
        if req.format == "opus":
            raise APIError(
                422,
                "VALIDATION_FAILED",
                "opus no está disponible (ver /health)",
                errors=[{"pointer": "/format", "message": "usá wav o pcm16le"}],
            )
        if req.voice and req.voice not in {v.id for v in tts_backend.voices(lang)}:
            raise APIError(422, "VOICE_NOT_FOUND", "voz desconocida para ese idioma (ver /voices)")
        before = getattr(tts_backend, "fallbacks", 0)
        try:
            sp = await guarded(
                "tts", s.tts_timeout_s, True, tts_backend.synthesize, text, lang, req.speed, req.voice
            )
        except TTSError as e:
            raise APIError(503, "DEPENDENCY_UNAVAILABLE", str(e), True, {"Retry-After": "5"}) from None
        if getattr(tts_backend, "fallbacks", 0) > before:
            metrics.tts_fallbacks.inc()
        metrics.tts_requests.labels(sp.engine, lang).inc()
        target_sr = None if mock else req.sample_rate  # el mock siempre es 16 kHz (§6.7)
        try:
            pcm, sr = wav_to_pcm16le(sp.audio, target_sr)
        except Exception:
            raise APIError(500, "INTERNAL", f"{sp.engine} devolvió audio inválido") from None
        duration_ms = int(round(len(pcm) / 2 / sr * 1000))
        if req.format == "pcm16le":
            body, media = pcm, f"audio/L16; rate={sr}; channels=1"
        else:
            body = sp.audio if target_sr is None else to_wav_bytes(read_pcm16le(pcm, sr, sr), sr)
            media = "audio/wav"
        return Response(
            body,
            media_type=media,
            headers={
                "X-Audio-Duration-Ms": str(duration_ms),
                "X-Sample-Rate": str(sr),
                "X-Voice": sp.voice,
                "X-Engine": sp.engine,
                "Cache-Control": "no-store",
            },
        )

    return app
