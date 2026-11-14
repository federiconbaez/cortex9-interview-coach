import { beforeEach, describe, expect, it, vi } from 'vitest';
import { FakeClock, FakeWs, readyData, resetServerSeq, srv } from '../test/fakes';
import { BinCodec, BinKind, SUBPROTOCOL, decodeFrame, encodeFrame } from './contract/ws-types';
import { DEFAULT_BACKOFF, LiveConnection, backoffDelay, type ConnectionState } from './ws';

function setup(opts: { random?: () => number } = {}) {
  const clock = new FakeClock();
  const states: { s: ConnectionState; delay?: number | undefined; close?: number | undefined; err?: string | undefined }[] = [];
  const msgs: { type: string }[] = [];
  const frames: { turnNo: number; chunkIndex: number }[] = [];
  const invalid: string[] = [];
  const c = new LiveConnection({
    url: 'ws://h/v1/sessions/S/live',
    sessionId: 'S',
    factory: (u, p) => new FakeWs(u, p),
    clock,
    random: opts.random ?? (() => 0.5),
    onMessage: (m) => msgs.push(m),
    onTtsFrame: (h) => frames.push({ turnNo: h.turnNo, chunkIndex: h.chunkIndex }),
    onState: (s, i) => states.push({ s, delay: i.nextDelayMs, close: i.closeCode, err: i.errorCode }),
    onInvalid: (r) => invalid.push(r),
  });
  return { c, clock, states, msgs, frames, invalid, ws: () => FakeWs.all[FakeWs.all.length - 1] as FakeWs };
}

function connectReady(t: ReturnType<typeof setup>, over: Record<string, unknown> = {}) {
  t.c.connect();
  t.ws().open();
  t.ws().recv(srv('session.ready', readyData(over), { ack: 1 }));
}

beforeEach(() => {
  FakeWs.all = [];
  resetServerSeq();
});

describe('backoffDelay', () => {
  it('sigue 0,5 / 1 / 2 / 4 / 8 / 8 s sin jitter', () => {
    const o = { ...DEFAULT_BACKOFF, jitter: 0 };
    expect([0, 1, 2, 3, 4, 5].map((n) => backoffDelay(n, o))).toEqual([500, 1000, 2000, 4000, 8000, 8000]);
  });
  it('aplica jitter acotado', () => {
    expect(backoffDelay(0, DEFAULT_BACKOFF, () => 0)).toBe(400);
    expect(backoffDelay(0, DEFAULT_BACKOFF, () => 1)).toBe(600);
  });
});

describe('frames binarios (docs/ws-types.ts)', () => {
  it('decode(encode(h, p)) es identidad y payload_len inconsistente se rechaza', () => {
    const p = new Uint8Array([1, 2, 3, 4]);
    const buf = encodeFrame({ kind: BinKind.AUDIO_CHUNK, seq: 3, turnNo: 1, chunkIndex: 0, codec: BinCodec.PCM16LE_16K, last: true }, p);
    const { header, payload } = decodeFrame(buf);
    expect(header).toEqual({ kind: 1, seq: 3, turnNo: 1, chunkIndex: 0, codec: 1, last: true, payloadLen: 4 });
    expect([...payload]).toEqual([1, 2, 3, 4]);
    expect([...new Uint8Array(buf, 0, 20)]).toEqual([0x43, 0x58, 1, 1, 0, 0, 0, 3, 0, 0, 0, 1, 0, 0, 0, 0, 1, 1, 0, 4]);
    expect(() => decodeFrame(buf.slice(0, 22))).toThrow();
  });
});

describe('LiveConnection', () => {
  it('abre con el subprotocolo y manda hello con PCM16 16 kHz como base', () => {
    const t = setup();
    t.c.connect();
    expect(t.ws().protocols).toEqual([SUBPROTOCOL]);
    expect(t.ws().binaryType).toBe('arraybuffer');
    t.ws().open();
    const [hello] = t.ws().json();
    expect(hello).toMatchObject({
      v: 1,
      type: 'hello',
      seq: 1,
      session_id: 'S',
      data: { audio_formats: [{ codec: 'pcm16le', sample_rate: 16000, channels: 1 }], tts_delivery: ['binary', 'url'], partials: true, resume: null },
    });
    expect(t.states.map((x) => x.s)).toEqual(['connecting']);
    t.ws().recv(srv('session.ready', readyData()));
    expect(t.c.connectionState).toBe('open');
  });

  it('no manda nada antes de session.ready y luego vacía la cola en orden de seq', () => {
    const t = setup();
    t.c.connect();
    t.ws().open();
    t.c.sendJson('tts.played', { utterance_id: 'u-1' });
    expect(t.ws().json().map((m) => m.type)).toEqual(['hello']);
    t.ws().recv(srv('session.ready', readyData()));
    expect(t.ws().json().map((m) => [m.type, m.seq])).toEqual([
      ['hello', 1],
      ['tts.played', 2],
    ]);
  });

  it('los frames de audio consumen seq de la misma secuencia que el JSON', () => {
    const t = setup();
    connectReady(t);
    t.c.sendJson('tts.played', { utterance_id: 'u-1' });
    t.c.sendAudio(1, 0, new Uint8Array(3200), false);
    t.c.sendAudio(1, 1, new Uint8Array(3200), true);
    t.c.sendJson('audio.end', { turn_id: 't-1', chunks: 2, bytes: 6400, duration_ms: 200 });
    const seqs = t.ws().sent.map((d) => (typeof d === 'string' ? (JSON.parse(d) as { seq: number }).seq : decodeFrame(d).header.seq));
    expect(seqs).toEqual([1, 2, 3, 4, 5]);
    const last = decodeFrame(t.ws().sent[3] as ArrayBuffer).header;
    expect(last).toMatchObject({ kind: 1, turnNo: 1, chunkIndex: 1, codec: 1, last: true, payloadLen: 3200 });
  });

  it('backpressure: retiene audio que supera max_inflight_bytes hasta recibir ack (sin descartar)', () => {
    const t = setup();
    connectReady(t, { max_inflight_bytes: 6400 });
    for (let i = 0; i < 4; i++) t.c.sendAudio(1, i, new Uint8Array(3200), i === 3);
    expect(t.ws().sent.filter((d) => typeof d !== 'string')).toHaveLength(2);
    expect(t.c.inflightAudioBytes).toBe(6400);
    expect(t.c.pendingCount).toBe(2);
    t.ws().recv(srv('transcript.partial', { turn_id: 't-1', text: 'x', revision: 1, audio_ms: 100 }, { ack: 3 }));
    expect(t.ws().sent.filter((d) => typeof d !== 'string')).toHaveLength(4);
    expect(t.c.pendingCount).toBe(0);
  });

  it('backpressure: también espera si bufferedAmount supera la ventana', () => {
    const t = setup();
    connectReady(t, { max_inflight_bytes: 96000 });
    t.ws().bufferedAmount = 200000;
    t.c.sendAudio(1, 0, new Uint8Array(3200), false);
    expect(t.c.pendingCount).toBe(1);
    t.ws().bufferedAmount = 0;
    t.c.pump();
    expect(t.c.pendingCount).toBe(0);
  });

  it('responde ping con pong y manda ping propio cada heartbeat_ms', () => {
    const t = setup();
    connectReady(t);
    t.ws().recv(srv('ping', { nonce: 'n1' }));
    expect(t.ws().json().at(-1)).toMatchObject({ type: 'pong', data: { nonce: 'n1' } });
    t.clock.advance(15000);
    expect(t.ws().json().at(-1)).toMatchObject({ type: 'ping', data: { nonce: 'c1' } });
    t.clock.advance(15000);
    expect(t.ws().json().filter((m) => m.type === 'ping')).toHaveLength(2);
  });

  it('reconecta con backoff y resume: last_server_seq y reenvío de lo no confirmado con su seq original', () => {
    const t = setup();
    connectReady(t);
    t.ws().recv(srv('question', { turn_id: 't-1', question_id: 'q', index: 1, total: 2, text: 'Q', lang: 'es', utterance_id: 'u-1', kind: 'main' }));
    t.c.sendJson('tts.played', { utterance_id: 'u-1' }); // seq 2
    t.c.sendAudio(1, 0, new Uint8Array(3200), false); // seq 3
    const first = t.ws();
    first.drop(4008);
    expect(t.states.at(-1)).toMatchObject({ s: 'reconnecting', delay: 500, close: 4008 });
    t.c.sendAudio(1, 1, new Uint8Array(3200), true); // seq 4, queda en cola
    t.clock.advance(500);
    const second = t.ws();
    expect(second).not.toBe(first);
    second.open();
    expect(second.json()[0]).toMatchObject({ type: 'hello', seq: 5, data: { resume: { last_server_seq: 2 } } });
    // El servidor solo recibió hasta seq 2: el cliente reenvía 3 (original) y después 4.
    second.recv({ ...srv('session.ready', readyData({ resumed: true, last_client_seq: 2 })), seq: 4 });
    const resent = second.sent.slice(1).map((d) => decodeFrame(d as ArrayBuffer).header.seq);
    expect(resent).toEqual([3, 4]);
    expect(t.c.connectionState).toBe('open');
  });

  it('acepta mensajes viejos reenviados DESPUÉS de session.ready y descarta duplicados', () => {
    const t = setup();
    connectReady(t); // server seq 1
    t.ws().drop(1006);
    t.clock.advance(500);
    t.ws().open();
    // ready con seq 4; luego se reenvían 2 y 3; 3 llega repetido.
    t.ws().recv({ ...srv('session.ready', readyData({ resumed: true })), seq: 4 });
    t.ws().recv({ ...srv('transcript.partial', { turn_id: 't-1', text: 'a', revision: 1, audio_ms: 1 }), seq: 2 });
    t.ws().recv({ ...srv('transcript.partial', { turn_id: 't-1', text: 'b', revision: 2, audio_ms: 2 }), seq: 3 });
    t.ws().recv({ ...srv('transcript.partial', { turn_id: 't-1', text: 'b', revision: 2, audio_ms: 2 }), seq: 3 });
    expect(t.msgs.map((m) => m.type)).toEqual(['session.ready', 'session.ready', 'transcript.partial', 'transcript.partial']);
    expect(t.c.lastContiguousServerSeq).toBe(4);
  });

  it('sin resume (RESUME_GAP) acepta el hueco', () => {
    const t = setup();
    connectReady(t);
    t.ws().drop(1006);
    t.clock.advance(500);
    t.ws().open();
    t.ws().recv({ ...srv('session.ready', readyData({ resumed: false })), seq: 9 });
    expect(t.c.lastContiguousServerSeq).toBe(9);
  });

  it('no reconecta ante cierres terminales (4009 reemplazada, 1000 normal)', () => {
    const t = setup();
    connectReady(t);
    t.ws().drop(4009);
    expect(t.states.at(-1)).toMatchObject({ s: 'failed', close: 4009 });
    expect(t.clock.pending()).toEqual([]);
    const t2 = setup();
    connectReady(t2);
    t2.ws().drop(1000);
    expect(t2.states.at(-1)?.s).toBe('closed');
  });

  it('error fatal no reintentable → failed con su código', () => {
    const t = setup();
    connectReady(t);
    t.ws().recv(srv('error', { code: 'AUTH_FAILED', message: 'x', fatal: true, retryable: false, close_code: 4001 }));
    t.ws().drop(4001);
    expect(t.states.at(-1)).toMatchObject({ s: 'failed', err: 'AUTH_FAILED' });
  });

  it('error fatal reintentable (HELLO_REQUIRED, 4400) → reintenta', () => {
    const t = setup();
    connectReady(t);
    t.ws().recv(srv('error', { code: 'HELLO_REQUIRED', message: 'x', fatal: true, retryable: true, close_code: 4400 }));
    t.ws().drop(4400);
    expect(t.states.at(-1)?.s).toBe('reconnecting');
  });

  it('agota la gracia de 120 s y queda en failed', () => {
    const t = setup();
    connectReady(t);
    t.ws().drop(1006);
    for (let i = 0; i < 40 && t.c.connectionState !== 'failed'; i++) {
      t.clock.advance(8000);
      t.ws().drop(1006);
    }
    expect(t.c.connectionState).toBe('failed');
    expect(t.states.filter((x) => x.s === 'reconnecting' && x.delay !== undefined).map((x) => x.delay).slice(0, 5)).toEqual([500, 1000, 2000, 4000, 8000]);
  });

  it('45 s sin recibir nada → reconecta', () => {
    const t = setup();
    connectReady(t);
    const first = t.ws();
    t.clock.advance(45000);
    expect(first.closed?.code).toBe(3000);
    expect(t.states.some((x) => x.s === 'reconnecting')).toBe(true);
  });

  it('ignora mensajes inválidos o de tipo desconocido y enruta frames TTS', () => {
    const t = setup();
    connectReady(t);
    t.ws().recv('no es json');
    t.ws().recv({ v: 2, type: 'question', seq: 99, data: {} });
    t.ws().recv(srv('suggestion', { text: 'x' }));
    t.ws().recvBin(new ArrayBuffer(5));
    const f = encodeFrame({ kind: BinKind.TTS_AUDIO, seq: 99, turnNo: 1, chunkIndex: 0, codec: BinCodec.WAV, last: true }, new Uint8Array(4));
    t.ws().recvBin(f);
    t.ws().onmessage?.({ data: 42 });
    expect(t.msgs.map((m) => m.type)).toEqual(['session.ready']);
    expect(t.frames).toEqual([{ turnNo: 1, chunkIndex: 0 }]);
    expect(t.invalid).toEqual(expect.arrayContaining(['invalid json', 'invalid envelope', 'unknown type suggestion', 'non-string message']));
  });

  it('close() cierra con 1000 y cancela el reintento pendiente', () => {
    const t = setup();
    connectReady(t);
    t.ws().drop(1006);
    t.c.close();
    expect(t.clock.pending()).toEqual([]);
    expect(t.c.connectionState).toBe('closed');
    const t2 = setup();
    connectReady(t2);
    const spy = vi.spyOn(t2.ws(), 'close');
    t2.c.close();
    expect(spy).toHaveBeenCalledWith(1000, 'client');
  });
});
