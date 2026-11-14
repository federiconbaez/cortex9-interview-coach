"""Tests de contrato del servicio de voz (docs/contract.md §6) con backends mock."""

import hashlib
import threading
import time
from urllib.parse import quote

from conftest import L16, pcm
from fastapi.testclient import TestClient

from coach_speech.app import create_app
from coach_speech.audio import tone
from coach_speech.config import Settings
from coach_speech.stt import MockSTT, Segment, Transcript, Word
from coach_speech.tts import FallbackTTS, MockTTS


def stt_post(client, body=None, ctype=L16, headers=None, **params):
    params.setdefault("language", "es")
    body = pcm(1.0) if body is None else body
    return client.post(
        "/stt", params=params, content=body, headers={"Content-Type": ctype, **(headers or {})}
    )


def is_problem(r, status, code):
    assert r.status_code == status, r.text
    assert r.headers["content-type"] == "application/problem+json"
    body = r.json()
    assert body["status"] == status and body["code"] == code
    assert body["type"] == "urn:cx9:problem:" + code.lower().replace("_", "-")
    assert body["title"] and body["instance"].startswith("/") and body["request_id"]
    return body


# --- §6.3 /health, /healthz, /readyz, /metrics, X-Request-Id ------------------


def test_health_mock(client):
    r = client.get("/health")
    body = r.json()
    assert r.status_code == 200 and body["status"] == "ok" and body["mode"] == "mock"
    assert body["stt"] == {
        "ready": True,
        "engine": "mock",
        "model": "mock",
        "languages": ["es", "en"],
        "input_formats": ["audio/L16;rate=16000;channels=1", "audio/wav"],
        "detail": "mock",
    }
    assert body["tts"] == {
        "ready": True,
        "engine": "mock",
        "fallback": None,
        "output_formats": ["wav", "pcm16le"],
    }
    assert client.get("/readyz").json() == body
    assert client.get("/healthz").json()["status"] == "ok"


def test_health_down_while_model_loads(settings, tts):
    class Loading(MockSTT):
        name, model, loading = "faster-whisper", "small", True

        def ready(self):
            return False, "cargando modelo"

    with TestClient(create_app(settings, stt=Loading(), tts=tts)) as c:
        r = c.get("/health")
        assert r.status_code == 503 and r.json()["status"] == "down" and r.headers["retry-after"] == "5"


def test_health_degraded_when_tts_falls_back(settings, stt):
    class Down(MockTTS):
        name = "piper"

        def available(self, language=None):
            return False, "sin voz"

        def voices(self, language):
            return []

    class Esp(MockTTS):
        name = "espeak-ng"

    with TestClient(create_app(settings, stt=stt, tts=FallbackTTS([Down(), Esp()]))) as c:
        body = c.get("/health").json()
    assert body["status"] == "degraded" and body["tts"]["engine"] == "espeak-ng"


def test_request_id_propagated_or_generated(client):
    assert (
        client.get("/healthz", headers={"X-Request-Id": "abc-12345678"}).headers["x-request-id"]
        == "abc-12345678"
    )
    rid = client.get("/healthz", headers={"X-Request-Id": "bad id!"}).headers["x-request-id"]
    assert rid != "bad id!" and len(rid) == 36


def test_metrics_never_contain_user_text(client):
    stt_post(client, headers={"X-Mock-Transcript": quote("trabajé en Telecom")})
    client.post("/tts", json={"text": "hola", "language": "es"})
    text = client.get("/metrics").text
    assert 'speech_requests_total{endpoint="/stt",method="POST",status="200"} 1.0' in text
    assert 'speech_tts_synth_total{engine="mock",language="es"} 1.0' in text
    assert "Telecom" not in text


# --- §6.1 / §6.7 POST /stt ----------------------------------------------------


def test_stt_mock_default_text_is_hash(client):
    body = pcm(1.0)
    r = stt_post(client, body)
    data = r.json()
    assert r.status_code == 200
    assert data["text"] == "respuesta simulada " + hashlib.sha256(body).hexdigest()[:8]
    assert data["language"] == "es" and data["duration_s"] == 1.0
    assert data["engine"] == "mock" and data["model"] == "mock" and data["processing_ms"] == 0
    assert data["confidence"] == 0.9
    seg = data["segments"][0]
    assert len(data["segments"]) == 1 and seg["start_s"] == 0.0 and seg["end_s"] == 1.0
    assert [w["word"] for w in seg["words"]][:2] == ["respuesta", "simulada"]
    assert set(seg["words"][0]) >= {"word", "start_s", "end_s", "confidence"}
    en = stt_post(client, body, language="en").json()
    assert en["text"].startswith("simulated answer ")


def test_stt_mock_is_byte_identical(client):
    a = stt_post(client, pcm(0.5), headers={"X-Request-Id": "same-id-0001"})
    b = stt_post(client, pcm(0.5), headers={"X-Request-Id": "same-id-0001"})
    assert a.content == b.content


def test_stt_mock_transcript_header_and_low_confidence(client):
    text = "Eh, este... o sea, trabajé en ?Telecom con Go"
    r = stt_post(client, pcm(2.0), headers={"X-Mock-Transcript": quote(text)})
    data = r.json()
    words = data["segments"][0]["words"]
    assert data["text"] == "Eh, este... o sea, trabajé en Telecom con Go"
    tele = next(w for w in words if w["word"] == "Telecom")
    assert tele["confidence"] == 0.3
    assert words[0]["start_s"] == 0.0 and words[-1]["end_s"] == 2.0  # reparto uniforme
    assert data["confidence"] == round((0.9 * 8 + 0.3) / 9, 4)
    assert [f["text"] for f in data["fillers"]] == ["eh", "este", "o sea"] and data["filler_count"] == 3


def test_stt_wav_duration_from_header(client):
    r = stt_post(client, tone(1.5, sr=22050), ctype="audio/wav")
    assert r.status_code == 200 and r.json()["duration_s"] == 1.5


def test_stt_word_confidence_false(client):
    data = stt_post(client, word_confidence="false").json()
    assert data["segments"][0]["words"] == [] and data["confidence"] == 0.9


def test_stt_l16_other_rate(client):
    assert stt_post(client, pcm(1.0, 8000), ctype="audio/L16; rate=8000").json()["duration_s"] == 1.0


def test_stt_no_audio(client):
    is_problem(stt_post(client, b""), 422, "NO_AUDIO")
    is_problem(stt_post(client, pcm(0.05)), 422, "NO_AUDIO")


def test_stt_too_long_or_large(client, settings):
    is_problem(stt_post(client, pcm(11.0)), 413, "PAYLOAD_TOO_LARGE")
    is_problem(stt_post(client, b"\0" * (settings.max_upload_bytes + 2)), 413, "PAYLOAD_TOO_LARGE")


def test_stt_chunked_body_is_cut(client):
    def gen():
        for _ in range(50):
            yield b"\0" * 10_000

    r = client.post("/stt", params={"language": "es"}, content=gen(), headers={"Content-Type": L16})
    is_problem(r, 413, "PAYLOAD_TOO_LARGE")


def test_stt_unsupported_media(client):
    is_problem(stt_post(client, b"\0" * 3200, ctype="audio/mpeg"), 415, "UNSUPPORTED_MEDIA_TYPE")
    is_problem(stt_post(client, b"OggS" * 800, ctype="audio/ogg; codecs=opus"), 415, "UNSUPPORTED_MEDIA_TYPE")
    is_problem(
        stt_post(client, b"\0" * 3200, ctype="audio/L16; rate=16000; channels=2"),
        415,
        "UNSUPPORTED_MEDIA_TYPE",
    )
    is_problem(stt_post(client, b"not a wav file", ctype="audio/wav"), 415, "UNSUPPORTED_MEDIA_TYPE")
    is_problem(stt_post(client, b"\0" * 3201), 400, "BAD_REQUEST")


def test_stt_language_rules(client):
    is_problem(stt_post(client, language="fr"), 422, "UNSUPPORTED_LANGUAGE")
    r = client.post("/stt", content=pcm(1.0), headers={"Content-Type": L16})
    is_problem(r, 422, "VALIDATION_FAILED")
    assert stt_post(client, language="auto").status_code == 200


def test_stt_prompt_and_partial_reach_backend(client, stt):
    stt_post(client, prompt="Eh, este... o sea", partial="true")
    assert stt.calls[-1].prompt == "Eh, este... o sea" and stt.calls[-1].partial is True
    body = is_problem(stt_post(client, prompt="x" * 501), 422, "VALIDATION_FAILED")
    assert body["errors"][0]["pointer"] == "/prompt"


def test_stt_mock_fail_header(client):
    r = is_problem(stt_post(client, headers={"X-Mock-Fail": "503"}), 503, "DEPENDENCY_UNAVAILABLE")
    assert r["retryable"] is True
    is_problem(stt_post(client, headers={"X-Mock-Fail": "504"}), 504, "DEPENDENCY_TIMEOUT")


def test_stt_mock_fail_timeout_hangs_until_client_gives_up():
    import asyncio

    import httpx

    app = create_app(Settings(mode="mock", stt_backend="mock", tts_backend="mock", preload=False))

    async def go():
        transport = httpx.ASGITransport(app=app)
        async with httpx.AsyncClient(transport=transport, base_url="http://t") as c:
            try:
                await asyncio.wait_for(
                    c.post(
                        "/stt?language=es",
                        content=pcm(1.0),
                        headers={"Content-Type": L16, "X-Mock-Fail": "timeout"},
                    ),
                    0.5,
                )
            except asyncio.TimeoutError:
                return "client-timeout"
            return "answered"

    assert asyncio.run(go()) == "client-timeout"


def test_mock_headers_ignored_in_real_mode(settings, tts):
    class Fixed(MockSTT):
        def transcribe(self, req):
            assert req.mock_transcript is None
            return Transcript("hola", "es", [Segment(0, 1, "hola", [Word("hola", 0, 1, 0.8)])], "x", "y")

    settings.mode = "real"
    with TestClient(create_app(settings, stt=Fixed(), tts=tts)) as c:
        r = stt_post(c, headers={"X-Mock-Fail": "503", "X-Mock-Transcript": "nope"})
    assert r.status_code == 200 and r.json()["text"] == "hola"


def test_stt_real_mode_decodes_and_weights_confidence(settings, tts):
    seen = {}

    class Fixed(MockSTT):
        name, model = "faster-whisper", "tiny"

        def transcribe(self, req):
            seen["samples"] = req.samples
            words = [Word("So,", 0.0, 0.3, 0.8), Word("I", 1.2, 1.3, 0.95), Word("built", 1.3, 2.3, 0.4)]
            return Transcript(
                "So, I built", "en", [Segment(0.0, 2.3, "So, I built", words, -0.2)], self.name, self.model
            )

    settings.mode = "real"
    with TestClient(create_app(settings, stt=Fixed(), tts=tts)) as c:
        data = stt_post(c, tone(3.0, sr=16000), ctype="audio/wav", language="en").json()
    assert seen["samples"].dtype.name == "float32" and seen["samples"].size == 48000
    assert data["duration_s"] == 3.0  # duración del audio, no del habla
    assert data["confidence"] == round((0.8 * 0.3 + 0.95 * 0.1 + 0.4 * 1.0) / 1.4, 4)
    assert data["pauses"] == [{"start_s": 0.3, "end_s": 1.2, "duration_s": 0.9}]
    assert [f["text"] for f in data["fillers"]] == ["so"]
    assert data["engine"] == "faster-whisper" and data["model"] == "tiny"


def test_stt_model_loading_503(settings, tts):
    class Loading(MockSTT):
        loading = True

    settings.mode = "real"
    with TestClient(create_app(settings, stt=Loading(), tts=tts)) as c:
        r = stt_post(c)
    is_problem(r, 503, "MODEL_LOADING")
    assert r.headers["retry-after"] == "5"


def test_stt_timeout_and_internal_error(settings, tts):
    class Slow(MockSTT):
        def transcribe(self, req):
            time.sleep(1.0)
            return super().transcribe(req)

    class Boom(MockSTT):
        def transcribe(self, req):
            raise RuntimeError("ruta/secreta")

    settings.stt_timeout_s = 0.2
    with TestClient(create_app(settings, stt=Slow(), tts=tts)) as c:
        is_problem(stt_post(c), 504, "DEPENDENCY_TIMEOUT")
        assert 'speech_timeouts_total{op="stt"} 1.0' in c.get("/metrics").text
    with TestClient(create_app(settings, stt=Boom(), tts=tts)) as c:
        r = stt_post(c)
        is_problem(r, 500, "INTERNAL")
        assert "secreta" not in r.text


def test_busy_final_waits_partial_is_rejected(settings, tts):
    gate = threading.Event()

    class Blocking(MockSTT):
        def transcribe(self, req):
            gate.wait(3)
            return super().transcribe(req)

    settings.stt_timeout_s = 5
    results = []
    with TestClient(create_app(settings, stt=Blocking(), tts=tts)) as c:
        t = threading.Thread(target=lambda: results.append(stt_post(c).status_code))
        t.start()
        time.sleep(0.2)
        t0 = time.perf_counter()
        partial = stt_post(c, partial="true")
        partial_s = time.perf_counter() - t0
        final = stt_post(c)  # espera queue_timeout_s y se rinde
        gate.set()
        t.join()
    is_problem(partial, 503, "DEPENDENCY_UNAVAILABLE")
    assert partial_s < 0.25 and partial.headers["retry-after"] == "1"
    is_problem(final, 503, "DEPENDENCY_UNAVAILABLE")
    assert results == [200]


def test_bearer_token_when_configured(settings, stt, tts):
    settings.token = "s3cr3t"
    with TestClient(create_app(settings, stt=stt, tts=tts)) as c:
        is_problem(stt_post(c), 401, "UNAUTHENTICATED")
        is_problem(c.get("/voices"), 401, "UNAUTHENTICATED")
        assert stt_post(c, headers={"Authorization": "Bearer s3cr3t"}).status_code == 200
        assert c.get("/health").status_code == 200  # health/metrics sin auth


# --- §6.2 / §6.7 POST /tts ----------------------------------------------------


def test_tts_mock_silence_wav(client, tts):
    r = client.post("/tts", json={"text": "Para empezar, contame tu recorrido.", "language": "es"})
    assert r.status_code == 200 and r.headers["content-type"] == "audio/wav"
    assert r.headers["x-audio-duration-ms"] == str(300 + 60 * 5)
    assert r.headers["x-sample-rate"] == "16000" and r.headers["x-engine"] == "mock"
    assert r.headers["x-voice"] == "mock-es"
    assert r.content[:4] == b"RIFF" and set(r.content[44:]) == {0}
    assert len(r.content) == 44 + 2 * 16000 * 600 // 1000
    again = client.post("/tts", json={"text": "Para empezar, contame tu recorrido.", "language": "es"})
    assert again.content == r.content


def test_tts_mock_pcm16le(client):
    r = client.post(
        "/tts",
        json={
            "text": "Tell me about yourself",
            "language": "en",
            "format": "pcm16le",
            "speed": 0.85,
            "sample_rate": 22050,
        },
    )
    assert r.headers["content-type"] == "audio/L16; rate=16000; channels=1"
    assert r.headers["x-audio-duration-ms"] == "540" and len(r.content) == 2 * 16000 * 540 // 1000
    assert r.headers["x-voice"] == "mock-en"


def test_tts_passes_speed_and_voice(client, tts):
    client.post("/tts", json={"text": "hi", "language": "en", "speed": 0.85, "voice": "mock-en"})
    assert tts.calls[-1] == {"text": "hi", "language": "en", "speed": 0.85, "voice": "mock-en"}


def test_tts_real_mode_resamples(settings, stt):
    settings.mode = "real"
    with TestClient(create_app(settings, stt=stt, tts=MockTTS())) as c:
        r = c.post("/tts", json={"text": "hola", "language": "es", "sample_rate": 22050})
        assert r.headers["x-sample-rate"] == "22050" and r.content[:4] == b"RIFF"
        assert r.headers["x-audio-duration-ms"] == "360"


def test_tts_validation(client):
    is_problem(
        client.post("/tts", json={"text": "hola", "language": "es", "voice": "es_ES-x"}),
        422,
        "VOICE_NOT_FOUND",
    )
    is_problem(client.post("/tts", json={"text": "hola", "language": "de"}), 422, "UNSUPPORTED_LANGUAGE")
    for bad in (
        {"text": "", "language": "es"},
        {"text": "x" * 2001, "language": "es"},
        {"text": "   ", "language": "es"},
        {"text": "hola"},
        {"text": "hola", "language": "es", "speed": 3},
        {"text": "hola", "language": "es", "format": "mp3"},
        {"text": "hola", "language": "es", "format": "opus"},
        {"text": "hola", "language": "es", "extra": 1},
    ):
        is_problem(client.post("/tts", json=bad), 422, "VALIDATION_FAILED")
    r = client.post("/tts", content=b"{nope", headers={"Content-Type": "application/json"})
    is_problem(r, 400, "BAD_REQUEST")


def test_tts_unavailable(settings, stt):
    class Nope(MockTTS):
        name = "piper"

        def available(self, language=None):
            return False, "no instalado"

    with TestClient(create_app(settings, stt=stt, tts=FallbackTTS([Nope()]))) as c:
        r = c.post("/tts", json={"text": "hola", "language": "es"})
        is_problem(r, 503, "DEPENDENCY_UNAVAILABLE")
        assert r.headers["retry-after"] == "5"
        assert c.get("/health").json()["status"] == "degraded"


def test_tts_mock_fail(client):
    is_problem(
        client.post("/tts", json={"text": "hola", "language": "es"}, headers={"X-Mock-Fail": "503"}),
        503,
        "DEPENDENCY_UNAVAILABLE",
    )


# --- §6.4 GET /voices ---------------------------------------------------------


def test_voices_mock(client):
    assert client.get("/voices").json() == {
        "items": [
            {"id": "mock-es", "language": "es", "engine": "mock", "default": True},
            {"id": "mock-en", "language": "en", "engine": "mock", "default": True},
        ]
    }
