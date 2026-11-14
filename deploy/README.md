# Despliegue local (`/deploy`)

Dos formas de correr el coach, ambas **solo en tu máquina**.

## 1. Docker Compose

```bash
cp deploy/.env.example deploy/.env      # completá POSTGRES_PASSWORD, GRAFANA_ADMIN_PASSWORD, CX9_PASSWORD_HASH
deploy/local/fetch-models.sh small      # opcional: modelos para el modo real (o CX9_SPEECH_MODE=mock)
docker compose -f deploy/docker-compose.yml --env-file deploy/.env build backend
printf 'tu-clave\n' | docker run --rm -i cortex9/backend:local hash-password   # pegalo en .env entre comillas simples
docker compose -f deploy/docker-compose.yml --env-file deploy/.env up --build
```

| Servicio | URL en el host | Red |
|---|---|---|
| frontend (nginx + proxy `/v1` → backend) | http://127.0.0.1:5173 | backplane + egress |
| backend | http://127.0.0.1:8080 | backplane + egress (solo para el LLM elegido) |
| speech | no publicado (`http://speech:8090` interno) | backplane (sin salida) |
| postgres | no publicado (`postgres:5432` interno) | backplane (sin salida) |
| prometheus | http://127.0.0.1:9090 | backplane + egress |
| grafana | http://127.0.0.1:3000 (dashboard "CORTEX-9 Interview Coach") | backplane + egress, telemetría apagada |

- Todos los puertos publicados se atan a `127.0.0.1`. `backplane` es `internal: true`: speech y postgres no pueden salir a Internet.
- `docker-compose.debug.yml` publica speech y postgres en `127.0.0.1` para depurar (los suma a `egress`).
- Secretos solo en `deploy/.env` (ignorado por git). `POSTGRES_PASSWORD`, `GRAFANA_ADMIN_PASSWORD` y `CX9_PASSWORD_HASH` son obligatorias (compose falla si faltan). `CX9_DEV_NO_AUTH` no sirve en Docker: el backend solo lo acepta si escucha en loopback.
- El hash argon2id tiene `$`: en `deploy/.env` va entre comillas simples (`CX9_PASSWORD_HASH='$argon2id$…'`) para que compose no lo interpole.
- Imágenes: `speech/Dockerfile` (Apex), `backend/Dockerfile` (Nexus: distroless sin shell, binario `/app/coach`, usuario 65532), `deploy/frontend/Dockerfile` (build de `/frontend` + nginx, Apex).
- Healthchecks: speech con Python (`/healthz`); backend con su propio binario (`/app/coach healthcheck`, GET `/v1/health` en loopback, porque la imagen no tiene shell ni curl). backend espera a postgres y speech sanos; frontend espera al backend sano.
- backend: `CX9_DATA_DIR=/data` en el volumen `backend-data`, sistema de archivos de solo lectura y `/tmp` en tmpfs. La base es el Postgres del compose (la imagen trae SQLite por defecto, que el compose reemplaza).
- nginx pasa `Host` con el puerto (`$http_host`): el backend acepta un `Origin` igual al `Host`, y en `CX9_ENV=prod` ignora la lista CORS.
- Modelos montados en solo lectura desde `models/` (`CX9_MODELS_DIR`). La imagen de speech no descarga nada (`SPEECH_WHISPER_LOCAL_ONLY=true`, `HF_HUB_OFFLINE=1`).

## 2. Sin Docker

```bash
cp deploy/.env.example deploy/.env
deploy/local/run-all.sh                 # speech + backend + frontend, logs en deploy/local/logs/
```

O por separado: `deploy/local/run-speech.sh`, `run-backend.sh`, `run-frontend.sh`.

- Todo escucha en `127.0.0.1` (speech 8090, backend 8080, frontend 5173).
- speech crea su venv en `speech/.venv`. Con `CX9_SPEECH_MODE=mock` no instala modelos.
- Base: SQLite en `deploy/local/local-data/cx9.db` (ignorado por git). Con `CX9_LOCAL_DB=postgres` usa un Postgres propio en `127.0.0.1:5432`. Prometheus y Grafana solo vienen en el modo Docker.
- `run-backend.sh` corre `cd backend && go run ./cmd/coach serve`: la raíz del módulo Go es `backend/`, así que `go run ./backend/cmd/coach` desde la raíz del repo no funciona. Se cambia con `CX9_BACKEND_CMD`.
- Login: `CX9_PASSWORD_HASH` (`cd backend && printf 'tu-clave\n' | go run ./cmd/coach hash-password`) o, solo así en loopback, `CX9_DEV_NO_AUTH=1`.

## Prueba de integración (sin Docker)

```bash
python3 -m venv .venv && . .venv/bin/activate
pip install -r speech/requirements.txt -r deploy/local/requirements-it.txt && pip install --no-deps -e speech
deploy/local/integration-test.sh mock    # speech mock + backend (SQLite, LLM mock) + flujo REST/WebSocket
deploy/local/integration-test.sh real    # Whisper tiny + Piper/espeak-ng (modelos ya descargados, ver abajo)
```

Compila el backend (`go build ./cmd/coach` en `backend/`), elige puertos libres en `127.0.0.1`, crea una clave descartable y recorre el contrato con `deploy/local/integration_client.py`: health/ready, login, sesión con `Idempotency-Key`, WebSocket `cx9.live.v1` (hello, TTS binario, `tts.played`, `control.repeat`, audio PCM16 con ventana de acks, parciales, final, repreguntas, `session.end`), informe y métricas. En modo real la respuesta se sintetiza con el `/tts` del propio servicio de voz y la transcribe Whisper. Variables: `SPEECH_WHISPER_MODEL` (por defecto `tiny`), `SPEECH_WHISPER_DOWNLOAD_ROOT`, `SPEECH_PIPER_BIN`, `SPEECH_PIPER_VOICE_ES_AR`, `CX9_COACH_BIN`, `CX9_IT_OUT`, `CX9_IT_KEEP=1`. Resultados en [`INTEGRATION.md`](INTEGRATION.md). CI corre el modo mock y el mismo cliente contra el compose.

## Variables que el compose le pasa al backend

`CX9_ENV`, `CX9_HTTP_ADDR`, `CX9_DATABASE_URL`, `CX9_DATA_DIR`, `CX9_SPEECH_URL`, `CX9_SPEECH_TOKEN`, `CX9_PASSWORD_HASH`, `CX9_CORS_ORIGINS`, `CX9_LOG_TRANSCRIPTS`, `CX9_LOG_FORMAT`, `CX9_LLM_PROVIDER`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `XAI_API_KEY`, `CX9_OLLAMA_URL` (todas en `docs/contract.md` §2.7 y `backend/README.md`). El resto (`CX9_PARTIAL_INTERVAL`, `CX9_PARTIAL_WINDOW`, `CX9_MAX_TURN_MS`, modelos por proveedor…) queda en el default del backend. Binario: `backend/cmd/coach` con `serve` (por defecto), `hash-password`, `speech-mock`, `healthcheck`, `version`.

Ver también [`SECURITY.md`](SECURITY.md).
