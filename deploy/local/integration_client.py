#!/usr/bin/env python3
"""Cliente de integración: recorre el flujo real del backend según docs/contract.md.

REST (health, ready, login, idiomas, escenarios, sesión con Idempotency-Key) →
WebSocket cx9.live.v1 (hello seq 0, session.ready, question + TTS binario,
tts.played, control.repeat, audio PCM16 en frames binarios con ventana de acks,
audio.end, transcript.partial/final, turn.end, followup, session.end) →
informe (202 → 200) y detalle de la sesión con muletillas de STT.

Lo usa deploy/local/integration-test.sh. Requiere httpx y websockets.
Sale con código 1 si falla algún chequeo; escribe un resumen JSON con --out.
"""

from __future__ import annotations

import argparse
import asyncio
import hashlib
import json
import math
import struct
import sys
import time
import uuid
from datetime import datetime, timezone

import httpx
import websockets

FRAME = struct.Struct(">2sBBIIIBBH")  # §5.4: 20 bytes, big-endian
KIND_AUDIO, KIND_TTS = 0x01, 0x02
CODEC_PCM16, CODEC_WAV = 0x01, 0x03
NO_ANSWER_KINDS = {"intro", "closing"}


class Checks:
    def __init__(self) -> None:
        self.items: list[dict] = []

    def add(self, name: str, ok: bool, detail: object = "") -> bool:
        self.items.append({"check": name, "ok": bool(ok), "detail": detail})
        print(
            f"[{'OK' if ok else 'FALLA'}] {name}" + (f" — {detail}" if detail not in ("", None) else ""),
            flush=True,
        )
        return ok

    @property
    def failed(self) -> list[dict]:
        return [c for c in self.items if not c["ok"]]


def synth_answer(seconds: float, rate: int = 16000) -> bytes:
    """Señal tipo voz (armónicos con envolvente silábica) para que no sea silencio."""
    n = int(seconds * rate)
    out = bytearray()
    for i in range(n):
        t = i / rate
        env = 0.5 * (1 - math.cos(2 * math.pi * 4 * t))  # ~4 sílabas por segundo
        f0 = 140 + 20 * math.sin(2 * math.pi * 0.5 * t)
        v = sum(math.sin(2 * math.pi * f0 * k * t) / k for k in range(1, 6))
        out += struct.pack("<h", int(max(-1, min(1, 0.25 * env * v)) * 32767))
    return bytes(out)


class Live:
    """Conexión WS con secuencia del cliente, acks y cola de mensajes del servidor."""

    def __init__(self, ws, session_id: str):
        self.ws, self.sid = ws, session_id
        self.seq = 0
        self.ack = 0
        self.window = 96000
        self.inflight: dict[int, int] = {}
        self.queue: asyncio.Queue = asyncio.Queue()
        self.server_seqs: list[int] = []
        self.acks_seen = 0
        self.ack_event = asyncio.Event()

    def _ts(self) -> str:
        return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")

    async def send(self, typ: str, data: dict, seq0: bool = False) -> int:
        seq = 0 if seq0 else self._next()
        await self.ws.send(
            json.dumps(
                {"v": 1, "type": typ, "seq": seq, "ts": self._ts(), "session_id": self.sid, "data": data}
            )
        )
        return seq

    def _next(self) -> int:
        self.seq += 1
        return self.seq

    async def send_frame(self, turn_no: int, idx: int, payload: bytes, last: bool) -> None:
        while sum(self.inflight.values()) + len(payload) > self.window:
            self.ack_event.clear()
            await asyncio.wait_for(self.ack_event.wait(), 10)
        seq = self._next()
        self.inflight[seq] = len(payload)
        hdr = FRAME.pack(b"CX", 1, KIND_AUDIO, seq, turn_no, idx, CODEC_PCM16, 1 if last else 0, len(payload))
        await self.ws.send(hdr + payload)

    def _on_ack(self, n: int) -> None:
        if n > self.ack:
            self.ack = n
            for s in [s for s in self.inflight if s <= n]:
                del self.inflight[s]
            self.ack_event.set()

    async def reader(self) -> None:
        try:
            async for raw in self.ws:
                if isinstance(raw, bytes):
                    magic, ver, kind, seq, utt, idx, codec, flags, plen = FRAME.unpack(raw[:20])
                    self.server_seqs.append(seq)
                    await self.queue.put(
                        (
                            "frame",
                            {
                                "seq": seq,
                                "kind": kind,
                                "utt": utt,
                                "idx": idx,
                                "codec": codec,
                                "last": bool(flags & 1),
                                "payload": raw[20:],
                                "ok": magic == b"CX" and ver == 1 and plen == len(raw) - 20,
                            },
                        )
                    )
                    continue
                msg = json.loads(raw)
                if msg.get("ack") is not None:
                    self._on_ack(int(msg["ack"]))
                if msg["type"] == "ack":
                    self.acks_seen += 1
                    continue
                if msg["seq"]:
                    self.server_seqs.append(msg["seq"])
                await self.queue.put(("msg", msg))
        except websockets.ConnectionClosed as e:
            await self.queue.put(("closed", {"code": e.code if hasattr(e, "code") else None}))

    async def next(self, timeout: float = 30.0):
        return await asyncio.wait_for(self.queue.get(), timeout)


async def collect_tts(live: Live, ann: dict, timeout: float) -> tuple[bytes, list]:
    chunks: list[dict] = []
    while len(chunks) < ann["chunks"]:
        kind, item = await live.next(timeout)
        if kind == "frame":
            chunks.append(item)
        elif kind == "msg":  # no debería intercalarse nada antes de los frames
            await live.queue.put((kind, item))
            break
    return b"".join(c["payload"] for c in chunks), chunks


async def run(args) -> int:
    ck = Checks()
    base = args.base.rstrip("/")
    async with httpx.AsyncClient(base_url=base, timeout=30) as http:
        r = await http.get("/v1/health")
        ck.add("GET /v1/health sin auth", r.status_code == 200, r.json().get("status"))
        r = await http.get("/v1/ready")
        ready = r.json()
        sp = ready.get("checks", {}).get("speech", {})
        ck.add(
            "GET /v1/ready con voz usable",
            r.status_code == 200 and sp.get("status") in ("ok", "degraded"),
            f"{ready.get('status')} · speech {sp.get('status')}: {sp.get('detail')}",
        )

        r = await http.get("/v1/scenarios")
        ck.add("REST protegido sin login → 401", r.status_code == 401, r.status_code)
        r = await http.post("/v1/auth/login", json={"password": args.password})
        ck.add("POST /v1/auth/login", r.status_code == 200 and "cx9_session" in http.cookies, r.status_code)

        langs = (await http.get("/v1/languages")).json()["items"]
        es = next((x for x in langs if x["code"] == args.lang), {})
        ck.add(
            "GET /v1/languages informa STT y voces",
            es.get("stt") is True and bool(es.get("tts_voices")),
            {k: es.get(k) for k in ("stt", "tts_voices", "default_voice")},
        )
        scen = (await http.get("/v1/scenarios")).json()["items"]
        sid_scen = args.scenario or next(s["id"] for s in scen if args.lang in s["languages"])
        ck.add("GET /v1/scenarios", len(scen) > 0, f"{len(scen)} escenarios, uso {sid_scen}")

        body = {
            "scenario_id": sid_scen,
            "lang": args.lang,
            "mode": "practice",
            "input": "voice",
            "llm_provider": args.llm,
            "num_questions": args.questions,
            "max_followups": args.followups,
            "shuffle": False,
            "think_pause_ms": 0,
            "learn_profile": False,
            "save_audio": False,
        }
        key = "it-" + uuid.uuid4().hex[:16]
        r1 = await http.post("/v1/sessions", json=body, headers={"Idempotency-Key": key})
        r2 = await http.post("/v1/sessions", json=body, headers={"Idempotency-Key": key})
        sess = r1.json()
        ck.add("POST /v1/sessions → 201", r1.status_code == 201, sess.get("id"))
        ck.add(
            "Idempotency-Key repite la misma sesión",
            r2.json().get("id") == sess.get("id") and r2.headers.get("idempotent-replayed") == "true",
            r2.headers.get("idempotent-replayed"),
        )
        if r1.status_code != 201:
            return finish(ck, args, {})

        answer_pcm = None
        if args.speech_url:  # modo real: la respuesta se sintetiza con el propio TTS del servicio de voz
            rr = await http.post(
                args.speech_url.rstrip("/") + "/tts",
                json={
                    "text": args.answer_text,
                    "language": args.lang,
                    "format": "pcm16le",
                    "sample_rate": 16000,
                },
            )
            ck.add(
                "Respuesta hablada sintetizada con /tts del servicio de voz",
                rr.status_code == 200,
                f"{rr.headers.get('x-engine')} {rr.headers.get('x-audio-duration-ms')} ms",
            )
            answer_pcm = rr.content
        if not answer_pcm:
            answer_pcm = synth_answer(args.answer_seconds)

        ws_url = base.replace("http", "ws", 1) + sess["live_url"]
        cookie = "; ".join(f"{k}={v}" for k, v in http.cookies.items())
        stats = {
            "questions": [],
            "followups": 0,
            "partials": 0,
            "finals": [],
            "tts_utterances": 0,
            "tts_bytes": 0,
            "repeat_ok": None,
            "errors": [],
        }
        t0 = time.monotonic()
        async with websockets.connect(
            ws_url,
            subprotocols=["cx9.live.v1"],
            max_size=2**22,
            additional_headers={"Cookie": cookie, "Origin": args.origin},
        ) as ws:
            ck.add("WebSocket con subprotocolo cx9.live.v1", ws.subprotocol == "cx9.live.v1", ws.subprotocol)
            live = Live(ws, sess["id"])
            reader = asyncio.create_task(live.reader())
            await live.send(
                "hello",
                {
                    "client": "cx9-integration/1.0",
                    "partials": True,
                    "audio_formats": [{"codec": "pcm16le", "sample_rate": 16000, "channels": 1}],
                    "tts_delivery": ["binary"],
                    "tts_formats": ["pcm16le", "wav"],
                    "resume": None,
                },
                seq0=True,
            )
            kind, ready_msg = await live.next(10)
            rd = ready_msg.get("data", {}) if kind == "msg" else {}
            ck.add(
                "hello (seq 0) → session.ready",
                kind == "msg" and ready_msg["type"] == "session.ready",
                {
                    k: rd.get(k)
                    for k in ("state", "tts_delivery", "tts_format", "max_inflight_bytes", "question_count")
                },
            )
            live.window = rd.get("max_inflight_bytes", 96000)

            pong_nonce = "n-" + uuid.uuid4().hex[:8]
            await live.send("ping", {"nonce": pong_nonce})
            repeated = False
            current = None
            ended = None
            while ended is None:
                kind, m = await live.next(args.timeout)
                if kind == "closed":
                    stats["errors"].append(f"cerrado {m}")
                    break
                if kind == "frame":
                    stats["errors"].append(f"frame suelto utt {m['utt']}")
                    continue
                typ, d = m["type"], m.get("data", {})
                if typ in ("question", "followup"):
                    current = {**d, "type": typ}
                    if typ == "question" and d.get("kind") not in NO_ANSWER_KINDS and not d.get("repeat"):
                        stats["questions"].append(d.get("question_id"))
                    if typ == "followup" and not d.get("repeat"):
                        stats["followups"] += 1
                    if d.get("repeat") and repeated and stats["repeat_ok"] is None:
                        stats["repeat_ok"] = d.get("turn_id") == repeated
                elif typ == "tts.audio":
                    audio, frames = await collect_tts(live, d, args.timeout)
                    ok = (
                        len(frames) == d["chunks"]
                        and len(audio) == d["bytes"]
                        and all(f["ok"] and f["kind"] == KIND_TTS for f in frames)
                        and [f["idx"] for f in frames] == list(range(len(frames)))
                        and frames[-1]["last"]
                        and (not d.get("sha256") or hashlib.sha256(audio).hexdigest() == d["sha256"])
                    )
                    if not ok:
                        stats["errors"].append(f"tts {d['utterance_id']} inconsistente")
                    stats["tts_utterances"] += 1
                    stats["tts_bytes"] += len(audio)
                    if (
                        current
                        and current.get("utterance_id") == d["utterance_id"]
                        and current.get("kind") != "closing"
                    ):
                        await live.send("tts.played", {"utterance_id": d["utterance_id"]})
                        if current.get("kind") in NO_ANSWER_KINDS:
                            continue
                        if not repeated and args.test_repeat and current["type"] == "question":
                            repeated = current["turn_id"]
                            await live.send("control.repeat", {"turn_id": current["turn_id"]})
                            continue
                        await answer(live, current["turn_id"], answer_pcm, args.realtime)
                elif typ == "transcript.partial":
                    stats["partials"] += 1
                    if stats["partials"] == 1:
                        stats["first_partial"] = d.get("text")
                elif typ == "transcript.final":
                    stats["finals"].append(d)
                elif typ == "turn.end":
                    stats.setdefault("turn_ends", []).append(
                        {k: d.get(k) for k in ("turn_id", "reason", "next", "followups_used")}
                    )
                elif typ == "pong":
                    stats["pong_ok"] = d.get("nonce") == pong_nonce
                elif typ == "error":
                    stats["errors"].append(f"{d.get('code')}: {d.get('message')}")
                elif typ == "session.end":
                    ended = d
            reader.cancel()
        elapsed = round(time.monotonic() - t0, 1)

        ck.add("pong con el mismo nonce", stats.get("pong_ok") is True)
        ck.add(
            "TTS por WS (tts.audio + frames binarios 0x02, sha256)",
            stats["tts_utterances"] > 0 and not any(e.startswith("tts") for e in stats["errors"]),
            f"{stats['tts_utterances']} intervenciones, {stats['tts_bytes']} bytes",
        )
        if args.test_repeat:
            ck.add(
                "control.repeat re-emite la misma pregunta (repeat=true, mismo turn_id)",
                stats["repeat_ok"] is True,
            )
        ck.add("acks periódicos (seq 0) durante el audio", live.acks_seen > 0, live.acks_seen)
        ck.add(
            "transcript.partial durante la respuesta",
            stats["partials"] > 0,
            f"{stats['partials']} parciales; primero: {stats.get('first_partial')!r}",
        )
        finals = stats["finals"]
        ck.add(
            "transcript.final por cada respuesta",
            len(finals) > 0 and all(f.get("source") == "stt" for f in finals),
            f"{len(finals)} finales",
        )
        if finals:
            f0 = finals[0]
            ck.add(
                "final con texto, duración y confianza",
                bool(f0["text"]) and f0["duration_ms"] > 0 and f0.get("confidence") is not None,
                {k: f0.get(k) for k in ("text", "duration_ms", "confidence", "low_confidence")},
            )
            if args.expect_low_conf:
                ck.add(
                    "low_confidence marca la palabra dudosa",
                    args.expect_low_conf in f0.get("low_confidence", []),
                    f0.get("low_confidence"),
                )
        ck.add(
            "repreguntas ≤ máximo pedido",
            stats["followups"] <= args.followups * max(1, len(stats["questions"])),
            f"{stats['followups']} repreguntas, {len(stats['questions'])} preguntas",
        )
        ck.add(
            "session.end",
            ended is not None and bool(ended.get("report_url")),
            ended and {k: ended.get(k) for k in ("reason", "summary")},
        )
        ck.add("sin errores WS", not stats["errors"], stats["errors"])
        ck.add(
            "orden de seq del servidor sin huecos",
            sorted(live.server_seqs)
            == list(range(min(live.server_seqs or [1]), max(live.server_seqs or [0]) + 1)),
            f"{len(live.server_seqs)} mensajes",
        )

        report = {}
        if ended:
            url = ended["report_url"]
            first = None
            for _ in range(int(args.report_timeout * 2)):
                rr = await http.get(url)
                first = first or rr.status_code
                if rr.status_code == 200:
                    report = rr.json()
                    break
                await asyncio.sleep(0.5)
            ck.add("GET informe → 200 (202 mientras puntúa)", bool(report), f"primero {first}")
            dims = report.get("dim_means") or {}
            ck.add(
                "informe con 6 dimensiones",
                len(dims) == 6 if isinstance(dims, (dict, list)) else False,
                sorted(dims) if isinstance(dims, dict) else dims,
            )
            ck.add("top3 con 3 elementos", len(report.get("top3", [])) == 3)
            text = json.dumps(report, ensure_ascii=False)
            if args.expect_fillers:
                ck.add(
                    "muletillas de STT en el informe (muletillas_fuente=stt)",
                    '"muletillas_fuente": "stt"' in text,
                    _find(report, "muletillas"),
                )
            detail = (await http.get(f"/v1/sessions/{sess['id']}")).json()
            turns = detail.get("turns", [])
            answered = [t for t in turns if t.get("answer")]
            ck.add(
                "sesión con turnos y respuestas transcriptas",
                len(answered) > 0 and detail.get("status") in ("completed", "scoring", "ended"),
                f"status {detail.get('status')}, {len(answered)}/{len(turns)} con respuesta",
            )
            if args.expect_fillers and answered:
                ck.add(
                    "Turn.stt_fillers y Turn.pauses desde la voz",
                    answered[0].get("stt_fillers") is not None,
                    {k: answered[0].get(k) for k in ("stt_fillers", "pauses", "low_confidence")},
                )
            if (
                not args.expect_fillers and answered
            ):  # modo real: Whisper tiny puede omitir muletillas; solo informa
                print(
                    f"[INFO] muletillas: fuente {_find(report, 'muletillas_fuente')!r}, "
                    f"Turn.stt_fillers {answered[0].get('stt_fillers')!r}, pausas {answered[0].get('pauses')!r}",
                    flush=True,
                )
                stats["fillers_info"] = {
                    "fuente": _find(report, "muletillas_fuente"),
                    "stt_fillers": [t.get("stt_fillers") for t in answered],
                    "answers": [t.get("answer") for t in answered],
                }
            md = await http.get(url, headers={"Accept": "text/markdown"})
            ck.add(
                "informe en Markdown",
                md.status_code == 200 and "markdown" in md.headers.get("content-type", ""),
                md.headers.get("content-type"),
            )
        metrics = (await http.get(args.metrics_url or "/metrics")).text
        ck.add("métricas del backend hacia la voz", "cx9_speech_request_duration_seconds" in metrics)
        stats["elapsed_s"] = elapsed
        return finish(
            ck, args, {"session_id": sess["id"], "stats": stats, "report_overall": report.get("overall")}
        )


def _find(obj, key):
    if isinstance(obj, dict):
        for k, v in obj.items():
            if k == key:
                return v
            got = _find(v, key)
            if got is not None:
                return got
    if isinstance(obj, list):
        for v in obj:
            got = _find(v, key)
            if got is not None:
                return got
    return None


async def answer(live: Live, turn_id: str, pcm: bytes, realtime: float) -> None:
    turn_no = int(turn_id.split("-")[1])
    step = 3200  # 100 ms
    chunks = [pcm[i : i + step] for i in range(0, len(pcm), step)]
    for i, c in enumerate(chunks):
        await live.send_frame(turn_no, i, c, i == len(chunks) - 1)
        if realtime > 0:
            await asyncio.sleep(0.1 / realtime)
    await live.send(
        "audio.end",
        {
            "turn_id": turn_id,
            "chunks": len(chunks),
            "bytes": len(pcm),
            "duration_ms": len(pcm) * 1000 // 32000,
        },
    )


def finish(ck: Checks, args, extra: dict) -> int:
    summary = {
        "ok": not ck.failed,
        "passed": len(ck.items) - len(ck.failed),
        "failed": len(ck.failed),
        "checks": ck.items,
        **extra,
    }
    if args.out:
        with open(args.out, "w", encoding="utf-8") as f:
            json.dump(summary, f, ensure_ascii=False, indent=2, default=str)
    print(f"\nResultado: {summary['passed']} OK, {summary['failed']} con falla")
    return 0 if summary["ok"] else 1


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--base", required=True, help="URL del backend, p. ej. http://127.0.0.1:28080")
    p.add_argument("--password", required=True)
    p.add_argument("--origin", default="http://127.0.0.1:5173")
    p.add_argument("--lang", default="es")
    p.add_argument("--scenario")
    p.add_argument("--llm", default="mock")
    p.add_argument("--questions", type=int, default=2)
    p.add_argument("--followups", type=int, default=1)
    p.add_argument("--answer-seconds", type=float, default=5.0)
    p.add_argument(
        "--answer-text",
        default="Eh, este... o sea, trabajé tres años en backend con Kafka y, um, bajé la latencia un cuarenta por ciento.",
    )
    p.add_argument("--speech-url", help="modo real: sintetiza la respuesta con /tts de este servicio de voz")
    p.add_argument(
        "--realtime",
        type=float,
        default=2.0,
        help="velocidad de envío del audio (1 = tiempo real, 0 = sin pausa)",
    )
    p.add_argument("--test-repeat", action="store_true")
    p.add_argument("--expect-fillers", action="store_true")
    p.add_argument("--expect-low-conf")
    p.add_argument("--timeout", type=float, default=60)
    p.add_argument("--report-timeout", type=float, default=60)
    p.add_argument(
        "--metrics-url", help="por defecto <base>/metrics (detrás de nginx, el puerto directo del backend)"
    )
    p.add_argument("--out")
    sys.exit(asyncio.run(run(p.parse_args())))


if __name__ == "__main__":
    main()
