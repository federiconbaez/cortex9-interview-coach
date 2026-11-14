import subprocess

import pytest

from coach_speech.config import Settings
from coach_speech.tts import EspeakTTS, FallbackTTS, MockTTS, PiperTTS, TTSError, make_tts


@pytest.fixture
def voices(tmp_path):
    es, en = tmp_path / "es_AR-daniela-high.onnx", tmp_path / "en_US-lessac-medium.onnx"
    es.write_bytes(b"x")
    en.write_bytes(b"x")
    return {"es_AR": str(es), "en_US": str(en)}


def test_piper_args_use_configured_voice(voices, tmp_path):
    p = PiperTTS(Settings(piper_bin="/bin/true", piper_voices=voices))
    assert p.available() == (True, "ok")
    args = p.args("es-AR", 0.8, tmp_path / "o.wav")
    assert args[0] == "/bin/true" and args[args.index("--model") + 1] == voices["es_AR"]
    assert args[args.index("--length_scale") + 1] == "1.250"
    assert p.args("en", 1.0, tmp_path / "o.wav")[2] == voices["en_US"]


def test_piper_missing_voice(tmp_path):
    p = PiperTTS(Settings(piper_bin="/bin/true", piper_voices={}))
    ok, why = p.available("es")
    assert not ok and "es_AR" in why
    with pytest.raises(TTSError):
        p.args("es", 1.0, tmp_path / "o.wav")


def test_piper_synthesize_mocked(monkeypatch, voices):
    p = PiperTTS(Settings(piper_bin="/bin/true", piper_voices=voices))

    def fake_run(cmd, input, **kw):
        out = cmd[cmd.index("--output_file") + 1]
        with open(out, "wb") as f:
            f.write(b"RIFFxxxxWAVE" + input)
        assert kw["timeout"] == p.s.tts_timeout_s and "shell" not in kw
        return subprocess.CompletedProcess(cmd, 0)

    monkeypatch.setattr(subprocess, "run", fake_run)
    sp = p.synthesize("hola", "es")
    assert sp.audio.startswith(b"RIFF") and sp.engine == "piper" and sp.voice == "es_AR-daniela-high"


def test_piper_timeout_raises_ttserror(monkeypatch, voices):
    p = PiperTTS(Settings(piper_bin="/bin/true", piper_voices=voices))

    def fake_run(cmd, **kw):
        raise subprocess.TimeoutExpired(cmd, 1)

    monkeypatch.setattr(subprocess, "run", fake_run)
    with pytest.raises(TTSError, match="TimeoutExpired"):
        p.synthesize("hola", "es")


def test_espeak_args(monkeypatch):
    monkeypatch.setattr("shutil.which", lambda b: "/usr/bin/espeak-ng" if b == "espeak-ng" else None)
    e = EspeakTTS(Settings())
    fast, slow = e.args("en", 1.0), e.args("en", 0.85)
    assert fast[:3] == ["/usr/bin/espeak-ng", "-v", "en-us"] and fast[-1] == "--stdout"
    assert int(slow[slow.index("-s") + 1]) < int(fast[fast.index("-s") + 1])
    assert e.args("es", 1.0)[2] == "es-419" and e.name == "espeak-ng"


def test_espeak_text_goes_via_stdin(monkeypatch):
    monkeypatch.setattr("shutil.which", lambda b: "/usr/bin/espeak-ng" if b == "espeak-ng" else None)
    seen = {}

    def fake_run(cmd, input, **kw):
        seen["cmd"], seen["input"] = cmd, input
        return subprocess.CompletedProcess(cmd, 0, stdout=b"RIFF....WAVE", stderr=b"")

    monkeypatch.setattr(subprocess, "run", fake_run)
    sp = EspeakTTS(Settings()).synthesize("--help; rm -rf /", "es")
    assert "--help; rm -rf /" not in seen["cmd"] and seen["input"] == b"--help; rm -rf /"
    assert sp.engine == "espeak-ng" and sp.voice == "espeak-ng-es-419"


class _Failing(MockTTS):
    name = "failing"

    def synthesize(self, text, language, speed=1.0, voice=None):
        raise TTSError("roto")


class _Missing(MockTTS):
    name = "missing"

    def available(self, language=None):
        return False, "no está"


def test_fallback_uses_next_engine():
    fb = FallbackTTS([_Failing(), MockTTS()])
    sp = fb.synthesize("hola", "es")
    assert sp.engine == "mock" and fb.fallbacks == 1


def test_fallback_skips_unavailable():
    fb = FallbackTTS([_Missing(), MockTTS()])
    assert fb.available() == (True, "mock disponible") and fb.name == "missing"
    assert fb.synthesize("hola", "en").engine == "mock"


def test_fallback_all_fail():
    fb = FallbackTTS([_Missing(), _Failing()])
    with pytest.raises(TTSError, match="ningún motor"):
        fb.synthesize("hola", "es")


def test_factory():
    fb = make_tts(Settings(tts_backend="auto"))
    assert fb.name == "piper" and fb.fallback in ("espeak-ng", "espeak")
    assert make_tts(Settings(tts_backend="espeak")).name in ("espeak-ng", "espeak")
    assert make_tts(Settings(tts_backend="mock")).name == "mock"
    with pytest.raises(ValueError):
        make_tts(Settings(tts_backend="cloud"))


def test_voice_listing(voices, monkeypatch):
    monkeypatch.setattr("shutil.which", lambda b: "/usr/bin/espeak-ng" if b == "espeak-ng" else None)
    fb = make_tts(Settings(tts_backend="auto", piper_bin="/bin/true", piper_voices=voices))
    assert [(v.id, v.engine) for v in fb.voices("es")] == [
        ("es_AR-daniela-high", "piper"),
        ("espeak-ng-es-419", "espeak-ng"),
    ]
    assert [v.id for v in fb.voices("en")] == ["en_US-lessac-medium", "espeak-ng-en-us"]


def test_fallback_prefers_engine_owning_requested_voice(monkeypatch):
    class Other(MockTTS):
        name = "other"

        def voices(self, language):
            from coach_speech.tts import Voice

            return [Voice("other-es", "es", "other")]

    fb = FallbackTTS([MockTTS(), Other()])
    assert fb.synthesize("hola", "es", 1.0, voice="other-es").engine == "other"
    assert fb.fallbacks == 1


def test_mock_tts_duration_rule():
    import wave
    from io import BytesIO

    sp = MockTTS().synthesize("uno dos tres", "es")
    with wave.open(BytesIO(sp.audio)) as w:
        assert w.getframerate() == 16000 and w.getnframes() == 16000 * 480 // 1000
