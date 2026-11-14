# CORTEX-9 Interview Coach

Entrenador de entrevistas por voz, **solo para práctica**, en inglés y español, que corre **localmente** en tu máquina.

Practicás respondiendo preguntas de entrevista en voz alta; el coach transcribe tu respuesta (conservando muletillas como "um", "eh", "este", "o sea"), mide ritmo, pausas y claridad, y te devuelve feedback hablado y escrito.

## Qué es y qué no es

- **Es** un simulador de práctica: el entrevistador es el propio coach.
- **No es** una herramienta de asistencia en vivo. **No existe ningún modo para escuchar a un entrevistador real** ni para soplar respuestas durante una entrevista verdadera, y no se va a agregar.
- Tu audio y tu perfil **se quedan en tu máquina**. Lo único que sale es lo que vos decidas enviar al proveedor de LLM que elijas (o nada, si usás un modelo local o el modo mock).

## Componentes

| Carpeta | Componente | Responsable |
|---|---|---|
| `/speech` | Servicio de voz (FastAPI): STT con faster-whisper (con muletillas y confianza por palabra) y TTS con piper (es_AR, en_US) con respaldo espeak-ng | Apex |
| `/backend` | API principal: sesiones, preguntas, scoring, integración con el LLM | Nexus |
| `/frontend` | Interfaz web de práctica | Cronos |
| `/docs` | Contrato entre componentes (`docs/contract.md`) | Nexus |
| `/deploy` | docker-compose (backend, frontend, speech, postgres, prometheus, grafana), modo sin Docker, `SECURITY.md` | Apex |
| `/.github` | CI sin secretos: tests, lint, build de imágenes, e2e con mocks | Apex |

## Inicio rápido

```bash
cp deploy/.env.example deploy/.env   # completá solo lo que necesites
# Con Docker:
docker compose -f deploy/docker-compose.yml --env-file deploy/.env up
# Sin Docker:
deploy/local/run-all.sh
```

Todos los puertos se publican solo en `127.0.0.1`.

## Modelos

Los modelos (whisper, voces de piper) **no se versionan**. Descargalos a `models/` (ignorado por git) y configurá sus rutas en `deploy/.env`.

## Licencia

Apache-2.0. Ver `LICENSE`.
