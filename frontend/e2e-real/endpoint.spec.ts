import { expect, test } from '@playwright/test';

// §5.15 (v1.2) contra el backend real: el micrófono falso reproduce 1 s de tono y 2,5 s de
// silencio; sin tocar "Terminar respuesta", el servidor cierra el turno por silencio.
test('real · endpointing: el turno se cierra por silencio sin audio.end', async ({ page }) => {
  const sent: string[] = [];
  const received: string[] = [];
  page.on('websocket', (ws) => {
    ws.on('framesent', (f) => {
      if (typeof f.payload === 'string') sent.push(f.payload);
    });
    ws.on('framereceived', (f) => {
      if (typeof f.payload === 'string') received.push(f.payload);
    });
  });
  await page.goto('/');
  await page.getByLabel('Contraseña').fill('practica');
  await page.getByRole('button', { name: 'Ingresar' }).click();
  await page.getByLabel(/^Screening con IA/).check();
  await page.getByLabel('Cantidad de preguntas').fill('1');
  await page.getByLabel('Repreguntas por pregunta (máximo 2)').selectOption('0');
  await page.getByLabel('Por voz (micrófono)').check();
  await page.getByRole('button', { name: 'Empezar práctica' }).click();
  await expect(page.getByText('Pregunta 1 de 1')).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId('tts-toggle')).toHaveText('Escuchar de nuevo', { timeout: 15_000 });
  await expect(page.getByTestId('endpoint-hint')).toBeVisible();
  await page.getByTestId('answer-toggle').click();
  await expect(page.getByRole('heading', { name: 'Práctica terminada' })).toBeVisible({ timeout: 20_000 });
  const got = received.map((s) => JSON.parse(s) as { type: string; data: Record<string, unknown> });
  const out = sent.map((s) => JSON.parse(s) as { type: string; data: Record<string, unknown> });
  const ends = got.filter((m) => m.type === 'turn.end').map((m) => m.data.reason);
  console.log(`[real] turn.end=${ends.join(',')}`);
  expect(ends).toContain('endpoint');
  expect(out.filter((m) => m.type === 'audio.end')).toEqual([]);
  // transcript.final antes de turn.end
  const iFinal = got.findIndex((m) => m.type === 'transcript.final');
  const iEnd = got.findIndex((m) => m.type === 'turn.end' && m.data.reason === 'endpoint');
  expect(iFinal).toBeGreaterThanOrEqual(0);
  expect(iFinal).toBeLessThan(iEnd);
  expect(got.filter((m) => m.type === 'error').map((m) => m.data.code)).toEqual([]);
});
