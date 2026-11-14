import { beforeEach, describe, expect, it } from 'vitest';
import { FakeClock, FakeWs } from '../test/fakes';
import { backoffMs, CaptionsConnection, captionsUrl, type CapConnState } from './connection';
import { msgs } from '../test/captions';
import type { ServerMsg } from './protocol';

function setup(opts: { idleMs?: number } = {}) {
  FakeWs.all = [];
  const clock = new FakeClock();
  const got: ServerMsg[] = [];
  const states: [CapConnState, number | null, number | null][] = [];
  const logs: string[] = [];
  const c = new CaptionsConnection({
    url: 'ws://h/captions/v1/live',
    createWs: (u, p) => new FakeWs(u, p),
    clock,
    onMessage: (m) => got.push(m),
    onState: (s, i) => states.push([s, i.delayMs, i.code]),
    log: (l) => logs.push(l),
    ...opts,
  });
  const ws = () => {
    const w = FakeWs.all[FakeWs.all.length - 1];
    if (!w) throw new Error('sin socket');
    return w;
  };
  return { c, clock, got, states, logs, ws };
}

describe('CaptionsConnection', () => {
  beforeEach(() => {
    FakeWs.all = [];
  });

  it('usa el subprotocolo cx9.captions.v1 y la ruta same-origin', () => {
    const { c, ws } = setup();
    c.connect();
    expect(ws().protocols).toEqual(['cx9.captions.v1']);
    expect(captionsUrl({ protocol: 'https:', host: 'a:1' })).toBe('wss://a:1/captions/v1/live');
    expect(captionsUrl({ protocol: 'http:', host: 'b' })).toBe('ws://b/captions/v1/live');
  });

  it('seq del servidor desde 1: descarta duplicados, avisa huecos, ignora inválidos', () => {
    const { c, ws, got, logs } = setup();
    const m = msgs();
    c.connect();
    ws().open();
    const h = m.hello();
    ws().recv(h);
    ws().recv(h);
    ws().recv('basura');
    m.caps();
    ws().recv(m.devices()); // seq 3, falta el 2
    expect(got.map((x) => x.type)).toEqual(['hello', 'devices']);
    expect(logs.some((l) => l.includes('hueco'))).toBe(true);
    expect(logs.some((l) => l.includes('inválido'))).toBe(true);
  });

  it('mensajes del cliente con seq desde 1 por conexión (supuesto) y ping periódico', () => {
    const { c, ws, clock } = setup({ idleMs: 60_000 });
    expect(c.send({ type: 'pause' })).toBe(false); // sin conexión no se manda
    c.connect();
    ws().open();
    c.send({ type: 'pause' });
    c.send({ type: 'resume' });
    clock.advance(15_000);
    ws().recv(msgs().hello());
    expect(ws().json()).toEqual([
      { type: 'pause', seq: 1 },
      { type: 'resume', seq: 2 },
      { type: 'ping', seq: 3 },
    ]);
  });

  it('reconecta con backoff exponencial 0,5 → 10 s y reinicia el seq; el hello resetea el backoff', () => {
    expect([1, 2, 3, 4, 5, 6, 9].map(backoffMs)).toEqual([500, 1000, 2000, 4000, 8000, 10_000, 10_000]);
    const { c, ws, clock, states, got } = setup();
    c.connect();
    ws().open();
    ws().recv(msgs().hello());
    c.send({ type: 'ping' });
    ws().drop(1011);
    expect(states.at(-1)).toEqual(['reconnecting', 500, 1011]);
    clock.advance(500);
    expect(FakeWs.all).toHaveLength(2);
    ws().drop(1006);
    expect(states.at(-1)).toEqual(['reconnecting', 1000, 1006]);
    clock.advance(1000);
    ws().open();
    ws().recv(msgs().hello()); // seq 1 de nuevo: conexión nueva, sin replay
    expect(got).toHaveLength(2);
    c.send({ type: 'pause' });
    expect(ws().json()).toEqual([{ type: 'pause', seq: 1 }]);
    ws().drop(1001);
    expect(states.at(-1)).toEqual(['reconnecting', 500, 1001]);
  });

  it('4409: mensaje claro y sin reintento automático; reintento manual', () => {
    const { c, ws, clock, states } = setup();
    c.connect();
    ws().drop(4409);
    expect(states.at(-1)?.[0]).toBe('busy');
    clock.advance(60_000);
    expect(FakeWs.all).toHaveLength(1);
    c.retryNow();
    expect(FakeWs.all).toHaveLength(2);
    expect(states.at(-1)?.[0]).toBe('connecting');
  });

  it('1008 no se reintenta; 1000 del servidor termina (supuesto)', () => {
    const a = setup();
    a.c.connect();
    a.ws().drop(1008);
    a.clock.advance(60_000);
    expect([a.states.at(-1)?.[0], FakeWs.all.length]).toEqual(['forbidden', 1]);
    const b = setup();
    b.c.connect();
    b.ws().drop(1000);
    b.clock.advance(60_000);
    expect([b.states.at(-1)?.[0], FakeWs.all.length]).toEqual(['ended', 1]);
  });

  it('sin mensajes durante idleMs cierra y reconecta (supuesto)', () => {
    const { c, ws, clock, states } = setup({ idleMs: 10_000 });
    c.connect();
    ws().open();
    clock.advance(9_000);
    ws().recv(msgs().hello());
    clock.advance(9_000);
    expect(FakeWs.all).toHaveLength(1);
    clock.advance(1_000);
    expect(FakeWs.all[0]?.closed?.code).toBe(4000);
    expect(states.at(-1)).toEqual(['reconnecting', 500, null]);
    clock.advance(500);
    expect(FakeWs.all).toHaveLength(2);
  });

  it('close() del usuario no reconecta', () => {
    const { c, ws, clock, states } = setup();
    c.connect();
    ws().open();
    c.close();
    expect(FakeWs.all[0]?.closed?.code).toBe(1000);
    clock.advance(60_000);
    expect(FakeWs.all).toHaveLength(1);
    expect(states.at(-1)?.[0]).toBe('closed');
    expect(c.current).toBe('closed');
  });

  it('si el constructor del WebSocket falla, reintenta', () => {
    const clock = new FakeClock();
    let n = 0;
    const states: CapConnState[] = [];
    const c = new CaptionsConnection({
      url: 'ws://x',
      clock,
      createWs: (u, p) => {
        n += 1;
        if (n === 1) throw new Error('x');
        return new FakeWs(u, p);
      },
      onMessage: () => undefined,
      onState: (s) => states.push(s),
    });
    c.connect();
    clock.advance(500);
    expect(n).toBe(2);
    expect(states).toEqual(['connecting', 'reconnecting', 'reconnecting']);
  });
});
