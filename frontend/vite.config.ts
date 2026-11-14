/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Backend real (Go) en 127.0.0.1:8080 (§2.5). Para usar el mock: COACH_BACKEND_URL=http://127.0.0.1:8787.
const backend = process.env.COACH_BACKEND_URL ?? 'http://127.0.0.1:8080';
// §2.5: proxy de Vite para /v1 (incluido el WebSocket) → todo same-origin, sin CORS.
// Servicio de subtítulos (Apex, no pasa por el backend de Nexus): nginx lo expone same-origin en
// /captions/v1/live y el servicio escucha en ws://127.0.0.1:8091/v1/captions/live.
// Para el mock: COACH_CAPTIONS_URL=http://127.0.0.1:18791.
const captions = process.env.COACH_CAPTIONS_URL ?? 'http://127.0.0.1:8091';
const proxy = {
  '/v1': { target: backend, changeOrigin: false, ws: true },
  '/captions': {
    target: captions,
    changeOrigin: false,
    ws: true,
    rewrite: (p: string) => p.replace(/^\/captions\/v1\/live/, '/v1/captions/live'),
  },
};

// Misma CSP que sirve deploy/frontend/nginx.conf (Apex), aplicada a `vite preview` para que los
// e2e detecten si algo del frontend la rompería (p. ej. un AudioWorklet inline como data:).
const csp =
  "default-src 'self'; connect-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; " +
  "style-src 'self' 'unsafe-inline'; worker-src 'self' blob:; frame-ancestors 'none'";

export default defineConfig({
  plugins: [react()],
  server: { host: '127.0.0.1', port: 5173, strictPort: true, proxy },
  preview: { host: '127.0.0.1', port: 4173, strictPort: true, proxy, headers: { 'Content-Security-Policy': csp } },
  build: { sourcemap: false, target: 'es2022' },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'json-summary'],
      include: ['src/**/*.{ts,tsx}'],
      exclude: ['src/**/*.test.{ts,tsx}', 'src/test/**', 'src/main.tsx', 'src/vite-env.d.ts'],
    },
  },
});
