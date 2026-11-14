/** Estado de la sesión en vivo derivado de los mensajes del contrato (§5.5, §5.6). */
import type { Probe, ServerMessage, SessionEnd, SessionState as WsState, WsErrorCode } from '../api/contract/ws-types';
import type { ConnectionState } from '../api/ws';

export type TurnKind = 'intro' | 'main' | 'closing' | 'repeat' | 'followup';

export interface TurnView {
  turnId: string;
  turnNo: number;
  questionId: string;
  kind: TurnKind;
  text: string;
  utteranceId: string;
  index: number | null;
  total: number | null;
  followupIndex: number | null;
  maxFollowups: number | null;
  probe: Probe | null;
}

export interface TranscriptEntry {
  id: string;
  role: 'interviewer' | 'candidate';
  text: string;
  lowConfidence: string[];
}

export interface LiveState {
  conn: ConnectionState;
  attempt: number;
  nextDelayMs: number | null;
  closeCode: number | null;
  connError: string | null;
  server: WsState | null;
  turn: TurnView | null;
  lastIndex: number | null;
  total: number | null;
  tts: 'pending' | 'available' | 'unavailable';
  transcript: TranscriptEntry[];
  partial: { turnId: string; text: string; revision: number } | null;
  error: { code: WsErrorCode; message: string; fatal: boolean } | null;
  paused: { reason: 'user' | 'think_time' | 'reconnect' } | null;
  ended: SessionEnd['data'] | null;
  recording: boolean;
}

export type LiveAction =
  | { type: 'conn'; state: ConnectionState; attempt: number; nextDelayMs?: number; closeCode?: number; errorCode?: string }
  | { type: 'server'; msg: ServerMessage }
  | { type: 'ttsPlayed' }
  | { type: 'recording'; on: boolean }
  | { type: 'answerSent' }
  | { type: 'dismissError' };

export const initialLive: LiveState = {
  conn: 'idle',
  attempt: 0,
  nextDelayMs: null,
  closeCode: null,
  connError: null,
  server: null,
  turn: null,
  lastIndex: null,
  total: null,
  tts: 'pending',
  transcript: [],
  partial: null,
  error: null,
  paused: null,
  ended: null,
  recording: false,
};

export const turnNo = (turnId: string): number => Number(/^t-(\d+)$/.exec(turnId)?.[1] ?? 0);

/** ¿Se puede responder ahora? Intro y cierre no se responden. */
export function canAnswer(s: LiveState): boolean {
  return (
    s.conn === 'open' &&
    s.turn !== null &&
    s.turn.kind !== 'intro' &&
    s.turn.kind !== 'closing' &&
    (s.server === 'asking' || s.server === 'listening') &&
    !s.recording
  );
}

export function liveReducer(s: LiveState, a: LiveAction): LiveState {
  switch (a.type) {
    case 'conn':
      return {
        ...s,
        conn: a.state,
        attempt: a.attempt,
        nextDelayMs: a.nextDelayMs ?? null,
        closeCode: a.closeCode ?? s.closeCode,
        connError: a.errorCode ?? (a.state === 'open' ? null : s.connError),
      };
    case 'ttsPlayed':
      return s.server === 'asking' ? { ...s, server: 'listening' } : s;
    case 'recording':
      return { ...s, recording: a.on };
    case 'answerSent':
      return { ...s, recording: false, server: 'processing' };
    case 'dismissError':
      return { ...s, error: null };
    case 'server':
      return onServer(s, a.msg);
  }
}

function addInterviewer(s: LiveState, turn: TurnView): LiveState {
  const dup = s.turn?.turnId === turn.turnId && s.turn.utteranceId === turn.utteranceId;
  return {
    ...s,
    server: 'asking',
    turn,
    lastIndex: turn.index ?? s.lastIndex,
    total: turn.total ?? s.total,
    tts: dup ? s.tts : 'pending',
    partial: null,
    paused: null,
    transcript: dup
      ? s.transcript
      : [...s.transcript, { id: `${turn.turnId}-${turn.utteranceId}`, role: 'interviewer', text: turn.text, lowConfidence: [] }],
  };
}

function onServer(s: LiveState, m: ServerMessage): LiveState {
  switch (m.type) {
    case 'session.ready':
      return {
        ...s,
        server: m.data.state,
        total: m.data.question_count ?? s.total,
        lastIndex: m.data.question_index ?? s.lastIndex,
        tts: m.data.tts_delivery === 'none' ? 'unavailable' : s.tts,
      };
    case 'question': {
      const d = m.data;
      return addInterviewer(s, {
        turnId: d.turn_id,
        turnNo: turnNo(d.turn_id),
        questionId: d.question_id,
        kind: d.kind ?? 'main',
        text: d.text,
        utteranceId: d.utterance_id,
        index: d.index,
        total: d.total,
        followupIndex: null,
        maxFollowups: null,
        probe: null,
      });
    }
    case 'followup': {
      const d = m.data;
      return addInterviewer(s, {
        turnId: d.turn_id,
        turnNo: turnNo(d.turn_id),
        questionId: d.question_id,
        kind: 'followup',
        text: d.text,
        utteranceId: d.utterance_id,
        index: s.lastIndex,
        total: s.total,
        followupIndex: d.followup_index,
        maxFollowups: d.max_followups,
        probe: d.probe,
      });
    }
    case 'tts.audio':
    case 'tts.url':
      return s.turn?.utteranceId === m.data.utterance_id ? { ...s, tts: 'available' } : s;
    case 'transcript.partial':
      if (s.partial && s.partial.turnId === m.data.turn_id && m.data.revision <= s.partial.revision) return s;
      return { ...s, partial: { turnId: m.data.turn_id, text: m.data.text, revision: m.data.revision } };
    case 'transcript.final': {
      const id = `${m.data.turn_id}-answer`;
      const exists = s.transcript.some((e) => e.id === id);
      return {
        ...s,
        partial: null,
        transcript:
          exists || !m.data.text.trim()
            ? s.transcript
            : [...s.transcript, { id, role: 'candidate', text: m.data.text, lowConfidence: m.data.low_confidence }],
      };
    }
    case 'pause':
      return { ...s, server: 'paused', paused: { reason: m.data.reason }, recording: false };
    case 'resume':
      return { ...s, server: m.data.state, paused: null };
    case 'turn.end':
      return { ...s, server: m.data.next === 'end' ? 'closing' : 'processing', recording: false };
    case 'session.end':
      return { ...s, server: 'ended', ended: m.data, recording: false, partial: null };
    case 'error': {
      const next = { ...s, error: { code: m.data.code, message: m.data.message, fatal: m.data.fatal } };
      return m.data.code === 'TTS_UNAVAILABLE' ? { ...next, tts: 'unavailable' } : next;
    }
    case 'ping':
    case 'pong':
      return s;
  }
}
