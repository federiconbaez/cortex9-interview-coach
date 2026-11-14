import { defineConfig, devices } from '@playwright/test';
import { writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

// Audio falso con voz y silencio (1 s de tono + 2,5 s de silencio, en bucle) para que el
// endpointing por silencio del backend (§5.15) pueda cerrar el turno solo.
function speechThenSilenceWav(): string {
  const rate = 48000;
  const n = Math.round(rate * 3.5);
  const b = Buffer.alloc(44 + n * 2);
  b.write('RIFF', 0);
  b.writeUInt32LE(36 + n * 2, 4);
  b.write('WAVEfmt ', 8);
  b.writeUInt32LE(16, 16);
  b.writeUInt16LE(1, 20);
  b.writeUInt16LE(1, 22);
  b.writeUInt32LE(rate, 24);
  b.writeUInt32LE(rate * 2, 28);
  b.writeUInt16LE(2, 32);
  b.writeUInt16LE(16, 34);
  b.write('data', 36);
  b.writeUInt32LE(n * 2, 40);
  for (let i = 0; i < rate; i += 1) b.writeInt16LE(Math.round(9000 * Math.sin((2 * Math.PI * 220 * i) / rate)), 44 + i * 2);
  const f = join(tmpdir(), 'cx9-fe-real-speech-silence.wav');
  writeFileSync(f, b);
  return f;
}
const ENDPOINT_WAV = speechThenSilenceWav();
const BASE_ARGS = ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream', '--autoplay-policy=no-user-gesture-required'];

// Proyecto aparte contra el backend Go REAL (CX9_LLM_PROVIDER=mock + `coach speech-mock`).
// `npm run test:e2e:real`. No lo usa `test:e2e` ni el CI: requiere Go y el código de ../backend.
const BACKEND = 'http://127.0.0.1:18580';

export default defineConfig({
  testDir: './e2e-real',
  fullyParallel: false,
  workers: 1,
  forbidOnly: true,
  retries: 0,
  reporter: [['list']],
  timeout: 60_000,
  use: {
    baseURL: 'http://127.0.0.1:4174',
    trace: 'off',
    locale: 'es-AR',
    timezoneId: 'America/Argentina/Buenos_Aires',
  },
  projects: [
    {
      name: 'real-backend',
      use: {
        ...devices['Desktop Chrome'],
        permissions: ['microphone'],
        launchOptions: { args: BASE_ARGS },
      },
      testIgnore: /endpoint\.spec\.ts/,
    },
    {
      name: 'real-backend-endpoint',
      testMatch: /endpoint\.spec\.ts/,
      use: {
        ...devices['Desktop Chrome'],
        permissions: ['microphone'],
        launchOptions: { args: [...BASE_ARGS, `--use-file-for-fake-audio-capture=${ENDPOINT_WAV}`] },
      },
    },
  ],
  webServer: [
    {
      command: 'node e2e-real/backend.mjs',
      url: `${BACKEND}/v1/health`,
      reuseExistingServer: false,
      timeout: 180_000,
      gracefulShutdown: { signal: 'SIGTERM', timeout: 5000 },
    },
    {
      command: 'npx vite build && npx vite preview --port 4174 --strictPort',
      url: 'http://127.0.0.1:4174',
      reuseExistingServer: false,
      timeout: 120_000,
      env: { COACH_BACKEND_URL: BACKEND },
    },
  ],
});
