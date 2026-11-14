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
├── mock-server/server.mjs         mock del contrato v1.1 para los e2e (no es el backend real)
├── e2e-real/                      e2e contra el backend Go real (`npm run test:e2e:real`)
├── e2e/                           Playwright
└── docs/                          VAD.md, ACCESIBILIDAD.md
```

- **Todo same-origin.** La app llama exactamente a `/v1/...`. En desarrollo, el proxy de
  Vite reenvía `/v1` (HTTP y WebSocket, `ws: true`) al backend en `http://127.0.0.1:8080`
  (se puede cambiar con `COACH_BACKEND_URL`). En deploy, `deploy/frontend/nginx.conf` hace lo
  mismo. No hace falta CORS.
- **Flujo en vivo.** `POST /v1/sessions` → `live_url` → WebSocket con subprotocolo
  `cx9.live.v1` → `hello` → `session.ready` → presentación (`kind: intro`, `turn_id` `t-0`,
  índice 0) → `question`/`followup` + `tts.audio` (wav o pcm16le, frames binarios kind 0x02) o
  `tts.url` → el usuario responde (frames binarios kind 0x01 + `audio.end`,
  o `answer.text` en sesiones por escrito) → `transcript.partial/final` → `turn.end` → … →
  cierre (`kind: closing`, `t-0`, índice = total) → `session.end` → `GET /v1/sessions/{id}/report` (202 con `Retry-After` hasta que esté).
- **Conexión (contrato v1.1).** `seq` por dirección (los frames binarios consumen seq). `hello`
  va siempre con seq 0 y no consume seq. Los mensajes del servidor se ordenan y deduplican por
  seq (`session.ready` puede llegar antes que lo reenviado; si falta un seq 2 s, se sigue en
  orden). El mensaje `ack` (seq 0, cada 250 ms o 32 KB) y el `ack` del sobre recortan lo
  pendiente y mueven la ventana `max_inflight_bytes` (96 KB) + `bufferedAmount` (el audio espera, no se descarta),
  ping cada `heartbeat_ms`, reconexión si no llega nada en `idle_timeout_ms`, backoff
  0,5/1/2/4/8 s con jitter dentro de 120 s, `resume.last_server_seq` y reenvío de lo que el
  servidor no recibió, con su seq original. Los cierres 1000, 4001, 4003, 4004, 4009 y 4010 no se
  reintentan; 4001 y cualquier 401 `UNAUTHENTICATED` vuelven al login con aviso.
- **Turnos.** `repeat: true` marca una re-emisión: `control.repeat` repite el mismo turno; tras
  `NO_AUDIO` o un fallo de STT llega `turn.end` con `next: repeat` y la misma pregunta con
  `turn_id` nuevo. Con `tts_delivery: none` no se manda `tts.played`; tampoco para el cierre.
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
npm run test:e2e:real            # opcional: contra el backend Go real (necesita Go; no está en CI)
```

Los e2e levantan el mock en `127.0.0.1:8787` y `vite build && vite preview` en
`127.0.0.1:4173` con la misma CSP que `deploy/frontend/nginx.conf`. Chromium se lanza con
`--use-fake-device-for-media-stream` y `--use-fake-ui-for-media-stream`. Ningún test usa red
externa. `forbidOnly` está activo y no hay tests `skip`/`todo`. El mock acepta
`POST /__mock/config` (`{"tts":"none"}`, `{"partials":false}`) para probar esos modos.

### e2e contra el backend real (`npm run test:e2e:real`)

`playwright.real.config.ts` corre `e2e-real/backend.mjs`, que compila `../backend/cmd/coach`
a un directorio temporal (o usa el binario de `CX9_COACH_BIN`) y levanta `coach speech-mock`
(127.0.0.1:18590) y `coach serve` (127.0.0.1:18580) con SQLite temporal, `CX9_LLM_PROVIDER=mock`,
contraseña `practica` y `CX9_CORS_ORIGINS=http://127.0.0.1:4174` (el backend compara el `Origin`
del WebSocket con esa lista). El preview queda en 4174 apuntando a ese backend. Al terminar se
borra el directorio temporal. No forma parte de `test:e2e` ni del CI. Sin `CX9_COACH_BIN`
compila el árbol de trabajo de `backend/`, con lo que haya sin commitear.

### Comandos de CI (`.github/workflows/ci.yml`, de Apex; no se edita desde acá)

| Job | Comandos | Dónde |
|---|---|---|
| frontend | `npm ci --no-audit --no-fund`, `npm run lint --if-present`, `npm run typecheck --if-present`, `npm test --if-present` | `frontend/`, Node 22 |
| e2e | `npm ci`, `npx playwright install --with-deps chromium`, `npm run test:e2e` (si existe `frontend/playwright.config.*`) | `frontend/` |

## Resultados medidos

Corrida desde un clon limpio del commit `436f2a1` (código y tests; los docs no cambian el
resultado), con Node v22.23.5, el 14/11/2026:

- `npm ci`: 267 paquetes.
- `npm run lint` y `npm run typecheck`: sin errores ni warnings.
- `npm test`: **13 archivos, 107 tests, 107 pasan**, 0 omitidos.
  Cobertura: sentencias 91,92 % (1366/1486), ramas 82,42 % (882/1070), funciones 83,37 % (341/409),
  líneas 94,91 % (1194/1258).
- `npm run test:e2e`: **14 tests, 14 pasan** (31,2 s); 5 de ellos hacen en total 7 chequeos axe (WCAG 2.0/2.1 A y AA).
- `npm run test:e2e:real` con el backend compilado del commit `7f510cf` (backend/ igual que en
  `a1be862`): **2 tests, 2 pasan** (18,5 s), con 2 chequeos axe.
- `npm run build`: `dist/` = 236 755 bytes (JS 230 483 B, 73,02 kB gzip; CSS 5 428 B;
  `capture-worklet.js` 381 B; `index.html` 463 B).

Tests unitarios por archivo: ws 23, live 11, views (SetupForm, ReportView, HistoryView, perfil) 10,
vad 9, App 8, client 8, SessionView 8, pcm 7, tts 6, i18n 6, history 4, profile 4, mic 3.

## Qué está probado y qué no

Probado:

- VAD con PCM sintético (silencio, ruido, clics, pausas, ruido fuerte que sube el umbral).
- Re-muestreo 48 kHz → 16 kHz sin perder muestras entre bloques; chunks de 3200 bytes; flag de último chunk.
- WebSocket con socket y reloj falsos: hello con seq 0, seq compartido JSON/binario, mensajes
  `ack` que mueven la ventana de 96 KB, `bufferedAmount`, ping/pong, backoff, gracia de 120 s,
  cierres terminales, resume con hello seq 0 y reenvío con seq originales, orden y
  deduplicación por seq (incluido el hueco de 2 s), mensajes inválidos o de tipo desconocido.
- Frames binarios: `encodeFrame`/`decodeFrame` de `ws-types.ts`.
- TTS: ensamblado, errores de orden o tamaño, PCM → WAV, sha256 y reproductor.
- Cliente REST con `fetch` falso: Idempotency-Key, filtros, 202/409 del informe, 404/ETag/412 del perfil, errores.
- i18n: mismas claves en es/en, sin textos vacíos, mismos placeholders, sin texto visible
  hardcodeado en JSX (análisis del AST de TypeScript) y sin claves sin usar.
- Componentes con Testing Library: sesión por voz, push-to-talk, permiso de micrófono denegado,
  sesión por texto, controles, errores, reconexión; formulario; informe con 202; historial; perfil.
- e2e: login, i18n, aviso de proveedor remoto, voz con VAD y botón, push-to-talk, corte del WS
  durante una respuesta con reenvío del audio (hello seq 0 en las dos conexiones), reconexión
  4008 + pausa, ventana liberada solo por mensajes `ack` (4,5 s de audio sin parciales),
  `tts_delivery: none`, repetir y `NO_AUDIO`, 401 → login, muletillas y pausas en el informe,
  historial, perfil con ETag y axe.
- Backend Go real (`test:e2e:real`): sesión por texto completa (presentación, preguntas,
  repreguntas, cierre, informe con muletillas detectadas en el texto) y por voz (TTS wav,
  repetir con `repeat: true`, `NO_AUDIO` con la misma pregunta y `turn_id` nuevo, 4,5 s de audio
  con mensajes `ack` de seq 0, presentación t-0/índice 0, cierre t-0/índice total, hello seq 0,
  seq del cliente crecientes, informe). El e2e de voz corre con la CSP de deploy y verifica que no haya errores de CSP en consola.

No probado:

- Contra el backend real: la reconexión con resume, la pausa y `tts_delivery: none` (solo
  contra el mock). `Turn.stt_fillers` y `Turn.pauses` del detalle de sesión: la UI no pide ese
  endpoint; el informe muestra `details.muletillas`, `muletillas_fuente` y `details.pausas`.
- Navegadores distintos de Chromium (Firefox, Safari).
- El corte automático por silencio con un micrófono real: el dispositivo falso de Chromium emite
  un tono continuo, así que en e2e la respuesta se termina con el botón.
- `live-ticket` (opcional en v1.1; no se usa: la cookie alcanza con el proxy same-origin).
- Contraste en modo oscuro: axe corre con el esquema claro.

## Supuestos sobre el contrato

Están marcados en el código y en el informe de la entrega; los principales:

- `answer.text` solo en sesiones `input: text`.
- Las preguntas `kind: intro` y `closing` (`turn_id` `t-0`) no se responden ni muestran "Pregunta N de M".
- `tts.played` se manda una vez por `utterance_id`, también si el usuario empieza a responder
  antes de que termine (salvo `tts_delivery: none` y el cierre, como dice v1.1).
- Si falta un seq del servidor durante 2 s, se procesan los mensajes retenidos en orden (el
  contrato no fija ese plazo).
- El cierre 4001 dispara `GET /v1/auth/me`; el 401 de esa llamada lleva al login.
- `sha256` de `tts.audio` se calcula sobre los bytes concatenados tal como llegan.
- El cierre 4400 se reintenta salvo que antes llegue un `error` fatal no reintentable.
- Repetir y saltar solo desde `asking`/`listening` (diagrama de estados del contrato).
