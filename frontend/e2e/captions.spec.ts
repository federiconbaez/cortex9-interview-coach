import { expect, test, type APIRequestContext, type Page } from '@playwright/test';
import { expectNoA11yViolations } from './helpers';

// "Subtítulos en vivo" contra mock-server/captions.mjs (contrato WS v0.2). Sin login ni API del coach.
const CAP = 'http://127.0.0.1:18791';

async function capState(request: APIRequestContext) {
  return (await (await request.get(`${CAP}/__mock/state`)).json()) as {
    connections: number;
    busyRejected: number;
    connected: boolean;
    applied: { device_id: string; translation: { enabled: boolean; provider: string; target_lang: string; remote: boolean } };
    received: { type: string; seq?: number }[];
  };
}
const post = async (request: APIRequestContext, path: string, data: unknown = {}) => expect((await request.post(`${CAP}${path}`, { data })).ok()).toBe(true);

async function openCaptions(page: Page) {
  const v1: string[] = [];
  page.on('request', (r) => {
    if (new URL(r.url()).pathname.startsWith('/v1/')) v1.push(r.url());
  });
  await page.goto('/#/subtitulos');
  await expect(page.getByRole('heading', { name: 'Subtítulos en vivo', level: 2 })).toBeVisible();
  await expect(page.getByTestId('captions-conn')).toContainText('Conectado');
  return v1;
}

test.beforeEach(async ({ request }) => {
  await post(request, '/__mock/reset');
});

test('subtítulos: parciales que se reemplazan, finales, idioma detectado, sin API del coach, axe', async ({ page, request }) => {
  const v1 = await openCaptions(page);
  const list = page.getByTestId('captions-list');
  await expect(page.getByTestId('cap-final')).toHaveCount(3, { timeout: 10_000 });
  await expect(page.getByTestId('cap-final').nth(0)).toContainText('Hello everyone, welcome to the meeting.');
  await expect(page.getByTestId('cap-final').nth(0)).toContainText('Idioma detectado: inglés (98 %)');
  await expect(page.getByTestId('cap-final').nth(1)).toContainText('Today we review the roadmap.');
  await expect(page.getByTestId('cap-final').nth(2)).toContainText('Hola a todos.');
  await expect(page.getByTestId('cap-partial')).toHaveCount(0); // el parcial tardío de seg-1 se ignoró
  await expect(list).not.toContainText('tardío');
  await expect(page.getByTestId('cap-translation')).toHaveCount(0);
  // aria-live solo con el último final
  await expect(page.getByTestId('captions-live')).toHaveText('Hola a todos.');
  await expect(page.getByTestId('capture-state')).toHaveText('Escuchando');
  await expectNoA11yViolations(page);
  expect(v1).toEqual([]);
  // Nada se persiste en el navegador.
  const stored = await page.evaluate(async () => ({
    local: Object.keys(localStorage),
    session: Object.keys(sessionStorage),
    idb: (await (globalThis as unknown as { indexedDB: { databases(): Promise<{ name?: string }[]> } }).indexedDB.databases()).map((d) => d.name),
  }));
  expect(stored.local.filter((k) => k !== 'cx9.lang')).toEqual([]);
  expect(stored.session).toEqual([]);
  expect(stored.idb).toEqual([]);
  // seq de los mensajes del cliente (supuesto): sin mensajes todavía, salvo pings
  expect((await capState(request)).connections).toBe(1);
});

test('traducción local, luego remota con confirmación y aviso persistente; la UI sigue a config.applied', async ({ page, request }) => {
  await openCaptions(page);
  await expect(page.getByTestId('remote-hint')).toContainText('El audio nunca sale de tu PC');
  const provider = page.getByLabel('Proveedor de traducción');
  await expect(provider.locator('option', { hasText: 'NLLB' })).toBeDisabled();
  await expect(provider.locator('option', { hasText: 'LLM remoto' })).toContainText('(remoto: el texto sale de tu PC)');

  await provider.selectOption('argos');
  await expect(provider).toHaveValue('argos');
  await expect(page.getByLabel('Traducir los subtítulos')).toBeChecked();
  // Francés no está en los idiomas de Argos.
  await expect(page.getByLabel('Idioma de destino').locator('option[value="fr"]')).toBeDisabled();
  await expect(page.getByTestId('remote-notice')).toHaveCount(0);
  await expect(page.getByTestId('cap-translation').first()).toHaveText(/Hola a todos, bienvenidos a la reunión\./, { timeout: 10_000 });

  await provider.selectOption('llm');
  const confirm = page.getByTestId('remote-confirm');
  await expect(confirm).toBeVisible();
  await expect(confirm).toBeFocused();
  await expect(provider).toHaveValue('argos'); // todavía no se mandó nada
  await expectNoA11yViolations(page);
  await page.getByRole('button', { name: 'Cancelar' }).click();
  await expect(confirm).toHaveCount(0);
  expect((await capState(request)).applied.translation.provider).toBe('argos');

  await provider.selectOption('llm');
  await page.getByRole('button', { name: 'Sí, enviar el texto' }).click();
  await expect(page.getByTestId('remote-notice')).toContainText('el TEXTO de los subtítulos sale de tu PC hacia LLM remoto');
  await expect(page.getByTestId('remote-notice')).toContainText('El audio nunca sale de tu PC');
  await expect(provider).toHaveValue('llm');
  await expect(page.getByTestId('remote-notice').getByRole('button')).toHaveCount(0); // no se puede cerrar
  await page.getByLabel('Idioma de destino').selectOption('fr');
  await expect(page.getByLabel('Idioma de destino')).toHaveValue('fr');
  await expect(page.getByTestId('remote-notice')).toBeVisible();
  const st = await capState(request);
  expect(st.applied.translation).toEqual({ enabled: true, target_lang: 'fr', provider: 'llm', remote: true });
  // Cada config lleva el estado completo; seq del cliente desde 1 (supuesto documentado).
  const cfgs = st.received.filter((m) => m.type === 'config');
  expect(cfgs.length).toBe(3);
  const seqs = st.received.map((m) => m.seq);
  expect(seqs).toEqual(seqs.map((_, i) => i + 1));
});

test('dispositivos: cambio confirmado, DEVICE_NOT_FOUND mantiene el anterior, lista actualizada por el servidor', async ({ page, request }) => {
  await openCaptions(page);
  const device = page.getByLabel('Dispositivo de captura');
  await expect(device).toHaveValue('monitor-0');
  await device.selectOption('mic-0');
  await expect(device).toHaveValue('mic-0');
  await post(request, '/__mock/drop-device', { id: 'monitor-0' });
  await device.selectOption('monitor-0');
  await expect(page.getByTestId('captions-error')).toContainText('Ese dispositivo ya no existe. Se mantiene el anterior.');
  await expect(device).toHaveValue('mic-0');
  await expect(page.getByTestId('captions-pending')).toHaveCount(0);
  await post(request, '/__mock/add-device');
  await expect(device.locator('option[value="monitor-usb"]')).toHaveText('Monitor of USB Headset (audio del sistema)');
});

test('pausa/reanudar, sin audio, fallo de captura y nivel', async ({ page, request }) => {
  await openCaptions(page);
  const state = page.getByTestId('capture-state');
  await page.getByRole('button', { name: 'Pausar subtítulos' }).click();
  await expect(state).toHaveText('En pausa');
  await page.getByRole('button', { name: 'Reanudar subtítulos' }).click();
  await expect(state).toHaveText('Escuchando');
  await expect(page.getByTestId('level')).toHaveAttribute('value', /^-(18|19|20)$/);
  await post(request, '/__mock/capture', { state: 'no_audio' });
  await expect(state).toHaveText('Sin audio');
  await post(request, '/__mock/capture', { state: 'failed' });
  await expect(state).toHaveText('Error de captura');
  await expect(page.getByTestId('captions-error')).toContainText('No se pudo capturar el audio del sistema');
  await expect(page.getByTestId('captions-error').getByRole('button')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Pausar subtítulos' })).toBeDisabled();
});

test('reconexión tras 1011 sin replay (config = la última aplicada) y 4409 sin reintento automático', async ({ page, request, context }) => {
  await openCaptions(page);
  await page.getByLabel('Proveedor de traducción').selectOption('argos');
  await expect(page.getByLabel('Proveedor de traducción')).toHaveValue('argos');
  await expect(page.getByTestId('cap-final')).toHaveCount(3, { timeout: 10_000 });
  await post(request, '/__mock/close', { code: 1011 });
  await expect(page.getByTestId('captions-conn')).toContainText('Reconectando');
  await expect(page.getByTestId('captions-conn')).toContainText('Conectado');
  // Conexión nueva: los segmentos anteriores siguen en pantalla y llegan los nuevos (mismos ids, otra conexión).
  await expect(page.getByTestId('cap-final')).toHaveCount(6, { timeout: 10_000 });
  await expect(page.getByLabel('Proveedor de traducción')).toHaveValue('argos');
  expect((await capState(request)).connections).toBe(2);

  // Segunda pestaña: el servicio admite un solo cliente → 4409, mensaje claro y sin reintentos.
  const other = await context.newPage();
  await other.goto('/#/subtitulos');
  await expect(other.getByTestId('captions-closed')).toContainText('ya está en uso en otra pestaña');
  await other.waitForTimeout(2500);
  expect((await capState(request)).busyRejected).toBe(1);
  await page.close();
  await other.getByRole('button', { name: 'Reintentar' }).click();
  await expect(other.getByTestId('captions-conn')).toContainText('Conectado');
});

test('tamaño de letra, inglés y enlace desde la app', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('link', { name: 'Subtítulos en vivo' }).click();
  await expect(page.getByTestId('captions-conn')).toContainText('Conectado');
  await page.getByLabel('Tamaño del texto').selectOption('200');
  await expect(page.getByTestId('captions-list')).toHaveAttribute('style', /font-size: 200%/);
  await page.getByLabel('Idioma de la interfaz y de la entrevista').selectOption('en');
  await expect(page.getByRole('heading', { name: 'Live captions', level: 2 })).toBeVisible();
  await expect(page.getByTestId('captions-scope')).toContainText('does not suggest answers');
  await page.getByRole('link', { name: 'Back to interview practice' }).click();
  await expect(page).toHaveURL(/#\/$/);
});
