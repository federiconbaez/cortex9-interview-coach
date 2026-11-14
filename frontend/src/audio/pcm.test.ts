import { describe, expect, it } from 'vitest';
import { CHUNK_SAMPLES, Chunker, Downsampler, floatToPcm16le, wavFromPcm16 } from './pcm';
import { sine } from './synth';

describe('Downsampler', () => {
  it('48 kHz → 16 kHz en bloques de 128 conserva la cantidad exacta de muestras', () => {
    const d = new Downsampler(48000);
    let out = 0;
    for (let i = 0; i < 375; i++) out += d.process(new Float32Array(128).fill(0.25)).length; // 48000 muestras = 1 s
    expect(out).toBe(16000);
  });

  it('44,1 kHz → 16 kHz: error ≤ 1 muestra en 1 s', () => {
    const d = new Downsampler(44100);
    let out = 0;
    const block = new Float32Array(441);
    for (let i = 0; i < 100; i++) out += d.process(block).length;
    expect(Math.abs(out - 16000)).toBeLessThanOrEqual(1);
  });

  it('preserva el nivel de continua y la amplitud de un tono bajo', () => {
    const d = new Downsampler(48000);
    const dc = d.process(new Float32Array(4800).fill(0.5));
    expect(dc.every((v) => Math.abs(v - 0.5) < 1e-6)).toBe(true);
    const tone = new Downsampler(48000).process(sine(200, 0.8, 1000, 48000));
    expect(Math.max(...tone)).toBeGreaterThan(0.79);
  });

  it('16 kHz → 16 kHz es identidad y rechaza subir la frecuencia', () => {
    const x = Float32Array.from([0.1, -0.2, 0.3]);
    expect([...new Downsampler(16000).process(x)]).toEqual([...x]);
    expect(() => new Downsampler(8000)).toThrow(RangeError);
  });
});

describe('floatToPcm16le', () => {
  it('convierte con saturación y en little-endian', () => {
    const b = floatToPcm16le(Float32Array.from([0, 1, -1, 2, -2, 0.5]));
    const dv = new DataView(b.buffer);
    expect([0, 1, 2, 3, 4, 5].map((i) => dv.getInt16(i * 2, true))).toEqual([0, 32767, -32768, 32767, -32768, 16384]);
    expect(b[2]).toBe(0xff);
    expect(b[3]).toBe(0x7f);
  });
});

describe('Chunker', () => {
  it('entrega chunks de 100 ms (1600 muestras = 3200 bytes) y el resto al vaciar', () => {
    const c = new Chunker();
    const got: number[] = [];
    for (let i = 0; i < 30; i++) c.push(new Float32Array(128), (x) => got.push(x.length));
    expect(got).toEqual([CHUNK_SAMPLES, CHUNK_SAMPLES]); // 3840 muestras → 2 chunks + 640
    expect(c.drain().length).toBe(3840 - 2 * CHUNK_SAMPLES);
    expect(c.drain().length).toBe(0);
    expect(floatToPcm16le(new Float32Array(CHUNK_SAMPLES)).byteLength).toBe(3200);
  });
});

describe('wavFromPcm16', () => {
  it('arma un header RIFF/WAVE PCM mono válido', () => {
    const w = wavFromPcm16(new Uint8Array(10), 22050);
    const dv = new DataView(w.buffer);
    const str = (o: number) => String.fromCharCode(...w.slice(o, o + 4));
    expect([str(0), str(8), str(12), str(36)]).toEqual(['RIFF', 'WAVE', 'fmt ', 'data']);
    expect(dv.getUint32(4, true)).toBe(46);
    expect(dv.getUint16(22, true)).toBe(1);
    expect(dv.getUint32(24, true)).toBe(22050);
    expect(dv.getUint32(40, true)).toBe(10);
    expect(w.byteLength).toBe(54);
  });
});
