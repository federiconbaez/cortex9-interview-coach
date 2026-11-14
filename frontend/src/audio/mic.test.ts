import { afterEach, describe, expect, it, vi } from 'vitest';
import { MicCapture } from './mic';
import { concat, sine } from './synth';

class FakeNode {
  static last: FakeNode | null = null;
  port: { onmessage: ((ev: { data: Float32Array }) => void) | null } = { onmessage: null };
  disconnect = vi.fn();
  constructor(
    readonly ctx: unknown,
    readonly name: string,
  ) {
    FakeNode.last = this;
  }
}

function install(rate: number, opts: { rejectRate?: boolean } = {}) {
  const track = { stop: vi.fn() };
  const modules: string[] = [];
  const ctxs: { sampleRate: number; close: () => Promise<void> }[] = [];
  class FakeCtx {
    sampleRate: number;
    audioWorklet = { addModule: vi.fn(async (u: string) => void modules.push(u)) };
    constructor(o?: { sampleRate?: number }) {
      if (o?.sampleRate && opts.rejectRate) throw new Error('NotSupportedError');
      this.sampleRate = o?.sampleRate ?? rate;
      ctxs.push(this);
    }
    createMediaStreamSource = () => ({ connect: vi.fn() });
    resume = vi.fn(async () => undefined);
    close = vi.fn(async () => undefined);
  }
  const getUserMedia = vi.fn(async () => ({ getTracks: () => [track] }));
  vi.stubGlobal('navigator', { mediaDevices: { getUserMedia } });
  vi.stubGlobal('AudioContext', FakeCtx);
  vi.stubGlobal('AudioWorkletNode', FakeNode);
  return { track, modules, ctxs, getUserMedia };
}

/** Envía audio al worklet falso en bloques de 128 muestras, como el render quantum real. */
function feed(samples: Float32Array) {
  for (let i = 0; i < samples.length; i += 128) FakeNode.last?.port.onmessage?.({ data: samples.subarray(i, i + 128) });
}

afterEach(() => vi.unstubAllGlobals());

describe('MicCapture', () => {
  it('48 kHz → PCM16LE 16 kHz en chunks de 3200 bytes; el último lleva last=true', async () => {
    const env = install(48000, { rejectRate: true });
    const chunks: { bytes: number; last: boolean }[] = [];
    const levels: number[] = [];
    const m = new MicCapture({ onChunk: (p, last) => chunks.push({ bytes: p.byteLength, last }), onLevel: (l) => levels.push(l) });
    expect(() => m.startAnswer(false)).toThrow('mic not open');
    await m.open();
    expect(env.ctxs[0]?.sampleRate).toBe(48000);
    expect(env.modules).toEqual(['/capture-worklet.js']);
    expect(env.getUserMedia).toHaveBeenCalledWith({ audio: { echoCancellation: true, noiseSuppression: true, channelCount: 1 } });
    feed(sine(440, 0.3, 500, 48000)); // antes de empezar: se ignora
    m.startAnswer(false);
    feed(sine(440, 0.3, 350, 48000)); // 350 ms → 3 chunks completos + 50 ms de resto
    const stats = m.endAnswer();
    expect(chunks.map((c) => c.bytes)).toEqual([3200, 3200, 3200, 1600]);
    expect(chunks.map((c) => c.last)).toEqual([false, false, false, true]);
    expect(stats).toEqual({ chunks: 4, bytes: 11200, durationMs: 350 });
    expect(levels.at(-1)).toBe(0);
    expect(m.endAnswer()).toEqual(stats); // idempotente
    await m.close();
    expect(env.track.stop).toHaveBeenCalled();
    expect(m.active).toBe(false);
  });

  it('con VAD emite speech_start y speech_end tras el silencio (hangover)', async () => {
    install(16000);
    const events: string[] = [];
    const m = new MicCapture({ onChunk: () => undefined, onVad: (e) => events.push(e) }, { hangoverMs: 300 });
    await m.open();
    m.startAnswer(true);
    feed(concat(sine(0, 0, 300, 16000), sine(300, 0.3, 600, 16000), sine(0, 0, 600, 16000)));
    expect(events).toEqual(['speech_start', 'speech_end']);
  });

  it('cancel descarta el turno y sin soporte de AudioWorklet falla con NotSupportedError', async () => {
    install(16000);
    const chunks: number[] = [];
    const m = new MicCapture({ onChunk: (p) => chunks.push(p.byteLength) });
    await m.open();
    await m.open(); // idempotente
    m.startAnswer(false);
    feed(sine(440, 0.3, 250, 16000));
    m.cancel();
    expect(m.endAnswer()).toEqual({ chunks: 1, bytes: 3200, durationMs: 100 });
    expect(chunks).toEqual([3200]);
    vi.stubGlobal('AudioWorkletNode', undefined);
    await expect(new MicCapture({ onChunk: () => undefined }).open()).rejects.toMatchObject({ name: 'NotSupportedError' });
  });
});
