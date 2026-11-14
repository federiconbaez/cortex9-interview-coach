// Levanta el backend Go REAL (más su servicio de voz mock) para `npm run test:e2e:real`.
// No forma parte de `test:e2e` ni del CI: necesita Go y el módulo del backend en ../backend.
// Compila a un directorio temporal (nada se escribe en backend/) y usa una SQLite temporal.
import { execFileSync, spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

const HTTP = process.env.REAL_HTTP_ADDR ?? '127.0.0.1:18580';
const SPEECH = process.env.REAL_SPEECH_ADDR ?? '127.0.0.1:18590';
const ORIGIN = process.env.REAL_ORIGIN ?? 'http://127.0.0.1:4174';
const PASSWORD = process.env.REAL_PASSWORD ?? 'practica';

const dir = mkdtempSync(join(tmpdir(), 'cx9-fe-real-'));
const backendDir = resolve(import.meta.dirname, '..', '..', 'backend');
const bin = process.env.CX9_COACH_BIN ?? join(dir, 'cx9-fe-real-coach');
if (!process.env.CX9_COACH_BIN) {
  console.log(`[real] go build ./cmd/coach → ${bin}`);
  execFileSync('go', ['build', '-o', bin, './cmd/coach'], { cwd: backendDir, stdio: 'inherit', env: { ...process.env, GOFLAGS: '-mod=readonly' } });
}
const hash = execFileSync(bin, ['hash-password'], { input: PASSWORD }).toString().trim();

const base = { ...process.env, CX9_LOG_FORMAT: 'text' };
const children = [
  spawn(bin, ['speech-mock'], { cwd: dir, stdio: 'inherit', env: { ...base, CX9_SPEECH_MOCK_ADDR: SPEECH } }),
  spawn(bin, ['serve'], {
    cwd: dir,
    stdio: 'inherit',
    env: {
      ...base,
      CX9_HTTP_ADDR: HTTP,
      CX9_DATABASE_URL: `sqlite://${join(dir, 'cx9.db')}`,
      CX9_DATA_DIR: join(dir, 'data'),
      CX9_PASSWORD_HASH: hash,
      CX9_CORS_ORIGINS: ORIGIN,
      CX9_SPEECH_URL: `http://${SPEECH}`,
      CX9_LLM_PROVIDER: 'mock',
    },
  }),
];
let stopping = false;
function stop(code = 0) {
  if (stopping) return;
  stopping = true;
  for (const c of children) c.kill('SIGTERM');
  setTimeout(() => {
    rmSync(dir, { recursive: true, force: true });
    process.exit(code);
  }, 500);
}
for (const c of children) c.on('exit', (code) => { if (!stopping) { console.error(`[real] proceso terminó (${code})`); stop(1); } });
process.on('SIGTERM', () => stop());
process.on('SIGINT', () => stop());
