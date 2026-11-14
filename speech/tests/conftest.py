import pytest
from fastapi.testclient import TestClient

from coach_speech.app import create_app
from coach_speech.config import Settings
from coach_speech.stt import MockSTT
from coach_speech.tts import MockTTS


@pytest.fixture
def settings():
    return Settings(
        mode="mock",
        stt_backend="mock",
        tts_backend="mock",
        max_upload_bytes=400_000,
        max_audio_s=10,
        stt_timeout_s=2,
        tts_timeout_s=2,
        queue_timeout_s=0.3,
        preload=False,
    )


@pytest.fixture
def stt():
    return MockSTT()


@pytest.fixture
def tts():
    return MockTTS()


@pytest.fixture
def client(settings, stt, tts):
    with TestClient(create_app(settings, stt=stt, tts=tts)) as c:
        yield c


def pcm(seconds: float, rate: int = 16000) -> bytes:
    return b"\0\0" * int(seconds * rate)


L16 = "audio/L16; rate=16000; channels=1"
