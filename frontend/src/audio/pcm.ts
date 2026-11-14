/** Utilidades PCM: re-muestreo a 16 kHz, float32 → PCM16LE y armado de chunks de 100 ms (§5.3). */

export const TARGET_RATE = 16000;
/** 100 ms a 16 kHz = 1600 muestras = 3200 bytes (recomendado por el contrato). */
export const CHUNK_SAMPLES = 1600;

/**
 * Re-muestreo por promedio de ventana (filtro anti-alias simple tipo "box").
 * Mantiene la fase fraccional entre llamadas para que la concatenación sea continua.
 */
export class Downsampler {
  /** Muestras de entrada que quedaron sin consumir (ventana partida). */
  private carry: Float32Array = new Float32Array(0);
  /** Posición fraccional de la próxima ventana dentro de `carry + input`. */
  private pos = 0;
  private readonly ratio: number;

  constructor(
    readonly inputRate: number,
    readonly outputRate = TARGET_RATE,
  ) {
    if (inputRate < outputRate) throw new RangeError('inputRate must be >= outputRate');
    this.ratio = inputRate / outputRate;
  }

  process(input: Float32Array): Float32Array {
    if (this.ratio === 1) return input.slice();
    const buf = new Float32Array(this.carry.length + input.length);
    buf.set(this.carry);
    buf.set(input, this.carry.length);
    const out: number[] = [];
    let pos = this.pos;
    while (pos + this.ratio <= buf.length) {
      const start = Math.floor(pos);
      const end = Math.floor(pos + this.ratio);
      let acc = 0;
      for (let i = start; i < end; i++) acc += buf[i] ?? 0;
      out.push(acc / Math.max(1, end - start));
      pos += this.ratio;
    }
    const keep = Math.floor(pos);
    this.carry = buf.slice(keep);
    this.pos = pos - keep;
    return Float32Array.from(out);
  }
}

export function floatToPcm16le(f: Float32Array): Uint8Array {
  const out = new Uint8Array(f.length * 2);
  const dv = new DataView(out.buffer);
  for (let i = 0; i < f.length; i++) {
    const s = Math.max(-1, Math.min(1, f[i] ?? 0));
    dv.setInt16(i * 2, s < 0 ? Math.round(s * 0x8000) : Math.round(s * 0x7fff), true);
  }
  return out;
}

/** Acumula muestras y entrega chunks de tamaño fijo. */
export class Chunker {
  private buf: Float32Array;
  private n = 0;

  constructor(private readonly size = CHUNK_SAMPLES) {
    this.buf = new Float32Array(size);
  }

  push(samples: Float32Array, emit: (chunk: Float32Array) => void): void {
    let i = 0;
    while (i < samples.length) {
      const take = Math.min(this.size - this.n, samples.length - i);
      this.buf.set(samples.subarray(i, i + take), this.n);
      this.n += take;
      i += take;
      if (this.n === this.size) {
        emit(this.buf.slice());
        this.n = 0;
      }
    }
  }

  /** Devuelve lo que quedó (chunk final más corto) y vacía el buffer. */
  drain(): Float32Array {
    const rest = this.buf.slice(0, this.n);
    this.n = 0;
    return rest;
  }
}

/** Header WAV PCM16 mono para reproducir `tts_format: pcm16le`. */
export function wavFromPcm16(pcm: Uint8Array, sampleRate: number): Uint8Array {
  const out = new Uint8Array(44 + pcm.byteLength);
  const dv = new DataView(out.buffer);
  const w = (o: number, s: string) => [...s].forEach((c, i) => dv.setUint8(o + i, c.charCodeAt(0)));
  w(0, 'RIFF');
  dv.setUint32(4, 36 + pcm.byteLength, true);
  w(8, 'WAVE');
  w(12, 'fmt ');
  dv.setUint32(16, 16, true);
  dv.setUint16(20, 1, true);
  dv.setUint16(22, 1, true);
  dv.setUint32(24, sampleRate, true);
  dv.setUint32(28, sampleRate * 2, true);
  dv.setUint16(32, 2, true);
  dv.setUint16(34, 16, true);
  w(36, 'data');
  dv.setUint32(40, pcm.byteLength, true);
  out.set(pcm, 44);
  return out;
}
