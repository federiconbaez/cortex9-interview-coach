import io
import sys
import types
import wave

import numpy as np
import pytest

from coach_speech.audio import (
    is_wav,
    parse_content_type,
    read_pcm16le,
    read_wav_bytes,
    to_wav_bytes,
    tone,
    wav_duration_s,
    wav_to_pcm16le,
)
from coach_speech.config import Settings, normalize_language, voice_key
from coach_speech.stt import FasterWhisperSTT, MockSTT, STTRequest, make_stt


def test_language_normalization():
    assert normalize_language("es-AR") == "es" and normalize_language("EN_us") == "en"
    assert voice_key("es") == "es_AR" and voice_key("en") == "en_US"
    with pytest.raises(ValueError):
        normalize_language("pt")


def test_content_type_parsing():
    assert parse_content_type("audio/L16; rate=16000; channels=1") == (
        "audio/l16",
        {"rate": "16000", "channels": "1"},
    )
    assert parse_content_type('audio/ogg; codecs="opus"') == ("audio/ogg", {"codecs": "opus"})
    assert parse_content_type(None) == ("", {})


def test_wav_roundtrip_and_resample():
    data = tone(0.5, sr=22050)
    assert is_wav(data) and not is_wav(b"OggS....")
    assert wav_duration_s(data) == pytest.approx(0.5, abs=1e-3)
    audio = read_wav_bytes(data)
    assert audio.dtype == np.float32 and abs(audio.size - 8000) <= 1
    pcm, sr = wav_to_pcm16le(data, 16000)
    assert sr == 16000 and abs(len(pcm) - 16000) <= 2
    assert read_pcm16le(b"\0\0" * 8000, 8000).size == 16000
    with pytest.raises(ValueError):
        read_pcm16le(b"\0\0\0")


def test_wav_rejects_8bit_and_downmixes_stereo():
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(1)
        w.setframerate(8000)
        w.writeframes(b"\x80" * 80)
    with pytest.raises(ValueError):
        read_wav_bytes(buf.getvalue())
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(2)
        w.setsampwidth(2)
        w.setframerate(16000)
        w.writeframes(np.array([1000, -1000] * 160, dtype="<i2").tobytes())
    assert np.allclose(read_wav_bytes(buf.getvalue()), 0.0)


def test_decode_options_keep_fillers():
    stt = FasterWhisperSTT(Settings())
    o = stt.decode_options("es", None)
    assert o["suppress_tokens"] == [] and o["suppress_blank"] is False
    assert o["word_timestamps"] is True and o["vad_filter"] is True
    assert o["vad_parameters"]["min_silence_duration_ms"] >= 500 and o["vad_parameters"]["speech_pad_ms"] > 0
    assert o["condition_on_previous_text"] is False and o["beam_size"] == 5
    for f in ("eh", "este", "o sea", "um"):
        assert f in o["initial_prompt"].lower()
    assert "uh" in stt.decode_options("en", None)["initial_prompt"].lower()
    assert stt.decode_options("en", "custom")["initial_prompt"] == "custom"
    assert stt.decode_options(None, None)["language"] is None  # auto
    assert stt.decode_options("es", None, partial=True)["beam_size"] == 1


class _FakeModel:
    instances: list = []

    def __init__(self, name, **kw):
        self.name, self.kw, self.calls = name, kw, []
        _FakeModel.instances.append(self)

    def transcribe(self, src, **opts):
        self.calls.append((src, opts))
        n = types.SimpleNamespace
        seg = n(
            start=0.0,
            end=1.1,
            text=" Um, I built it.",
            avg_logprob=-0.3,
            no_speech_prob=0.02,
            words=[
                n(word=" Um,", start=0.0, end=0.31234, probability=0.61234),
                n(word=" I", start=0.5, end=0.6, probability=0.99),
                n(word=" built", start=0.6, end=0.9, probability=0.4),
                n(word=" it.", start=0.9, end=1.1, probability=0.97),
            ],
        )
        return iter([seg]), n(language="en", duration=1.5)


@pytest.fixture
def fake_fw(monkeypatch):
    _FakeModel.instances.clear()
    monkeypatch.setitem(sys.modules, "faster_whisper", types.SimpleNamespace(WhisperModel=_FakeModel))
    return _FakeModel


def _req(**kw):
    base = {"body": b"", "duration_s": 1.5, "language": "en", "samples": np.zeros(24000, np.float32)}
    base.update(kw)
    return STTRequest(**base)


def test_faster_whisper_with_fake_model(fake_fw):
    stt = FasterWhisperSTT(Settings(whisper_model="/models/whisper/tiny", whisper_cpu_threads=1))
    assert stt.ready()[0] and stt.model == "tiny"
    tr = stt.transcribe(_req())
    m = fake_fw.instances[0]
    assert m.kw["local_files_only"] is True and m.kw["cpu_threads"] == 1
    src, opts = m.calls[0]
    assert isinstance(src, np.ndarray) and opts["suppress_tokens"] == []
    assert tr.text == "Um, I built it." and tr.language == "en" and tr.engine == "faster-whisper"
    w = tr.segments[0].words[0]
    assert (w.word, w.start_s, w.end_s, w.confidence) == ("Um,", 0.0, 0.312, 0.6123)
    stt.transcribe(_req())
    assert len(fake_fw.instances) == 1  # el modelo se carga una sola vez
    assert stt.ready() == (True, "modelo tiny cargado")


def test_faster_whisper_container_uses_temp_file(fake_fw):
    import os

    FasterWhisperSTT(Settings()).transcribe(_req(samples=None, container=b"OggS-bytes"))
    src = fake_fw.instances[0].calls[0][0]
    assert isinstance(src, str) and not os.path.exists(src)  # se borra después


def test_load_error_reported(monkeypatch):
    class Bad:
        def __init__(self, *a, **k):
            raise FileNotFoundError("/ruta/privada")

    monkeypatch.setitem(sys.modules, "faster_whisper", types.SimpleNamespace(WhisperModel=Bad))
    stt = FasterWhisperSTT(Settings())
    with pytest.raises(FileNotFoundError):
        stt.load()
    ok, why = stt.ready()
    assert not ok and "FileNotFoundError" in why and "privada" not in why


def test_ready_without_library(monkeypatch):
    monkeypatch.setitem(sys.modules, "faster_whisper", None)
    ok, why = FasterWhisperSTT(Settings()).ready()
    assert not ok and "no instalado" in why


def test_mock_stt_contract_rules():
    tr = MockSTT().transcribe(_req(body=b"abc", duration_s=2.0, language="es", mock_transcript="hola ?Kafka"))
    words = tr.segments[0].words
    assert tr.text == "hola Kafka" and [w.confidence for w in words] == [0.9, 0.3]
    assert (words[1].start_s, words[1].end_s) == (1.0, 2.0)
    assert MockSTT().transcribe(_req(body=b"abc", language=None)).text.startswith("respuesta simulada ")


def test_factory():
    assert make_stt(Settings(stt_backend="mock")).name == "mock"
    assert make_stt(Settings(stt_backend="faster-whisper")).name == "faster-whisper"
    with pytest.raises(ValueError):
        make_stt(Settings(stt_backend="cloud"))


def test_to_wav_bytes_header():
    assert len(to_wav_bytes(np.zeros(16000, np.float32))) == 44 + 32000


def test_decode_ogg_opus_when_pyav_available():
    av = pytest.importorskip("av")
    from coach_speech.audio import decode_container

    buf = io.BytesIO()
    out = av.open(buf, "w", format="ogg")
    st = out.add_stream("libopus", rate=48000)
    st.layout = "mono"
    pcm = (np.sin(np.arange(48000) / 10) * 8000).astype("<i2")
    for i in range(0, pcm.size, 960):
        f = av.AudioFrame.from_ndarray(pcm[i : i + 960].reshape(1, -1), format="s16", layout="mono")
        f.sample_rate = 48000
        for p in st.encode(f):
            out.mux(p)
    for p in st.encode(None):
        out.mux(p)
    out.close()
    audio = decode_container(buf.getvalue())
    assert audio.dtype == np.float32 and abs(audio.size / 16000 - 1.0) < 0.05
    with pytest.raises(ValueError):
        decode_container(buf.getvalue(), max_s=0.1)


def test_mock_default_text_extension(monkeypatch):
    monkeypatch.setenv("CX9_SPEECH_MODE", "mock")
    monkeypatch.setenv("SPEECH_MOCK_TEXT", "Eh, este... o sea, usé ?Kafka")
    stt = make_stt(Settings.from_env())
    tr = stt.transcribe(_req(body=b"x", language="es"))
    assert tr.text == "Eh, este... o sea, usé Kafka"
    # El header del contrato sigue teniendo prioridad.
    assert stt.transcribe(_req(body=b"x", language="es", mock_transcript="hola")).text == "hola"
    monkeypatch.delenv("SPEECH_MOCK_TEXT")
    assert (
        make_stt(Settings.from_env())
        .transcribe(_req(body=b"x", language="es"))
        .text.startswith("respuesta simulada")
    )
