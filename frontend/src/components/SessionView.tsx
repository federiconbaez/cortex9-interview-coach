import { useEffect, useRef, useState, type FormEvent } from 'react';
import { CloseCode } from '../api/contract/ws-types';
import { useApi } from '../api/context';
import type { CaptureMode, InputMode } from '../api/schema';
import { useI18n, type MessageKey } from '../i18n';
import type { LiveState } from '../state/live';
import { useLiveSession, type LiveDeps } from '../state/useLiveSession';
import { ConnectionBadge } from './ConnectionBadge';
import { Transcript } from './Transcript';

interface Props {
  url: string;
  sessionId: string;
  input: InputMode;
  capture: CaptureMode;
  deps: LiveDeps;
  onReport: (sessionId: string) => void;
}

const isTyping = (el: EventTarget | null) =>
  el instanceof HTMLElement && (el.tagName === 'TEXTAREA' || el.tagName === 'INPUT' || el.tagName === 'SELECT' || el.isContentEditable);

const KNOWN_WS_ERRORS = new Set([
  'NO_AUDIO', 'STT_TIMEOUT', 'STT_UNAVAILABLE', 'TTS_UNAVAILABLE', 'LLM_DEGRADED', 'AUDIO_TOO_LONG', 'BACKPRESSURE', 'RESUME_GAP', 'STATE_CONFLICT',
]);

function connFailureKey(s: LiveState): MessageKey {
  switch (s.closeCode) {
    case CloseCode.REPLACED:
      return 'conn.replaced';
    case CloseCode.AUTH_FAILED:
      return 'conn.auth';
    case CloseCode.SESSION_NOT_FOUND:
      return 'conn.notFound';
    case CloseCode.SESSION_ENDED:
      return 'conn.ended';
    case CloseCode.FORBIDDEN_ORIGIN:
      return 'conn.origin';
    default:
      return 'conn.failed';
  }
}

export function SessionView({ url, sessionId, input, capture, deps, onReport }: Props) {
  const { t, formatNumber } = useI18n();
  const api = useApi();
  const s = useLiveSession({ url, sessionId, input, capture, deps });
  const { state, tts } = s;
  const [text, setText] = useState('');
  const endedRef = useRef<HTMLHeadingElement>(null);
  const turn = state.turn;
  const recording = state.recording;

  // Cierre 4001 (AUTH_FAILED): se verifica la cookie; si da 401 UNAUTHENTICATED, la app vuelve al login.
  const authFailed = state.conn === 'failed' && state.closeCode === CloseCode.AUTH_FAILED;
  useEffect(() => {
    if (authFailed) api.me().catch(() => undefined);
  }, [authFailed, api]);

  // Push-to-talk con la barra espaciadora (si el foco no está en un campo de texto).
  const pttRef = useRef({ start: s.startAnswer, end: s.endAnswer, can: s.answerable, recording });
  useEffect(() => {
    pttRef.current = { start: s.startAnswer, end: s.endAnswer, can: s.answerable, recording };
  });
  useEffect(() => {
    if (input !== 'voice' || capture !== 'ptt') return;
    const down = (e: KeyboardEvent) => {
      if (e.code !== 'Space' || e.repeat || isTyping(e.target) || e.target instanceof HTMLButtonElement) return;
      e.preventDefault();
      if (pttRef.current.can) void pttRef.current.start();
    };
    const up = (e: KeyboardEvent) => {
      if (e.code !== 'Space' || isTyping(e.target) || e.target instanceof HTMLButtonElement) return;
      e.preventDefault();
      if (pttRef.current.recording) pttRef.current.end();
    };
    window.addEventListener('keydown', down);
    window.addEventListener('keyup', up);
    return () => {
      window.removeEventListener('keydown', down);
      window.removeEventListener('keyup', up);
    };
  }, [input, capture]);

  useEffect(() => {
    if (state.ended) endedRef.current?.focus();
  }, [state.ended]);

  if (state.ended) {
    const ended = state.ended;
    return (
      <section className="card stack" aria-labelledby="ended-h">
        <h2 id="ended-h" tabIndex={-1} ref={endedRef}>
          {t('ended.heading')}
        </h2>
        <p>{t('report.answered', { count: ended.summary.questions_answered })}</p>
        <div>
          <button type="button" className="primary" onClick={() => onReport(sessionId)}>
            {t('ended.report')}
          </button>
        </div>
        <Transcript entries={state.transcript} partial="" />
      </section>
    );
  }

  const submitText = (e: FormEvent) => {
    e.preventDefault();
    if (s.sendText(text)) setText('');
  };

  const status = recording
    ? capture === 'vad'
      ? t('answer.listening')
      : t('answer.recording')
    : state.server === 'processing'
      ? t('practice.processing')
      : state.paused
        ? state.paused.reason === 'think_time'
          ? t('practice.pausedThink')
          : state.paused.reason === 'reconnect'
            ? t('practice.pausedReconnect')
            : t('practice.paused')
        : '';

  const baseLabel =
    turn?.kind === 'followup'
      ? t('practice.followupN', { n: turn.followupIndex ?? 1, max: turn.maxFollowups ?? 2 })
      : turn?.kind === 'intro'
        ? t('practice.intro')
        : turn?.kind === 'closing'
          ? t('practice.closing')
          : null;
  // v1.1: `repeat: true` marca una re-emisión (también de una repregunta).
  const kindLabel = turn?.repeat ? [baseLabel, t('practice.repeat')].filter(Boolean).join(' · ') : baseLabel;
  // Intro (index 0) y cierre (index = total) no son "Pregunta N de M".
  const showProgress = turn !== null && turn.kind !== 'intro' && turn.kind !== 'closing' && turn.index !== null && turn.total !== null && turn.index > 0;

  const open = state.conn === 'open';
  const askingOrListening = state.server === 'asking' || state.server === 'listening';
  const err = state.error;

  return (
    <div className="session stack">
      <div className="row between">
        <h2>{t('practice.heading')}</h2>
        <ConnectionBadge conn={state.conn} attempt={state.attempt} nextDelayMs={state.nextDelayMs} />
      </div>
      {state.conn === 'failed' && (
        <p role="alert" className="alert" data-testid="conn-failed">
          {t(connFailureKey(state))}
        </p>
      )}
      {err && (
        <div role="alert" className="alert row between" data-testid="session-error">
          <span>{KNOWN_WS_ERRORS.has(err.code) ? t(`wserr.${err.code}` as MessageKey) : t('wserr.generic', { code: err.code })}</span>
          <button type="button" className="link" onClick={s.dismissError}>
            {t('control.dismiss')}
          </button>
        </div>
      )}
      {s.micError && (
        <p role="alert" className="alert">
          {t(s.micError)}
        </p>
      )}

      <section className="card stack" aria-labelledby="q-h">
        <h3 id="q-h" className="visually-hidden">
          {t('practice.question')}
        </h3>
        {turn ? (
          <>
            <p className="muted">
              {showProgress && t('practice.progress', { index: turn.index ?? 0, total: turn.total ?? 0 })}
              {kindLabel && <span className="tag">{showProgress ? ' · ' : ''}{kindLabel}</span>}
              {turn.probe && <span className="tag"> · {t(`probe.${turn.probe}`)}</span>}
            </p>
            {/* La pregunta nueva se anuncia una vez por lector de pantalla. */}
            <p className="question" aria-live="polite" data-testid="question">
              {turn.text}
            </p>
            <div className="row" role="group" aria-label={t('practice.question')}>
              {state.tts === 'unavailable' ? null : tts === 'playing' ? (
                <button type="button" onClick={s.ttsPause} data-testid="tts-toggle">
                  {t('tts.pause')}
                </button>
              ) : tts === 'loading' || (state.tts === 'pending' && tts === 'none') ? (
                <p className="muted" data-testid="tts-loading">
                  {t('tts.loading')}
                </p>
              ) : (
                <button type="button" onClick={s.ttsPlay} data-testid="tts-toggle" disabled={tts === 'none' || tts === 'error'}>
                  {tts === 'ended' ? t('tts.replay') : tts === 'paused' ? t('tts.resume') : t('tts.play')}
                </button>
              )}
            </div>
          </>
        ) : (
          <p role="status">{t('practice.waiting')}</p>
        )}
      </section>

      <section className="card stack" aria-labelledby="ans-h">
        <h3 id="ans-h">{t('transcript.you')}</h3>
        {input === 'voice' ? (
          <>
            <p className="muted">{capture === 'ptt' ? t('answer.pttHint') : t('answer.vadHint')}</p>
            {capture !== 'ptt' && state.endpointing?.enabled === true && (
              <p className="muted small" data-testid="endpoint-hint">
                {t('answer.endpointHint', { seconds: formatNumber(state.endpointing.silence_ms / 1000) })}
              </p>
            )}
            {tts === 'playing' && <p className="muted">{t('practice.ttsPlayingNote')}</p>}
            <div className="row">
              {capture === 'ptt' ? (
                <button
                  type="button"
                  className="primary"
                  aria-pressed={recording}
                  aria-disabled={!s.answerable && !recording}
                  data-testid="ptt"
                  onPointerDown={(e) => {
                    e.preventDefault();
                    if (s.answerable) void s.startAnswer();
                  }}
                  onPointerUp={() => recording && s.endAnswer()}
                  onPointerLeave={() => recording && s.endAnswer()}
                  onKeyDown={(e) => {
                    if ((e.key === 'Enter' || e.key === ' ') && !e.repeat) {
                      e.preventDefault();
                      if (s.answerable) void s.startAnswer();
                    }
                  }}
                  onKeyUp={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault();
                      if (recording) s.endAnswer();
                    }
                  }}
                >
                  {t('answer.ptt')}
                </button>
              ) : (
                <button
                  type="button"
                  className="primary"
                  aria-disabled={!s.answerable && !recording}
                  data-testid="answer-toggle"
                  onClick={() => {
                    if (recording) s.endAnswer();
                    else if (s.answerable) void s.startAnswer();
                  }}
                >
                  {recording ? t('answer.end') : t('answer.start')}
                </button>
              )}
              <meter min={0} max={0.3} value={Math.min(0.3, s.level)} aria-label={t('answer.level')} className={recording ? '' : 'idle'} />
            </div>
          </>
        ) : (
          <form onSubmit={submitText} className="stack">
            <label htmlFor="typed">{t('answer.textOnly')}</label>
            <textarea id="typed" rows={4} maxLength={8000} value={text} onChange={(e) => setText(e.target.value)} aria-describedby="typed-hint" />
            <span id="typed-hint" className="hint">
              {t('answer.textTooLong')}
            </span>
            <div>
              <button type="submit" disabled={!s.answerable || !text.trim()}>
                {t('answer.typedSend')}
              </button>
            </div>
          </form>
        )}
        <p role="status" aria-live="polite" data-testid="answer-status" className="status-line">
          {status}
        </p>
      </section>

      <div className="row" role="group" aria-label={t('practice.heading')}>
        {/* §5.6: repetir y saltar solo desde asking/listening (si no, el servidor responde STATE_CONFLICT). */}
        <button type="button" onClick={() => s.control('repeat')} disabled={!open || !turn || !askingOrListening}>
          {t('control.repeat')}
        </button>
        <button type="button" onClick={() => s.control('skip')} disabled={!open || !turn || !askingOrListening}>
          {t('control.skip')}
        </button>
        {state.server === 'paused' ? (
          <button type="button" onClick={() => s.control('resume')} disabled={!open}>
            {t('control.resume')}
          </button>
        ) : (
          <button type="button" onClick={() => s.control('pause')} disabled={!open || !askingOrListening}>
            {t('control.pause')}
          </button>
        )}
        <button
          type="button"
          className="danger"
          onClick={() => {
            // Con el WS abierto se usa control.end; si no, POST /end (idempotente, §4.5).
            if (open) s.control('end');
            else void api.endSession(sessionId).then(() => onReport(sessionId), () => undefined);
          }}
        >
          {t('control.end')}
        </button>
      </div>

      <Transcript entries={state.transcript} partial={state.partial?.text ?? ''} />
    </div>
  );
}
