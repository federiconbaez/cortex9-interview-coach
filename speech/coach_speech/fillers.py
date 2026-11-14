"""Detección de muletillas y pausas sobre palabras con timestamps.

Los patrones salen del analizador de interview-sim, adaptados a nivel palabra.
"""

from __future__ import annotations

import re
import unicodedata

# Vacilaciones de una sola palabra (siempre son muletilla).
HESITATIONS = re.compile(r"^(?:u+m+|u+h+|u+hm+|e+r+m*|e+h+|e+m+|m+|mm+|h+m+|a+h+)$")

SINGLE = {
    "es": {"este", "bueno", "digamos", "tipo", "viste", "básicamente", "basicamente", "nada"},
    "en": {"like", "basically", "actually", "well", "so"},
}
# "este", "bueno", etc. solo cuentan si van seguidos de coma/puntos suspensivos o pausa.
NEEDS_BREAK = {"este", "bueno", "tipo", "nada", "like", "well", "so"}

PHRASES = {
    "es": [("o", "sea"), ("como", "que"), ("en", "plan")],
    "en": [("you", "know"), ("i", "mean"), ("kind", "of"), ("sort", "of")],
}


def clean(word: str) -> str:
    w = unicodedata.normalize("NFKC", word).strip().lower()
    return w.strip(".,;:!?¿¡…\"'()-")


def _has_break(raw: str) -> bool:
    return bool(re.search(r"[,…]|\.\.\.|-$", raw.strip()))


def mark_fillers(words: list[dict], lang: str, pause_s: float = 0.6) -> list[dict]:
    """Agrega is_filler a cada palabra y devuelve la lista de muletillas encontradas.

    words: [{"word", "start_s", "end_s", "confidence"}] en orden.
    """
    found: list[dict] = []
    n = len(words)
    for w in words:
        w["is_filler"] = False
    i = 0
    while i < n:
        w = words[i]
        c = clean(w["word"])
        nxt = clean(words[i + 1]["word"]) if i + 1 < n else ""
        # Frases de dos palabras.
        if nxt and (c, nxt) in PHRASES.get(lang, []):
            words[i]["is_filler"] = words[i + 1]["is_filler"] = True
            found.append({"text": f"{c} {nxt}", "start_s": w["start_s"], "end_s": words[i + 1]["end_s"]})
            i += 2
            continue
        hit = False
        if c and HESITATIONS.match(c):
            hit = True
        elif c in SINGLE.get(lang, set()):
            if c not in NEEDS_BREAK:
                hit = True
            else:
                gap = (words[i + 1]["start_s"] - w["end_s"]) if i + 1 < n else 0.0
                hit = _has_break(w["word"]) or gap >= pause_s
        if hit:
            w["is_filler"] = True
            found.append({"text": c, "start_s": w["start_s"], "end_s": w["end_s"]})
        i += 1
    return found


def find_pauses(words: list[dict], threshold_s: float = 0.6) -> list[dict]:
    out = []
    for a, b in zip(words, words[1:], strict=False):
        gap = round(b["start_s"] - a["end_s"], 3)
        if gap >= threshold_s:
            out.append({"start_s": a["end_s"], "end_s": b["start_s"], "duration_s": gap})
    return out
