import { describe, expect, it } from 'vitest';
import type { ServerMessage } from '../api/contract/ws-types';
import { canAnswer, initialLive, liveReducer, turnNo, type LiveAction, type LiveState } from './live';

let seq = 0;
const m = (type: string, data: unknown) => ({ type: 'server', msg: { v: 1, type, seq: ++seq, ts: '', session_id: 'S', data } as unknown as ServerMessage }) as LiveAction;
const run = (...a: LiveAction[]) => a.reduce(liveReducer, initialLive);
const q = (over: Record<string, unknown> = {}) =>
  m('question', { turn_id: 't-1', question_id: 'q1', index: 1, total: 3, text: '¿Qué hiciste?', lang: 'es', utterance_id: 'u-1', kind: 'main', ...over });
const open: LiveAction = { type: 'conn', state: 'open', attempt: 0 };

describe('liveReducer', () => {
  it('turnNo extrae el número de t-N', () => {
    expect(turnNo('t-12')).toBe(12);
    expect(turnNo('x')).toBe(0);
  });
  it('pregunta → asking, agrega a la transcripción una sola vez y se puede responder', () => {
    const s = run(open, m('session.ready', { state: 'ready', question_count: 3, question_index: 0, tts_delivery: 'binary' }), q(), q());
    expect(s.server).toBe('asking');
    expect(s.total).toBe(3);
    expect(s.transcript).toHaveLength(1);
    expect(canAnswer(s)).toBe(true);
    expect(liveReducer(s, { type: 'ttsPlayed' }).server).toBe('listening');
    expect(canAnswer(liveReducer(s, { type: 'recording', on: true }))).toBe(false);
  });
  it('intro y cierre no se responden; sin conexión tampoco', () => {
    expect(canAnswer(run(open, q({ kind: 'intro' })))).toBe(false);
    expect(canAnswer(run(open, q({ kind: 'closing' })))).toBe(false);
    expect(canAnswer(run(q()))).toBe(false);
  });
  it('repregunta conserva índice y guarda probe/followup_index', () => {
    const s = run(open, q(), m('followup', { turn_id: 't-1', question_id: 'q1', followup_index: 1, max_followups: 2, probe: 'metric', text: '¿Cuánto?', lang: 'es', utterance_id: 'u-2' }));
    expect(s.turn).toMatchObject({ kind: 'followup', index: 1, total: 3, followupIndex: 1, maxFollowups: 2, probe: 'metric' });
    expect(s.transcript).toHaveLength(2);
  });
  it('parciales por revisión; final agrega la respuesta (no vacía, sin duplicar) y limpia el parcial', () => {
    let s = run(open, q(), m('transcript.partial', { turn_id: 't-1', text: 'ho', revision: 2, audio_ms: 1 }));
    s = liveReducer(s, m('transcript.partial', { turn_id: 't-1', text: 'viejo', revision: 1, audio_ms: 1 }));
    expect(s.partial?.text).toBe('ho');
    const fin = m('transcript.final', { turn_id: 't-1', text: 'hola', low_confidence: ['hola'], confidence: 0.5, duration_ms: 1 });
    s = liveReducer(liveReducer(s, fin), fin);
    expect(s.partial).toBeNull();
    expect(s.transcript.filter((e) => e.role === 'candidate')).toEqual([{ id: 't-1-answer', role: 'candidate', text: 'hola', lowConfidence: ['hola'] }]);
    expect(liveReducer(s, m('transcript.final', { turn_id: 't-2', text: '  ', low_confidence: [] })).transcript).toHaveLength(2);
  });
  it('TTS disponible solo para la intervención actual; TTS_UNAVAILABLE y tts none la marcan no disponible', () => {
    let s = run(open, q());
    expect(liveReducer(s, m('tts.audio', { utterance_id: 'u-9' })).tts).toBe('pending');
    s = liveReducer(s, m('tts.url', { utterance_id: 'u-1', url: '/x' }));
    expect(s.tts).toBe('available');
    const e = liveReducer(s, m('error', { code: 'TTS_UNAVAILABLE', message: 'x', fatal: false, retryable: false }));
    expect(e.tts).toBe('unavailable');
    expect(e.error?.code).toBe('TTS_UNAVAILABLE');
    expect(liveReducer(e, { type: 'dismissError' }).error).toBeNull();
    expect(run(m('session.ready', { state: 'ready', tts_delivery: 'none' })).tts).toBe('unavailable');
  });
  it('pausa, reanudación, fin de turno y fin de sesión', () => {
    let s: LiveState = run(open, q(), { type: 'recording', on: true }, m('pause', { reason: 'user' }));
    expect(s).toMatchObject({ server: 'paused', paused: { reason: 'user' }, recording: false });
    s = liveReducer(s, m('resume', { state: 'listening' }));
    expect(s).toMatchObject({ server: 'listening', paused: null });
    expect(liveReducer(s, { type: 'answerSent' }).server).toBe('processing');
    expect(liveReducer(s, m('turn.end', { turn_id: 't-1', reason: 'answered', next: 'end' })).server).toBe('closing');
    expect(liveReducer(s, m('turn.end', { turn_id: 't-1', reason: 'answered', next: 'question' })).server).toBe('processing');
    const end = liveReducer(s, m('session.end', { reason: 'completed', report_status: 'pending' }));
    expect(end.ended).toMatchObject({ reason: 'completed' });
    expect(liveReducer(end, m('ping', { nonce: 'n' }))).toBe(end);
  });
  it('estado de conexión: conserva closeCode y limpia el error al abrir', () => {
    let s = liveReducer(initialLive, { type: 'conn', state: 'failed', attempt: 3, closeCode: 4001, errorCode: 'AUTH_FAILED' });
    expect(s).toMatchObject({ conn: 'failed', closeCode: 4001, connError: 'AUTH_FAILED' });
    s = liveReducer(s, { type: 'conn', state: 'open', attempt: 0 });
    expect(s).toMatchObject({ conn: 'open', closeCode: 4001, connError: null, nextDelayMs: null });
  });
  it('v1.1: intro index 0 y cierre index total con t-0 no cambian el progreso ni se responden', () => {
    let s = run(open, m('session.ready', { state: 'ready', question_count: 2, question_index: 0, tts_delivery: 'binary' }));
    s = liveReducer(s, q({ turn_id: 't-0', question_id: 'intro', index: 0, kind: 'intro', utterance_id: 'u-1' }));
    expect(s.lastIndex).toBe(0);
    expect(canAnswer(s)).toBe(false);
    s = liveReducer(s, q({ index: 1, utterance_id: 'u-2' }));
    expect(s.lastIndex).toBe(1);
    s = liveReducer(s, q({ turn_id: 't-0', question_id: 'closing', index: 2, total: 2, kind: 'closing', utterance_id: 'u-9' }));
    expect(s.lastIndex).toBe(1);
    expect(canAnswer(s)).toBe(false);
  });
  it('v1.1: repeat=true marca la re-emisión (pregunta y repregunta) sin duplicar el turno', () => {
    let s = run(open, q());
    s = liveReducer(s, q({ utterance_id: 'u-2', kind: 'repeat', repeat: true }));
    expect(s.turn).toMatchObject({ turnId: 't-1', kind: 'repeat', repeat: true, index: 1 });
    expect(s.transcript).toHaveLength(2);
    expect(canAnswer(s)).toBe(true);
    s = liveReducer(s, m('followup', { turn_id: 't-2', question_id: 'q1', followup_index: 1, max_followups: 2, probe: 'other', text: 'x', lang: 'es', utterance_id: 'u-3', repeat: true }));
    expect(s.turn).toMatchObject({ kind: 'followup', repeat: true, probe: 'other', followupIndex: 1 });
    expect(run(open, q({ repeat: true, kind: undefined })).turn?.kind).toBe('repeat');
    expect(run(open, q()).turn?.repeat).toBe(false);
  });
  it('guarda tts_delivery de session.ready y el ack no cambia el estado', () => {
    const s = run(m('session.ready', { state: 'ready', tts_delivery: 'url' }));
    expect(s.ttsDelivery).toBe('url');
    expect(liveReducer(s, m('ack', { last_client_seq: 3 }))).toBe(s);
  });
});
