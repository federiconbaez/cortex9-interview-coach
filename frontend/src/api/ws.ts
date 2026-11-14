/**
 * Cliente del WebSocket en vivo (contract.md §5): subprotocolo `cx9.live.v1`, sobre JSON con
 * `seq`/`ack`, frames binarios con header de 20 bytes, heartbeat, backpressure y resume.
 */
import {
  BinCodec,
  BinKind,
  CloseCode,
  HEARTBEAT_MS,
  IDLE_TIMEOUT_MS,
  RECONNECT_GRACE_MS,
  SUBPROTOCOL,
  BASELINE_AUDIO,
  decodeFrame,
  encodeFrame,
  type BinaryFrameHeader,
  type ClientMessage,
  type ServerMessage,
  type WsError,
} from './contract/ws-types';

export type ConnectionState = 'idle' | 'connecting' | 'open' | 'reconnecting' | 'closed' | 'failed';

export interface BackoffOptions {
  initialMs: number;
  maxMs: number;
  factor: number;
  jitter: number;
  graceMs: number;
}

/** §5.9: 0,5 s, 1 s, 2 s, 4 s, 8 s (tope 8 s) con jitter, hasta agotar la gracia de 120 s. */
export const DEFAULT_BACKOFF: BackoffOptions = { initialMs: 500, maxMs: 8000, factor: 2, jitter: 0.2, graceMs: RECONNECT_GRACE_MS };

export function backoffDelay(attempt: number, o: BackoffOptions, random: () => number = Math.random): number {
  const base = Math.min(o.maxMs, o.initialMs * o.factor ** attempt);
  return Math.max(0, Math.round(base + base * o.jitter * (random() * 2 - 1)));
}

/** Cierres tras los cuales NO se reintenta (§5.11). */
const TERMINAL_CLOSES = new Set<number>([
  CloseCode.NORMAL,
  CloseCode.AUTH_FAILED,
  CloseCode.FORBIDDEN_ORIGIN,
  CloseCode.SESSION_NOT_FOUND,
  CloseCode.REPLACED,
  CloseCode.SESSION_ENDED,
]);

export interface WsLike {
  readyState: number;
  binaryType: string;
  bufferedAmount: number;
  protocol: string;
  onopen: ((ev: Event) => void) | null;
  onclose: ((ev: { code: number; reason: string }) => void) | null;
  onerror: ((ev: Event) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  send(data: string | ArrayBuffer): void;
  close(code?: number, reason?: string): void;
}

export type WsFactory = (url: string, protocols: string[]) => WsLike;

export interface Clock {
  now(): number;
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(h: unknown): void;
  setInterval(fn: () => void, ms: number): unknown;
  clearInterval(h: unknown): void;
}

export const realClock: Clock = {
  now: () => Date.now(),
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
  setInterval: (fn, ms) => setInterval(fn, ms),
  clearInterval: (h) => clearInterval(h as ReturnType<typeof setInterval>),
};

export interface StateInfo {
  attempt: number;
  nextDelayMs?: number;
  closeCode?: number;
  errorCode?: string;
}

export interface LiveOptions {
  url: string;
  sessionId: string;
  client?: string;
  factory?: WsFactory;
  clock?: Clock;
  random?: () => number;
  backoff?: Partial<BackoffOptions>;
  onMessage: (m: ServerMessage) => void;
  onTtsFrame?: (h: BinaryFrameHeader, payload: Uint8Array) => void;
  onState: (s: ConnectionState, info: StateInfo) => void;
  /** Mensajes o frames que no cumplen el contrato (se ignoran; útil para depurar). */
  onInvalid?: (reason: string) => void;
}

type OutItem = { seq: number; data: string | ArrayBuffer; audioBytes: number };
type ClientData<T extends ClientMessage['type']> = Extract<ClientMessage, { type: T }>['data'];

const OPEN = 1;
const SERVER_TYPES = new Set<string>([
  'session.ready', 'question', 'followup', 'tts.audio', 'tts.url', 'transcript.partial', 'transcript.final',
  'pause', 'resume', 'turn.end', 'session.end', 'error', 'ack', 'ping', 'pong',
]);
/** Tiempo máximo esperando que se llene un hueco de seq del servidor antes de aceptarlo. */
const GAP_TIMEOUT_MS = 2000;
/** Máximo de mensajes retenidos fuera de orden. */
const MAX_HELD = 1024;

export class LiveConnection {
  private ws: WsLike | null = null;
  private state: ConnectionState = 'idle';
  private attempt = 0;
  private outSeq = 0;
  private lastServerSeq = 0;
  /** Seq mayores al contiguo ya procesados (solo `session.ready`, que no espera). */
  private seenAhead = new Set<number>();
  /** Mensajes retenidos hasta que llegue el seq que falta (§5.2: el cliente ordena por seq). */
  private held = new Map<number, () => void>();
  private ack = 0;
  private ready = false;
  private everReady = false;
  private stopped = false;
  private forceReconnect = false;
  private lastFatal: WsError['data'] | null = null;
  private disconnectedAt: number | null = null;
  private maxInflight = 96000;
  private heartbeatMs: number = HEARTBEAT_MS;
  private idleMs: number = IDLE_TIMEOUT_MS;
  private outbox: OutItem[] = [];
  private unacked: OutItem[] = [];
  private timers: { reconnect: unknown; heartbeat: unknown; idle: unknown; gap: unknown } = { reconnect: null, heartbeat: null, idle: null, gap: null };
  private nonce = 0;
  private readonly o: BackoffOptions;
  private readonly clock: Clock;
  private readonly factory: WsFactory;

  constructor(private readonly opts: LiveOptions) {
    this.o = { ...DEFAULT_BACKOFF, ...opts.backoff };
    this.clock = opts.clock ?? realClock;
    this.factory = opts.factory ?? ((url, p) => new WebSocket(url, p) as unknown as WsLike);
  }

  get connectionState(): ConnectionState {
    return this.state;
  }

  /** Bytes de audio enviados y todavía no confirmados por `ack`. */
  get inflightAudioBytes(): number {
    return this.unacked.reduce((a, x) => a + x.audioBytes, 0);
  }

  get pendingCount(): number {
    return this.outbox.length;
  }

  connect(): void {
    this.stopped = false;
    this.open();
  }

  private setState(s: ConnectionState, extra: Omit<StateInfo, 'attempt'> = {}) {
    this.state = s;
    this.opts.onState(s, { attempt: this.attempt, ...extra });
  }

  private nextSeq(): number {
    this.outSeq += 1;
    return this.outSeq;
  }

  private envelope(type: string, seq: number, data: unknown): string {
    return JSON.stringify({ v: 1, type, seq, ts: new Date(this.clock.now()).toISOString(), session_id: this.opts.sessionId, data });
  }

  private open() {
    this.ready = false;
    this.setState(this.everReady ? 'reconnecting' : 'connecting');
    const ws = this.factory(this.opts.url, [SUBPROTOCOL]);
    ws.binaryType = 'arraybuffer';
    this.ws = ws;
    ws.onopen = () => {
      // §5.1/§5.2 (v1.1): hello en ≤ 5 s, SIEMPRE con seq 0: no consume secuencia ni cuenta
      // para el dedupe. Con resume, la secuencia del cliente sigue desde su último seq.
      const data: ClientData<'hello'> = {
        client: this.opts.client ?? 'cx9-frontend/0.1.0',
        audio_formats: [BASELINE_AUDIO],
        tts_delivery: ['binary', 'url'],
        tts_formats: ['wav', 'pcm16le'],
        partials: true,
        resume: this.everReady ? { last_server_seq: this.lastServerSeq } : null,
      };
      ws.send(this.envelope('hello', 0, data));
      this.armIdle();
    };
    ws.onmessage = (ev) => this.onRaw(ev.data);
    ws.onerror = () => {
      /* el onclose posterior decide */
    };
    ws.onclose = (ev) => this.onClose(ev.code);
  }

  private onClose(code: number) {
    this.ws = null;
    this.ready = false;
    this.stopTimers();
    const fatal = this.lastFatal;
    this.lastFatal = null;
    if (this.stopped) return this.setState('closed', { closeCode: code });
    const terminal = !this.forceReconnect && (TERMINAL_CLOSES.has(code) || (fatal !== null && !fatal.retryable));
    this.forceReconnect = false;
    if (terminal) {
      const info: Omit<StateInfo, 'attempt'> = fatal ? { closeCode: code, errorCode: fatal.code } : { closeCode: code };
      return this.setState(code === CloseCode.NORMAL && !fatal ? 'closed' : 'failed', info);
    }
    const now = this.clock.now();
    this.disconnectedAt ??= now;
    if (now - this.disconnectedAt >= this.o.graceMs) return this.setState('failed', { closeCode: code });
    const delay = backoffDelay(this.attempt, this.o, this.opts.random);
    this.attempt += 1;
    this.setState('reconnecting', { nextDelayMs: delay, closeCode: code });
    this.timers.reconnect = this.clock.setTimeout(() => {
      this.timers.reconnect = null;
      if (!this.stopped) this.open();
    }, delay);
  }

  private armIdle() {
    if (this.timers.idle !== null) this.clock.clearTimeout(this.timers.idle);
    this.timers.idle = this.clock.setTimeout(() => {
      // §5.9: 45 s sin recibir nada → conexión muerta, se reconecta.
      this.forceReconnect = true;
      this.ws?.close(3000, 'idle');
      // Algunos entornos no emiten onclose tras close(): se fuerza.
      if (this.ws) this.onClose(3000);
    }, this.idleMs);
  }

  private stopTimers() {
    if (this.timers.heartbeat !== null) this.clock.clearInterval(this.timers.heartbeat);
    if (this.timers.idle !== null) this.clock.clearTimeout(this.timers.idle);
    this.timers.heartbeat = null;
    this.timers.idle = null;
    this.clearGap();
  }

  private onRaw(raw: unknown) {
    this.armIdle();
    if (raw instanceof ArrayBuffer) {
      let frame: ReturnType<typeof decodeFrame>;
      try {
        frame = decodeFrame(raw);
      } catch (e) {
        this.opts.onInvalid?.(e instanceof Error ? e.message : 'frame');
        return;
      }
      const { header, payload } = frame;
      this.inOrder(header.seq, false, () => {
        if (header.kind === BinKind.TTS_AUDIO) this.opts.onTtsFrame?.(header, payload);
        else this.opts.onInvalid?.(`binary kind ${header.kind}`);
      });
      return;
    }
    if (typeof raw !== 'string') return this.opts.onInvalid?.('non-string message');
    let v: unknown;
    try {
      v = JSON.parse(raw);
    } catch {
      return this.opts.onInvalid?.('invalid json');
    }
    if (!isEnvelope(v)) return this.opts.onInvalid?.('invalid envelope');
    if (!SERVER_TYPES.has(v.type)) return this.opts.onInvalid?.(`unknown type ${v.type}`); // §2.1: se ignora
    if (v.type === 'ack') {
      // §5.8 (v1.1): ack periódico con seq 0, fuera de la secuencia. Libera la ventana de audio.
      const d = v.data as { last_client_seq?: unknown };
      const n = typeof v.ack === 'number' ? v.ack : typeof d.last_client_seq === 'number' ? d.last_client_seq : null;
      if (v.seq !== 0 || n === null) return this.opts.onInvalid?.('invalid ack');
      this.onAck(n);
      return;
    }
    if (v.seq === 0) return this.opts.onInvalid?.('invalid envelope');
    // El ack del sobre se aplica al recibir (es monótono), aunque el mensaje quede retenido.
    if (typeof v.ack === 'number') this.onAck(v.ack);
    const m = v as unknown as ServerMessage;
    this.inOrder(v.seq, m.type === 'session.ready', () => this.dispatch(m));
  }

  private dispatch(m: ServerMessage) {
    switch (m.type) {
      case 'session.ready':
        this.onReady(m.data);
        break;
      case 'ping':
        this.sendJson('pong', { nonce: m.data.nonce });
        break;
      case 'error':
        if (m.data.fatal) this.lastFatal = m.data;
        break;
      default:
        break;
    }
    this.opts.onMessage(m);
  }

  /**
   * Orden y duplicados del servidor (§5.2). `lastServerSeq` es el último seq procesado de forma
   * CONTIGUA (lo que se manda en `resume.last_server_seq`). Un seq repetido se descarta. Un seq
   * adelantado se retiene hasta que llegue el que falta, salvo `session.ready`, que se procesa
   * enseguida: tras reconectar llega con un seq nuevo ANTES que los reenvíos de seq menores.
   */
  private inOrder(seq: number, immediate: boolean, run: () => void) {
    if (seq <= this.lastServerSeq || this.seenAhead.has(seq) || this.held.has(seq)) return; // duplicado
    if (seq === this.lastServerSeq + 1) {
      this.lastServerSeq = seq;
      run();
      this.drain();
      return;
    }
    if (immediate) {
      this.seenAhead.add(seq);
      run();
    } else {
      this.held.set(seq, run);
    }
    const pending = this.held.size + this.seenAhead.size;
    if (pending > MAX_HELD) this.skipGap();
    else if (pending > 0) this.armGap();
  }

  /** Procesa lo retenido que ya es contiguo. */
  private drain() {
    for (;;) {
      const next = this.lastServerSeq + 1;
      if (this.seenAhead.delete(next)) {
        this.lastServerSeq = next;
        continue;
      }
      const run = this.held.get(next);
      if (!run) break;
      this.held.delete(next);
      this.lastServerSeq = next;
      run();
    }
    if (this.held.size === 0 && this.seenAhead.size === 0) this.clearGap();
  }

  private armGap() {
    if (this.timers.gap !== null) return;
    this.timers.gap = this.clock.setTimeout(() => {
      this.timers.gap = null;
      this.skipGap();
    }, GAP_TIMEOUT_MS);
  }

  private clearGap() {
    if (this.timers.gap !== null) this.clock.clearTimeout(this.timers.gap);
    this.timers.gap = null;
  }

  /** Acepta un hueco que no se va a llenar (RESUME_GAP o timeout): procesa lo retenido en orden. */
  private skipGap() {
    if (this.held.size === 0 && this.seenAhead.size === 0) return;
    this.opts.onInvalid?.(`server seq gap after ${this.lastServerSeq}`);
    const all = [...new Set([...this.held.keys(), ...this.seenAhead])].sort((a, b) => a - b);
    const held = this.held;
    this.held = new Map();
    this.seenAhead.clear();
    this.clearGap();
    for (const seq of all) {
      this.lastServerSeq = seq;
      held.get(seq)?.();
    }
  }

  /** Último seq del servidor procesado de forma contigua (para tests y diagnóstico). */
  get lastContiguousServerSeq(): number {
    return this.lastServerSeq;
  }

  private onAck(ack: number) {
    if (ack <= this.ack) return;
    this.ack = ack;
    this.unacked = this.unacked.filter((x) => x.seq > ack);
    this.flush();
  }

  private onReady(d: Extract<ServerMessage, { type: 'session.ready' }>['data']) {
    // Sin resume (primera conexión o RESUME_GAP) no hay nada que reenviar: se acepta el hueco.
    // Con resume, los mensajes viejos llegan DESPUÉS de este session.ready y llenan el hueco.
    if (!d.resumed) this.skipGap();
    this.ready = true;
    this.everReady = true;
    this.attempt = 0;
    this.disconnectedAt = null;
    this.maxInflight = d.max_inflight_bytes;
    this.heartbeatMs = d.heartbeat_ms;
    this.idleMs = d.idle_timeout_ms;
    // §5.9: reenviar lo que el servidor no recibió (seq > last_client_seq).
    // Ojo: sin llamar a flush() acá; primero se reordena la cola (si no, se enviaría lo nuevo
    // antes que lo reenviado y además duplicado).
    const m = d.last_client_seq;
    if (m > this.ack) this.ack = m;
    const resend = this.unacked.filter((x) => x.seq > m);
    this.unacked = [];
    this.outbox = [...resend, ...this.outbox];
    if (this.timers.heartbeat !== null) this.clock.clearInterval(this.timers.heartbeat);
    this.timers.heartbeat = this.clock.setInterval(() => {
      this.nonce += 1;
      this.sendJson('ping', { nonce: `c${this.nonce}` });
    }, this.heartbeatMs);
    this.setState('open');
    this.flush();
  }

  private flush() {
    const ws = this.ws;
    if (!ws || ws.readyState !== OPEN || !this.ready) return;
    while (this.outbox.length > 0) {
      const item = this.outbox[0] as OutItem;
      if (item.audioBytes > 0) {
        // §5.8: ventana de bytes sin ack y bufferedAmount. No se descarta: se espera.
        const inflight = this.inflightAudioBytes;
        if (inflight > 0 && inflight + item.audioBytes > this.maxInflight) break;
        if (ws.bufferedAmount > this.maxInflight) break;
      }
      this.outbox.shift();
      ws.send(item.data);
      this.unacked.push(item);
    }
  }

  sendJson<T extends ClientMessage['type']>(type: T, data: ClientData<T>): number {
    const seq = this.nextSeq();
    this.outbox.push({ seq, data: this.envelope(type, seq, data), audioBytes: 0 });
    this.flush();
    return seq;
  }

  /** Encola un chunk PCM16LE 16 kHz (§5.4). Nunca se descarta: si no hay ventana o conexión, espera. */
  sendAudio(turnNo: number, chunkIndex: number, payload: Uint8Array, last: boolean): number {
    const seq = this.nextSeq();
    const frame = encodeFrame({ kind: BinKind.AUDIO_CHUNK, seq, turnNo, chunkIndex, codec: BinCodec.PCM16LE_16K, last }, payload);
    this.outbox.push({ seq, data: frame, audioBytes: payload.byteLength });
    this.flush();
    return seq;
  }

  /** Reintenta enviar lo pendiente (llamar si bajó `bufferedAmount`). */
  pump(): void {
    this.flush();
  }

  close(): void {
    this.stopped = true;
    if (this.timers.reconnect !== null) this.clock.clearTimeout(this.timers.reconnect);
    this.timers.reconnect = null;
    this.stopTimers();
    if (this.ws) this.ws.close(CloseCode.NORMAL, 'client');
    else this.setState('closed');
  }
}

function isEnvelope(v: unknown): v is { v: 1; type: string; seq: number; ack?: number; data: Record<string, unknown> } {
  if (typeof v !== 'object' || v === null) return false;
  const o = v as Record<string, unknown>;
  // seq 0 solo es válido en `ack` (se valida aparte).
  return o.v === 1 && typeof o.type === 'string' && typeof o.seq === 'number' && Number.isInteger(o.seq) && o.seq >= 0 &&
    typeof o.data === 'object' && o.data !== null;
}
