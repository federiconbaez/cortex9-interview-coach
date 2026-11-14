# VAD (detección de voz) — notas

Código: `src/audio/vad.ts`. Tests: `src/audio/vad.test.ts` (9 tests con PCM sintético de `src/audio/synth.ts`).

## Cómo funciona

1. El micrófono entrega bloques float32; `mic.ts` los lleva a 16 kHz y los corta en chunks de
   100 ms (1600 muestras). El VAD corre sobre esos mismos chunks.
2. Por chunk se calcula el RMS.
3. Umbral = `max(minRms, piso_de_ruido × ratio)`.
4. El piso de ruido es una media exponencial (`noiseAlpha`) que **solo** se actualiza en
   chunks sin voz, así hablar no sube el umbral.
5. `speech_start` cuando hay `minSpeechMs` seguidos sobre el umbral (filtra clics).
6. `speech_end` cuando hay `hangoverMs` seguidos bajo el umbral (no corta en pausas entre frases).
   En modo automático, `speech_end` termina la respuesta y se manda `audio.end`.

## Valores por defecto

| Parámetro | Valor |
|---|---|
| `minRms` | 0,01 |
| `ratio` | 3 |
| `minSpeechMs` | 120 ms |
| `hangoverMs` | 1500 ms |
| `noiseAlpha` | 0,05 |
| `initialNoiseRms` | 0,003 |

## Qué cubren los tests

Silencio digital, ruido por debajo del mínimo, inicio y fin con hangover, pausas más cortas que
el hangover, clics más cortos que `minSpeechMs`, ruido fuerte que sube el umbral, duración de
cuadro explícita y `reset`. `mic.test.ts` prueba el VAD dentro de la captura con un AudioWorklet falso.

## Límites

- Es un VAD de energía: con ruido fuerte y variable (TV, gente hablando) puede cortar tarde o
  no cortar. Para esos casos está el push-to-talk (barra espaciadora o botón).
- El e2e no cubre el corte automático real (el micrófono falso de Chromium es un tono continuo).
  Lo mismo vale para `test:e2e:real` contra el backend Go: las respuestas se terminan con el
  botón (la de 4,5 s prueba la ventana de 96 KB y los mensajes `ack`, no el VAD).
- `getUserMedia` pide `echoCancellation` y `noiseSuppression`; el resultado depende del navegador.
