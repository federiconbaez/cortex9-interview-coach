# Backend del CORTEX-9 Interview Coach

Servidor en Go del coach de entrevistas por voz. **Solo práctica**: la app hace de
entrevistadora, pregunta, escucha la respuesta del propio usuario, repregunta (como
mucho 2 veces por pregunta) y al final arma el informe. No existe ningún modo que
escuche a un entrevistador real ni que sugiera respuestas en vivo, y el contrato lo
prohíbe (`docs/contract.md` §1).

- Contrato: `../docs/contract.md`, `../docs/openapi.yaml`, `../docs/ws-types.ts` (v1.1).
- Binario: `cmd/coach` → `coach`.
- Decisiones de diseño: [`docs/decisiones.md`](docs/decisiones.md).
- Cobertura de la última corrida: `results/coverage.txt`.

## Arquitectura

```
cmd/coach            main: serve | hash-password | speech-mock | healthcheck | version
internal/app         arma todo (config → store → LLM → voz → servicio → hub → HTTP) y el apagado ordenado
internal/config      lee el entorno (única fuente de claves) y valida
internal/api         REST /v1 (net/http 1.22+), problem+json, CORS dev, Idempotency-Key, ETag/If-Match,
                     auth, métricas y logs por request; upgrade del WebSocket
internal/live        WebSocket cx9.live.v1: Hub (sesiones vivas), Runtime (una goroutine por sesión
                     que es dueña del motor), Conn (escritor con cola acotada, heartbeat, idle)
internal/engine      máquina de estados de la entrevista: preguntas, ≤ 2 repreguntas, pausa,
                     saltear, repetir, terminar. Puro y determinista (sin red ni reloj)
internal/coach       servicio de dominio: escenarios, perfil, creación de sesiones, modo historia,
                     cola de scoring, recuperación al arrancar, caché TTS para tts.url
internal/scenario    escenarios YAML embebidos (ES/EN) y validación de los del usuario
internal/scoring     6 dimensiones, versión mejorada solo con palabras del usuario, top3 = 3,
                     [FALTA]/null en vez de datos inventados; JSON y Markdown
internal/analysis    heurísticas de texto (muletillas, números, STAR, términos inventados)
internal/profile     perfil del modo historia (CRUD, merge-patch, huecos, evidencia literal)
internal/llm         proveedores openai, anthropic, xai, ollama y mock con timeout,
                     reintentos y circuit breaker por proveedor
internal/speech      cliente del servicio de voz (/stt, /tts, /health, /voices) + speechmock
internal/resilience  reintentos con backoff/jitter y circuit breaker
internal/store       Postgres (pgx) y SQLite (modernc, sin CGO) con migraciones embebidas
internal/auth        argon2id, tokens opacos (solo se guarda el SHA-256), límite de login
internal/metrics     métricas Prometheus del contrato (§4.1)
internal/endpoint    endpointing por silencio: VAD de energía RMS, campos de VAD del /stt parcial,
                     ventana de gracia y similitud de textos para la decisión especulativa
internal/protocol    tipos del WS y header binario de 20 bytes
internal/secret      tipo para claves que nunca se imprimen ni se serializan
```

Flujo de una sesión de voz: `POST /v1/sessions` → `GET /v1/sessions/{id}/live`
(cookie o ticket) → `hello` (seq 0) → `session.ready` → `question` + TTS → audio del
usuario en frames binarios (acks cada 250 ms o 32 KB) → parciales cada ~1,5 s con los
últimos 12 s → `audio.end` o cierre por silencio (endpointing, `turn.end.reason =
"endpoint"`) → STT del turno completo → `transcript.final` → el LLM decide
repregunta (o la lógica local si falla) → `turn.end` → … → `session.end` → scoring en
segundo plano → `GET /v1/sessions/{id}/report` (202 mientras puntúa, después 200).

## Cómo correrlo

Requisitos: Go 1.27 (sin CGO). Todo funciona sin red con el proveedor `mock` y el
servicio de voz mock.

```sh
cd backend
go build -o coach ./cmd/coach

# 1) servicio de voz mock (en otra terminal), mismo contrato que /speech en modo mock
./coach speech-mock                       # escucha en 127.0.0.1:8090

# 2) contraseña local
export CX9_PASSWORD_HASH="$(printf 'mi-clave\n' | ./coach hash-password)"
#    o en dev, sin login (solo si escucha en loopback):
#    export CX9_DEV_NO_AUTH=1 CX9_HTTP_ADDR=127.0.0.1:8080

# 3) API
./coach serve                             # :8080, SQLite en ./cx9.db, LLM mock
curl -s localhost:8080/v1/ready
```

Con Postgres: `CX9_DATABASE_URL=postgres://usuario:clave@host:5432/cx9?sslmode=disable`.
Las migraciones se aplican solas al arrancar.

Con Docker (lo usa `../deploy/docker-compose.yml`):

```sh
docker build -t cortex9/backend:local .
docker run --rm -p 127.0.0.1:8080:8080 -e CX9_DEV_NO_AUTH=0 -e CX9_PASSWORD_HASH=... cortex9/backend:local
```

La imagen es multi-stage, binario estático (`CGO_ENABLED=0`) sobre
`distroless/static:nonroot`, escucha en `:8080` con `/metrics` en el mismo puerto y trae
`HEALTHCHECK` con `coach healthcheck`.

### Tests

```sh
go vet ./...
go test -race ./...                                    # todo offline: httptest + cliente WS real
CX9_TEST_DATABASE_URL=postgres://cx9@127.0.0.1:5432/cx9_test?sslmode=disable \
  go test -race -p 1 ./internal/store/ ./internal/api/ # además contra Postgres (borra y recrea las tablas de esa base)
go test -race -coverpkg=./... -coverprofile=results/cover.out ./... \
  && go tool cover -func=results/cover.out > results/coverage.txt
```

Los tests de integración levantan el servidor real con `httptest`, un servicio de voz
fake que cumple el contrato (§6.7) y un cliente WebSocket real (`coder/websocket`).
No dependen de que el mock quite el `?` del texto.

## Subcomandos

| Comando | Qué hace |
|---|---|
| `coach` / `coach serve` | Sirve la API, el WebSocket y `/metrics` |
| `coach hash-password` | Lee la contraseña por stdin e imprime el hash argon2id para `CX9_PASSWORD_HASH` |
| `coach speech-mock` | Servicio de voz mock en memoria (dev/tests) en `CX9_SPEECH_MOCK_ADDR` |
| `coach healthcheck` | `GET /v1/health` local; sale 0 si responde 200 (HEALTHCHECK de Docker) |
| `coach version` | Imprime la versión |

## Variables de entorno (todas)

Las claves salen **solo** del entorno; nunca se loguean ni se devuelven (la API de
proveedores solo dice `configured: true/false`).

| Variable | Default | Descripción |
|---|---|---|
| `CX9_HTTP_ADDR` | `:8080` | Dirección de escucha (API, WS y `/metrics`) |
| `CX9_ENV` | `dev` | `dev` o `prod`. En `prod` CORS se apaga (solo vale el mismo origen). No cambia la cookie: ver la nota de abajo |
| `CX9_DATABASE_URL` | `sqlite://cx9.db` | `sqlite://<ruta>` (o `sqlite://:memory:`) o `postgres://…` / `postgresql://…` |
| `CX9_PASSWORD_HASH` | vacío | Hash argon2id (`coach hash-password`). Sin hash ni `CX9_DEV_NO_AUTH`, el login falla y `/v1/ready` lo informa |
| `CX9_DEV_NO_AUTH` | `0` | `1` desactiva el login; solo se acepta si `CX9_HTTP_ADDR` es loopback (si no, no arranca) |
| `CX9_CORS_ORIGINS` | `http://localhost:5173,http://127.0.0.1:5173` | Orígenes permitidos en dev (CORS y `Origin` del WS) |
| `CX9_TOKEN_TTL` | `12h` | Vida del token de sesión (cookie `cx9_session`) |
| `CX9_SPEECH_URL` | `http://127.0.0.1:8090` | Servicio de voz (`http://speech:8090` en compose) |
| `CX9_SPEECH_TOKEN` | vacío | Bearer para el servicio de voz, si lo exige |
| `CX9_SPEECH_MOCK_ADDR` | `127.0.0.1:8090` | Solo `coach speech-mock` |
| `CX9_PARTIAL_INTERVAL` | `1.5s` | Cada cuánto se re-transcribe un parcial |
| `CX9_PARTIAL_WINDOW` | `12s` | Audio que se manda en cada parcial (los últimos N segundos) |
| `CX9_RECONNECT_GRACE` | `120s` | Gracia para reconectar con resume |
| `CX9_MAX_TURN_MS` | `300000` | Duración máxima de una respuesta |
| `CX9_TTS_PLAYED_GRACE_MS` | `3000` | Espera de `tts.played` además de `duration_ms` del audio; si no llega, el servidor avanza igual (log `warn` y `cx9_tts_played_timeouts_total`). Rango 0-60000 |
| `CX9_TTS_PLAYED_TIMEOUT_MS` | `20000` | Espera de `tts.played` cuando la duración del audio no se conoce. Rango 1000-300000 |
| `CX9_ENDPOINT_MODE` | `auto` | Endpointing por silencio: `off`, `stt` (campos de VAD del `/stt` parcial), `energy` (VAD RMS del backend) o `auto` (stt y, si el servicio no los manda o falla, energy) |
| `CX9_ENDPOINT_SILENCE_MS` | `700` | Silencio final (ms de audio) que cierra el turno después de haber voz. Rango 100-10000 |
| `CX9_ENDPOINT_GRACE_MS` | `200` | Ventana de gracia antes de cerrar: si vuelve la voz, no se corta. Rango 0-5000 |
| `CX9_ENDPOINT_RMS` | `500` | Umbral RMS de voz (sobre 32767) del VAD de energía, frames de 20 ms |
| `CX9_ENDPOINT_MIN_VOICE_MS` | `150` | Voz mínima (energía) para considerar que hubo respuesta |
| `CX9_SPECULATIVE_FOLLOWUP` | `true` | Al cerrar por silencio, decide la repregunta con el último parcial mientras corre el STT final; se usa solo si el final se parece ≥ 0,9. `false` o `0` lo apaga |
| `CX9_SHUTDOWN_TIMEOUT` | `20s` | Tope del apagado ordenado |
| `CX9_DATA_DIR` | `data` | Audio guardado con `save_audio` (`/data` en la imagen) |
| `CX9_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CX9_LOG_FORMAT` | `json` | `json` o `text` (slog) |
| `CX9_LOG_TRANSCRIPTS` | `0` | `1` incluye el texto de las respuestas en los logs (solo dev) |
| `CX9_VERSION` | `1.0.0` | Versión que informan `/v1/health` y `/v1/ready` |
| `CX9_LLM_PROVIDER` | `mock` | Proveedor por defecto: `openai`, `anthropic`, `xai`, `ollama`, `mock` |
| `CX9_LLM_TIMEOUT` | `30s` | Timeout por llamada al LLM |
| `CX9_LLM_RETRIES` | `2` | Reintentos ante errores transitorios |
| `OPENAI_API_KEY` | vacío | Clave de OpenAI |
| `OPENAI_MODEL` | `gpt-4o-mini` | Modelo |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | Base URL (compatible OpenAI) |
| `ANTHROPIC_API_KEY` | vacío | Clave de Anthropic |
| `ANTHROPIC_MODEL` | `claude-3-5-haiku-latest` | Modelo |
| `ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | Base URL |
| `XAI_API_KEY` | vacío | Clave de xAI |
| `XAI_MODEL` | `grok-3-mini` | Modelo |
| `XAI_BASE_URL` | `https://api.x.ai/v1` | Base URL |
| `CX9_OLLAMA_URL` | `http://localhost:11434` | Ollama local (`OLLAMA_URL` como respaldo; en compose `http://host.docker.internal:11434`) |
| `OLLAMA_MODEL` | `llama3.1` | Modelo de Ollama |
| `CX9_TEST_DATABASE_URL` | vacío | Solo tests: corre los tests de `internal/store` y la regresión de muletillas de `internal/api` también contra Postgres (usar una base descartable y `-p 1`) |

Duraciones en formato Go (`1.5s`, `2m`, `12h`).

**Cookie `cx9_session` y `Secure`.** Es siempre `HttpOnly`, `SameSite=Strict` y
`Path=/`. El atributo `Secure` **no** depende de `CX9_ENV`: se pone cuando el `Host` del
request **no** es loopback (`localhost`, `127.0.0.0/8` o `::1`) y se omite cuando sí lo
es, en dev y en prod. No hay variable para forzarlo. Detrás de un proxy decide el `Host`
que llega al backend: con `proxy_set_header Host $http_host` (como el nginx de
`/deploy`) un acceso por `http://localhost:8080` recibe la cookie sin `Secure` y uno por
un nombre o IP de red la recibe con `Secure`, así que ese acceso tiene que ser por HTTPS.

## Operación

- `GET /v1/health`: vivo. `GET /v1/ready`: base, servicio de voz (`ok`/`degraded`/`down`),
  LLM por defecto y auth. `degraded` responde 200; `down` en base o voz, 503.
- `GET /metrics`: Prometheus, mismo puerto. Ninguna etiqueta lleva texto del usuario,
  IDs de sesión ni claves.
- Logs: `slog` en JSON con `request_id`. Sin claves; el texto de las respuestas solo con
  `CX9_LOG_TRANSCRIPTS=1`.
- Apagado (SIGINT/SIGTERM): deja de aceptar, cierra los WebSocket con `1001` y marca las
  sesiones vivas como terminadas (`ended_reason: error`, el informe se arma igual),
  espera los requests en curso, termina la cola de scoring y cierra la base. Al
  arrancar, las sesiones que quedaron `live`/`detached` se cierran como `disconnected`
  y se re-encolan los informes pendientes.

Licencia: Apache-2.0.
