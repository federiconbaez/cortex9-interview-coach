import { useCallback, useEffect, useReducer, useRef, useState } from 'react';
import type { CaptureMode, InputMode } from '../api/schema';
import { LiveConnection, type LiveOptions } from '../api/ws';
import { MicCapture, type MicCallbacks, type MicStats } from '../audio/mic';
import { TtsAssembler, TtsPlayer, sha256Hex, type AudioLike, type TtsState } from '../audio/tts';
import { canAnswer, initialLive, liveReducer } from './live';

type Conn = Pick<LiveConnection, 'connect' | 'sendJson' | 'sendAudio' | 'close'>;
type Mic = Pick<MicCapture, 'open' | 'startAnswer' | 'endAnswer' | 'cancel' | 'close'>;

export interface LiveDeps {
  createConnection: (o: LiveOptions) => Conn;
  createMic: (cb: MicCallbacks) => Mic;
  createAudio: () => AudioLike;
  resolveUrl: (path: string) => string;
}

export const defaultLiveDeps = (resolveUrl: (p: string) => string): LiveDeps => ({
  createConnection: (o) => new LiveConnection(o),
  createMic: (cb) => new MicCapture(cb),
  createAudio: () => new Audio() as unknown as AudioLike,
  resolveUrl,
});

export interface LiveArgs {
  url: string;
  sessionId: string;
  input: InputMode;
  capture: CaptureMode;
  deps: LiveDeps;
}

export function useLiveSession({ url, sessionId, input, capture, deps }: LiveArgs) {
  const depsRef = useRef(deps);
  const [state, dispatch] = useReducer(liveReducer, initialLive);
  const [tts, setTts] = useState<TtsState>('none');
  const [level, setLevel] = useState(0);
  const [micError, setMicError] = useState<'mic.denied' | 'mic.unsupported' | null>(null);
  const conn = useRef<Conn | null>(null);
  const mic = useRef<Mic | null>(null);
  const player = useRef<TtsPlayer | null>(null);
  const turnRef = useRef<{ turnId: string; turnNo: number; chunk: number } | null>(null);
  const utteranceRef = useRef<string | null>(null);
  const playedRef = useRef<Set<string>>(new Set());
  const endRef = useRef<() => void>(() => undefined);

  const markPlayed = useCallback(() => {
    const u = utteranceRef.current;
    if (!u || playedRef.current.has(u)) return;
    playedRef.current.add(u);
    conn.current?.sendJson('tts.played', { utterance_id: u });
    dispatch({ type: 'ttsPlayed' });
  }, []);

  useEffect(() => {
    const d = depsRef.current;
    const p = new TtsPlayer(d.createAudio(), setTts, markPlayed);
    player.current = p;
    const asm = new TtsAssembler();
    const c = d.createConnection({
      url,
      sessionId,
      onState: (st, info) => {
        const a: Parameters<typeof dispatch>[0] = { type: 'conn', state: st, attempt: info.attempt };
        if (info.nextDelayMs !== undefined) a.nextDelayMs = info.nextDelayMs;
        if (info.closeCode !== undefined) a.closeCode = info.closeCode;
        if (info.errorCode !== undefined) a.errorCode = info.errorCode;
        dispatch(a);
        // Si se corta la conexión durante una respuesta, el audio ya encolado se reenvía al
        // reconectar (§5.9); la captura sigue y el usuario no pierde lo dicho.
      },
      onMessage: (msg) => {
        if (msg.type === 'question' || msg.type === 'followup') {
          utteranceRef.current = msg.data.utterance_id;
          p.release();
        } else if (msg.type === 'tts.audio') {
          if (msg.data.utterance_id === utteranceRef.current) {
            asm.begin(msg.data);
            p.setLoading();
          }
        } else if (msg.type === 'tts.url') {
          if (msg.data.utterance_id === utteranceRef.current) void p.loadUrl(d.resolveUrl(msg.data.url));
        } else if (msg.type === 'pause' || msg.type === 'session.end') {
          if (turnRef.current) {
            turnRef.current = null;
            mic.current?.cancel();
          }
          p.pause();
        }
        dispatch({ type: 'server', msg });
      },
      onTtsFrame: (h, payload) => {
        const r = asm.push(h, payload);
        if (r === null) return;
        if (r === 'error') {
          p.fail();
          return;
        }
        void (async () => {
          if (r.meta.sha256) {
            const got = await sha256Hex(r.raw);
            if (got !== null && got !== r.meta.sha256) {
              p.fail();
              return;
            }
          }
          if (r.meta.utterance_id === utteranceRef.current) await p.loadBlob(r.bytes, r.mime);
        })();
      },
    });
    conn.current = c;
    const m = d.createMic({
      onChunk: (pcm, last) => {
        const t = turnRef.current;
        if (!t) return;
        c.sendAudio(t.turnNo, t.chunk, pcm, last);
        t.chunk += 1;
      },
      onLevel: setLevel,
      onVad: (e) => {
        if (e === 'speech_end') endRef.current();
      },
    });
    mic.current = m;
    c.connect();
    return () => {
      c.close();
      void m.close();
      p.release();
    };
  }, [url, sessionId, markPlayed]);

  // Sin audio TTS para la intervención actual: la pregunta solo se lee (el servidor pasa solo a listening).
  const answerable = canAnswer(state);

  const startAnswer = useCallback(async () => {
    const t = state.turn;
    if (!t || !answerable || input !== 'voice') return;
    player.current?.pause();
    markPlayed();
    try {
      await mic.current?.open();
    } catch (e) {
      const name = e instanceof Error ? e.name : '';
      setMicError(name === 'NotAllowedError' || name === 'SecurityError' ? 'mic.denied' : 'mic.unsupported');
      return;
    }
    setMicError(null);
    turnRef.current = { turnId: t.turnId, turnNo: t.turnNo, chunk: 0 };
    mic.current?.startAnswer(capture === 'vad');
    dispatch({ type: 'recording', on: true });
  }, [state.turn, answerable, input, capture, markPlayed]);

  const endAnswer = useCallback(() => {
    const t = turnRef.current;
    if (!t) return;
    const stats: MicStats = mic.current?.endAnswer() ?? { chunks: 0, bytes: 0, durationMs: 0 };
    turnRef.current = null;
    conn.current?.sendJson('audio.end', {
      turn_id: t.turnId,
      chunks: stats.chunks,
      bytes: stats.bytes,
      duration_ms: stats.durationMs,
    });
    dispatch({ type: 'answerSent' });
  }, []);

  useEffect(() => {
    endRef.current = endAnswer;
  }, [endAnswer]);

  const sendText = useCallback(
    (text: string): boolean => {
      const t = state.turn;
      const clean = text.trim();
      if (!t || !answerable || input !== 'text' || !clean || clean.length > 8000) return false;
      player.current?.pause();
      markPlayed();
      conn.current?.sendJson('answer.text', { turn_id: t.turnId, text: clean });
      dispatch({ type: 'answerSent' });
      return true;
    },
    [state.turn, answerable, input, markPlayed],
  );

  const cancelRecording = useCallback(() => {
    if (turnRef.current) {
      turnRef.current = null;
      mic.current?.cancel();
      dispatch({ type: 'recording', on: false });
    }
  }, []);

  const control = useCallback(
    (action: 'repeat' | 'skip' | 'pause' | 'resume' | 'end') => {
      const c = conn.current;
      const t = state.turn;
      if (!c) return;
      if (action !== 'resume') cancelRecording();
      if (action === 'end') c.sendJson('control.end', {});
      else if (action === 'pause') c.sendJson('control.pause', t ? { turn_id: t.turnId } : {});
      else if (action === 'resume') c.sendJson('control.resume', t ? { turn_id: t.turnId } : {});
      else if (t) c.sendJson(action === 'skip' ? 'control.skip' : 'control.repeat', { turn_id: t.turnId });
    },
    [state.turn, cancelRecording],
  );

  return {
    state,
    tts,
    level,
    micError,
    answerable,
    startAnswer,
    endAnswer,
    sendText,
    control,
    dismissError: () => dispatch({ type: 'dismissError' }),
    ttsPlay: () => void player.current?.play(tts === 'ended'),
    ttsPause: () => player.current?.pause(),
  };
}
