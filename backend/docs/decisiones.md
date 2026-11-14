# Decisiones del backend

Ref: CX9-6174. Cada decisión dice qué se eligió, por qué y qué descarta. Lo que fija
el contrato está en `../../docs/contract.md`; acá va cómo lo implementa el backend.

## 1. Solo práctica

- El WebSocket tiene una lista cerrada de tipos (`internal/protocol`); cualquier otro
  da `UNSUPPORTED_MESSAGE` y 3 seguidos cierran con `4400`.
- El cliente no puede mandar texto de preguntas: salen del escenario, del perfil o del
  motor. El informe da `409 SESSION_NOT_ENDED` mientras la sesión sigue.
- No hay endpoint ni mensaje para audio de terceros ni sugerencias en vivo.

## 2. Concurrencia del WebSocket: un dueño por sesión

- Cada sesión viva tiene un `Runtime` con **una goroutine** que es dueña del motor
  (`internal/engine`, puro y sin locks). Lector del socket, timers, STT/TTS/LLM
  asíncronos y el REST (`POST /end`) le **postean closures**; así no hay carreras sobre
  el estado y los tests corren con `-race`.
- Cada `Conn` tiene un escritor con cola acotada (256 mensajes / 1 MB). Llena → cierre
  `4008` y el cliente reanuda.
- Se usa `github.com/coder/websocket` (antes nhooyr): API con `context`, sin goroutines
  ocultas y con `CloseNow`.

## 3. Secuencia, dedupe y reenvío

- `seq` del servidor: contador atómico; cada mensaje (y cada frame TTS) toma uno.
- `hello` y `ack` van con `seq: 0` fuera de la secuencia (contrato §5.2). El servidor
  ignora el `seq` del `hello`.
- Dedupe del cliente: se descarta en silencio `seq <= last_client_seq`; un hueco da
  `SEQ_OUT_OF_ORDER` con `ref_seq = last+1`, una vez por hueco. Nada se renumera.
- `hello` con `resume: null` es un cliente nuevo y su contador vuelve a empezar; con
  `resume` sigue.
- Buffer de reenvío: últimos 512 mensajes o 60 s. **No** guarda `ack` (no tienen seq) ni
  errores fatales (consumen seq pero son de la conexión que se cierra).
- Al reanudar se manda primero `session.ready` (seq nuevo) y después los reenvíos con su
  seq original; el cliente ordena por seq. Si falta algo, `RESUME_GAP` y se re-emite la
  intervención vigente con `repeat: true`.
- Errores antes del `hello` (p. ej. `HELLO_REQUIRED`) van con `seq` 1 porque la conexión
  todavía no está atada a la secuencia de la sesión.

## 4. Backpressure y acks

- Ventana de 96 KB. En `listening` el servidor manda `ack` cada 250 ms o cada 32 KB
  recibidos (lo primero), aunque los parciales estén apagados y solo si avanzó. Test:
  10 s de audio continuo sin parciales no frenan (`TestLiveAcksKeepTenSecondsFlowingWithoutPartials`).
- Entrada: si hay más de 2 × ventana sin procesar → `BACKPRESSURE`; si sigue 10 s →
  `4008`. El audio aceptado nunca se descarta.
- Salida: con la cola "pesada" (> 64 mensajes o 512 KB) los parciales **se saltean**
  en vez de fusionarse: fusionar dejaría huecos de seq. Si el cliente aceptó `url`, el
  TTS siguiente va por `tts.url`.

## 5. Voz

- Formato de entrada: siempre PCM16LE 16 kHz mono. **Opus nunca se elige en v1**,
  aunque el cliente lo ofrezca (el contrato lo reserva).
- TTS: `wav` (por defecto) o `pcm16le`; `url` fuerza `wav`. Nunca opus (el servicio da 422).
- Parciales: cada `CX9_PARTIAL_INTERVAL` (1,5 s) se mandan **solo los últimos
  `CX9_PARTIAL_WINDOW` (12 s)** con `partial=true`, sin reintentos ni breaker. Cualquier
  falla, incluido el `503` inmediato de "ocupado", devuelve `ErrPartialSkipped`: se
  saltea sin `error` al cliente y sin contar para el breaker (solo métrica
  `op="stt_partial"`). El final manda el turno completo con 1 reintento y breaker.
- Extensiones de `/stt` (`fillers`, `filler_count`, `pauses`, `is_filler`): se toleran y
  son opcionales. Si vienen, las muletillas del scoring salen del STT
  (`details.muletillas_fuente = "stt"`) y las pausas largas van a `Turn.pauses` y
  `details.pausas`; si no, se detectan en el texto y `pausas` es `null`.
- `degraded` en `/health` de voz es usable: `/v1/ready` lo informa con 200.
- STT fallido tras el reintento → `STT_TIMEOUT`/`STT_UNAVAILABLE`, `turn.end{next:repeat}`
  y la misma pregunta otra vez (turn_id nuevo, sin consumir repregunta).
- `tts.played` (contrato §5.14). El TTS del **cierre** es síncrono (va antes de
  `session.end`) y no espera `tts.played`. Para intro, pregunta y repregunta el servidor
  espera `tts.played` antes de pasar a `listening` (o, en la intro, antes de la primera
  pregunta). Si no llega en `duration_ms + CX9_TTS_PLAYED_GRACE_MS` (3 s), o en
  `CX9_TTS_PLAYED_TIMEOUT_MS` (20 s) si la duración no se conoce, avanza igual: no es un
  error, queda un log `warn` y `cx9_tts_played_timeouts_total`. Con `tts_delivery=none`
  avanza enseguida. Ya se esperaba así desde v1.0 (duración + 3 s, también para la intro);
  v1.2 lo documenta, suma el timeout fijo para duración desconocida, el log/métrica y la
  regla explícita de duplicados: el runtime guarda el último `utterance_id` confirmado y
  descarta cualquier `tts.played` con número menor o igual (los `utterance_id` crecen
  siempre, una repetición tiene uno nuevo). Elegido por simple: un entero, sin mapa.

## 5b. Endpointing por silencio (contrato v1.2 §5.15)

- Solo se analiza el audio del micrófono del usuario que manda el cliente en su turno.
- Paquete `internal/endpoint` puro (sin red ni reloj, tiempos en ms de audio del turno):
  VAD de energía RMS sobre frames de 20 ms y aplicación de `speech_ended_ms` /
  `trailing_silence_ms` del `/stt` parcial (relativos a la ventana mandada; se pasan a
  absolutos con el inicio de la ventana). El runtime pone el reloj: la ventana de gracia
  es de reloj (`CX9_ENDPOINT_GRACE_MS`) y se confirma solo si no hubo voz nueva después
  del audio que había al detectar el candidato.
- `auto` empieza con `stt` y cae a `energy` en cuanto un parcial vuelve sin los campos
  (servicio viejo) o falla/está ocupado (503). Para no esperar al próximo parcial
  periódico (1,5 s), cuando la energía ve un silencio candidato se pide un parcial en el
  momento (una vez por silencio). Con endpointing `stt`/`auto` se piden parciales aunque
  el cliente haya apagado `partials` (no se le mandan `transcript.partial`).
- Al cerrar se marca el turno como cerrado por endpoint: los chunks que lleguen tarde de
  ese turno se descartan sin `STATE_CONFLICT` (el cliente todavía no vio el `turn.end`).
- Latencia: al cerrar se lanza enseguida el STT final. Si el último parcial cubre todo el
  turno (ventana desde 0) hasta el fin de voz, en paralelo se consulta al LLM con ese
  parcial (especulativo). Al llegar el final se compara por palabras normalizadas (1 −
  distancia de edición / largo); con ≥ 0,9 se usa la decisión especulativa (si todavía no
  llegó, se espera a esa en vez de lanzar otra) y si no se descarta. Lo guardado y
  puntuado es siempre el texto final. `transcript.final` sale siempre antes que la
  decisión (`turn.end`), aunque la especulativa ya esté lista.
- Métricas: `cx9_endpoint_to_followup_tts_seconds{mode,provider}` (desde el fin de voz
  estimado por la hora de llegada del chunk que lo contiene, o desde `audio.end`, hasta el
  primer frame o `tts.url` de la intervención siguiente; con `tts_delivery=none` no se
  observa), `cx9_turn_stage_seconds{stage=stt_final|llm_followup|tts_first_byte}`,
  `cx9_endpoints_total{source}` y `cx9_speculative_followups_total{result}`.

## 6. Motor de la entrevista

- Repreguntas: tope duro de 2 por pregunta en el motor (además del que diga el LLM).
- `control.repeat`: re-emite la misma intervención, mismo `turn_id` y `question_id`,
  `utterance_id` y seq nuevos, `repeat: true`; para preguntas principales además
  `kind: "repeat"`. Sin `turn.end` y sin consumir repregunta.
- Intro y cierre usan `turn_id "t-0"` (no esperan respuesta); la intro `index: 0` y el
  cierre `index: total`.
- `probe: "other"` existe en el enum del contrato porque el LLM puede repreguntar fuera
  de los probes del escenario; la validación de escenarios **no** lo acepta en `probes`.

## 7. LLM

- Proveedores `openai`, `anthropic`, `xai` (compatible OpenAI), `ollama` y `mock`, con
  timeout (`CX9_LLM_TIMEOUT`), reintentos con backoff y jitter (`CX9_LLM_RETRIES`) y
  circuit breaker por proveedor (5 fallos, half-open a los 30 s).
- Si el proveedor de la sesión falla, la repregunta sale de la lógica local del `mock`,
  la sesión sigue y se avisa con `LLM_DEGRADED` y en `Report.llm_errors`.
- Las claves viven en `secret.String`: `String()`, `MarshalJSON` y `LogValue` devuelven
  `[REDACTED]`.

## 8. Scoring

- 6 dimensiones (claridad, estructura, concreción, duración, muletillas, relevancia).
  Sin datos para una dimensión → `null` y aparece en `faltantes`; nunca un número
  inventado.
- Versión mejorada: solo con palabras del usuario. Si el LLM agrega términos que no
  dijo (números, nombres), se rechazan (`rejected_terms`) y se cae a reordenar las
  frases del usuario; lo que falta va como `[FALTA: …]`.
- `top3` siempre tiene exactamente 3 elementos.
- El scoring corre en una cola en segundo plano (2 workers, deduplicada); el informe
  responde 202 con `Retry-After: 2` mientras tanto.

## 9. Modo historia

- Las preguntas salen de los huecos del perfil; cada respuesta se aprende al perfil
  solo con **evidencia literal** (la frase del usuario). Sin huecos → `409`.
- El aprendizaje del perfil está serializado con un mutex para no pisar cambios.

## 10. Persistencia

- Postgres (pgx/stdlib) y SQLite (modernc, sin CGO) con el mismo SQL salvo diferencias
  mínimas por dialecto; migraciones embebidas y versionadas que corren al arrancar.
- SQLite con una sola conexión (serializa escrituras), WAL y `busy_timeout`.
- Tokens: solo se guarda el SHA-256. Tickets de WS: un solo uso, 60 s.

## 11. Auth y HTTP

- argon2id (m=64 MiB, t=3, p=2) en formato PHC; 5 logins fallidos por minuto → 429.
- Cookie `cx9_session` HttpOnly, SameSite=Strict; `Secure` cuando el `Host` del request no es loopback (independiente de `CX9_ENV`, sin variable para forzarlo).
- `Origin` (escrituras y WS): sin `Origin` pasa; si no, tiene que ser igual al `Host` del request, puerto incluido (o, fuera de prod, estar en `CX9_CORS_ORIGINS`). Un proxy tiene que pasar el `Host` original.
- CSRF: `Origin` en métodos de escritura + solo JSON/YAML/merge-patch (415 si no).
- `/live-ticket` es opcional: un cliente del mismo origen abre el WS con la cookie.
- Idempotency-Key: alcance método + ruta + clave; el hash del cuerpo incluye el media
  type; mismo key con otro cuerpo → 422.

## 12. Apagado y arranque

- Orden de apagado: hub (cierra WS con 1001 y termina sesiones) → HTTP → cola de
  scoring → base. Tope `CX9_SHUTDOWN_TIMEOUT`.
- Al arrancar, sesiones `live`/`detached` huérfanas se cierran como `disconnected` y se
  re-encolan los informes `ended`/`scoring`.

## 13. Imagen

- Multi-stage, `CGO_ENABLED=0`, `distroless/static-debian12:nonroot`, `:8080`,
  `HEALTHCHECK` con `coach healthcheck` (la imagen no tiene shell ni curl).
