/** Generadores de PCM sintético para tests (deterministas). */

export function sine(freq: number, amp: number, ms: number, sampleRate: number): Float32Array {
  const n = Math.round((ms / 1000) * sampleRate);
  const out = new Float32Array(n);
  for (let i = 0; i < n; i++) out[i] = amp * Math.sin((2 * Math.PI * freq * i) / sampleRate);
  return out;
}

/** Ruido uniforme con LCG de semilla fija. */
export function noise(amp: number, ms: number, sampleRate: number, seed = 1): Float32Array {
  const n = Math.round((ms / 1000) * sampleRate);
  const out = new Float32Array(n);
  let s = seed >>> 0;
  for (let i = 0; i < n; i++) {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
    out[i] = amp * ((s / 0xffffffff) * 2 - 1);
  }
  return out;
}

export function concat(...parts: Float32Array[]): Float32Array {
  const out = new Float32Array(parts.reduce((a, p) => a + p.length, 0));
  let o = 0;
  for (const p of parts) {
    out.set(p, o);
    o += p.length;
  }
  return out;
}

export function* frames(signal: Float32Array, frameSize: number): Generator<Float32Array> {
  for (let i = 0; i + frameSize <= signal.length; i += frameSize) yield signal.subarray(i, i + frameSize);
}
