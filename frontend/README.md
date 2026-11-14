# Frontend — Coach de entrevistas (CORTEX-9)

App web para **practicar** entrevistas por voz. La app hace de entrevistadora: hace la
pregunta (texto + voz), escucha **tu** respuesta, repregunta y al final muestra un informe.

> **Solo práctica.** La app nunca escucha a un entrevistador real ni sugiere respuestas en
> vivo. El micrófono se abre únicamente durante tu turno de respuesta dentro de una sesión de
> práctica. No la uses durante una entrevista real. (El mismo aviso aparece en la UI:
> clave `app.ethics` en `src/i18n/es.ts` y `src/i18n/en.ts`.)

Ref: CX9-6174. Contrato: `../docs/contract.md`, `../docs/openapi.yaml`, `../docs/ws-types.ts`.

## Arquitectura

```
frontend/
├── src/
│   ├── api/
│   │   ├── contract/ws-types.ts   copia textual de docs/ws-types.ts (no se edita)
│   │   ├── contract/openapi.ts    generado de docs/openapi.yaml (npm run gen:api)
│   │   ├── schema.ts              alias con nombre de los esquemas OpenAPI
│   │   ├── client.ts              cliente REST /v1 (cookie cx9_session, problem+json, ETag, Idempotency-Key)
│   │   └── ws.ts                  LiveConnection: WebSocket /v1/sessions/{id}/live
│   ├── audio/
│   │   ├── mic.ts                 getUserMedia + AudioWorklet → PCM16LE 16 kHz, chunks de 100 ms
│   │   ├── pcm.ts                 re-muestreo, conversión a PCM16, chunker, cabecera WAV
│   │   ├── vad.ts                 VAD por energía (ver docs/VAD.md)
│   │   └── tts.ts                 ensamblado de frames TTS, sha256, reproductor con pausa
│   ├── state/                     reducer de la sesión en vivo, hook useLiveSession, perfil, historial
│   ├── components/                vistas: práctica, informe, historial, perfil, login
│   ├── i18n/                      es.ts (fuente de claves) y en.ts (mismo tipo)
│   └── App.tsx                    auth, navegación, idioma, foco
├── public/capture-worklet.js      procesador del AudioWorklet (archivo aparte por la CSP)
├── mock-server/server.mjs         mock del contrato para los e2e (no es el backend real)
├── e2e/                           Playwright
└── docs/                          VAD.md, ACCESIBILIDAD.md
```

- **Todo same-origin.** La app llama exactamente a `/v1/...`. En desarrollo, el proxy de
  Vite reenvía `/v1` (HTTP y WebSocket, `ws: true`) al backend en `http://127.0.0.1:8080`
  (se puede cambiar con `COACH_BACKEND_URL`). En deploy, `deploy/frontend/nginx.conf` hace lo
  mismo. No hace falta CORS.
- **Flujo en vivo.** `POST /v1/sessions` → `live_url` → WebSocket con subprotocolo
  `cx9.live.v1` → `hello` → `session.ready` → `question`/`followup` + `tts.audio` (frames
  binarios kind 0x02) o `tts.url` → el usuario responde (frames binarios kind 0x01 + `audio.end`,
  o `answer.text` en sesiones por escrito) → `transcript.partial/final` → `turn.end` →
  `session.end` → `GET /v1/sessions/{id}/report` (202 con `Retry-After` hasta que esté).
- **Conexión.** `seq` por dirección (los frames binarios consumen seq), `ack` recorta lo
  pendiente, ventana `max_inflight_bytes` + `bufferedAmount` (el audio espera, no se descarta),
  ping cada `heartbeat_ms`, reconexión si no llega nada en `idle_timeout_ms`, backoff
  0,5/1/2/4/8 s con jitter dentro de 120 s, `resume.last_server_seq` y reenvío de lo que el
  servidor no recibió. Los cierres 1000, 4001, 4003, 4004, 4009 y 4010 no se reintentan.
- **Build:** `dist/` (lo copia `deploy/frontend/Dockerfile`).

## Cómo correr

Requisitos: Node 22 (el que usa CI; también probado con Node 24).

```bash
cd frontend
npm ci
npm run dev                 # http://127.0.0.1:5173, proxy /v1 → 127.0.0.1:8080 (backend real)
```

Sin backend, contra el mock del contrato:

```bash
npm run mock                                              # mock en 127.0.0.1:8787, contraseña: practica
COACH_BACKEND_URL=http://127.0.0.1:8787 npm run dev
```

`deploy/local/run-frontend.sh` (de deploy) ya exporta `COACH_BACKEND_URL` al puerto del backend.

Otros scripts: `npm run build` (typecheck + build a `dist/`), `npm run preview`,
`npm run gen:api` (regenera `src/api/contract/openapi.ts` desde `../docs/openapi.yaml`).

## Cómo testear

```bash
npm run typecheck
npm run lint                     # eslint, 0 warnings permitidos
npm test                         # Vitest + cobertura v8 (jsdom, sin red)
npx playwright install chromium  # una vez
npm run test:e2e                 # Playwright contra el mock, Chromium con medios falsos
```

Los e2e levantan el mock en `127.0.0.1:8787` y `vite build && vite preview` en
`127.0.0.1:4173` con la misma CSP que `deploy/frontend/nginx.conf`. Chromium se lanza con
`--use-fake-device-for-media-stream` y `--use-fake-ui-for-media-stream`. Ningún test usa red
externa. `forbidOnly` está activo y no hay tests `skip`/`todo`.

### Comandos de CI (`.github/workflows/ci.yml`, de Apex; no se edita desde acá)

| Job | Comandos | Dónde |
|---|---|---|
| frontend | `npm ci --no-audit --no-fund`, `npm run lint --if-present`, `npm run typecheck --if-present`, `npm test --if-present` | `frontend/`, Node 22 |
| e2e | `npm ci`, `npx playwright install --with-deps chromium`, `npm run test:e2e` (si existe `frontend/playwright.config.*`) | `frontend/` |

## Resultados medidos

Corrida desde un clon limpio del commit de código `ea0c06b445951deb91fd69d0ee8f5421ee2d0c1d`,
con Node v22.23.5, el 14/11/2026:

- `npm ci`: 267 paquetes.
- `npm run lint` y `npm run typecheck`: sin errores ni warnings.
- `npm test`: **13 archivos, 94 tests, 94 pasan**, 0 omitidos.
  Cobertura: sentencias 91,18 % (1282/1406), ramas 80,3 % (787/980), funciones 82,39 % (323/392),
  líneas 94,46 % (1127/1193).
- `npm run test:e2e`: **10 tests, 10 pasan** (20,5 s); 4 de ellos hacen en total 6 chequeos axe (WCAG 2.0/2.1 A y AA).
- `npm run build`: `dist/` = 233 105 bytes (JS 226 887 B, 71,90 kB gzip; CSS 5 374 B;
  `capture-worklet.js` 381 B; `index.html` 463 B).

Tests unitarios por archivo: ws 19, vad 9, views (SetupForm, ReportView, HistoryView, perfil) 9,
live 8, pcm 7, client 7, App 6, SessionView 6, tts 6, i18n 6, history 4, profile 4, mic 3.

## Qué está probado y qué no

Probado:

- VAD con PCM sintético (silencio, ruido, clics, pausas, ruido fuerte que sube el umbral).
- Re-muestreo 48 kHz → 16 kHz sin perder muestras entre bloques; chunks de 3200 bytes; flag de último chunk.
- WebSocket con socket y reloj falsos: hello, seq compartido JSON/binario, ack, ventana de bytes,
  `bufferedAmount`, ping/pong, backoff, gracia de 120 s, cierres terminales, resume con reenvío
  ordenado, deduplicación, mensajes inválidos o de tipo desconocido.
- Frames binarios: `encodeFrame`/`decodeFrame` de `ws-types.ts`.
- TTS: ensamblado, errores de orden o tamaño, PCM → WAV, sha256 y reproductor.
- Cliente REST con `fetch` falso: Idempotency-Key, filtros, 202/409 del informe, 404/ETag/412 del perfil, errores.
- i18n: mismas claves en es/en, sin textos vacíos, mismos placeholders, sin texto visible
  hardcodeado en JSX (análisis del AST de TypeScript) y sin claves sin usar.
- Componentes con Testing Library: sesión por voz, push-to-talk, permiso de micrófono denegado,
  sesión por texto, controles, errores, reconexión; formulario; informe con 202; historial; perfil.
- e2e: login, i18n, aviso de proveedor remoto, voz con VAD y botón, push-to-talk, corte del WS
  durante una respuesta con reenvío del audio, reconexión 4008 + pausa, historial, perfil con
  ETag y axe. El e2e de voz corre con la CSP de deploy y verifica que no haya errores de CSP en consola.

No probado:

- **Contra el backend real de Go** (Nexus): solo contra `mock-server/`, que es mi lectura del contrato.
- Navegadores distintos de Chromium (Firefox, Safari).
- El corte automático por silencio con un micrófono real: el dispositivo falso de Chromium emite
  un tono continuo, así que en e2e la respuesta se termina con el botón.
- Opus y `live-ticket` (no se usan: PCM16 16 kHz es el formato base y la cookie alcanza con el proxy same-origin).
- Contraste en modo oscuro: axe corre con el esquema claro.

## Supuestos sobre el contrato

Están marcados en el código y en el informe de la entrega; los principales:

- `answer.text` solo en sesiones `input: text`.
- Las preguntas `kind: intro` y `closing` no se responden.
- `tts.played` se manda una vez por `utterance_id`, también si el usuario empieza a responder antes de que termine.
- `sha256` de `tts.audio` se calcula sobre los bytes concatenados tal como llegan.
- El cierre 4400 se reintenta salvo que antes llegue un `error` fatal no reintentable.
- Repetir y saltar solo desde `asking`/`listening` (diagrama de estados del contrato).
