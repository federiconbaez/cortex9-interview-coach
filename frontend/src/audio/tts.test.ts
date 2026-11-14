import { describe, expect, it, vi } from 'vitest';
import type { BinaryFrameHeader, TtsAudio } from '../api/contract/ws-types';
import { TtsAssembler, TtsPlayer, sha256Hex, type AudioLike } from './tts';

const meta = (over: Partial<TtsAudio['data']> = {}): TtsAudio['data'] =>
  ({ turn_id: 't-1', utterance_id: 'u-3', format: 'wav', sample_rate: 16000, chunks: 2, bytes: 6, duration_ms: 10, sha256: null, ...over }) as TtsAudio['data'];
const h = (chunkIndex: number, last: boolean, turnNo = 3): BinaryFrameHeader =>
  ({ kind: 2, seq: 1, turnNo, chunkIndex, codec: 2, last, payloadLen: 3 }) as BinaryFrameHeader;

describe('TtsAssembler', () => {
  it('une los frames en orden y verifica chunks/bytes', () => {
    const a = new TtsAssembler();
    a.begin(meta());
    expect(a.utteranceId).toBe('u-3');
    expect(a.push(h(0, false), new Uint8Array([1, 2, 3]))).toBeNull();
    const r = a.push(h(1, true), new Uint8Array([4, 5, 6]));
    expect(r).not.toBe('error');
    expect(r && r !== 'error' ? [...r.bytes] : null).toEqual([1, 2, 3, 4, 5, 6]);
    expect(r && r !== 'error' ? r.mime : null).toBe('audio/wav');
  });
  it('ignora frames de otra intervención y falla con frames fuera de orden o bytes incorrectos', () => {
    const a = new TtsAssembler();
    expect(a.push(h(0, true), new Uint8Array(3))).toBeNull(); // sin begin
    a.begin(meta());
    expect(a.push(h(0, false, 9), new Uint8Array(3))).toBeNull();
    expect(a.push(h(1, false), new Uint8Array(3))).toBe('error');
    a.begin(meta({ bytes: 7 }));
    a.push(h(0, false), new Uint8Array(3));
    expect(a.push(h(1, true), new Uint8Array(3))).toBe('error');
  });
  it('PCM16 crudo se envuelve en WAV (44 bytes de cabecera)', () => {
    const a = new TtsAssembler();
    a.begin(meta({ format: 'pcm16le', chunks: 1, bytes: 4 }));
    const r = a.push(h(0, true), new Uint8Array(4));
    if (!r || r === 'error') throw new Error('esperaba audio');
    expect(r.raw.byteLength).toBe(4);
    expect(r.bytes.byteLength).toBe(48);
    expect(new TextDecoder().decode(r.bytes.slice(0, 4))).toBe('RIFF');
  });
  it('sha256Hex coincide con el valor conocido de "abc"', async () => {
    expect(await sha256Hex(new TextEncoder().encode('abc'))).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad');
  });
});

function fakeAudio(playOk = true): AudioLike & { paused: number } {
  return {
    src: '',
    currentTime: 5,
    paused: 0,
    play: vi.fn(() => (playOk ? Promise.resolve() : Promise.reject(new Error('NotAllowed')))),
    pause() {
      this.paused += 1;
    },
    onended: null,
    onerror: null,
  };
}

describe('TtsPlayer', () => {
  const urls = () => ({ create: vi.fn(() => 'blob:1'), revoke: vi.fn() });
  it('carga, reproduce desde el inicio, pausa, reanuda y avisa al terminar', async () => {
    const audio = fakeAudio();
    const states: string[] = [];
    const ended = vi.fn();
    const u = urls();
    const p = new TtsPlayer(audio, (s) => states.push(s), ended, u);
    await p.loadBlob(new Uint8Array(2), 'audio/wav');
    expect(audio.src).toBe('blob:1');
    expect(audio.currentTime).toBe(0);
    p.pause();
    await p.play();
    audio.onended?.();
    expect(states).toEqual(['none', 'playing', 'paused', 'playing', 'ended']);
    expect(ended).toHaveBeenCalledOnce();
    p.release();
    expect(u.revoke).toHaveBeenCalledWith('blob:1');
  });
  it('autoplay bloqueado queda en pausa; error de audio → error; URL externa no se revoca', async () => {
    const audio = fakeAudio(false);
    const states: string[] = [];
    const u = urls();
    const p = new TtsPlayer(audio, (s) => states.push(s), () => undefined, u);
    await p.loadUrl('/v1/tts/x');
    expect(p.current).toBe('paused');
    audio.onerror?.();
    expect(p.current).toBe('error');
    p.release();
    expect(u.revoke).not.toHaveBeenCalled();
    p.setLoading();
    expect(p.current).toBe('loading');
    p.fail();
    expect(p.current).toBe('error');
    p.pause(); // no hace nada si no está sonando
    await p.play(); // sin URL no hace nada
    expect(audio.play).toHaveBeenCalledTimes(1);
  });
});
