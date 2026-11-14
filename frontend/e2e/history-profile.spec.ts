import { expect, test } from '@playwright/test';
import { expectNoA11yViolations, login, resetMock } from './helpers';

test('historial: gráfico de progreso accesible y apertura de un informe', async ({ page, request }) => {
  await resetMock(request, true);
  await login(page);
  await page.goto('/');
  await page.getByRole('button', { name: 'Historial' }).click();
  await expect(page.getByRole('button', { name: 'Historial' })).toHaveAttribute('aria-current', 'page');
  const chart = page.getByRole('img', { name: /Puntaje general a lo largo del tiempo/ });
  await expect(chart).toBeVisible();
  await expect(page.getByTestId('chart').locator('circle')).toHaveCount(3);
  await expect(page.locator('figcaption')).toContainText('Mejoraste');
  await expect(page.getByTestId('history-table').locator('tbody tr')).toHaveCount(3);
  await expectNoA11yViolations(page);
  await page.getByRole('button', { name: /Ver informe/ }).first().click();
  await expect(page.getByRole('heading', { name: 'Informe de práctica' })).toBeVisible();
});

test('perfil: validación con foco, guardado con ETag, recarga y borrado', async ({ page, request }) => {
  await resetMock(request);
  await login(page);
  await page.goto('/');
  await page.getByRole('button', { name: 'Perfil (modo historia)' }).click();
  await expect(page.getByTestId('gaps')).toContainText('recorrido profesional');
  await page.getByRole('button', { name: 'Agregar experiencia' }).click();
  await page.getByLabel('Período').fill('el año pasado');
  await page.getByRole('button', { name: 'Guardar' }).click();
  await expect(page.getByTestId('profile-msg')).toContainText('hay 2 errores');
  await expect(page.getByLabel('Empresa *')).toBeFocused();
  await page.getByLabel('Empresa *').fill('Telecom');
  await page.getByLabel('Período').fill('2021-2024');
  await page.getByLabel('Tecnologías').fill('Go, PostgreSQL, go');
  await page.getByRole('button', { name: 'Guardar' }).click();
  await expect(page.getByTestId('profile-msg')).toHaveText('Perfil guardado.');
  await expect(page.getByLabel('Tecnologías')).toHaveValue('Go, PostgreSQL');
  await expectNoA11yViolations(page);
  await page.reload();
  await page.getByRole('button', { name: 'Perfil (modo historia)' }).click();
  await expect(page.getByLabel('Empresa *')).toHaveValue('Telecom');
  await expect(page.getByTestId('gaps')).not.toContainText('recorrido profesional');
  await page.getByRole('button', { name: 'Borrar perfil' }).click();
  await expect(page.getByTestId('profile-msg')).toContainText('tocá de nuevo');
  await page.getByRole('button', { name: 'Borrar perfil' }).click();
  await expect(page.getByTestId('profile-msg')).toHaveText('Perfil borrado.');
  await expect(page.getByLabel('Empresa *')).toHaveCount(0);
});
