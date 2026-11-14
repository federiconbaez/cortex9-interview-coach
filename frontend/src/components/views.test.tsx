import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api/client';
import type { Session } from '../api/schema';
import { es } from '../i18n/es';
import { fakeApi, fx } from '../test/fakeApi';
import { renderUi } from '../test/render';
import { HistoryView } from './HistoryView';
import { ProfilePage } from './ProfileEditor';
import { ReportView } from './ReportView';
import { SetupForm } from './SetupForm';

afterEach(() => vi.useRealTimers());

describe('SetupForm', () => {
  it('arma CreateSessionRequest según el contrato y avisa proveedor remoto', async () => {
    const onStart = vi.fn(async () => undefined);
    renderUi(<SetupForm onStart={onStart} />);
    await screen.findByRole('heading', { name: es['setup.heading'] });
    await userEvent.click(screen.getByLabelText(/Conductual STAR/));
    await userEvent.selectOptions(screen.getByLabelText(es['setup.followups']), '1');
    await userEvent.click(screen.getByLabelText(es['setup.input.text']));
    await userEvent.selectOptions(screen.getByLabelText(es['setup.provider']), 'openai');
    expect(screen.getByText(es['setup.providerRemote'])).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'xAI (no disponible)' })).toBeDisabled();
    await userEvent.click(screen.getByRole('button', { name: es['setup.start'] }));
    expect(onStart).toHaveBeenCalledWith({
      capture: 'vad',
      req: {
        lang: 'es', mode: 'practice', input: 'text', llm_provider: 'openai', shuffle: false, max_followups: 1, think_pause_ms: 0,
        learn_profile: false, save_audio: false, scenario_id: 'behavioral-star', num_questions: 2,
      },
    });
  });
  it('modo historia no manda scenario_id y aprende del perfil; error del servidor se muestra', async () => {
    const onStart = vi.fn(async () => Promise.reject(new ApiError(422, 'VALIDATION_FAILED', 'x')));
    renderUi(<SetupForm onStart={onStart} />);
    await userEvent.click(await screen.findByLabelText(es['setup.mode.story']));
    await userEvent.click(screen.getByRole('button', { name: es['setup.start'] }));
    const req = (onStart.mock.calls[0] as unknown as [{ req: Record<string, unknown> }])[0].req;
    expect(req).toMatchObject({ mode: 'story', learn_profile: true });
    expect(req).not.toHaveProperty('scenario_id');
    expect(await screen.findByText(/VALIDATION_FAILED/)).toBeInTheDocument();
  });
  it('sin STT para el idioma obliga a responder escribiendo', async () => {
    const api = fakeApi({ languages: vi.fn(async () => fx.languages.map((l) => ({ ...l, stt: false }))) });
    renderUi(<SetupForm onStart={vi.fn()} />, api);
    expect(await screen.findByLabelText(es['setup.input.voice'])).toBeDisabled();
    expect(screen.getByLabelText(es['setup.input.text'])).toBeChecked();
    expect(screen.getByText(es['setup.sttMissing'])).toBeInTheDocument();
  });
});

describe('ReportView', () => {
  it('202 → reintenta tras Retry-After → muestra top3, criterios, mejora y faltantes; foco al título', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const api = fakeApi({
      report: vi
        .fn()
        .mockResolvedValueOnce({ status: 'pending', session: { ...fx.sessions.items[0], status: 'scoring' } as Session, retryAfterS: 2 })
        .mockResolvedValue({ status: 'ready', report: fx.report }),
    });
    renderUi(<ReportView sessionId="s" onBack={vi.fn()} />, api);
    expect(await screen.findByTestId('report-pending')).toHaveTextContent('2 s');
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    const h = await screen.findByRole('heading', { name: es['report.heading'] });
    await waitFor(() => expect(h).toHaveFocus());
    expect(api.report).toHaveBeenCalledTimes(2);
    const top = within(screen.getByTestId('top3')).getAllByRole('listitem');
    expect(top).toHaveLength(3);
    expect(top[0]).toHaveTextContent('Duración (promedio 2 de 5)');
    expect(screen.getByTestId('overall')).toHaveTextContent('4 de 5');
    expect(screen.getByRole('table')).toHaveTextContent(`${es['dim.estructura']}${es['report.na']}`); // dimensión null → n/a
    expect(screen.getAllByTestId('improved')[0]).toHaveTextContent('Telecom');
    expect(screen.getByText(es['report.disclaimer'])).toBeInTheDocument();
  });
  it('v1.1: muestra muletillas con su fuente y pausas largas cuando vienen', async () => {
    const base = fx.report;
    const block = base.blocks[0];
    if (!block) throw new Error('fixture sin bloques');
    const t0 = block.turns[0];
    if (!t0) throw new Error('fixture sin turnos');
    const report = {
      ...base,
      blocks: [
        {
          ...block,
          turns: [
            { ...t0, turn_id: 't-1', details: { ...t0.details, muletillas: ['eh', 'este'], muletillas_cada_100: 12.5, muletillas_fuente: 'stt' as const, pausas: { cantidad: 2, total_s: 3.5, mas_larga_s: 2 } } },
            { ...t0, turn_id: 't-2', details: { ...t0.details, muletillas: [], muletillas_fuente: 'texto' as const, pausas: { cantidad: 0, total_s: 0, mas_larga_s: 0 } } },
            { ...t0, turn_id: 't-3', details: { ...t0.details, muletillas: [], pausas: null } },
          ],
        },
      ],
    };
    renderUi(<ReportView sessionId="s" onBack={vi.fn()} />, fakeApi({ report: vi.fn(async () => ({ status: 'ready' as const, report })) }));
    const items = await screen.findAllByTestId('turn-speech');
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent('Muletillas: eh, este (12,5 cada 100 palabras) (detectadas por el reconocimiento de voz)');
    expect(items[0]).toHaveTextContent('Pausas largas: 2 (en total 3,5 s; la más larga, 2 s)');
    expect(items[1]).toHaveTextContent(`${es['report.fillersNone']} (${es['report.fillersSource.texto']})`);
    expect(items[1]).toHaveTextContent(es['report.pausesNone']);
  });

  it('409 SESSION_NOT_ENDED, sesión fallida y error con reintento', async () => {
    const a1 = fakeApi({ report: vi.fn(async () => Promise.reject(new ApiError(409, 'SESSION_NOT_ENDED', 'x'))) });
    const r1 = renderUi(<ReportView sessionId="s" onBack={vi.fn()} />, a1);
    expect(await screen.findByRole('alert')).toHaveTextContent(es['report.notEnded']);
    r1.unmount();
    const a2 = fakeApi({ report: vi.fn(async () => ({ status: 'pending' as const, session: { status: 'failed' } as Session, retryAfterS: 1 })) });
    const r2 = renderUi(<ReportView sessionId="s" onBack={vi.fn()} />, a2);
    expect(await screen.findByRole('alert')).toHaveTextContent(es['report.failed']);
    r2.unmount();
    const a3 = fakeApi({ report: vi.fn().mockRejectedValueOnce(new ApiError(503, 'DEPENDENCY_UNAVAILABLE', 'x')).mockResolvedValue({ status: 'ready', report: fx.report }) });
    renderUi(<ReportView sessionId="s" onBack={vi.fn()} />, a3);
    await userEvent.click(await screen.findByRole('button', { name: es['common.retry'] }));
    expect(await screen.findByRole('heading', { name: es['report.heading'] })).toBeInTheDocument();
  });
});

describe('HistoryView', () => {
  it('gráfico SVG accesible, tabla, filtros y abrir informe', async () => {
    const onOpen = vi.fn();
    const api = fakeApi();
    renderUi(<HistoryView onOpen={onOpen} />, api);
    const chart = await screen.findByRole('img', { name: /Puntaje general a lo largo del tiempo/ });
    expect(chart).toHaveAttribute('data-testid', 'chart');
    expect(chart.querySelector('desc')?.textContent).toMatch(/^3 sesiones/);
    expect(screen.getByText(/Mejoraste|Bajaste|Sin cambios/)).toBeInTheDocument();
    expect(within(screen.getByTestId('history-table')).getAllByRole('row')).toHaveLength(4);
    await userEvent.selectOptions(screen.getByLabelText(es['history.filterLang']), 'en');
    await waitFor(() => expect(api.listSessions).toHaveBeenLastCalledWith(expect.objectContaining({ lang: 'en' })));
    await userEvent.selectOptions(screen.getByLabelText(es['history.filterLang']), '');
    await userEvent.click((await screen.findAllByRole('button', { name: new RegExp(es['history.open']) }))[0] as HTMLElement);
    expect(onOpen).toHaveBeenCalledWith(fx.sessions.items[0]?.id);
  });
  it('historial vacío y "cargar más" con cursor', async () => {
    const empty = fakeApi({ listSessions: vi.fn(async () => ({ items: [], next_cursor: null }) as never) });
    const r = renderUi(<HistoryView onOpen={vi.fn()} />, empty);
    expect(await screen.findByText(es['history.empty'])).toBeInTheDocument();
    r.unmount();
    const [a, b, c] = fx.sessions.items;
    const paged = fakeApi({
      listSessions: vi.fn(async (f: { cursor?: string }) => (f.cursor ? { items: [c], next_cursor: null } : { items: [a, b], next_cursor: 'n1', total: 3 }) as never),
    });
    renderUi(<HistoryView onOpen={vi.fn()} />, paged);
    await userEvent.click(await screen.findByRole('button', { name: es['history.more'] }));
    await waitFor(() => expect(within(screen.getByTestId('history-table')).getAllByRole('row')).toHaveLength(4));
    expect(paged.listSessions).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: 'n1' }));
    expect(screen.queryByRole('button', { name: es['history.more'] })).not.toBeInTheDocument();
  });
});

describe('ProfilePage', () => {
  it('valida y enfoca el primer campo inválido; guarda normalizado con If-Match; muestra huecos', async () => {
    const api = fakeApi({
      getProfile: vi.fn(async () => ({ profile: { experiences: [], education: [], skills: ['Go'], updated_at: '2026-11-13T10:00:00Z' } as never, etag: '"1"' })),
    });
    renderUi(<ProfilePage />, api);
    expect(within(await screen.findByTestId('gaps')).getAllByRole('listitem')).toHaveLength(2);
    await userEvent.click(screen.getByRole('button', { name: es['profile.addExperience'] }));
    await userEvent.type(screen.getByLabelText(new RegExp(es['profile.period'])), '2020');
    await userEvent.click(screen.getByRole('button', { name: es['common.save'] }));
    expect(screen.getByTestId('profile-msg')).toHaveTextContent('hay 2 errores');
    const company = screen.getByLabelText(new RegExp(`^${es['profile.company']}`));
    await waitFor(() => expect(company).toHaveFocus());
    expect(company).toHaveAttribute('aria-invalid', 'true');
    expect(company).toHaveAccessibleDescription(es['validation.required']);
    await userEvent.type(company, ' Acme ');
    await userEvent.clear(screen.getByLabelText(new RegExp(es['profile.period'])));
    await userEvent.type(screen.getByLabelText(new RegExp(es['profile.period'])), '2020-actual');
    await userEvent.type(screen.getByLabelText(new RegExp(es['profile.technologies'])), 'Go, go, React');
    await userEvent.click(screen.getByRole('button', { name: es['common.save'] }));
    await waitFor(() => expect(screen.getByTestId('profile-msg')).toHaveTextContent(es['profile.saved']));
    const [body, etag] = vi.mocked(api.putProfile).mock.calls[0] as unknown as [Record<string, unknown>, string];
    expect(etag).toBe('"1"');
    expect(Object.keys(body).sort()).toEqual(['education', 'experiences', 'skills']);
    expect(body.experiences).toEqual([expect.objectContaining({ company: 'Acme', period: '2020-actual', technologies: ['Go', 'React'] })]);
  });
  it('412 muestra conflicto con recarga; borrar pide confirmación', async () => {
    const api = fakeApi({ putProfile: vi.fn(async () => Promise.reject(new ApiError(412, 'PRECONDITION_FAILED', 'x'))) });
    renderUi(<ProfilePage />, api);
    await userEvent.click(await screen.findByRole('button', { name: es['common.save'] }));
    expect(await screen.findByText(es['profile.conflict'])).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: es['profile.reload'] }));
    await waitFor(() => expect(api.getProfile).toHaveBeenCalledTimes(2));
    await userEvent.click(await screen.findByRole('button', { name: es['profile.delete'] }));
    expect(api.deleteProfile).not.toHaveBeenCalled();
    expect(screen.getByTestId('profile-msg')).toHaveTextContent(es['profile.deleteConfirm']);
    await userEvent.click(screen.getByRole('button', { name: es['profile.delete'] }));
    await waitFor(() => expect(api.deleteProfile).toHaveBeenCalledOnce());
    expect(screen.getByTestId('profile-msg')).toHaveTextContent(es['profile.deleted']);
  });
});
