// Cliente WS de subtítulos. Separado a propósito del cliente del motor de entrevistas
// (src/api/ws.ts): otro servicio, otra ruta, sin estado compartido ni resume.
import { CloseCodes, CAPTIONS_PATH, CAPTIONS_SUBPROTOCOL, parseServerMsg, type ClientMsg, type ServerMsg } from './protocol';

export interface CapWs {
  readyState: number;
  onopen: ((ev: Event) => void) | null;
  onclose: ((ev: { code: number; reason: string }) => void) | null;
  onerror: ((ev: Event) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  send(data: string): void;
  close(code?: number, reason?: string): void;
}
export interface CapClock {
  now(): number;
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(h: unknown): void;
  setInterval(fn: () => void, ms: number): unknown;
  clearInterval(h: unknown): void;
}
const realClock: CapClock = {
  now: () => Date.now(),
  setTimeout: (fn, ms) => globalThis.setTimeout(fn, ms),
  clearTimeout: (h) => globalThis.clearTimeout(h as number),
  setInterval: (fn, ms) => globalThis.setInterval(fn, ms),
  clearInterval: (h) => globalThis.clearInterval(h as number),
};

/**
 * - `busy`: cierre 4409 (ya hay otro cliente). No se reintenta solo; el usuario puede reintentar.
 * - `forbidden`: cierre 1008 (origen no permitido). No se reintenta.
 * - `ended`: cierre 1000 del servidor. SUPUESTO: no se reintenta solo (el contrato solo dice "normal").
 */
export type CapConnState = 'idle' | 'connecting' | 'open' | 'reconnecting' | 'busy' | 'forbidden' | 'ended' | 'closed';
export interface CapConnInfo {
  attempt: number;
  delayMs: number | null;
  code: number | null;
}

export interface CaptionsOptions {
  url: string;
  onMessage: (m: ServerMsg) => void;
  onState: (s: CapConnState, info: CapConnInfo) => void;
  createWs?: (url: string, protocols: string[]) => CapWs;
  clock?: CapClock;
  /** SUPUESTO: el contrato no fija un intervalo de ping; 15 s. */
  pingMs?: number;
  /** SUPUESTO: `status` llega ~1/s; sin ningún mensaje en 10 s se da la conexión por perdida. */
  idleMs?: number;
  log?: (msg: string) => void;
}

/** Backoff exponencial 0,5 → 10 s (contrato v0.2). */
export const backoffMs = (attempt: number) => Math.min(10_000, 500 * 2 ** Math.max(0, attempt - 1));

/** URL same-origin del servicio: ws(s)://<host>/captions/v1/live. */
export function captionsUrl(loc: { protocol: string; host: string } = globalThis.location): string {
  return `${loc.protocol === 'https:' ? 'wss:' : 'ws:'}//${loc.host}${CAPTIONS_PATH}`;
}

const IDLE_CLOSE = 4000;

export class CaptionsConnection {
  private ws: CapWs | null = null;
  private clock: CapClock;
  private attempt = 0;
  private outSeq = 0;
  private inSeq = 0;
  private stopped = false;
  private retryTimer: unknown = null;
  private pingTimer: unknown = null;
  private idleTimer: unknown = null;
  private state: CapConnState = 'idle';

  constructor(private readonly o: CaptionsOptions) {
    this.clock = o.clock ?? realClock;
  }

  get current(): CapConnState {
    return this.state;
  }

  connect() {
    this.stopped = false;
    this.open();
  }

  /** Reintento manual (después de un 4409 o un 1000). */
  retryNow() {
    this.clearRetry();
    this.attempt = 0;
    this.connect();
  }

  /**
   * SUPUESTO (a confirmar con CONTRACT.md): los mensajes del cliente también llevan `seq`,
   * desde 1 por conexión, porque el resumen dice "todos los mensajes llevan type y seq".
   */
  send(m: ClientMsg): boolean {
    if (!this.ws || this.ws.readyState !== 1) return false;
    this.outSeq += 1;
    this.ws.send(JSON.stringify({ ...m, seq: this.outSeq }));
    return true;
  }

  close() {
    this.stopped = true;
    this.clearRetry();
    this.stopTimers();
    const ws = this.ws;
    this.ws = null;
    if (ws) {
      ws.onclose = null;
      ws.close(1000, 'client');
    }
    this.setState('closed', null);
  }

  private setState(s: CapConnState, code: number | null, delayMs: number | null = null) {
    this.state = s;
    this.o.onState(s, { attempt: this.attempt, delayMs, code });
  }

  private open() {
    const make = this.o.createWs ?? ((u, p) => new WebSocket(u, p) as unknown as CapWs);
    this.outSeq = 0;
    this.inSeq = 0;
    this.setState(this.attempt === 0 ? 'connecting' : 'reconnecting', null);
    let ws: CapWs;
    try {
      ws = make(this.o.url, [CAPTIONS_SUBPROTOCOL]);
    } catch {
      this.schedule(null);
      return;
    }
    this.ws = ws;
    ws.onopen = () => {
      if (this.ws !== ws) return;
      this.setState('open', null);
      this.armIdle();
      this.pingTimer = this.clock.setInterval(() => this.send({ type: 'ping' }), this.o.pingMs ?? 15_000);
    };
    ws.onmessage = (ev) => {
      if (this.ws !== ws) return;
      this.armIdle();
      const m = parseServerMsg(ev.data);
      if (!m) {
        this.o.log?.('captions: mensaje inválido descartado');
        return;
      }
      if (m.seq <= this.inSeq) return; // duplicado o viejo
      if (m.seq !== this.inSeq + 1) this.o.log?.(`captions: hueco de seq (${this.inSeq + 1}..${m.seq - 1})`);
      this.inSeq = m.seq;
      if (m.type === 'hello') this.attempt = 0; // conexión sana: el próximo corte empieza el backoff de cero
      this.o.onMessage(m);
    };
    ws.onerror = () => undefined;
    ws.onclose = (ev) => {
      if (this.ws !== ws) return;
      this.ws = null;
      this.stopTimers();
      if (this.stopped) return;
      if (ev.code === CloseCodes.ORIGIN) return this.setState('forbidden', ev.code);
      if (ev.code === CloseCodes.BUSY) return this.setState('busy', ev.code);
      if (ev.code === CloseCodes.NORMAL) return this.setState('ended', ev.code);
      this.schedule(ev.code);
    };
  }

  private schedule(code: number | null) {
    this.attempt += 1;
    const delay = backoffMs(this.attempt);
    this.setState('reconnecting', code, delay);
    this.retryTimer = this.clock.setTimeout(() => {
      this.retryTimer = null;
      if (!this.stopped) this.open();
    }, delay);
  }

  private armIdle() {
    if (this.idleTimer !== null) this.clock.clearTimeout(this.idleTimer);
    this.idleTimer = this.clock.setTimeout(() => {
      this.idleTimer = null;
      const ws = this.ws;
      if (!ws) return;
      this.o.log?.('captions: sin mensajes, se reconecta');
      ws.onclose = null;
      this.ws = null;
      this.stopTimers();
      ws.close(IDLE_CLOSE, 'idle');
      this.schedule(null);
    }, this.o.idleMs ?? 10_000);
  }

  private stopTimers() {
    if (this.pingTimer !== null) this.clock.clearInterval(this.pingTimer);
    if (this.idleTimer !== null) this.clock.clearTimeout(this.idleTimer);
    this.pingTimer = null;
    this.idleTimer = null;
  }

  private clearRetry() {
    if (this.retryTimer !== null) this.clock.clearTimeout(this.retryTimer);
    this.retryTimer = null;
  }
}
