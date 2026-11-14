import AxeBuilder from '@axe-core/playwright';
import { expect, test, type Page } from '@playwright/test';

// Sesiones completas contra el backend Go REAL (LLM mock + `coach speech-mock`).
// Las aserciones solo usan comportamiento documentado en docs/contract.md v1.1.
const SCENARIO = /^Screening con IA/;

async function loginUi(page: Page) {
  await page.goto('/');
  await page.getByLabel('Contraseña').fill('practica');
  await page.getByRole('button', { name: 'Ingresar' }).click();
  await expect(page.getByRole('heading', { name: 'Nueva práctica' })).toBeVisible();
}

async function start(page: Page, input: 'voice' | 'text', count: number, followups: number) {
  await page.getByLabel(SCENARIO).check();
  await page.getByLabel('Cantidad de preguntas').fill(String(count));
  await page.getByLabel('Repreguntas por pregunta (máximo 2)').selectOption(String(followups));
  await page.getByLabel(input === 'voice' ? 'Por voz (micrófono)' : 'Escribiendo (sin micrófono)').check();
  await page.getByRole('button', { name: 'Empezar práctica' }).click();
  await expect(page.getByTestId('conn')).toContainText('Conectada');
}

async function noA11yViolations(page: Page) {
  const r = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
  expect(r.violations.map((v) => v.id)).toEqual([]);
}

async function openReport(page: Page) {
  await expect(page.getByRole('heading', { name: 'Práctica terminada' })).toBeVisible({ timeout: 20_000 });
  await page.getByRole('button', { name: 'Ver informe' }).click();
  await expect(page.getByRole('heading', { name: 'Informe de práctica' })).toBeVisible({ timeout: 20_000 });
  await expect(page.getByTestId('overall')).toContainText('de 5');
}

test.beforeEach(async ({ page }) => {
  await loginUi(page);
});

test('real · texto: intro, preguntas y repreguntas, cierre, informe con muletillas por texto', async ({ page }) => {
  await start(page, 'text', 2, 1);
  const box = page.getByLabel('Esta sesión es por escrito: escribí tu respuesta.');
  const done = page.getByRole('heading', { name: 'Práctica terminada' });
  const answers = ['Eh, trabajé en backend con Go.', 'Bajé la latencia un 40 % en 3 meses con un equipo de 4.'];
  // Primero la presentación (sin "Pregunta N de M"), después la pregunta 1.
  await expect(page.getByText('Pregunta 1 de 2')).toBeVisible({ timeout: 15_000 });
  for (let i = 0; i < 8 && !(await done.isVisible()); i += 1) {
    await expect(box).toBeEnabled({ timeout: 15_000 });
    const q = await page.getByTestId('question').textContent();
    await box.fill(answers[i % 2] ?? '');
    await page.getByRole('button', { name: 'Enviar texto' }).click();
    await expect(page.getByTestId('question').or(done).first()).not.toHaveText(q ?? '', { timeout: 15_000 });
  }
  await openReport(page);
  await expect(page.getByTestId('turn-speech').first()).toContainText('detectadas en el texto de la transcripción');
  await noA11yViolations(page);
});

test('real · voz: TTS wav, repetir (repeat=true), NO_AUDIO → misma pregunta, respuesta por STT, informe', async ({ page }) => {
  const sent: string[] = [];
  const received: string[] = [];
  let binarySent = 0;
  page.on('websocket', (ws) => {
    ws.on('framesent', (f) => {
      if (typeof f.payload === 'string') sent.push(f.payload);
      else binarySent += f.payload.length;
    });
    ws.on('framereceived', (f) => {
      if (typeof f.payload === 'string') received.push(f.payload);
    });
  });
  await start(page, 'voice', 1, 0);
  await expect(page.getByText('Pregunta 1 de 1')).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId('tts-toggle')).toHaveText('Escuchar de nuevo', { timeout: 15_000 });

  await page.getByRole('button', { name: 'Repetir pregunta' }).click();
  await expect(page.getByText(/Repetición/)).toBeVisible();
  await expect(page.getByTestId('tts-toggle')).toHaveText('Escuchar de nuevo', { timeout: 15_000 });

  const toggle = page.getByTestId('answer-toggle');
  await toggle.click();
  await expect(toggle).toHaveText('Terminar respuesta'); // el micrófono ya abrió
  await toggle.click();
  await expect(page.getByTestId('session-error')).toContainText('No se escuchó tu respuesta', { timeout: 15_000 });
  await expect(page.getByTestId('tts-toggle')).toHaveText('Escuchar de nuevo', { timeout: 15_000 });

  // 4,5 s de audio: más que la ventana de 96 KB (3 s), así que solo termina si los ack la liberan.
  await toggle.click();
  await page.waitForTimeout(4500);
  await toggle.click();
  await expect(page.getByTestId('transcript')).toContainText('Vos:', { timeout: 15_000 });
  await openReport(page);
  await noA11yViolations(page);

  const msgs = sent.map((s) => JSON.parse(s) as { type: string; seq: number; data?: unknown });
  const hello = msgs.filter((m) => m.type === 'hello');
  expect(hello.map((m) => m.seq)).toEqual([0]);
  const rest = msgs.filter((m) => m.type !== 'hello').map((m) => m.seq);
  expect(rest.every((s, i) => i === 0 || s > (rest[i - 1] ?? 0))).toBe(true);

  const got = received.map((s) => JSON.parse(s) as { type: string; seq: number; data: Record<string, unknown> });
  const acks = got.filter((m) => m.type === 'ack');
  expect(acks.length).toBeGreaterThan(0);
  expect(acks.every((m) => m.seq === 0)).toBe(true);
  expect(binarySent).toBeGreaterThan(96_000);
  const prompts = got.filter((m) => m.type === 'question').map((m) => m.data);
  expect(prompts[0]).toMatchObject({ turn_id: 't-0', index: 0, kind: 'intro' });
  expect(prompts.at(-1)).toMatchObject({ turn_id: 't-0', index: 1, kind: 'closing' });
  expect(prompts.filter((p) => p.repeat === true).length).toBe(2); // control.repeat + NO_AUDIO
  // v1.2: hello.endpointing true (modo automático), session.ready.endpointing efectivo y
  // turn.end por audio_end o por endpoint (el tono del micrófono falso puede tener silencios).
  expect(msgs.find((m) => m.type === 'hello')).toMatchObject({ data: { endpointing: true } });
  const ready = got.find((m) => m.type === 'session.ready');
  expect(ready?.data.endpointing).toMatchObject({ enabled: true });
  const reasons = got.filter((m) => m.type === 'turn.end').map((m) => m.data.reason);
  expect(reasons.every((r) => ['audio_end', 'endpoint', 'no_audio'].includes(String(r)))).toBe(true);
  // §5.14: tts.played solo de intro/preguntas, nunca del cierre.
  const closingUtt = prompts.at(-1)?.utterance_id;
  const played = msgs.filter((m) => m.type === 'tts.played') as unknown as { data: { utterance_id: string } }[];
  expect(played.some((m) => m.data.utterance_id === closingUtt)).toBe(false);
  console.log(`[real] endpointing=${JSON.stringify(ready?.data.endpointing)} turn.end=${reasons.join(',')} tts.played=${played.length}`);
  const ttsFormats = got.filter((m) => m.type === 'tts.audio').map((m) => m.data.format);
  expect(new Set(ttsFormats)).toEqual(new Set(['wav']));
});
