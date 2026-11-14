/**
 * VAD (detección de voz) por energía RMS con piso de ruido adaptativo e histéresis temporal.
 *
 * - Cada cuadro (frame) de PCM float32 en [-1, 1] se reduce a su RMS.
 * - El umbral es max(minRms, piso_de_ruido * ratio). El piso se actualiza con una media
 *   exponencial SOLO en cuadros sin voz, así la voz no lo "arrastra" hacia arriba.
 * - Arranque: hacen falta `minSpeechMs` de cuadros sobre el umbral seguidos.
 * - Corte (hangover): la voz termina después de `hangoverMs` seguidos bajo el umbral,
 *   para no cortar en las pausas naturales entre frases.
 */

export interface VadOptions {
  sampleRate: number;
  minRms: number;
  ratio: number;
  minSpeechMs: number;
  hangoverMs: number;
  /** Peso de la media exponencial del piso de ruido (0..1). */
  noiseAlpha: number;
  initialNoiseRms: number;
}

export const DEFAULT_VAD: Omit<VadOptions, 'sampleRate'> = {
  minRms: 0.01,
  ratio: 3,
  minSpeechMs: 120,
  hangoverMs: 1500,
  noiseAlpha: 0.05,
  initialNoiseRms: 0.003,
};

export type VadEvent = 'speech_start' | 'speech_end';

export function rms(frame: ArrayLike<number>): number {
  if (frame.length === 0) return 0;
  let acc = 0;
  for (let i = 0; i < frame.length; i++) {
    const s = frame[i] ?? 0;
    acc += s * s;
  }
  return Math.sqrt(acc / frame.length);
}

export class EnergyVad {
  private readonly o: VadOptions;
  private noise: number;
  private speaking = false;
  private aboveMs = 0;
  private belowMs = 0;

  constructor(opts: Partial<VadOptions> & { sampleRate: number }) {
    this.o = { ...DEFAULT_VAD, ...opts };
    if (this.o.sampleRate <= 0) throw new RangeError('sampleRate must be > 0');
    this.noise = this.o.initialNoiseRms;
  }

  get isSpeaking(): boolean {
    return this.speaking;
  }

  get threshold(): number {
    return Math.max(this.o.minRms, this.noise * this.o.ratio);
  }

  get noiseFloor(): number {
    return this.noise;
  }

  reset(): void {
    this.speaking = false;
    this.aboveMs = 0;
    this.belowMs = 0;
  }

  /**
   * Procesa un cuadro; devuelve un evento cuando cambia el estado.
   * `durationMs` permite indicar el tiempo real entre cuadros (p. ej. si se muestrea un AnalyserNode).
   */
  process(frame: ArrayLike<number>, durationMs?: number): VadEvent | null {
    const ms = durationMs ?? (frame.length / this.o.sampleRate) * 1000;
    const level = rms(frame);
    const voiced = level >= this.threshold;
    if (!voiced && !this.speaking) {
      this.noise = this.noise * (1 - this.o.noiseAlpha) + level * this.o.noiseAlpha;
    }
    if (voiced) {
      this.aboveMs += ms;
      this.belowMs = 0;
      if (!this.speaking && this.aboveMs >= this.o.minSpeechMs) {
        this.speaking = true;
        return 'speech_start';
      }
    } else {
      this.belowMs += ms;
      if (!this.speaking) this.aboveMs = 0;
      if (this.speaking && this.belowMs >= this.o.hangoverMs) {
        this.speaking = false;
        this.aboveMs = 0;
        return 'speech_end';
      }
    }
    return null;
  }
}
