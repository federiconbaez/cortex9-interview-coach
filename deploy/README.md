# Despliegue local (`/deploy`)

Dos formas de correr el coach, ambas **solo en tu máquina**.

## 1. Docker Compose

```bash
cp deploy/.env.example deploy/.env      # completá POSTGRES_PASSWORD, GRAFANA_ADMIN_PASSWORD y lo que uses
deploy/local/fetch-models.sh small      # opcional: modelos para el modo real (o CX9_SPEECH_MODE=mock)
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
- Secretos solo en `deploy/.env` (ignorado por git). `POSTGRES_PASSWORD` y `GRAFANA_ADMIN_PASSWORD` son obligatorias (compose falla si faltan).
- Imágenes: `speech/Dockerfile` (Apex), `backend/Dockerfile` (Nexus, debe escuchar en `:8080`), `deploy/frontend/Dockerfile` (build de `/frontend` + nginx, Apex).
- Modelos montados en solo lectura desde `models/` (`CX9_MODELS_DIR`). La imagen de speech no descarga nada (`SPEECH_WHISPER_LOCAL_ONLY=true`, `HF_HUB_OFFLINE=1`).

## 2. Sin Docker

```bash
cp deploy/.env.example deploy/.env
deploy/local/run-all.sh                 # speech + backend + frontend, logs en deploy/local/logs/
```

O por separado: `deploy/local/run-speech.sh`, `run-backend.sh`, `run-frontend.sh`.

- Todo escucha en `127.0.0.1` (speech 8090, backend 8080, frontend 5173).
- speech crea su venv en `speech/.venv`. Con `CX9_SPEECH_MODE=mock` no instala modelos.
- Postgres: usá uno local en `127.0.0.1:5432` (o `CX9_SKIP_DB=1` si el backend lo permite). Prometheus y Grafana solo vienen en el modo Docker.
- `run-backend.sh` corre `go run ./cmd/<primer main>`; se cambia con `CX9_BACKEND_CMD`.

## Variables que el compose le pasa al backend

`CX9_ENV`, `CX9_HTTP_ADDR`, `CX9_DATABASE_URL`, `CX9_SPEECH_URL`, `CX9_SPEECH_TOKEN`, `CX9_PASSWORD_HASH`, `CX9_CORS_ORIGINS`, `CX9_LOG_TRANSCRIPTS`, `CX9_LLM_PROVIDER`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `XAI_API_KEY`, `CX9_OLLAMA_URL`. Confirmadas por el backend: `CX9_HTTP_ADDR` (default `:8080`), `CX9_DATABASE_URL` (sqlite o postgres según el esquema) y `CX9_LLM_PROVIDER` (`openai`, `anthropic`, `xai`, `ollama` o `mock`; default `mock`). El binario es `backend/cmd/coach`.

Ver también [`SECURITY.md`](SECURITY.md).
