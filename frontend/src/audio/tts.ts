/**
 * TTS de la entrevistadora (§5.5): `tts.audio` + N frames binarios kind 0x02, o `tts.url`.
 * Se arma el audio, se verifica `sha256` si viene, y se reproduce con pausa/reanudar.
 * Al terminar de sonar se avisa con `tts.played` (lo manda el llamador).
 */
import { wavFromPcm16 } from './pcm';
import type { BinaryFrameHeader, TtsAudio } from '../api/contract/ws-types';

export type TtsState = 'none' | 'loading' | 'playing' | 'paused' | 'ended' | 'error';

export interface AssembledTts {
  /** Bytes tal cual llegaron (sobre ellos se verifica `sha256`). */
  raw: Uint8Array;
  /** Bytes reproducibles (WAV si llegó PCM crudo). */
  bytes: Uint8Array;
  mime: string;
  meta: TtsAudio['data'];
}

/** Ensambla los frames de una intervención. Devuelve el blob al recibir el último. */
export class TtsAssembler {
  private parts: Uint8Array[] = [];
  private expected = -1;
  private meta: TtsAudio['data'] | null = null;
  private utteranceNo = -1;

  begin(meta: TtsAudio['data']): void {
    this.meta = meta;
    this.parts = [];
    this.expected = 0;
    this.utteranceNo = Number(meta.utterance_id.replace(/^u-/, ''));
  }

  get utteranceId(): string | null {
    return this.meta?.utterance_id ?? null;
  }

  /** Devuelve el audio cuando se completó la intervención; `null` mientras faltan frames. */
  push(h: BinaryFrameHeader, payload: Uint8Array): AssembledTts | 'error' | null {
    const meta = this.meta;
    if (!meta || h.turnNo !== this.utteranceNo) return null; // frame de otra intervención: se ignora
    if (h.chunkIndex !== this.expected) {
      this.meta = null;
      return 'error';
    }
    this.parts.push(payload.slice());
    this.expected += 1;
    if (!h.last && this.expected < meta.chunks) return null;
    this.meta = null;
    const total = this.parts.reduce((a, p) => a + p.byteLength, 0);
    if (this.expected !== meta.chunks || total !== meta.bytes) return 'error';
    const joined = new Uint8Array(total);
    let o = 0;
    for (const p of this.parts) {
      joined.set(p, o);
      o += p.byteLength;
    }
    this.parts = [];
    if (meta.format === 'pcm16le') return { raw: joined, bytes: wavFromPcm16(joined, meta.sample_rate), mime: 'audio/wav', meta };
    // v1.1: el TTS es solo wav o pcm16le (opus ya no existe para TTS).
    return { raw: joined, bytes: joined, mime: 'audio/wav', meta };
  }
}

export async function sha256Hex(bytes: Uint8Array): Promise<string | null> {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle) return null;
  const d = await subtle.digest('SHA-256', bytes as unknown as ArrayBuffer);
  return [...new Uint8Array(d)].map((b) => b.toString(16).padStart(2, '0')).join('');
}

export interface AudioLike {
  src: string;
  currentTime: number;
  play(): Promise<void>;
  pause(): void;
  onended: (() => void) | null;
  onerror: (() => void) | null;
}

export class TtsPlayer {
  private url: string | null = null;
  private owned = false;
  private state: TtsState = 'none';

  constructor(
    private readonly audio: AudioLike,
    private readonly onState: (s: TtsState) => void,
    private readonly onEnded: () => void,
    private readonly urls: { create: (b: Blob) => string; revoke: (u: string) => void } = {
      create: (b) => URL.createObjectURL(b),
      revoke: (u) => URL.revokeObjectURL(u),
    },
  ) {
    audio.onended = () => {
      this.set('ended');
      this.onEnded();
    };
    audio.onerror = () => {
      if (this.url) this.set('error');
    };
  }

  get current(): TtsState {
    return this.state;
  }

  private set(s: TtsState) {
    this.state = s;
    this.onState(s);
  }

  setLoading(): void {
    this.release();
    this.set('loading');
  }

  async loadBlob(bytes: Uint8Array, mime: string): Promise<void> {
    this.release();
    this.url = this.urls.create(new Blob([bytes as unknown as ArrayBuffer], { type: mime }));
    this.owned = true;
    this.audio.src = this.url;
    await this.play(true);
  }

  async loadUrl(url: string): Promise<void> {
    this.release();
    this.url = url;
    this.owned = false;
    this.audio.src = url;
    await this.play(true);
  }

  async play(fromStart = false): Promise<void> {
    if (!this.url) return;
    if (fromStart) this.audio.currentTime = 0;
    try {
      await this.audio.play();
      this.set('playing');
    } catch {
      // Autoplay bloqueado: queda en pausa y el usuario toca "Reproducir".
      this.set('paused');
    }
  }

  pause(): void {
    if (this.state !== 'playing') return;
    this.audio.pause();
    this.set('paused');
  }

  fail(): void {
    this.release();
    this.set('error');
  }

  release(): void {
    if (this.url) {
      this.audio.pause();
      if (this.owned) this.urls.revoke(this.url);
      this.url = null;
    }
    this.set('none');
  }
}
