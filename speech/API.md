# API del servicio de voz (`/speech`)

**Fuente de verdad: [`docs/contract.md` §6](../docs/contract.md)** (contrato backend → voz, de Nexus).
Este archivo documenta cómo lo implementa `/speech`, las extensiones compatibles y la configuración.
Si hay diferencia, manda el contrato.

- Puerto por defecto `8090` (`SPEECH_PORT`), escucha en `127.0.0.1` salvo `SPEECH_HOST` (en compose `0.0.0.0` dentro de la red interna).
- Solo lo llama el backend (`CX9_SPEECH_URL`). El navegador nunca lo llama, así que no hay CORS.
- `Authorization: Bearer $CX9_SPEECH_TOKEN` obligatorio en `/stt`, `/tts` y `/voices` **solo si** la variable está definida. `/health`, `/healthz`, `/readyz` y `/metrics` no piden auth.
- Errores RFC 9457 `application/problem+json` con `type`, `title`, `status`, `detail`, `instance`, `code`, `request_id`, `retryable` y `errors` (validación).
- `X-Request-Id`: se respeta el recibido si cumple `^[A-Za-z0-9-]{8,64}$`; si no, se genera uno. Siempre vuelve en la respuesta.

## Rutas

| Método | Ruta | Contrato | Notas |
|---|---|---|---|
| POST | `/stt` | §6.1 | Cuerpo binario crudo |
| POST | `/tts` | §6.2 | JSON → audio |
| GET | `/health` | §6.3 | `503` si `status=down` (con `Retry-After` mientras carga el modelo) |
| GET | `/voices` | §6.4 | |
| GET | `/metrics` | – | Prometheus, sin texto del usuario en etiquetas |
| GET | `/healthz` | extensión | Liveness simple `{"status":"ok","version"}` (healthcheck de Docker) |
| GET | `/readyz` | extensión | Alias de `/health` |

## `POST /stt`

`Content-Type`:

- `audio/L16; rate=16000; channels=1` (base). Se aceptan otros `rate` entre 8000 y 48000 (se re-muestrea); `channels` distinto de 1 → `415`.
- `audio/wav` (también `audio/x-wav`, `audio/wave`): PCM16, cualquier rate; estéreo se mezcla a mono.
- `audio/ogg; codecs=opus`: solo en modo real y si PyAV está instalado (viene con faster-whisper). Se declara en `/health` → `stt.input_formats`.

Query: `language` (`es`|`en`|`auto`, obligatorio), `word_confidence` (por defecto `true`), `prompt` (≤ 500), `partial` (`true` → beam rápido, `SPEECH_WHISPER_PARTIAL_BEAM_SIZE`, por defecto 1).

Respuesta `200` (campos del contrato + extensiones marcadas):

```json
{
  "text": "Eh, bueno, trabajé en Telecom, o sea, varios años.",
  "language": "es",
  "duration_s": 6.084,
  "confidence": 0.87,
  "segments": [
    { "start_s": 0.0, "end_s": 4.1, "text": "Eh, bueno, trabajé en Telecom,", "confidence": 0.82,
      "words": [ { "word": "Eh,", "start_s": 0.0, "end_s": 0.3, "confidence": 0.95, "is_filler": true } ] }
  ],
  "engine": "faster-whisper",
  "model": "small",
  "processing_ms": 1830,
  "fillers": [ { "text": "eh", "start_s": 0.0, "end_s": 0.3 } ],
  "filler_count": 1,
  "pauses": [ { "start_s": 2.1, "end_s": 3.0, "duration_s": 0.9 } ]
}
```

- Extensiones (los clientes ignoran campos desconocidos, contrato §2.1): `words[].is_filler`, `fillers`, `filler_count`, `pauses` (huecos ≥ `SPEECH_PAUSE_THRESHOLD_S`, 0,6 s por defecto).
- `duration_s` es la duración del audio recibido (L16: `bytes / (2 × rate)`; WAV: header), no la del habla.
- `confidence`: promedio ponderado por duración de la confianza de las palabras (`probability` de Whisper); `null` sin palabras. Si un segmento no tiene palabras, su `confidence` es `exp(avg_logprob)`.
- Con `word_confidence=false` las palabras se usan igual para `confidence`, `fillers` y `pauses`, pero `segments[].words` va vacío.

### Cómo se conservan las muletillas

Whisper tiende a "limpiar" la transcripción. En `FasterWhisperSTT`:

- `suppress_tokens=[]` y `suppress_blank=False` (sin supresión de tokens).
- `initial_prompt` con muletillas (por idioma, configurable con `SPEECH_FILLER_PROMPT_ES/EN`; si el backend manda `prompt`, se usa ese).
- `vad_filter=False` (el VAD recorta vacilaciones) y `condition_on_previous_text=False`.
- `word_timestamps=True` con `probability` por palabra → `confidence`.

Detección (`fillers.py`): vacilaciones (`um`, `uh`, `eh`, `em`, `mm`, `er`…) siempre; `este`, `bueno`, `tipo`, `like`, `well`, `so` solo si van seguidas de coma/puntos suspensivos o de una pausa; frases `o sea`, `como que`, `en plan`, `you know`, `I mean`, `kind of`, `sort of`.

## `POST /tts`

```json
{ "text": "Contame tu recorrido.", "language": "es", "voice": "es_AR-daniela-high", "format": "wav", "sample_rate": 22050, "speed": 1.0 }
```

- Campos desconocidos → `422` (`extra=forbid`, contrato §2.1).
- `format`: `wav` | `pcm16le`. `opus` → `422 VALIDATION_FAILED` porque `/health` no lo declara (no hay encoder Opus de salida en v1).
- `speed` 0,5-2,0 → piper `length_scale = 1/speed`; espeak `-s = 160 × speed`.
- Respuesta: `audio/wav` o `audio/L16; rate=<sr>; channels=1`, headers `X-Audio-Duration-Ms`, `X-Sample-Rate`, `X-Voice`, `X-Engine` (`piper`/`espeak-ng`/`mock`).
- Motores: piper (voz `es_AR` y `en_US`, rutas en `SPEECH_PIPER_VOICE_ES_AR` / `SPEECH_PIPER_VOICE_EN_US`) con respaldo `espeak-ng`/`espeak`. Si se pide una voz, el motor dueño de esa voz va primero. El texto se pasa por stdin, nunca como argumento.

## Errores

| status | code | Cuándo |
|---|---|---|
| 400 | `BAD_REQUEST` | JSON mal formado, PCM16 con bytes impares, parámetros de L16 inválidos |
| 401 | `UNAUTHENTICATED` | Falta el Bearer cuando `CX9_SPEECH_TOKEN` está definido |
| 413 | `PAYLOAD_TOO_LARGE` | > 20 MB (`SPEECH_MAX_UPLOAD_MB`) o > 600 s (`SPEECH_MAX_AUDIO_S`); se corta a nivel ASGI aun con `Transfer-Encoding: chunked` |
| 415 | `UNSUPPORTED_MEDIA_TYPE` | Content-Type no soportado o audio indecodificable |
| 422 | `NO_AUDIO` | Cuerpo vacío o < 100 ms |
| 422 | `VALIDATION_FAILED` / `VOICE_NOT_FOUND` / `UNSUPPORTED_LANGUAGE` | |
| 500 | `INTERNAL` | Falla del motor (sin detalles: pueden tener rutas locales) |
| 503 | `MODEL_LOADING` | Modelo cargando al arrancar, `Retry-After: 5` |
| 503 | `DEPENDENCY_UNAVAILABLE` | Ocupado (`Retry-After: 1`) o ningún motor TTS disponible (`Retry-After: 5`) |
| 504 | `DEPENDENCY_TIMEOUT` | Superó `SPEECH_STT_TIMEOUT_S` / `SPEECH_TTS_TIMEOUT_S` |

Concurrencia: `SPEECH_MAX_CONCURRENCY` (1 por defecto, pensado para poca RAM). Un `/stt` final espera hasta `SPEECH_QUEUE_TIMEOUT_S`; un `partial=true` **no espera**: si está ocupado responde `503` al instante (el backend descarta parciales), así nunca demora una transcripción final.

## Modo mock (`CX9_SPEECH_MODE=mock`)

Según contrato §6.7: sin modelos ni red, salida idéntica byte a byte para la misma entrada.

- `/stt`: `X-Mock-Transcript` (URL-encoded) o `respuesta simulada <sha256[:8]>` / `simulated answer …`; un segmento; palabras uniformes con `0.9`, las que empiezan con `?` van con `0.3` y sin el `?` (también en `text`); `confidence` = promedio simple; `processing_ms: 0`.
- `X-Mock-Fail: 503|504|timeout` en `/stt` (y, como extensión, en `/tts`).
- `/tts`: silencio PCM16 a 16 kHz, `300 + 60 × palabras` ms (ignora `sample_rate`).
- `/voices`: `mock-es`, `mock-en`. `/health`: `"mode": "mock"`.
- Los headers `X-Mock-*` se ignoran en modo real.

## Configuración (variables de entorno)

| Variable | Defecto | |
|---|---|---|
| `CX9_SPEECH_MODE` | `real` | `real` \| `mock` |
| `CX9_SPEECH_TOKEN` | – | Bearer opcional |
| `SPEECH_HOST` / `SPEECH_PORT` | `127.0.0.1` / `8090` | |
| `SPEECH_STT_BACKEND` | `faster-whisper` | `faster-whisper` \| `mock` |
| `SPEECH_WHISPER_MODEL` | `small` | nombre o ruta local a un modelo CTranslate2 |
| `SPEECH_WHISPER_DOWNLOAD_ROOT` | `models/whisper` | |
| `SPEECH_WHISPER_LOCAL_ONLY` | `true` | nunca descarga en runtime |
| `SPEECH_WHISPER_DEVICE` / `_COMPUTE` / `_CPU_THREADS` | `cpu` / `int8` / `2` | |
| `SPEECH_WHISPER_BEAM_SIZE` / `_PARTIAL_BEAM_SIZE` | `5` / `1` | |
| `SPEECH_PRELOAD` | `true` | carga el modelo al arrancar, en segundo plano |
| `SPEECH_FILLER_PROMPT_ES` / `_EN` | ver `config.py` | |
| `SPEECH_TTS_BACKEND` | `auto` | `auto` (piper → espeak) \| `piper` \| `espeak` \| `mock` |
| `SPEECH_PIPER_BIN` | `piper` | |
| `SPEECH_PIPER_VOICE_ES_AR` / `_EN_US` | – | rutas a `.onnx` |
| `SPEECH_ESPEAK_VOICE_ES` / `_EN` | `es-419` / `en-us` | |
| `SPEECH_MAX_UPLOAD_MB` / `SPEECH_MAX_AUDIO_S` / `SPEECH_MAX_TTS_CHARS` | `20` / `600` / `2000` | |
| `SPEECH_STT_TIMEOUT_S` / `SPEECH_TTS_TIMEOUT_S` | `60` / `30` | |
| `SPEECH_MAX_CONCURRENCY` / `SPEECH_QUEUE_TIMEOUT_S` | `1` / `10` | |
| `SPEECH_LOW_CONFIDENCE` / `SPEECH_PAUSE_THRESHOLD_S` | `0.5` / `0.6` | |
