import { expect, test } from '@playwright/test';
import { MOCK, expectNoA11yViolations, login, mockConfig, mockState, resetMock, startSession } from './helpers';

test.beforeEach(async ({ page, request }) => {
  await resetMock(request);
  await login(page);
});

test('voz (VAD/botón): TTS, audio PCM binario, transcripción, repregunta, informe 202→200', async ({ page, request }) => {
  const cspErrors: string[] = [];
  page.on('console', (m) => {
    if (m.type() === 'error' && /Content Security Policy/i.test(m.text())) cspErrors.push(m.text());
  });
  await startSession(page, { input: 'voice', count: 2, followups: 1 });
  // El TTS del mock (300 ms de silencio WAV) se reproduce y termina → tts.played.
  await expect(page.getByTestId('tts-toggle')).toHaveText('Escuchar de nuevo');
  await expectNoA11yViolations(page);

  const toggle = page.getByTestId('answer-toggle');
  await toggle.click();
  await expect(toggle).toHaveText('Terminar respuesta');
  await expect(page.getByTestId('answer-status')).toHaveText('Micrófono activo: esperando que hables.');
  await page.waitForTimeout(1500);
  await toggle.click();
  await expect(page.getByTestId('transcript')).toContainText(/respuesta simulada [0-9a-f]{8}/);
  // Sin números → el mock repregunta una vez.
  await expect(page.getByTestId('question')).toHaveText('¿Me podés dar un número concreto?');
  await expect(page.getByText('Repregunta 1 de 1')).toBeVisible();

  const st = await mockState(request);
  const turn = st.sessions[0]?.turns[0];
  expect(turn?.source).toBe('stt');
  expect(turn?.duration_ms ?? 0).toBeGreaterThan(1000);

  await toggle.click();
  await page.waitForTimeout(600);
  await toggle.click();
  await expect(page.getByTestId('question')).toContainText('trabajando actualmente');
  await page.getByRole('button', { name: 'Terminar y ver informe' }).click();
  await expect(page.getByRole('heading', { name: 'Práctica terminada' })).toBeFocused();
  await page.getByRole('button', { name: 'Ver informe' }).click();
  await expect(page.getByTestId('report-pending')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Informe de práctica' })).toBeVisible({ timeout: 5000 });
  await expect(page.getByTestId('top3').locator('li')).toHaveCount(3);
  await expect(page.getByTestId('improved').first()).toContainText('[FALTA: un número concreto]');
  // v1.1: muletillas con su fuente y pausas largas del STT.
  await expect(page.getByTestId('turn-speech').first()).toContainText('detectadas por el reconocimiento de voz');
  await expect(page.getByTestId('turn-speech').first()).toContainText('Pausas largas: 1');
  await expectNoA11yViolations(page);
  expect(cspErrors).toEqual([]);
  const end = await mockState(request);
  expect(end.sessions[0]?.helloSeqs).toEqual([0]);
  expect(end.sessions[0]?.seqErrors).toBe(0);
});

test('voz: corte del WS durante la respuesta; el audio pendiente se reenvía tras el resume', async ({ page, request }) => {
  await startSession(page, { input: 'voice', count: 1, followups: 0 });
  await expect(page.getByTestId('tts-toggle')).toHaveText('Escuchar de nuevo');
  const toggle = page.getByTestId('answer-toggle');
  await toggle.click();
  await page.waitForTimeout(700);
  expect((await request.post(`${MOCK}/__mock/drop-ws`)).ok()).toBe(true);
  await expect(page.getByTestId('conn')).toContainText('Reconectando');
  await expect(page.getByTestId('conn')).toContainText('Conectada');
  await page.waitForTimeout(500);
  await toggle.click();
  await expect(page.getByRole('heading', { name: 'Práctica terminada' })).toBeVisible();
  const st = await mockState(request);
  // ~1,2 s grabados en total: si se hubiera perdido el audio del corte, serían < 700 ms.
  expect(st.sessions[0]?.turns[0]?.duration_ms ?? 0).toBeGreaterThan(900);
  // v1.1: los dos hello (inicial y de reanudación) van con seq 0 y el reenvío no generó huecos.
  expect(st.sessions[0]?.helloSeqs).toEqual([0, 0]);
  expect(st.sessions[0]?.seqErrors).toBe(0);
});

test('texto: respuesta con número no repregunta; saltar; fin automático', async ({ page }) => {
  await startSession(page, { input: 'text', count: 2, followups: 2 });
  await page.getByLabel('Esta sesión es por escrito: escribí tu respuesta.').fill('Trabajé 3 años en Telecom con 12 servicios.');
  await page.getByRole('button', { name: 'Enviar texto' }).click();
  await expect(page.getByTestId('transcript')).toContainText('Trabajé 3 años en Telecom');
  await expect(page.getByTestId('question')).toContainText('trabajando actualmente');
  await page.getByRole('button', { name: 'Saltar pregunta' }).click();
  await expect(page.getByRole('heading', { name: 'Práctica terminada' })).toBeVisible();
  await page.getByRole('button', { name: 'Ver informe' }).click();
  await expect(page.getByRole('heading', { name: 'Informe de práctica' })).toBeVisible({ timeout: 5000 });
  await expect(page.getByTestId('overall')).toContainText('de 5');
  await expect(page.getByTestId('faltantes')).toContainText('[FALTA] dim_means.estructura');
});

test('push-to-talk con la barra espaciadora', async ({ page, request }) => {
  await startSession(page, { input: 'voice', capture: 'ptt', count: 1, followups: 0 });
  await expect(page.getByTestId('tts-toggle')).toHaveText('Escuchar de nuevo');
  await page.getByRole('heading', { name: 'Práctica en curso' }).click();
  await page.keyboard.down('Space');
  await expect(page.getByTestId('ptt')).toHaveAttribute('aria-pressed', 'true');
  await page.waitForTimeout(1200);
  await page.keyboard.up('Space');
  await expect(page.getByRole('heading', { name: 'Práctica terminada' })).toBeVisible();
  const st = await mockState(request);
  expect(st.sessions[0]?.turns[0]?.duration_ms ?? 0).toBeGreaterThan(800);
});

test('reconexión con resume tras un corte 4008, y pausa/reanudar', async ({ page, request }) => {
  await startSession(page, { input: 'text', count: 2, followups: 0 });
  const before = await mockState(request);
  expect((await request.post(`${MOCK}/__mock/drop-ws`)).ok()).toBe(true);
  await expect(page.getByTestId('conn')).toContainText('Reconectando');
  await expect(page.getByTestId('conn')).toContainText('Conectada');
  const after = await mockState(request);
  expect(after.sessions[0]?.status).toBe('live');
  expect(after.sessions[0]?.lastClientSeq).toBeGreaterThanOrEqual(before.sessions[0]?.lastClientSeq ?? 0);
  // La misma pregunta sigue vigente y no se duplicó en la transcripción.
  // (presentación + primera pregunta)
  await expect(page.getByTestId('transcript').locator('li.tr-interviewer')).toHaveCount(2);
  expect(after.sessions[0]?.helloSeqs).toEqual([0, 0]);

  await page.getByRole('button', { name: 'Pausar sesión' }).click();
  await expect(page.getByTestId('answer-status')).toHaveText('Sesión en pausa.');
  await page.getByRole('button', { name: 'Reanudar sesión' }).click();
  await expect(page.getByText('Repetición')).toBeVisible();
  await page.getByLabel('Esta sesión es por escrito: escribí tu respuesta.').fill('Hace 2 años lideré un equipo de 4.');
  await page.getByRole('button', { name: 'Enviar texto' }).click();
  await expect(page.getByTestId('question')).toContainText('trabajando actualmente');
});

test('ventana de 96 KB liberada solo por mensajes ack (sin parciales): 4,5 s de audio llegan completos', async ({ page, request }) => {
  await mockConfig(request, { partials: false });
  await startSession(page, { input: 'voice', count: 1, followups: 0 });
  await expect(page.getByTestId('tts-toggle')).toHaveText('Escuchar de nuevo');
  const toggle = page.getByTestId('answer-toggle');
  await toggle.click();
  await page.waitForTimeout(4500);
  await toggle.click();
  await expect(page.getByRole('heading', { name: 'Práctica terminada' })).toBeVisible();
  const st = (await mockState(request)).sessions[0];
  // 96 000 B = 3 s de PCM16 a 16 kHz: sin acks el cliente se habría frenado a los 3 s.
  expect(st?.turns[0]?.duration_ms ?? 0).toBeGreaterThan(4000);
  expect(st?.acks ?? 0).toBeGreaterThan(5);
  expect(st?.seqErrors).toBe(0);
});

test('tts_delivery=none: sin audio ni tts.played; se responde por escrito', async ({ page, request }) => {
  await mockConfig(request, { tts: 'none' });
  await startSession(page, { input: 'text', count: 1, followups: 0 });
  await expect(page.getByTestId('tts-toggle')).toHaveCount(0);
  await page.getByLabel('Esta sesión es por escrito: escribí tu respuesta.').fill('Trabajé 3 años en Telecom.');
  await page.getByRole('button', { name: 'Enviar texto' }).click();
  await expect(page.getByRole('heading', { name: 'Práctica terminada' })).toBeVisible();
  expect((await mockState(request)).sessions[0]?.unexpectedPlayed).toBe(0);
});

test('repetir (mismo turno, repeat=true) y NO_AUDIO (turno nuevo, repeat=true)', async ({ page, request }) => {
  await startSession(page, { input: 'voice', count: 1, followups: 0 });
  await expect(page.getByTestId('tts-toggle')).toHaveText('Escuchar de nuevo');
  await page.getByRole('button', { name: 'Repetir pregunta' }).click();
  await expect(page.getByText(/Repetición/)).toBeVisible();
  await expect(page.getByTestId('tts-toggle')).toHaveText('Escuchar de nuevo');
  // Respuesta casi vacía → NO_AUDIO, turn.end next=repeat y la misma pregunta con turn_id nuevo.
  const toggle = page.getByTestId('answer-toggle');
  await toggle.click();
  await toggle.click();
  await expect(page.getByTestId('session-error')).toContainText('No se escuchó tu respuesta');
  await expect(page.getByText(/Repetición/)).toBeVisible();
  await expect(page.getByTestId('tts-toggle')).toHaveText('Escuchar de nuevo');
  await toggle.click();
  await page.waitForTimeout(800);
  await toggle.click();
  await expect(page.getByRole('heading', { name: 'Práctica terminada' })).toBeVisible();
  const st = (await mockState(request)).sessions[0];
  expect(st?.turns.map((t) => t.turn_id)).toEqual(['t-2']);
});
