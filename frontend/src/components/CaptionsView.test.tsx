import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { App, CAPTIONS_HASH } from '../App';
import { msgs } from '../test/captions';
import type { CaptionsDeps } from '../captions/useCaptions';
import { es } from '../i18n/es';
import { fakeApi } from '../test/fakeApi';
import { FakeWs } from '../test/fakes';
import { renderUi } from '../test/render';
import { CaptionsView } from './CaptionsView';

const deps: CaptionsDeps = { url: 'ws://test/captions/v1/live', createWs: (u, p) => new FakeWs(u, p) };

async function connected() {
  await waitFor(() => expect(FakeWs.all.length).toBeGreaterThan(0));
  const ws = FakeWs.all[FakeWs.all.length - 1];
  if (!ws) throw new Error('sin socket');
  const m = msgs();
  act(() => {
    ws.open();
    ws.recv(m.hello());
    ws.recv(m.caps());
    ws.recv(m.devices());
    ws.recv(m.status('listening', -20));
  });
  return { ws, m };
}

beforeEach(() => {
  FakeWs.all = [];
  localStorage.clear();
});
afterEach(() => {
  vi.restoreAllMocks();
});

describe('CaptionsView', () => {
  it('parciales visibles pero solo los finales van a la región aria-live; traducción y idioma detectado', async () => {
    renderUi(<CaptionsView deps={deps} />);
    const { ws, m } = await connected();
    expect(screen.getByTestId('captions-scope')).toHaveTextContent(es['captions.scope']);
    act(() => ws.recv(m.partial('a', 'Hello wor')));
    expect(screen.getByTestId('cap-partial')).toHaveTextContent('Hello wor');
    expect(screen.getByTestId('captions-live')).toHaveTextContent('');
    act(() => ws.recv(m.final('a', 'Hello world.', 'Hola mundo.')));
    expect(screen.queryByTestId('cap-partial')).toBeNull();
    expect(screen.getByTestId('cap-final')).toHaveTextContent('Hello world.');
    expect(screen.getByTestId('cap-translation')).toHaveTextContent('Hola mundo.');
    expect(screen.getByTestId('cap-final')).toHaveTextContent(/97 %/);
    expect(screen.getByTestId('captions-live')).toHaveTextContent('Hello world. — Hola mundo.');
    expect(screen.getByTestId('captions-list')).not.toHaveAttribute('aria-live');
    await userEvent.click(screen.getByRole('button', { name: es['captions.clear'] }));
    expect(screen.queryByTestId('cap-final')).toBeNull();
  });

  it('aviso remoto antes de elegir, confirmación, y aviso persistente solo con config.applied remoto', async () => {
    renderUi(<CaptionsView deps={deps} />);
    const { ws, m } = await connected();
    expect(screen.getByTestId('remote-hint')).toHaveTextContent(es['captions.remoteHint']);
    const provider = screen.getByLabelText(es['captions.provider']);
    expect(within(provider).getByRole('option', { name: /LLM remoto/ })).toHaveTextContent(es['captions.remoteTag']);
    expect(within(provider).getByRole('option', { name: /NLLB/ })).toBeDisabled();
    await userEvent.selectOptions(provider, 'llm');
    expect(screen.getByTestId('remote-confirm')).toHaveFocus();
    expect(ws.json()).toEqual([]);
    await userEvent.click(screen.getByRole('button', { name: es['captions.confirmYes'] }));
    expect(ws.json()).toEqual([{ type: 'config', device_id: 'monitor-0', translate: true, target_lang: 'es', provider: 'llm', seq: 1 }]);
    // Optimista no: hasta config.applied la UI sigue mostrando lo aplicado.
    expect(provider).toHaveValue('none');
    expect(screen.getByTestId('captions-pending')).toBeInTheDocument();
    expect(screen.queryByTestId('remote-notice')).toBeNull();
    act(() => ws.recv(m.applied('llm', true)));
    expect(provider).toHaveValue('llm');
    expect(screen.getByTestId('remote-notice')).toHaveTextContent('LLM remoto');
    expect(within(screen.getByTestId('remote-notice')).queryByRole('button')).toBeNull();
    // Cambiar el idioma con el remoto ya aplicado no pide otra confirmación; fr no está... sí está en llm.
    await userEvent.selectOptions(screen.getByLabelText(es['captions.target']), 'fr');
    expect(screen.queryByTestId('remote-confirm')).toBeNull();
    act(() => ws.recv(m.applied('argos', false)));
    expect(screen.queryByTestId('remote-notice')).toBeNull();
    // fr no está en los langs de Argos
    expect(within(screen.getByLabelText(es['captions.target'])).getByRole('option', { name: /francés/i })).toBeDisabled();
  });

  it('cancelar la confirmación no manda nada; pausa y reanudar siguen al status del servidor', async () => {
    renderUi(<CaptionsView deps={deps} />);
    const { ws, m } = await connected();
    await userEvent.selectOptions(screen.getByLabelText(es['captions.provider']), 'llm');
    await userEvent.click(screen.getByRole('button', { name: es['captions.confirmNo'] }));
    expect(screen.queryByTestId('remote-confirm')).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: es['captions.pause'] }));
    expect(ws.json().map((x) => x.type)).toEqual(['pause']);
    expect(screen.getByTestId('capture-state')).toHaveTextContent(es['captions.capture.listening']);
    act(() => ws.recv(m.status('paused', -90)));
    expect(screen.getByTestId('capture-state')).toHaveTextContent(es['captions.capture.paused']);
    expect(screen.getByTestId('level')).toHaveAttribute('value', '-60');
    await userEvent.click(screen.getByRole('button', { name: es['captions.resume'] }));
    await userEvent.click(screen.getByRole('button', { name: es['captions.refreshDevices'] }));
    expect(ws.json().map((x) => x.type)).toEqual(['pause', 'resume', 'devices.list']);
    act(() => ws.recv(m.status('no_audio', -80)));
    expect(screen.getByTestId('capture-state')).toHaveTextContent(es['captions.capture.no_audio']);
  });

  it('dispositivo: el select muestra el aplicado; DEVICE_NOT_FOUND avisa y mantiene el anterior', async () => {
    renderUi(<CaptionsView deps={deps} />);
    const { ws, m } = await connected();
    const device = screen.getByLabelText(es['captions.device']);
    expect(device).toHaveValue('monitor-0');
    await userEvent.selectOptions(device, 'mic-0');
    expect(ws.json()[0]).toMatchObject({ type: 'config', device_id: 'mic-0' });
    act(() => ws.recv(m.error('DEVICE_NOT_FOUND')));
    expect(device).toHaveValue('monitor-0');
    expect(screen.getByTestId('captions-error')).toHaveTextContent(es['captions.err.DEVICE_NOT_FOUND']);
    await userEvent.click(screen.getByRole('button', { name: es['captions.dismiss'] }));
    expect(screen.queryByTestId('captions-error')).toBeNull();
    act(() => ws.recv(m.error('RARO')));
    expect(screen.getByTestId('captions-error')).toHaveTextContent('RARO');
    act(() => ws.recv(m.error('CAPTURE_FAILED')));
    expect(within(screen.getByTestId('captions-error')).queryByRole('button')).toBeNull();
    expect(screen.getByRole('button', { name: es['captions.pause'] })).toBeDisabled();
  });

  it('cierres: 4409 con reintento manual, 1008 sin reintento, reconexión con cuenta regresiva', async () => {
    renderUi(<CaptionsView deps={deps} />);
    const { ws } = await connected();
    act(() => ws.drop(4409));
    expect(screen.getByTestId('captions-closed')).toHaveTextContent(es['captions.closed.busy']);
    await userEvent.click(screen.getByRole('button', { name: es['captions.retry'] }));
    expect(FakeWs.all).toHaveLength(2);
    act(() => FakeWs.all[1]?.drop(1008));
    expect(screen.getByTestId('captions-closed')).toHaveTextContent(es['captions.closed.forbidden']);
    expect(screen.queryByRole('button', { name: es['captions.retry'] })).toBeNull();
  });

  it('reconexión: muestra el reintento y los selects quedan deshabilitados', async () => {
    renderUi(<CaptionsView deps={deps} />);
    const { ws } = await connected();
    act(() => ws.drop(1011));
    expect(screen.getByTestId('captions-conn')).toHaveTextContent('Reconectando en 0,5 s (intento 1)');
    expect(screen.getByLabelText(es['captions.provider'])).toBeDisabled();
  });

  it('tamaño de letra sin persistir nada en el navegador', async () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem');
    renderUi(<CaptionsView deps={deps} />);
    const { ws, m } = await connected();
    act(() => ws.recv(m.final('a', 'x')));
    await userEvent.selectOptions(screen.getByLabelText(es['captions.fontSize']), '200');
    expect(screen.getByTestId('captions-list')).toHaveAttribute('style', expect.stringMatching(/font-size: 200%/));
    expect(setItem).not.toHaveBeenCalled();
  });

  it('la ruta #/subtitulos no usa la API del coach ni pide login; desmontar cierra el socket', async () => {
    localStorage.setItem('cx9.lang', 'es');
    const api = fakeApi();
    globalThis.location.hash = CAPTIONS_HASH;
    const view = render(<App client={api} captionsDeps={deps} />);
    expect(await screen.findByRole('heading', { name: es['captions.heading'], level: 2 })).toBeInTheDocument();
    await connected();
    expect(api.me).not.toHaveBeenCalled();
    expect(screen.queryByText(es['app.ethics'])).toBeNull();
    act(() => {
      globalThis.location.hash = '#/';
      globalThis.dispatchEvent(new HashChangeEvent('hashchange'));
    });
    await waitFor(() => expect(api.me).toHaveBeenCalled());
    expect(FakeWs.all[0]?.closed?.code).toBe(1000);
    view.unmount();
  });
});
