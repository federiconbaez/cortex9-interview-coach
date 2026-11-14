import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { BinCodec, BinKind, type ServerMessage } from '../api/contract/ws-types';
import { es } from '../i18n/es';
import { fakeApi } from '../test/fakeApi';
import { fakeLiveDeps, renderUi } from '../test/render';
import { SessionView } from './SessionView';

let seq = 0;
const env = (type: string, data: unknown) => ({ v: 1, type, seq: ++seq, ts: '', session_id: 'S', data }) as unknown as ServerMessage;
const ready = { state: 'ready', tts_delivery: 'binary', tts_format: 'wav', heartbeat_ms: 15000, idle_timeout_ms: 45000, max_inflight_bytes: 96000, resumed: false, last_client_seq: 1, question_index: 0, question_count: 2 };
const question = (over: Record<string, unknown> = {}) => ({ turn_id: 't-1', question_id: 'q1', index: 1, total: 2, text: '¿Cuál fue tu último proyecto?', lang: 'es', utterance_id: 'u-1', kind: 'main', ...over });

function setup(input: 'voice' | 'text', capture: 'vad' | 'ptt' = 'vad', micError?: string) {
  const { deps, h } = fakeLiveDeps(micError ? { micError } : {});
  const onReport = vi.fn();
  const api = fakeApi();
  renderUi(<SessionView url="ws://x/v1/sessions/S/live" sessionId="S" input={input} capture={capture} deps={deps} onReport={onReport} />, api);
  const o = () => {
    if (!h.opts) throw new Error('sin conexión');
    return h.opts;
  };
  const send = (type: string, data: unknown) => act(() => o().onMessage(env(type, data)));
  const open = (over: Record<string, unknown> = {}) => {
    act(() => o().onState('open', { attempt: 0 }));
    send('session.ready', { ...ready, ...over });
  };
  return { h, o, send, open, onReport, api };
}

describe('SessionView', () => {
  it('voz: muestra la pregunta, arma el TTS binario, avisa tts.played una vez y manda audio.end', async () => {
    const t = setup('voice');
    t.open();
    expect(screen.getByTestId('conn')).toHaveTextContent(es['conn.open']);
    t.send('question', question());
    expect(screen.getByTestId('question')).toHaveTextContent('¿Cuál fue tu último proyecto?');
    expect(screen.getByText('Pregunta 1 de 2')).toBeInTheDocument();
    t.send('tts.audio', { turn_id: 't-1', utterance_id: 'u-1', format: 'wav', sample_rate: 16000, chunks: 1, bytes: 4, duration_ms: 1, sha256: null });
    expect(screen.getByTestId('tts-loading')).toBeInTheDocument();
    await act(async () => {
      t.o().onTtsFrame?.({ kind: BinKind.TTS_AUDIO, seq: 9, turnNo: 1, chunkIndex: 0, codec: BinCodec.WAV, last: true, payloadLen: 4 }, new Uint8Array(4));
      await Promise.resolve();
    });
    await waitFor(() => expect(t.h.element?.played).toBe(1));
    expect(screen.getByTestId('tts-toggle')).toHaveTextContent(es['tts.pause']);
    act(() => t.h.element?.onended?.());
    expect(screen.getByTestId('tts-toggle')).toHaveTextContent(es['tts.replay']);
    expect(t.h.sent.filter((m) => m.type === 'tts.played')).toEqual([{ type: 'tts.played', data: { utterance_id: 'u-1' } }]);

    await userEvent.click(screen.getByTestId('answer-toggle'));
    expect(t.h.micCalls).toEqual(['open', 'start:true']);
    expect(screen.getByTestId('answer-toggle')).toHaveTextContent(es['answer.end']);
    act(() => {
      t.h.mic?.onChunk(new Uint8Array(3200), false);
      t.h.mic?.onChunk(new Uint8Array(3200), true);
    });
    expect(t.h.audio).toEqual([
      { turnNo: 1, chunkIndex: 0, last: false },
      { turnNo: 1, chunkIndex: 1, last: true },
    ]);
    act(() => t.h.mic?.onVad?.('speech_end'));
    expect(t.h.sent.at(-1)).toEqual({ type: 'audio.end', data: { turn_id: 't-1', chunks: 2, bytes: 6400, duration_ms: 200 } });
    expect(screen.getByTestId('answer-status')).toHaveTextContent(es['practice.processing']);
    t.send('transcript.final', { turn_id: 't-1', text: 'hice 3 cosas', low_confidence: ['cosas'], confidence: 0.6, duration_ms: 200 });
    expect(screen.getByTestId('transcript')).toHaveTextContent('hice 3 cosas');
    expect(screen.getByTestId('transcript')).toHaveTextContent('(Palabras que se entendieron con poca seguridad: cosas)');
  });

  it('verifica sha256 del TTS: si no coincide, no reproduce', async () => {
    const t = setup('voice');
    t.open();
    t.send('question', question());
    t.send('tts.audio', { turn_id: 't-1', utterance_id: 'u-1', format: 'wav', sample_rate: 16000, chunks: 1, bytes: 4, duration_ms: 1, sha256: '00'.repeat(32) });
    await act(async () => {
      t.o().onTtsFrame?.({ kind: 2, seq: 9, turnNo: 1, chunkIndex: 0, codec: 2, last: true, payloadLen: 4 } as never, new Uint8Array(4));
      await new Promise((r) => setTimeout(r, 10));
    });
    expect(t.h.element?.played).toBe(0);
    expect(screen.getByTestId('tts-toggle')).toBeDisabled();
  });

  it('push-to-talk con la barra espaciadora', () => {
    const t = setup('voice', 'ptt');
    t.open();
    t.send('question', question());
    return (async () => {
      await act(async () => {
        fireEvent.keyDown(window, { code: 'Space', key: ' ' });
        await Promise.resolve();
      });
      await waitFor(() => expect(t.h.micCalls).toEqual(['open', 'start:false']));
      act(() => void fireEvent.keyUp(window, { code: 'Space', key: ' ' }));
      expect(t.h.sent.at(-1)?.type).toBe('audio.end');
    })();
  });

  it('sin permiso de micrófono muestra el aviso y no graba', async () => {
    const t = setup('voice', 'vad', 'NotAllowedError');
    t.open();
    t.send('question', question());
    await userEvent.click(screen.getByTestId('answer-toggle'));
    expect(screen.getByRole('alert')).toHaveTextContent(es['mic.denied']);
    expect(t.h.micCalls).toEqual(['open']);
  });

  it('texto: envía answer.text, controles repetir/saltar/pausa y fin con informe', async () => {
    const t = setup('text');
    t.open();
    t.send('question', question({ kind: 'intro' }));
    expect(screen.getByText(es['practice.intro'])).toBeInTheDocument();
    expect(screen.queryByText(/Pregunta 0/)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: es['answer.typedSend'] })).toBeDisabled();
    t.send('question', question({ turn_id: 't-2', utterance_id: 'u-2' }));
    await userEvent.type(screen.getByLabelText(es['answer.textOnly']), 'Bajé la latencia 40 %');
    await userEvent.click(screen.getByRole('button', { name: es['answer.typedSend'] }));
    expect(t.h.sent.filter((m) => m.type === 'answer.text')).toEqual([{ type: 'answer.text', data: { turn_id: 't-2', text: 'Bajé la latencia 40 %' } }]);
    // Mientras procesa no se puede repetir/saltar; llega la pregunta siguiente.
    expect(screen.getByRole('button', { name: es['control.skip'] })).toBeDisabled();
    t.send('question', question({ turn_id: 't-3', utterance_id: 'u-3', index: 2 }));
    await userEvent.click(screen.getByRole('button', { name: es['control.repeat'] }));
    await userEvent.click(screen.getByRole('button', { name: es['control.skip'] }));
    await userEvent.click(screen.getByRole('button', { name: es['control.pause'] }));
    expect(t.h.sent.slice(-3).map((m) => m.type)).toEqual(['control.repeat', 'control.skip', 'control.pause']);
    t.send('pause', { reason: 'user' });
    expect(screen.getByTestId('answer-status')).toHaveTextContent(es['practice.paused']);
    await userEvent.click(screen.getByRole('button', { name: es['control.resume'] }));
    expect(t.h.sent.at(-1)).toEqual({ type: 'control.resume', data: { turn_id: 't-3' } });
    t.send('error', { code: 'STATE_CONFLICT', message: 'x', fatal: false, retryable: false });
    expect(screen.getByTestId('session-error')).toHaveTextContent(es['wserr.STATE_CONFLICT']);
    await userEvent.click(screen.getByRole('button', { name: es['control.dismiss'] }));
    expect(screen.queryByTestId('session-error')).not.toBeInTheDocument();
    t.send('session.end', { reason: 'completed', report_status: 'pending', summary: { questions_asked: 2, questions_answered: 1, followups_asked: 0, skipped: 1, total_answer_ms: 0, report_status: 'scoring' } });
    const h = screen.getByRole('heading', { name: es['ended.heading'] });
    await waitFor(() => expect(h).toHaveFocus());
    await userEvent.click(screen.getByRole('button', { name: es['ended.report'] }));
    expect(t.onReport).toHaveBeenCalledWith('S');
  });

  it('reconexión visible y cierre terminal 4009 con mensaje específico; terminar sin conexión usa POST /end', async () => {
    const t = setup('text');
    t.open();
    act(() => t.o().onState('reconnecting', { attempt: 1, nextDelayMs: 1000, closeCode: 1006 }));
    expect(screen.getByTestId('conn')).toHaveTextContent('Reconectando en 1 s (intento 1)…');
    act(() => t.o().onState('failed', { attempt: 1, closeCode: 4009 }));
    expect(screen.getByTestId('conn-failed')).toHaveTextContent(es['conn.replaced']);
    await userEvent.click(screen.getByRole('button', { name: es['control.end'] }));
    await waitFor(() => expect(t.api.endSession).toHaveBeenCalledWith('S'));
    await waitFor(() => expect(t.onReport).toHaveBeenCalledWith('S'));
  });
  it('v1.1: con tts_delivery=none no manda tts.played y se puede responder enseguida', async () => {
    const t = setup('text');
    t.open({ tts_delivery: 'none', tts_format: undefined });
    t.send('question', question());
    expect(screen.queryByTestId('tts-toggle')).not.toBeInTheDocument();
    await userEvent.type(screen.getByLabelText(es['answer.textOnly']), 'Hice 3 cosas');
    await userEvent.click(screen.getByRole('button', { name: es['answer.typedSend'] }));
    expect(t.h.sent.map((m) => m.type)).toEqual(['answer.text']);
  });

  it('v1.1: intro sin "Pregunta 0", repetición marcada y el cierre no manda tts.played', async () => {
    const t = setup('voice');
    t.open();
    const tts = async (utt: string, turnNo: number) => {
      t.send('tts.audio', { utterance_id: utt, format: 'wav', sample_rate: 16000, chunks: 1, bytes: 4, duration_ms: 1, sha256: null });
      await act(async () => {
        t.o().onTtsFrame?.({ kind: BinKind.TTS_AUDIO, seq: 1, turnNo, chunkIndex: 0, codec: BinCodec.WAV, last: true, payloadLen: 4 }, new Uint8Array(4));
        await new Promise((r) => setTimeout(r, 0));
      });
      act(() => t.h.element?.onended?.());
    };
    t.send('question', question({ turn_id: 't-0', question_id: 'intro', index: 0, kind: 'intro', utterance_id: 'u-1' }));
    expect(screen.queryByText(/Pregunta 0/)).not.toBeInTheDocument();
    expect(screen.getByTestId('answer-toggle')).toHaveAttribute('aria-disabled', 'true');
    await tts('u-1', 1);
    t.send('question', question({ utterance_id: 'u-2' }));
    await tts('u-2', 2);
    await userEvent.click(screen.getByRole('button', { name: es['control.repeat'] }));
    t.send('question', question({ utterance_id: 'u-3', kind: 'repeat', repeat: true }));
    expect(screen.getByText(`· ${es['practice.repeat']}`)).toBeInTheDocument();
    expect(screen.getByText('Pregunta 1 de 2')).toBeInTheDocument();
    await tts('u-3', 3);
    t.send('question', question({ turn_id: 't-0', question_id: 'closing', index: 2, total: 2, kind: 'closing', utterance_id: 'u-4' }));
    expect(screen.getByText(es['practice.closing'])).toBeInTheDocument();
    await tts('u-4', 4);
    const played = t.h.sent.filter((x) => x.type === 'tts.played').map((x) => (x.data as { utterance_id: string }).utterance_id);
    expect(played).toEqual(['u-1', 'u-2', 'u-3']);
    expect(t.h.sent.filter((x) => x.type === 'control.repeat')).toEqual([{ type: 'control.repeat', data: { turn_id: 't-1' } }]);
  });
});
