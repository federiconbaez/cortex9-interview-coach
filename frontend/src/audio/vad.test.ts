import { describe, expect, it } from 'vitest';
import { concat, frames, noise, sine } from './synth';
import { EnergyVad, rms } from './vad';

const SR = 16000;
const FRAME = 320; // 20 ms

function run(vad: EnergyVad, signal: Float32Array) {
  const events: { e: string; ms: number }[] = [];
  let ms = 0;
  for (const f of frames(signal, FRAME)) {
    ms += 20;
    const e = vad.process(f);
    if (e) events.push({ e, ms });
  }
  return events;
}

describe('rms', () => {
  it('es 0 para silencio y A/√2 para una senoidal', () => {
    expect(rms(new Float32Array(100))).toBe(0);
    expect(rms([])).toBe(0);
    expect(rms(sine(440, 0.5, 1000, SR))).toBeCloseTo(0.5 / Math.SQRT2, 3);
  });
});

describe('EnergyVad', () => {
  it('no dispara con silencio digital', () => {
    expect(run(new EnergyVad({ sampleRate: SR }), new Float32Array(SR * 2))).toEqual([]);
  });

  it('no dispara con ruido de fondo bajo el umbral mínimo', () => {
    // ruido uniforme amp 0.01 → RMS ≈ 0.0058 < minRms 0.01
    expect(run(new EnergyVad({ sampleRate: SR }), noise(0.01, 3000, SR))).toEqual([]);
  });

  it('detecta inicio tras minSpeechMs y fin tras el hangover', () => {
    const vad = new EnergyVad({ sampleRate: SR, minSpeechMs: 100, hangoverMs: 600 });
    const sig = concat(new Float32Array(SR / 2), sine(220, 0.3, 1000, SR), new Float32Array(SR));
    const ev = run(vad, sig);
    expect(ev).toEqual([
      { e: 'speech_start', ms: 600 }, // 500 ms de silencio + 5 cuadros de 20 ms
      { e: 'speech_end', ms: 2100 }, // 1500 ms (fin de voz) + 600 ms de hangover
    ]);
    expect(vad.isSpeaking).toBe(false);
  });

  it('no corta en pausas más cortas que el hangover', () => {
    const vad = new EnergyVad({ sampleRate: SR, minSpeechMs: 100, hangoverMs: 800 });
    const voice = sine(200, 0.3, 700, SR);
    const sig = concat(voice, new Float32Array((SR * 500) / 1000), voice, new Float32Array(SR * 2));
    const ev = run(vad, sig);
    expect(ev.map((x) => x.e)).toEqual(['speech_start', 'speech_end']);
    expect(ev[1]?.ms).toBe(1900 + 800);
  });

  it('ignora clics más cortos que minSpeechMs', () => {
    const vad = new EnergyVad({ sampleRate: SR, minSpeechMs: 120 });
    const sig = concat(new Float32Array(SR / 4), sine(1000, 0.8, 60, SR), new Float32Array(SR));
    expect(run(vad, sig)).toEqual([]);
  });

  it('adapta el piso de ruido: con ruido fuerte sube el umbral', () => {
    const vad = new EnergyVad({ sampleRate: SR, minRms: 0.002, ratio: 3, minSpeechMs: 200 });
    const before = vad.threshold;
    run(vad, noise(0.006, 2000, SR)); // RMS ≈ 0.0035; el piso converge hacia ahí
    expect(vad.noiseFloor).toBeGreaterThan(0.003);
    expect(vad.threshold).toBeGreaterThan(before);
  });

  it('acepta la duración real del cuadro (muestreo de AnalyserNode)', () => {
    const vad = new EnergyVad({ sampleRate: SR, minSpeechMs: 100, hangoverMs: 200 });
    const loud = sine(300, 0.5, 20, SR);
    expect(vad.process(loud, 50)).toBeNull();
    expect(vad.process(loud, 50)).toBe('speech_start');
    expect(vad.process(new Float32Array(320), 250)).toBe('speech_end');
  });

  it('reset vuelve al estado inicial y valida sampleRate', () => {
    const vad = new EnergyVad({ sampleRate: SR, minSpeechMs: 20 });
    vad.process(sine(300, 0.5, 20, SR));
    expect(vad.isSpeaking).toBe(true);
    vad.reset();
    expect(vad.isSpeaking).toBe(false);
    expect(() => new EnergyVad({ sampleRate: 0 })).toThrow(RangeError);
  });
});
