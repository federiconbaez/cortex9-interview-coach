import pytest

from coach_speech.config import Settings


def test_from_env(monkeypatch):
    monkeypatch.setenv("SPEECH_STT_BACKEND", "MOCK")
    monkeypatch.setenv("SPEECH_MAX_UPLOAD_MB", "2")
    monkeypatch.setenv("SPEECH_PIPER_VOICE_ES_AR", "/models/piper/es_AR.onnx")
    monkeypatch.setenv("SPEECH_FILLER_PROMPT_ES", "eh, este")
    monkeypatch.setenv("SPEECH_WHISPER_LOCAL_ONLY", "false")
    monkeypatch.setenv("SPEECH_MAX_CONCURRENCY", "0")
    monkeypatch.setenv("CX9_SPEECH_TOKEN", "t")
    s = Settings.from_env()
    assert s.stt_backend == "mock" and s.max_upload_bytes == 2 * 1024 * 1024
    assert s.piper_voices == {"es_AR": "/models/piper/es_AR.onnx"}
    assert s.filler_prompt["es"] == "eh, este" and s.whisper_local_only is False
    assert s.max_concurrency == 1 and s.token == "t" and s.mode == "real"


def test_mock_mode_forces_mock_backends(monkeypatch):
    monkeypatch.setenv("CX9_SPEECH_MODE", "mock")
    monkeypatch.setenv("SPEECH_STT_BACKEND", "faster-whisper")
    s = Settings.from_env()
    assert s.mode == "mock" and s.stt_backend == "mock" and s.tts_backend == "mock"
    monkeypatch.setenv("CX9_SPEECH_MODE", "cloud")
    with pytest.raises(ValueError):
        Settings.from_env()


def test_defaults_are_local_and_match_contract(monkeypatch):
    for k in ("SPEECH_WHISPER_LOCAL_ONLY", "SPEECH_STT_BACKEND", "CX9_SPEECH_MODE", "SPEECH_MAX_UPLOAD_MB"):
        monkeypatch.delenv(k, raising=False)
    s = Settings.from_env()
    assert s.whisper_local_only is True and s.whisper_device == "cpu"
    assert s.max_upload_bytes == 20 * 1024 * 1024 and s.max_audio_s == 600 and s.max_tts_chars == 2000
