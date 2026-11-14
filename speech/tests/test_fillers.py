from coach_speech.fillers import clean, find_pauses, mark_fillers


def W(word, start, end, p=0.9):
    return {"word": word, "start_s": start, "end_s": end, "confidence": p}


def seq(text, step=0.4):
    return [W(t, i * step, i * step + 0.3) for i, t in enumerate(text.split())]


def test_spanish_hesitations_and_phrases():
    ws = seq("Eh, este... o sea, mmm trabajé en este proyecto")
    found = [f["text"] for f in mark_fillers(ws, "es")]
    assert found == ["eh", "este", "o sea", "mmm"]
    # "este proyecto" (demostrativo) no es muletilla.
    assert ws[-2]["is_filler"] is False


def test_english_fillers():
    ws = seq("Um, uh, you know, I mean I like Python")
    found = [f["text"] for f in mark_fillers(ws, "en")]
    assert found == ["um", "uh", "you know", "i mean"]
    assert ws[-2]["is_filler"] is False  # "like" verbo, sin pausa


def test_break_by_pause():
    ws = [W("well", 0.0, 0.3), W("I", 1.2, 1.3)]
    assert [f["text"] for f in mark_fillers(ws, "en", pause_s=0.6)] == ["well"]


def test_pauses():
    ws = [W("a", 0, 0.2), W("b", 0.3, 0.5), W("c", 1.5, 1.7)]
    assert find_pauses(ws, 0.6) == [{"start_s": 0.5, "end_s": 1.5, "duration_s": 1.0}]
    assert find_pauses([], 0.6) == []


def test_clean():
    assert clean(" ¿Este...? ") == "este"
