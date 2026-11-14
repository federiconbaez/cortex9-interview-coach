# Contrato WebSocket de Subtítulos en vivo (`cx9.captions.v1`), versión 0.3

Estado: acordado entre /captions (Apex) y /frontend (Cronos). Ref: CX9-6174.

## 1. Alcance y límites

- El servicio captura audio del sistema (monitor de PipeWire o PulseAudio) o de una entrada elegida, lo transcribe, detecta el idioma y opcionalmente lo traduce.
- **Solo transcribe y traduce.** No genera respuestas sugeridas, no tiene modo "qué responder" y no se conecta al motor de entrevistas ni al backend.
- No guarda audio ni texto en disco. Solo persiste la configuración en `~/.config/cx9-captions/config.json` y escribe logs sin transcripciones.
- Por defecto todo es local. El audio sale de la PC solo si el usuario elige un proveedor STT remoto, y el texto sale solo si elige una traducción remota.

## 2. Conexión

- URL directa: `ws://127.0.0.1:8091/v1/captions/live`, con el subprotocolo `cx9.captions.v1`.
- Con nginx (compose): same-origin en `/captions/v1/live`.
- El servidor exige que `Origin` coincida con `Host`, incluido el puerto. Si no coincide, cierra con 1008.
- Un solo cliente a la vez. Un segundo cliente recibe el cierre 4409.
- Cada mensaje es un objeto JSON con `type` y `seq`. `seq` empieza en 1 en cada conexión, sube de a 1 y no hay replay.
- Reconexión: backoff exponencial de 0,5 s a 10 s. Al reconectar, el servidor vuelve a mandar `hello`, `capabilities` y `devices`, y la configuración es la última aplicada.

## 3. Servidor → cliente

### hello (primer mensaje)
```json
{"type":"hello","seq":1,"version":"0.3",
 "source":{"device":"alsa_output.pci-0000_00_1f.3.analog-stereo.monitor","kind":"monitor"},
 "stt":{"provider":"local","model":"small","remote":false},
 "translation":{"enabled":true,"target_lang":"es","provider":"argos","remote":false},
 "privacy":{"audio_leaves_pc":false,"text_leaves_pc":false,"destinations":[]}}
```

### capabilities (justo después de hello)
```json
{"type":"capabilities","seq":2,
 "target_langs":["es","en","pt"],
 "stt_providers":[
   {"id":"local","label":"Local (faster-whisper)","remote":false,"available":true,"models":["tiny","small"],"streaming":"chunked"},
   {"id":"whisper_api","label":"OpenAI Whisper API","remote":true,"available":false,"models":["whisper-1"],"streaming":"chunked","host":"api.openai.com"},
   {"id":"openai_compat","label":"Compatible con OpenAI","remote":true,"available":false,"models":[],"streaming":"chunked","host":"<configurado>"}],
 "providers":[
   {"id":"none","label":"Sin traducción","remote":false,"available":true,"langs":[]},
   {"id":"argos","label":"Argos (local)","remote":false,"available":true,"langs":["en>es"]},
   {"id":"nllb","label":"NLLB (local)","remote":false,"available":false,"langs":[]},
   {"id":"llm","label":"LLM configurado","remote":true,"available":true,"langs":["*"],"host":"api.openai.com"}]}
```
- `remote` y `host` están disponibles **antes** de elegir, para que la UI muestre el aviso de antemano.
- `llm` es `remote:false` cuando el LLM configurado es Ollama local.
- `available:false` indica que falta el modelo o la clave. Las claves están solo en `.env` y nunca pasan por el WS.

### devices
Llega al conectar, cuando cambia la lista y como respuesta a `devices.list`.
```json
{"type":"devices","seq":3,"devices":[{"id":"…monitor","label":"Parlantes (monitor)","kind":"monitor","default":true},{"id":"…","label":"Micrófono","kind":"input","default":false}]}
```

### caption.partial
```json
{"type":"caption.partial","seq":10,"segment_id":"s-0007","t_start_ms":41230,"t_end_ms":43010,"lang":"en","lang_prob":0.97,"text":"so what I was trying to","stt_provider":"local"}
```
- Reemplaza al parcial anterior con el mismo `segment_id`.
- Los parciales no traen traducción.

### caption.final
```json
{"type":"caption.final","seq":12,"segment_id":"s-0007","t_start_ms":41230,"t_end_ms":44120,"lang":"en","lang_prob":0.98,
 "text":"So what I was trying to say is…","stt_provider":"local",
 "translation":{"text":"Lo que intentaba decir es…","target_lang":"es","provider":"argos"},
 "latency_ms":{"capture_to_send":40,"network_rtt":0,"stt":310,"translate":120,"total":520}}
```
- `translation` es `null` si la traducción está apagada o falló. Si falló, además llega un `error` TRANSLATE_FAILED.
- Un final puede llegar sin parciales previos. El final cierra el segmento, y los `segment_id` no se reutilizan dentro de la conexión.
- `latency_ms.total` se mide desde el fin de voz del segmento hasta el envío del final.

### status (aprox. 1 por segundo)
```json
{"type":"status","seq":20,"state":"listening","level_dbfs":-32.5,"fallback":false}
```
- `state` toma uno de estos valores: `listening`, `paused`, `no_audio` o `error`.
- `fallback:true` indica que se pasó del STT remoto al local por un error remoto.

### config.applied
Responde a cada `config` aceptado.
```json
{"type":"config.applied","seq":21,"device_id":"…monitor",
 "stt":{"provider":"whisper_api","model":"whisper-1","remote":true},
 "translation":{"enabled":true,"target_lang":"es","provider":"llm","remote":true},
 "privacy":{"audio_leaves_pc":true,"text_leaves_pc":true,"destinations":["api.openai.com"]}}
```

### pong
```json
{"type":"pong","seq":22}
```

### error
```json
{"type":"error","seq":23,"code":"DEVICE_NOT_FOUND","detail":"…"}
```

| code | ¿Recuperable? | Efecto |
|---|---|---|
| BAD_MESSAGE | sí | se ignora el mensaje |
| DEVICE_NOT_FOUND | sí | se mantiene el dispositivo anterior |
| PROVIDER_UNAVAILABLE | sí | se mantiene la configuración anterior |
| LANG_UNSUPPORTED | sí | se mantiene la configuración anterior |
| CAPTURE_FAILED | no | `status.state = "error"` |
| STT_FAILED | sí | se pierde el segmento |
| TRANSLATE_FAILED | sí | el final llega con `translation:null` |
| REMOTE_AUTH | sí | fallback a local si está habilitado |
| REMOTE_RATE_LIMIT | sí | backoff y, si está habilitado, fallback |
| REMOTE_TIMEOUT | sí | idem |

Un `config` rechazado responde con `error` y no con `config.applied`.

## 4. Cliente → servidor

```json
{"type":"config","device_id":"…","stt_provider":"local","target_lang":"es","translate":true,"provider":"argos"}
{"type":"devices.list"}
{"type":"pause"}
{"type":"resume"}
{"type":"ping"}
```
En `config`, todos los campos son opcionales; los que se omiten no cambian.

## 5. Cierres

| código | motivo |
|---|---|
| 1000 | normal |
| 1001 | el servidor se apaga |
| 1008 | Origin no permitido |
| 1011 | error interno |
| 4409 | ya hay un cliente conectado |

## 6. Privacidad en la UI (obligatorio)

- Si `privacy.audio_leaves_pc` es true: aviso fijo y destacado "El audio (incluidas las voces de otras personas) sale de tu PC hacia <host>". Va **antes** de elegir el proveedor (con `capabilities.stt_providers[].remote`) y mientras esté activo.
- Si `privacy.text_leaves_pc` es true: aviso fijo "El texto transcripto sale de tu PC hacia <host> para traducirse".
- El navegador no persiste transcripciones.

## 7. Streaming remoto

- Antes de enviar, un VAD local recorta el audio y solo se mandan los tramos con voz.
- Parciales: se reenvía la ventana del segmento en curso cada 1 a 1,5 s, con un tope de costo configurable (`CX9_CAPTIONS_REMOTE_PARTIALS=on|off`, `CX9_CAPTIONS_REMOTE_MAX_SECONDS_PER_MIN`).
- Final: se envía el segmento completo cuando se cierra.
