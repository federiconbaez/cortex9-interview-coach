/**
 * Captura de micrófono con AudioWorklet → PCM16LE 16 kHz mono en chunks de 100 ms (§5.3),
 * con VAD por energía sobre los mismos chunks. Solo se usa durante el turno del usuario:
 * la app nunca escucha a un entrevistador real.
 */
import { Chunker, Downsampler, TARGET_RATE, floatToPcm16le } from './pcm';
import { EnergyVad, rms, type VadEvent, type VadOptions } from './vad';

export interface MicCallbacks {
  /** Chunk PCM16LE listo para mandar; `last` marca el último del turno. */
  onChunk: (pcm: Uint8Array, last: boolean) => void;
  onLevel?: (rms: number) => void;
  onVad?: (e: VadEvent) => void;
}

export interface MicStats {
  chunks: number;
  bytes: number;
  durationMs: number;
}

export class MicCapture {
  private stream: MediaStream | null = null;
  private ctx: AudioContext | null = null;
  private node: AudioWorkletNode | null = null;
  private down: Downsampler | null = null;
  private chunker = new Chunker();
  private vad: EnergyVad = new EnergyVad({ sampleRate: TARGET_RATE });
  private recording = false;
  private useVad = false;
  private stats: MicStats = { chunks: 0, bytes: 0, durationMs: 0 };
  private pendingChunk: Uint8Array | null = null;

  constructor(
    private readonly cb: MicCallbacks,
    private readonly vadOpts: Partial<VadOptions> = {},
  ) {}

  get active(): boolean {
    return this.stream !== null;
  }

  async open(): Promise<void> {
    if (this.stream) return;
    if (!navigator.mediaDevices?.getUserMedia || typeof AudioWorkletNode === 'undefined') {
      throw Object.assign(new Error('unsupported'), { name: 'NotSupportedError' });
    }
    this.stream = await navigator.mediaDevices.getUserMedia({
      audio: { echoCancellation: true, noiseSuppression: true, channelCount: 1 },
    });
    // Se pide 16 kHz al contexto; si el navegador no lo respeta, se re-muestrea a mano.
    let ctx: AudioContext;
    try {
      ctx = new AudioContext({ sampleRate: TARGET_RATE });
    } catch {
      ctx = new AudioContext();
    }
    this.ctx = ctx;
    // Archivo servido aparte (public/): la CSP de deploy (default-src 'self') no permite módulos data:.
    await ctx.audioWorklet.addModule(`${import.meta.env.BASE_URL}capture-worklet.js`);
    this.down = new Downsampler(ctx.sampleRate, TARGET_RATE);
    const src = ctx.createMediaStreamSource(this.stream);
    const node = new AudioWorkletNode(ctx, 'cx9-capture');
    node.port.onmessage = (ev: MessageEvent<Float32Array>) => this.onBlock(ev.data);
    src.connect(node);
    this.node = node;
    this.vad = new EnergyVad({ sampleRate: TARGET_RATE, ...this.vadOpts });
  }

  private onBlock(block: Float32Array) {
    if (!this.recording || !this.down) return;
    this.chunker.push(this.down.process(block), (chunk) => {
      this.cb.onLevel?.(rms(chunk));
      this.emit(floatToPcm16le(chunk));
      if (this.useVad) {
        const e = this.vad.process(chunk);
        if (e) this.cb.onVad?.(e);
      }
    });
  }

  // Se retiene un chunk para poder marcar el último con flags=1 al terminar.
  private emit(pcm: Uint8Array) {
    if (this.pendingChunk) this.send(this.pendingChunk, false);
    this.pendingChunk = pcm;
  }

  private send(pcm: Uint8Array, last: boolean) {
    this.stats.chunks += 1;
    this.stats.bytes += pcm.byteLength;
    this.stats.durationMs = Math.round((this.stats.bytes / 2 / TARGET_RATE) * 1000);
    this.cb.onChunk(pcm, last);
  }

  startAnswer(useVad: boolean): void {
    if (!this.stream) throw new Error('mic not open');
    this.chunker.drain();
    this.pendingChunk = null;
    this.vad.reset();
    this.useVad = useVad;
    this.stats = { chunks: 0, bytes: 0, durationMs: 0 };
    this.recording = true;
    void this.ctx?.resume();
  }

  /** Termina el turno: manda lo que queda con flags=último y devuelve los totales para `audio.end`. */
  endAnswer(): MicStats {
    if (!this.recording) return { ...this.stats };
    this.recording = false;
    const rest = this.chunker.drain();
    if (rest.length > 0) this.emit(floatToPcm16le(rest));
    if (this.pendingChunk) this.send(this.pendingChunk, true);
    this.pendingChunk = null;
    this.cb.onLevel?.(0);
    return { ...this.stats };
  }

  /** Cancela el turno sin mandar nada más (p. ej. pausa o repetición). */
  cancel(): void {
    this.recording = false;
    this.pendingChunk = null;
    this.chunker.drain();
    this.cb.onLevel?.(0);
  }

  async close(): Promise<void> {
    this.cancel();
    this.node?.disconnect();
    this.node = null;
    this.stream?.getTracks().forEach((t) => t.stop());
    this.stream = null;
    await this.ctx?.close().catch(() => undefined);
    this.ctx = null;
  }
}
