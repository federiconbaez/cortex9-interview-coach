import AxeBuilder from '@axe-core/playwright';
import { expect, type APIRequestContext, type Page } from '@playwright/test';

export const MOCK = 'http://127.0.0.1:8787';
export const PASSWORD = 'practica';

export async function resetMock(request: APIRequestContext, seed = false) {
  expect((await request.post(`${MOCK}/__mock/reset`)).ok()).toBe(true);
  if (seed) expect((await request.post(`${MOCK}/__mock/seed`)).ok()).toBe(true);
}

/** Login por API (misma cookie que el navegador, vía el proxy de Vite). */
export async function login(page: Page) {
  const r = await page.request.post('/v1/auth/login', { data: { password: PASSWORD }, headers: { Origin: 'http://127.0.0.1:4173' } });
  expect(r.ok()).toBe(true);
}

export async function mockState(request: APIRequestContext) {
  return (await (await request.get(`${MOCK}/__mock/state`)).json()) as {
    sessions: {
      id: string; status: string; lastClientSeq: number; engineState: string | null;
      acks: number; seqErrors: number; unexpectedPlayed: number; helloSeqs: number[];
      turns: { kind: string; turn_id: string; answer: string; source: string; duration_ms: number }[];
    }[];
  };
}

export async function mockConfig(request: APIRequestContext, cfg: { tts?: 'binary' | 'none'; partials?: boolean }) {
  expect((await request.post(`${MOCK}/__mock/config`, { data: cfg })).ok()).toBe(true);
}

export async function expectNoA11yViolations(page: Page) {
  const r = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual([]);
}

export async function startSession(page: Page, opts: { input: 'voice' | 'text'; capture?: 'vad' | 'ptt'; count?: number; followups?: number }) {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Nueva práctica' })).toBeVisible();
  await page.getByLabel('Screening con IA (mock)').check();
  if (opts.count) await page.getByLabel('Cantidad de preguntas').fill(String(opts.count));
  if (opts.followups !== undefined) await page.getByLabel('Repreguntas por pregunta (máximo 2)').selectOption(String(opts.followups));
  await page.getByLabel(opts.input === 'voice' ? 'Por voz (micrófono)' : 'Escribiendo (sin micrófono)').check();
  if (opts.capture === 'ptt') await page.getByLabel('Mantener para hablar (barra espaciadora o botón)').check();
  await page.getByRole('button', { name: 'Empezar práctica' }).click();
  await expect(page.getByTestId('conn')).toContainText('Conectada');
  await expect(page.getByTestId('question')).toContainText('recorrido profesional');
}
