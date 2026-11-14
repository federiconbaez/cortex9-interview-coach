import { defineConfig, devices } from '@playwright/test';

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
        launchOptions: {
          args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream', '--autoplay-policy=no-user-gesture-required'],
        },
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
