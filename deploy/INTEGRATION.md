# Integración sin Docker — CX9-6174

Fecha: 2026-11-14 (UTC-3). Probado contra el backend `a1be862` y el contrato v1.1 (`d3f6532`), con los cambios de `/speech` en `c8a5c26`, `/deploy` en `7421438` y CI en `fdc0b58`.

## Qué se probó

`deploy/local/integration-test.sh` compila el backend (`cd backend && go build ./cmd/coach`), levanta el servicio de voz y el backend en puertos libres de `127.0.0.1` (backend con SQLite, LLM `mock` y clave descartable con `coach hash-password`) y corre `deploy/local/integration_client.py`, que recorre el contrato como lo haría el frontend:

| Paso | Contrato | Verificación |
|---|---|---|
| `GET /v1/health`, `GET /v1/ready` sin auth | §2, §3 | 200; `checks.speech` `ok`/`degraded` |
| REST protegido sin login | §2.6 | 401 |
| `POST /v1/auth/login` | §2.6 | 200 + cookie `cx9_session` |
| `GET /v1/languages`, `GET /v1/scenarios` | §3 | STT y voces informadas; 3 escenarios |
| `POST /v1/sessions` ×2 con la misma `Idempotency-Key` | §3 | 201, misma sesión, `Idempotent-Replayed: true` |
| WS `cx9.live.v1` con cookie y `Origin` | §5.1 | subprotocolo aceptado |
| `hello` (seq 0) → `session.ready` | §5.2 | `tts_delivery: binary`, `pcm16le`, ventana 96000 |
| `ping` → `pong` | §5 | mismo `nonce` |
| `question`/`followup` + `tts.audio` + frames binarios `0x02` | §5.4 | cantidad de chunks, bytes, índices, flag `last`, `sha256` |
| `tts.played` | §5 | el servidor pasa a escuchar |
| `control.repeat` | §5 | la misma pregunta con `repeat: true` y el mismo `turn_id` |
| Audio PCM16 16 kHz en frames `0x01` (cabecera de 20 bytes) con ventana de acks, `audio.end` | §5.4 | acks periódicos (seq 0), sin errores |
| `transcript.partial` / `transcript.final` | §5, §6 | parciales durante la respuesta; final con `source: stt`, duración, confianza y `low_confidence` |
| Repreguntas, `turn.end`, cierre, `session.end` | §5 | ≤ `max_followups`, `report_url` |
| `GET` informe (202 → 200) y en Markdown | §4 | 6 dimensiones en `dim_means`, `top3` con 3 |
| `GET /v1/sessions/{id}` | §3 | turnos con respuesta; `Turn.stt_fillers`, `Turn.pauses` |
| `GET /metrics` del backend | §7 | métricas de las llamadas a la voz |

## Resultados

| Modo | Voz | Chequeos | Duración del WS | Parciales | Finales | TTS |
|---|---|---|---|---|---|---|
| mock | `CX9_SPEECH_MODE=mock` + `SPEECH_MOCK_TEXT` | **30/30 OK** | 10 s | 4 | 4 | 7 intervenciones |
| real | faster-whisper `tiny` (int8, CPU) + Piper `es_AR-daniela-high` | **28/28 OK** (+1 informativo) | 32 s | 12 | 4 | 7 intervenciones, 1 MB |

- Mock: final `Eh, este... o sea, trabajé tres años en backend con Kafka, um, y bajé la latencia un 40 por ciento.`, `low_confidence: ["Kafka"]`, `Turn.stt_fillers: ["eh","este","o sea","um"]`, `muletillas_fuente: "stt"`.
- Real: la respuesta se sintetiza con el `/tts` del propio servicio (Piper) y Whisper tiny la transcribe: `Esto sea, trabajé tres años en backend con cazca y un paje de la atención 40 por ciento.` (confianza 0.63; el primer parcial sí traía `Eh,`). Tiny es solo para la prueba; la calidad esperada es la de `small` (default).
- Real: `/v1/ready` queda en `degraded` porque falta la voz Piper `en_US` en esta máquina; el español funciona completo.
- Real: Whisper no detectó muletillas en la respuesta, `/speech` devolvió `fillers: []` / `filler_count: 0` y aun así el informe dice `muletillas_fuente: "texto"`. Ver el bug 1.

### Tests por componente

| Componente | Comando | Resultado |
|---|---|---|
| speech (unidad) | `cd speech && pytest -q` | 68 OK |
| speech (e2e caja negra) | `SPEECH_URL=… pytest -q e2e` contra el modo mock | 5 OK |
| speech (lint) | `ruff check` + `ruff format --check` | OK |
| backend | `cd backend && gofmt -l . && go vet ./... && go test -race -count=1 ./...` | 72 tests + 1 subtest OK; 15 paquetes OK, 5 sin tests; gofmt y vet limpios |
| integración | `deploy/local/integration-test.sh mock` / `real` | 30/30 y 28/28 |
| deploy | `shellcheck -x`, compose validado contra compose-spec, `actionlint` | OK |
| frontend | — | no se corrió (fuera de este alcance; ver Pendiente) |

Go del sistema: 1.27.3, igual a `backend/go.mod`; no hizo falta `GOTOOLCHAIN`.

## Comandos

```bash
python3 -m venv .venv && . .venv/bin/activate
pip install -r speech/requirements.txt -r deploy/local/requirements-it.txt && pip install --no-deps -e speech
deploy/local/integration-test.sh mock
# real: requiere el modelo tiny ya descargado y una voz Piper (o espeak-ng)
pip install -r speech/requirements-models.txt
SPEECH_WHISPER_DOWNLOAD_ROOT=models/whisper SPEECH_PIPER_BIN=… SPEECH_PIPER_VOICE_ES_AR=…/es_AR-daniela-high.onnx \
  deploy/local/integration-test.sh real
```

## Bugs y observaciones para Nexus (backend)

1. `backend/internal/engine/engine.go:65`: `STTFillers []string` con `json:"stt_fillers,omitempty"`. El turno se guarda como JSON (tabla `turns`), y una lista vacía (`/speech` dijo "cero muletillas") vuelve como `nil`. Entonces `backend/internal/scoring/scoring.go:257` (`t.STTFillers != nil`) cae a la detección por texto (`muletillas_fuente: "texto"`). Reproducido en el modo real. Fix sugerido: quitar `omitempty`, o guardar un `*[]string`, o marcar la fuente aparte.
2. `backend/README.md:120` dice que en `prod` "la cookie siempre es `Secure`", pero `backend/internal/api/misc.go:103` la marca `Secure` solo si el `Host` no es loopback (que es lo que pide `docs/contract.md:107`). Es solo documentación; el código coincide con el contrato.
3. `backend/internal/api/server.go:153` compara `Origin` con `Host` exacto, puerto incluido. Detrás de un proxy hay que pasar `Host` con puerto: se corrigió en `deploy/frontend/nginx.conf` (`$http_host`). El proxy de Vite de Cronos ya lo hace bien (`changeOrigin: false`). Conviene dejarlo escrito en el contrato §2.5.
4. `go run ./backend/cmd/coach` no funciona desde la raíz del repo, porque la raíz del módulo es `backend/`. Los scripts usan `cd backend && go run ./cmd/coach`.
5. El backend no reenvía `X-Mock-Transcript` a `/stt` (es esperable). Para probar el flujo con muletillas en modo mock, `/speech` suma `SPEECH_MOCK_TEXT`, que es opcional y está apagado por defecto.

Para Cronos no se detectó nada en la integración, porque el frontend no entró en esta prueba.

## Sin verificar

- **Docker**: no está instalado en esta máquina. No se construyó ninguna imagen ni se levantó el compose, así que quedan sin probar: el healthcheck `/app/coach healthcheck`, `depends_on: service_healthy`, `read_only` + tmpfs en el backend, el volumen `/data` con el dueño 65532, el proxy nginx con WebSocket y la red `backplane`. El compose se validó contra el esquema de compose-spec. CI tiene un job que lo levanta y corre el mismo cliente por `127.0.0.1:5173`.
- **Postgres**: la integración usó SQLite. El dialecto Postgres del backend solo tiene los tests del propio backend.
- **LLM real**: solo se usó `mock`.
- **CI en GitHub**: el repo es solo local, así que el workflow se validó con actionlint, sin ejecutarse.
- **Frontend**: está commiteado (`ea0c06b`, `2c25b91`) pero tenía cambios sin commitear en curso. No se tocó ni se corrieron sus tests.

## Pendiente para la integración final (con el frontend commiteado)

1. Correr en una máquina con Docker: `docker compose … up --build --wait`, y después `integration_client.py --base http://127.0.0.1:5173 --metrics-url http://127.0.0.1:8080/metrics` (como en CI).
2. Tests del frontend (`npm test`, Playwright) y una prueba manual en el navegador: micrófono, cookie y `Origin` por nginx.
3. Arreglar el bug 1 y volver a correr `integration-test.sh real`, que debería informar `muletillas_fuente: "stt"` también con cero muletillas.
4. Probar en modo real con `small` y las dos voces Piper (`/v1/ready` en `ok`).
5. Probar con Postgres (compose) y con un LLM real si se va a usar.
