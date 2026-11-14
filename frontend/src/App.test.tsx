import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from './api/client';
import { App, wsUrlFrom } from './App';
import { en } from './i18n/en';
import { es } from './i18n/es';
import { fakeApi, unauthorized } from './test/fakeApi';
import { fakeLiveDeps } from './test/render';

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('cx9.lang', 'es');
});

describe('App', () => {
  it('wsUrlFrom convierte live_url relativo a ws/wss del mismo origen', () => {
    expect(wsUrlFrom('/v1/sessions/s/live', { protocol: 'http:', host: 'h:5173' })).toBe('ws://h:5173/v1/sessions/s/live');
    expect(wsUrlFrom('/v1/sessions/s/live', { protocol: 'https:', host: 'h' })).toBe('wss://h/v1/sessions/s/live');
  });

  it('sin preferencia guardada usa el idioma del navegador (jsdom: en-US)', async () => {
    localStorage.clear();
    render(<App client={fakeApi()} />);
    expect(await screen.findByRole('heading', { name: en['setup.heading'] })).toBeInTheDocument();
  });

  it('401 → login; contraseña incorrecta muestra error; correcta entra', async () => {
    let logged = false;
    const api = fakeApi({
      me: vi.fn(async () => {
        if (!logged) throw unauthorized();
        return { authenticated: true } as never;
      }),
      login: vi.fn(async (p: string) => {
        if (p !== 'practica') throw new ApiError(401, 'UNAUTHENTICATED', 'no');
        logged = true;
      }),
    });
    render(<App client={api} liveDeps={fakeLiveDeps().deps} />);
    const pw = await screen.findByLabelText(es['login.password']);
    await userEvent.type(pw, 'mal');
    await userEvent.click(screen.getByRole('button', { name: es['login.submit'] }));
    expect(await screen.findByRole('alert')).toHaveTextContent(es['login.failed']);
    await userEvent.clear(pw);
    await userEvent.type(pw, 'practica');
    await userEvent.click(screen.getByRole('button', { name: es['login.submit'] }));
    expect(await screen.findByRole('heading', { name: es['setup.heading'] })).toBeInTheDocument();
    expect(screen.getByText(es['app.ethics'])).toBeInTheDocument();
  });

  it('cambia de idioma, lo persiste y actualiza html lang; navegación mueve el foco al contenido', async () => {
    const api = fakeApi();
    render(<App client={api} liveDeps={fakeLiveDeps().deps} />);
    await screen.findByRole('heading', { name: es['setup.heading'] });
    await userEvent.selectOptions(screen.getByLabelText(es['lang.label']), 'en');
    expect(await screen.findByRole('heading', { name: en['setup.heading'] })).toBeInTheDocument();
    expect(document.documentElement.lang).toBe('en');
    expect(localStorage.getItem('cx9.lang')).toBe('en');
    expect(api.scenarios).toHaveBeenLastCalledWith('en');
    await userEvent.click(screen.getByRole('button', { name: en['nav.history'] }));
    await waitFor(() => expect(screen.getByRole('main')).toHaveFocus());
    expect(screen.getByRole('button', { name: en['nav.history'] })).toHaveAttribute('aria-current', 'page');
  });

  it('empezar una práctica crea la sesión y abre la vista en vivo', async () => {
    const api = fakeApi();
    const { deps, h } = fakeLiveDeps();
    render(<App client={api} liveDeps={deps} />);
    await userEvent.click(await screen.findByRole('button', { name: es['setup.start'] }));
    expect(await screen.findByRole('heading', { name: es['practice.heading'] })).toBeInTheDocument();
    expect(api.createSession).toHaveBeenCalledOnce();
    const [req, key] = vi.mocked(api.createSession).mock.calls[0] as [Record<string, unknown>, string];
    expect(req).toMatchObject({ lang: 'es', mode: 'practice', input: 'voice', scenario_id: 'screening-ia', llm_provider: 'mock' });
    expect(key).toMatch(/^[0-9a-f-]{36}$/);
    expect(h.opts?.url).toMatch(/^ws:\/\/.+\/v1\/sessions\/.+\/live$/);
    expect(screen.getByLabelText(es['lang.label'])).toBeDisabled();
    act(() => h.opts?.onState('open', { attempt: 0 }));
  });

  it('error no 401 al verificar sesión muestra reintento', async () => {
    const api = fakeApi({ me: vi.fn(async () => Promise.reject(new ApiError(503, 'DEPENDENCY_UNAVAILABLE', 'x'))) });
    render(<App client={api} />);
    expect(await screen.findByRole('alert')).toHaveTextContent('DEPENDENCY_UNAVAILABLE');
  });
});
