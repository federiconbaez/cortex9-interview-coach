# Subtítulos en vivo — notas

Vista aparte, en `#/subtitulos` (enlace "Subtítulos en vivo" en el encabezado). Código:
`src/captions/` (protocolo, cliente WS, estado) y `src/components/CaptionsView.tsx`.

## Qué hace

- Muestra la transcripción del **audio del sistema** que captura el servicio `captions` de Apex
  (dispositivo `monitor` o de entrada), y la traducción si el usuario la pide.
- Mientras se habla aparece un texto provisorio (`caption.partial`), que se reemplaza por el
  mismo `segment_id` hasta que llega el texto final (`caption.final`).
- Cada línea muestra el idioma detectado y su probabilidad. Si la traducción está activa, la
  traducción va debajo del original.
- Permite elegir el dispositivo de captura, el proveedor de traducción y el idioma de destino,
  pausar y reanudar, y cambiar el tamaño de la letra (100 % a 200 %). Muestra el nivel de audio
  y los estados "En pausa", "Sin audio" y "Error de captura".

## Qué NO hace

- **No sugiere respuestas** ni qué decir. No tiene botones ni modos de respuesta: solo
  transcribe y traduce.
- **No se conecta con la práctica de entrevistas.** Otro servicio (no pasa por el backend de
  Nexus), otro cliente WebSocket (`src/captions/connection.ts`, independiente de `src/api/ws.ts`),
  otra ruta y ningún estado compartido. La vista no llama a `/v1/...`, ni siquiera para el login
  (lo verifican un test unitario y uno e2e).
- **No guarda nada** (ver Privacidad).

## Privacidad

- **El navegador no persiste nada:** ni subtítulos, ni traducciones, ni preferencias de la vista
  (dispositivo, proveedor, idioma, tamaño de letra). No usa localStorage, sessionStorage,
  IndexedDB ni cookies. Los subtítulos viven en memoria, con un tope de 500 segmentos, y se
  pierden al salir de la vista o recargar. "Borrar de la pantalla" los descarta antes.
  Lo comprueban un test unitario (no hay `setItem`) y uno e2e (`localStorage` solo tiene
  `cx9.lang`, el idioma de la interfaz de toda la app; `sessionStorage` e IndexedDB quedan vacíos).
- **El servicio** (según el contrato v0.2) no guarda audio ni texto: solo la configuración y logs
  sin transcripciones.
- **El audio nunca sale de la PC.** Con un proveedor de traducción **remoto**, lo que sale es el
  **texto** de los subtítulos, hacia ese proveedor:
  - Antes de elegirlo: la opción dice "(remoto: el texto sale de tu PC)", hay una nota fija
    debajo del selector y, al elegirla, una confirmación. Si se cancela, no se manda nada.
  - Mientras la traducción aplicada sea remota (`translation.remote` en `hello` o
    `config.applied`), hay un aviso visible y persistente, que no se puede cerrar, con el nombre
    del proveedor.

## Protocolo (contrato WS v0.2 de Apex)

`captions/CONTRACT.md` **todavía no está en el repo** (revisado el 14/11/2026). La implementación
sigue el resumen v0.2 que pasó Apex; cuando el archivo exista, manda él
(`TODO(CX9-6174)` en `src/captions/protocol.ts`).

- URL same-origin `ws(s)://<host>/captions/v1/live` con el subprotocolo `cx9.captions.v1`.
  - En desarrollo, el proxy de Vite reenvía `/captions` (con `ws: true`) a
    `COACH_CAPTIONS_URL` (por defecto `http://127.0.0.1:8091`) y reescribe
    `/captions/v1/live` → `/v1/captions/live`.
  - En deploy lo expone nginx. `deploy/frontend/nginx.conf` todavía no tiene ese `location`.
- Al conectar llegan `hello`, `capabilities` y `devices`; después `status` cada ~1 s, y
  `caption.partial` y `caption.final`.
- Los mensajes del servidor llevan `seq` desde 1 por conexión: los duplicados se descartan y los
  huecos se registran en la consola. No hay resume.
- **Configuración:** cada `config` va completo (`device_id`, `translate`, `target_lang`,
  `provider`). La UI muestra lo que confirma `config.applied`, no lo elegido; mientras tanto dice
  "Aplicando la configuración…". Si llega un error de rechazo (`BAD_MESSAGE`,
  `DEVICE_NOT_FOUND`, `PROVIDER_UNAVAILABLE`, `LANG_UNSUPPORTED`), queda la configuración anterior.
- Proveedores con `available: false` e idiomas que no están en `langs` del proveedor aparecen
  deshabilitados.
- **Segmentos:** un final puede llegar sin parciales previos. Un final cierra el segmento para
  siempre: los parciales tardíos se ignoran. Como los `segment_id` pueden repetirse entre
  conexiones, la clave interna es conexión + `segment_id`.
- **Errores:** todos se pueden cerrar, salvo `CAPTURE_FAILED`, que deja el estado en "Error de
  captura" hasta que un `status` muestre que la captura volvió.
- **Cierres:**
  - 4409 (ya hay otro cliente): mensaje claro y botón "Reintentar", sin reintento automático.
  - 1008 (origen no permitido): sin reintento.
  - 1001, 1011 y cortes: reconexión con backoff exponencial de 0,5 a 10 s. La conexión nueva
    trae `hello`, `capabilities` y `devices` desde cero, y la configuración vuelve a la última
    aplicada por el servidor. Los subtítulos ya mostrados quedan en pantalla.

### Supuestos (a confirmar con CONTRACT.md)

- Los mensajes del cliente también llevan `seq` desde 1 por conexión, porque el resumen dice que
  todos los mensajes llevan `type` y `seq`.
- `hello.source.device` es el id del dispositivo, el mismo de `devices[].id` y `config.device_id`.
- Un cierre 1000 iniciado por el servidor no se reintenta solo; hay un botón para reintentar.
- Se manda `ping` cada 15 s y, si no llega ningún mensaje en 10 s (`status` es ~1/s), la
  conexión se da por perdida y se reconecta.
- Al activar "Traducir" con el proveedor `none`, el cliente lo pide tal cual y el servidor decide
  (aplica o rechaza).

## Mock y tests

- `npm run mock:captions`: `mock-server/captions.mjs`, en `127.0.0.1:18791` (`CAPTIONS_MOCK_PORT`).
  - Secuencia determinista por conexión: parciales que se reemplazan, un final sin parciales,
    un parcial tardío de un segmento cerrado y otro idioma.
  - Proveedores `none` y `argos` (locales), `nllb` (no disponible) y `llm` (remoto).
  - Un solo cliente (4409), origen permitido (1008) y validación de `config` con sus errores.
  - Ganchos `/__mock/*` para cortar con un código, simular sin audio o fallo de captura, y
    quitar o agregar dispositivos.
- Contra el mock: `COACH_CAPTIONS_URL=http://127.0.0.1:18791 npm run dev` → `http://127.0.0.1:5173/#/subtitulos`.
- Unitarios: `src/captions/state.test.ts` (9), `src/captions/connection.test.ts` (9),
  `src/components/CaptionsView.test.tsx` (8).
- e2e: `e2e/captions.spec.ts` (6), con axe sobre la vista y sobre la confirmación remota abierta.
- El servicio real `captions --mock` todavía no está en el repo, así que no hay un e2e opcional
  contra él (como `test:e2e:real`).

## Límites

- Solo probado en Chromium, contra el mock; ni contra el servicio real ni con audio real.
- El aviso "Sin audio" y el nivel vienen del servicio; la vista no mide audio por su cuenta.
- La región `aria-live` anuncia el último final (original y traducción). Si llegan muchos
  finales seguidos, el lector de pantalla puede saltear alguno; la lista completa queda en pantalla.
