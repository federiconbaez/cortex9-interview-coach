import { defineConfig, devices } from '@playwright/test';

const MOCK = 'http://127.0.0.1:8787';

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  forbidOnly: true,
  retries: 0,
  reporter: [['list']],
  timeout: 30_000,
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'off',
    locale: 'es-AR',
    timezoneId: 'America/Argentina/Buenos_Aires',
  },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        permissions: ['microphone'],
        launchOptions: {
          args: [
            '--use-fake-device-for-media-stream',
            '--use-fake-ui-for-media-stream',
            '--autoplay-policy=no-user-gesture-required',
          ],
        },
      },
    },
  ],
  webServer: [
    {
      command: 'node mock-server/server.mjs',
      url: `${MOCK}/v1/health`,
      reuseExistingServer: false,
      env: { MOCK_PORT: '8787' },
    },
    {
      command: 'node mock-server/captions.mjs',
      url: 'http://127.0.0.1:18791/health',
      reuseExistingServer: false,
      env: { CAPTIONS_MOCK_PORT: '18791' },
    },
    {
      command: 'npx vite build && npx vite preview --port 4173 --strictPort',
      url: 'http://127.0.0.1:4173',
      reuseExistingServer: false,
      timeout: 120_000,
      env: { COACH_BACKEND_URL: MOCK, COACH_CAPTIONS_URL: 'http://127.0.0.1:18791' },
    },
  ],
});
