import { expect, test } from '@playwright/test';
import { PASSWORD, expectNoA11yViolations, login, resetMock } from './helpers';

test.beforeEach(async ({ request }) => resetMock(request));

test('login: contraseña incorrecta, luego correcta; sin violaciones de accesibilidad', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Ingresar' })).toBeVisible();
  await expectNoA11yViolations(page);
  await page.getByLabel('Contraseña').fill('incorrecta');
  await page.getByRole('button', { name: 'Ingresar' }).click();
  await expect(page.getByRole('alert')).toHaveText('Contraseña incorrecta.');
  await page.getByLabel('Contraseña').fill(PASSWORD);
  await page.getByRole('button', { name: 'Ingresar' }).click();
  await expect(page.getByRole('heading', { name: 'Nueva práctica' })).toBeVisible();
  await expectNoA11yViolations(page);
  await page.getByRole('button', { name: 'Salir' }).click();
  await expect(page.getByRole('heading', { name: 'Ingresar' })).toBeVisible();
});

test('i18n: cambiar a inglés traduce la UI y el atributo lang', async ({ page }) => {
  await login(page);
  await page.goto('/');
  await expect(page.locator('html')).toHaveAttribute('lang', 'es');
  await page.getByLabel('Idioma de la interfaz y de la entrevista').selectOption('en');
  await expect(page.getByRole('heading', { name: 'New practice' })).toBeVisible();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await expect(page.getByLabel('AI screening (mock)')).toBeVisible();
  await expect(page.getByRole('note').first()).toContainText('never listens to a real interviewer');
  await page.reload();
  await expect(page.getByRole('heading', { name: 'New practice' })).toBeVisible();
});

test('aviso de proveedor remoto y enlace para saltar al contenido', async ({ page }) => {
  await login(page);
  await page.goto('/');
  await page.getByLabel('Entrevistadora (proveedor LLM)').selectOption('openai');
  await expect(page.getByText('el texto de tus respuestas sale de tu computadora')).toBeVisible();
  await page.keyboard.press('Tab');
  await expect(page.getByRole('link', { name: 'Saltar al contenido' })).toBeFocused();
});
