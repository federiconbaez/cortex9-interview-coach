"""E2E de caja negra contra un servicio de voz corriendo en modo mock.

    CX9_SPEECH_MODE=mock SPEECH_PORT=8090 python -m coach_speech &
    SPEECH_URL=http://127.0.0.1:8090 pytest e2e

Se saltea si SPEECH_URL no está definida (los tests unitarios no lo necesitan).
"""

import hashlib
import os
from urllib.parse import quote

import httpx
import pytest

URL = os.environ.get("SPEECH_URL")
pytestmark = pytest.mark.skipif(not URL, reason="definí SPEECH_URL para correr el e2e")
L16 = "audio/L16; rate=16000; channels=1"


@pytest.fixture(scope="module")
def http():
    with httpx.Client(base_url=URL, timeout=10) as c:
        yield c


def test_health_is_mock(http):
    body = http.get("/health").json()
    assert body["status"] == "ok" and body["mode"] == "mock"


def test_stt_roundtrip_with_fillers(http):
    audio = b"\0\0" * 32000  # 2 s
    r = http.post(
        "/stt",
        params={"language": "es"},
        content=audio,
        headers={
            "Content-Type": L16,
            "X-Mock-Transcript": quote("Eh, este... o sea, usé ?Kafka"),
            "X-Request-Id": "e2e-000000001",
        },
    )
    assert r.status_code == 200 and r.headers["x-request-id"] == "e2e-000000001"
    body = r.json()
    assert body["duration_s"] == 2.0 and body["filler_count"] == 3
    assert body["segments"][0]["words"][-1] == {
        "word": "Kafka",
        "start_s": 1.667,
        "end_s": 2.0,
        "confidence": 0.3,
        "is_filler": False,
    }
    plain = http.post("/stt", params={"language": "en"}, content=audio, headers={"Content-Type": L16}).json()
    assert plain["text"] == "simulated answer " + hashlib.sha256(audio).hexdigest()[:8]


def test_tts_then_stt(http):
    r = http.post("/tts", json={"text": "Contame de un proyecto", "language": "es", "format": "pcm16le"})
    assert r.status_code == 200 and r.headers["x-audio-duration-ms"] == "540"
    back = http.post("/stt", params={"language": "es"}, content=r.content, headers={"Content-Type": L16})
    assert back.json()["duration_s"] == 0.54


def test_errors_are_problem_json(http):
    r = http.post("/stt", params={"language": "es"}, content=b"", headers={"Content-Type": L16})
    assert r.status_code == 422 and r.headers["content-type"] == "application/problem+json"
    r = http.post(
        "/stt",
        params={"language": "es"},
        content=b"\0\0" * 1600,
        headers={"Content-Type": L16, "X-Mock-Fail": "503"},
    )
    assert r.status_code == 503 and r.headers["retry-after"] == "1"


def test_metrics(http):
    assert "speech_requests_total" in http.get("/metrics").text
