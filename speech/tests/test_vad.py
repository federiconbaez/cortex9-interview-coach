"""VAD y campos de endpointing de /stt (contrato v1.2: speech_ended_ms, trailing_silence_ms)."""

import numpy as np
import pytest
from conftest import L16
from fastapi.testclient import TestClient

from coach_speech.app import create_app
from coach_speech.config import Settings
from coach_speech.stt import FasterWhisperSTT, MockSTT, STTRequest
from coach_speech.tts import MockTTS
from coach_speech.vad import endpoint, energy_segments, from_segments, whisper_vad_parameters

SR = 16000


def tone(seconds: float, amp: float = 0.3, f: float = 220.0) -> np.ndarray:
    t = np.arange(int(seconds * SR)) / SR
    return (amp * np.sin(2 * np.pi * f * t)).astype(np.float32)


def silence(seconds: float) -> np.ndarray:
    return np.zeros(int(seconds * SR), dtype=np.float32)


def to_pcm(x: np.ndarray) -> bytes:
    return (np.clip(x, -1, 1) * 32767).astype("<i2").tobytes()


def stt_post(client, body, headers=None, **params):
    params.setdefault("language", "es")
    return client.post("/stt", params=params, content=body, headers={"Content-Type": L16, **(headers or {})})


def test_energy_segments_and_endpoint():
    x = np.concatenate([silence(0.3), tone(1.2), silence(0.9)])
    segs = energy_segments(x, -45.0)
    assert len(segs) == 1 and abs(segs[0][0] - 300) <= 30 and abs(segs[0][1] - 1500) <= 30
    ep = endpoint(x, Settings(), use_silero=False)
    assert ep.engine == "energy" and abs(ep.speech_ended_ms - 1500) <= 30
    assert ep.speech_ended_ms + ep.trailing_silence_ms == 2400


def test_endpoint_without_speech_and_still_speaking():
    ep = endpoint(silence(1.0), Settings(), use_silero=False)
    assert ep.speech_ended_ms is None and ep.trailing_silence_ms == 1000
    ep = endpoint(np.concatenate([silence(0.2), tone(0.8)]), Settings(), use_silero=False)
    assert ep.speech_ended_ms == 1000 and ep.trailing_silence_ms == 0


def test_from_segments_clamps():
    assert from_segments([(0, 5000)], 4000, "x").fields() == {
        "speech_ended_ms": 4000,
        "trailing_silence_ms": 0,
    }
    assert from_segments([], 700, "x").fields() == {"speech_ended_ms": None, "trailing_silence_ms": 700}


def test_whisper_vad_parameters_partial_vs_final():
    s = Settings()
    final, partial = whisper_vad_parameters(s, False), whisper_vad_parameters(s, True)
    assert final["min_silence_duration_ms"] > partial["min_silence_duration_ms"] >= 300
    assert final["speech_pad_ms"] >= 200  # relleno para no cortar vacilaciones


def test_vad_filter_can_be_disabled():
    o = FasterWhisperSTT(Settings(vad_filter=False)).decode_options("es", None, partial=True)
    assert o["vad_filter"] is False and o["vad_parameters"] is None


def test_no_speech_skips_decoding():
    stt = FasterWhisperSTT(Settings())
    stt.load = lambda: pytest.fail("no debería cargar el modelo")  # type: ignore[method-assign]
    tr = stt.transcribe(
        STTRequest(body=b"", duration_s=1.0, language="es", samples=silence(1.0), no_speech=True)
    )
    assert tr.text == "" and tr.segments == []


def test_silero_does_not_take_a_tone_for_speech():
    pytest.importorskip("faster_whisper")
    x = np.concatenate([tone(1.0), silence(1.0)])
    ep = endpoint(x, Settings(), use_silero=True)
    # Silero no confunde un tono puro con voz (el VAD por energía sí): sin voz, todo es silencio.
    assert ep.engine == "silero" and ep.speech_ended_ms is None and ep.trailing_silence_ms == 2000
    assert endpoint(x, Settings(), use_silero=False).speech_ended_ms is not None


# --- /stt ---


def test_partial_reports_endpoint_from_pcm_energy(client):
    body = to_pcm(np.concatenate([tone(1.0), silence(0.8)]))
    r = stt_post(client, body, partial="true")
    assert r.status_code == 200, r.text
    d = r.json()
    assert abs(d["speech_ended_ms"] - 1000) <= 30 and abs(d["trailing_silence_ms"] - 800) <= 30
    again = stt_post(client, body, partial="true")
    assert again.content == r.content  # determinista


def test_partial_silence_only(client):
    d = stt_post(client, to_pcm(silence(1.0)), partial="true").json()
    assert d["speech_ended_ms"] is None and d["trailing_silence_ms"] == 1000


def test_final_has_no_endpoint_fields_unless_asked(client):
    body = to_pcm(np.concatenate([tone(0.5), silence(0.5)]))
    d = stt_post(client, body).json()
    assert "speech_ended_ms" not in d and "trailing_silence_ms" not in d
    d = stt_post(client, body, vad="true").json()
    assert abs(d["trailing_silence_ms"] - 500) <= 30
    d = stt_post(client, body, partial="true", vad="false").json()
    assert "trailing_silence_ms" not in d


def test_mock_headers_force_endpoint(client):
    body = to_pcm(tone(2.0))
    d = stt_post(client, body, {"X-Mock-Trailing-Silence": "750"}, partial="true").json()
    assert d["trailing_silence_ms"] == 750 and d["speech_ended_ms"] == 1250
    d = stt_post(client, body, {"X-Mock-Speech-Ended": "400"}, partial="true").json()
    assert d["speech_ended_ms"] == 400 and d["trailing_silence_ms"] == 1600
    r = stt_post(client, body, {"X-Mock-Trailing-Silence": "x"}, partial="true")
    assert r.status_code == 400


def test_mock_env_default_trailing_silence(settings):
    settings.mock_trailing_silence_ms = 900
    with TestClient(create_app(settings, stt=MockSTT(), tts=MockTTS())) as c:
        d = stt_post(c, to_pcm(tone(1.5)), partial="true").json()
    assert d["trailing_silence_ms"] == 900 and d["speech_ended_ms"] == 600


def test_vad_metric_counts_engine(client):
    stt_post(client, to_pcm(tone(0.5)), partial="true")
    m = client.get("/metrics").text
    assert 'speech_vad_endpoint_total{engine="energy"} 1.0' in m
