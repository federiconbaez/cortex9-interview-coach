import type { Clock, WsLike } from '../api/ws';

export class FakeWs implements WsLike {
  static all: FakeWs[] = [];
  readyState = 0;
  binaryType = 'blob';
  bufferedAmount = 0;
  protocol = '';
  sent: (string | ArrayBuffer)[] = [];
  closed: { code?: number | undefined; reason?: string | undefined } | null = null;
  onopen: ((ev: Event) => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  constructor(
    readonly url: string,
    readonly protocols: string[],
  ) {
    FakeWs.all.push(this);
  }
  send(d: string | ArrayBuffer) {
    this.sent.push(d);
  }
  close(code?: number, reason?: string) {
    this.closed = { code, reason };
  }
  // helpers de test
  open() {
    this.readyState = 1;
    this.onopen?.(new Event('open'));
  }
  drop(code: number) {
    this.readyState = 3;
    this.onclose?.({ code, reason: '' });
  }
  recv(o: unknown) {
    this.onmessage?.({ data: typeof o === 'string' ? o : JSON.stringify(o) });
  }
  recvBin(b: ArrayBuffer) {
    this.onmessage?.({ data: b });
  }
  json(): Record<string, unknown>[] {
    return this.sent.filter((x): x is string => typeof x === 'string').map((x) => JSON.parse(x) as Record<string, unknown>);
  }
}

/** Reloj manual: `advance(ms)` dispara timers en orden. */
export class FakeClock implements Clock {
  t = 1_700_000_000_000;
  private id = 0;
  private timers = new Map<number, { at: number; fn: () => void; every: number | null }>();
  now = () => this.t;
  setTimeout = (fn: () => void, ms: number) => this.add(fn, ms, null);
  setInterval = (fn: () => void, ms: number) => this.add(fn, ms, ms);
  clearTimeout = (h: unknown) => void this.timers.delete(h as number);
  clearInterval = (h: unknown) => void this.timers.delete(h as number);
  private add(fn: () => void, ms: number, every: number | null) {
    this.id += 1;
    this.timers.set(this.id, { at: this.t + ms, fn, every });
    return this.id;
  }
  pending(): number[] {
    return [...this.timers.values()].map((x) => x.at - this.t).sort((a, b) => a - b);
  }
  advance(ms: number) {
    const end = this.t + ms;
    for (;;) {
      let next: [number, { at: number; fn: () => void; every: number | null }] | null = null;
      for (const e of this.timers) if (e[1].at <= end && (!next || e[1].at < next[1].at)) next = e;
      if (!next) break;
      const [id, tm] = next;
      this.t = tm.at;
      if (tm.every === null) this.timers.delete(id);
      else tm.at += tm.every;
      tm.fn();
    }
    this.t = end;
  }
}

let serverSeq = 0;
export const resetServerSeq = () => (serverSeq = 0);
export function srv(type: string, data: unknown, extra: Record<string, unknown> = {}) {
  serverSeq += 1;
  return { v: 1, type, seq: serverSeq, ts: '2026-11-14T07:41:00.000Z', session_id: 'S', data, ...extra };
}
export const readyData = (over: Record<string, unknown> = {}) => ({
  state: 'ready',
  audio_format: { codec: 'pcm16le', sample_rate: 16000, channels: 1, frame_ms: 100 },
  tts_delivery: 'binary',
  tts_format: 'wav',
  heartbeat_ms: 15000,
  idle_timeout_ms: 45000,
  max_inflight_bytes: 96000,
  max_turn_ms: 300000,
  resumed: false,
  last_client_seq: 0,
  question_index: 0,
  question_count: 2,
  ...over,
});
