# Revisión de seguridad y privacidad (`/speech`, `/deploy`, `/.github`, raíz)

Alcance: el código de Apex (`/speech`, `/deploy`, `/.github` y archivos raíz). `/backend` (Nexus) y `/frontend` (Cronos) tienen su propia revisión; acá solo se fija lo que el despliegue les garantiza o les exige.

## Principio

**El audio y el perfil se quedan en tu máquina.** Lo único que puede salir es el **texto** que el backend le manda al proveedor LLM que vos elijas (`openai`, `anthropic`, `xai`). Con `mock` u `ollama` local no sale nada (contrato §7).

## Qué garantiza cada pieza

| Tema | Medida |
|---|---|
| Audio | `/speech` lo procesa en memoria; para Ogg/Opus usa un temporal que borra al terminar. No lo guarda ni lo loguea. En Docker, `/tmp` es `tmpfs` y el sistema de archivos es de solo lectura. |
| Transcripciones | No se loguean (sin access log, porque la URL lleva `prompt`). Las métricas no tienen texto del usuario en etiquetas (test `test_metrics_never_contain_user_text`). Los `detail` de error no incluyen rutas ni mensajes del motor. |
| Modelos | Nunca se descargan en runtime: `SPEECH_WHISPER_LOCAL_ONLY=true` (`local_files_only`) y `HF_HUB_OFFLINE=1` en la imagen. La única descarga es `deploy/local/fetch-models.sh`, que corrés vos a mano y solo baja archivos públicos. |
| Red (Docker) | speech y postgres solo en `backplane` (`internal: true`, sin salida a Internet) y sin puertos publicados. Solo backend tiene salida, para el LLM. Todo puerto publicado está en `127.0.0.1` (CI lo verifica). |
| Red (sin Docker) | Todos los procesos escuchan en `127.0.0.1`. |
| Secretos | Solo en `deploy/.env` (ignorado por git). `.env.example` no tiene claves reales; compose falla si faltan `POSTGRES_PASSWORD` o `GRAFANA_ADMIN_PASSWORD`. CI no usa secretos. |
| Servicio a servicio | `CX9_SPEECH_TOKEN` opcional (Bearer comparado en tiempo constante) por si speech se expone fuera de la red interna. |
| Grafana | Sin anónimo ni registro; telemetría, chequeo de updates, noticias y preinstalación de plugins apagados. |
| Contenedores | `cap_drop: [ALL]`, `no-new-privileges`, usuario no root en speech y nginx, límites de memoria, logs rotados. |
| Entrada | Límite de 20 MB / 600 s cortado a nivel ASGI (también con `chunked`), 2000 caracteres en TTS, timeouts por operación, concurrencia acotada. El texto va a piper/espeak por stdin, sin shell (`subprocess` con lista de argumentos). |
| Frontend (nginx de `/deploy`) | Mismo origen (sin CORS), CSP `default-src 'self'`, `X-Frame-Options DENY`, `Permissions-Policy` con micrófono solo `self`, sin access log. |
| CI | `permissions: contents: read`, `persist-credentials: false`, acciones fijadas por SHA, sin push de imágenes. |
| Producto | Solo práctica: no hay ningún modo para escuchar a un entrevistador real ni capturar audio del sistema u otra persona (contrato §1). `/speech` solo transcribe lo que el backend le manda de la sesión de práctica. |

## Verificación: URLs salientes en el código de Apex

Comando (desde la raíz):

```bash
git ls-files speech deploy .github README.md .gitignore \
  | xargs rg -n -o "https?://[^\s\"'\)\`>]+" | sort -u
```

Resultado: todas las URLs son `127.0.0.1`, `localhost` o nombres internos de compose (`speech`, `backend`, `prometheus`), más el placeholder `host.docker.internal:11434` (Ollama en tu propio host). **La única URL externa es `https://huggingface.co` en `deploy/local/fetch-models.sh`**, descarga manual y opcional de modelos públicos (no envía datos). No hay clientes HTTP en `speech/coach_speech` (`rg "requests\.|httpx|urlopen|socket\." speech/coach_speech` no encuentra nada; `urllib.parse.unquote` solo decodifica un header).

Verificación en ejecución: el servicio en modo real (whisper `tiny` + piper `es_AR`) atendió `/stt` (L16 y Ogg/Opus) y `/tts` con un audit hook de Python sobre `socket.connect`/`socket.getaddrinfo`/`http.client.connect`: **cero eventos de red**. (El hook no ve procesos hijos; piper y espeak-ng son binarios locales sin red.)

URLs del proveedor LLM: viven en `/backend` (Nexus) y dependen del proveedor que elijas. No están en el código de Apex.

## Riesgos conocidos y pendientes

- `.env.example` trae contraseñas de ejemplo (`cambiame-…`): cambialas antes de usarlo.
- Las imágenes base están fijadas por tag, no por digest (no había Docker en la máquina de desarrollo para resolver digests).
- prometheus y grafana están en `egress` solo para poder publicar su puerto en `127.0.0.1`; grafana tiene la telemetría apagada y prometheus solo hace scrape interno.
- `docker-compose.debug.yml` saca a speech y postgres de la red aislada: usalo solo para depurar.
- En modo sin Docker no hay aislamiento de red: depende de que nada escuche fuera de `127.0.0.1`.
- `fetch-models.sh` no verifica checksums de los modelos descargados.
