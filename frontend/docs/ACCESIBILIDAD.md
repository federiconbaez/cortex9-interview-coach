# Accesibilidad — notas

Objetivo: WCAG 2.1 AA. Verificado con axe (`@axe-core/playwright`, etiquetas wcag2a, wcag2aa,
wcag21a, wcag21aa): 7 chequeos en 5 de los 14 e2e contra el mock — pantalla de login y app ya
logueada, historial, perfil, vuelta al login por 401 (con el aviso de sesión vencida), y sesión
por voz (durante la sesión y en el informe, con muletillas y pausas). Contra el backend real
(`test:e2e:real`) hay 2 chequeos más: el informe de la sesión por texto y el de la sesión por voz.
La sesión por texto contra el mock, push-to-talk, reconexión, repetir y `tts_delivery: none` no corren axe.

## Qué hace la UI

- **Etiquetas:** todo control tiene `<label>` o `aria-label` traducido; los grupos usan `fieldset`/`legend`.
- **Teclado:** todo se opera con teclado; enlace "Saltar al contenido"; push-to-talk con la
  barra espaciadora (salvo si el foco está en un campo de texto); los botones deshabilitados
  por estado usan `aria-disabled` cuando deben seguir siendo enfocables.
- **Foco:** al cambiar de vista va a `<main>`; al terminar la práctica, al título "Práctica
  terminada"; al cargar el informe, a su título; al guardar un perfil inválido, al primer
  campo con error (`aria-invalid` + `aria-describedby` con el mensaje).
- **Anuncios:** la pregunta nueva con `aria-live="polite"`; la transcripción es un
  `role="log"` (solo se anuncian líneas finales, no cada parcial); estado de la respuesta y
  de la conexión con `role="status"`; errores con `role="alert"`.
- **Gráfico de progreso:** SVG con `role="img"`, `<title>` y `<desc>` con el resumen, y la
  misma información en una tabla y en el `figcaption` (tendencia).
- **Movimiento reducido:** las animaciones solo se activan con `prefers-reduced-motion: no-preference`.
- **Contraste:** paleta clara y oscura (`prefers-color-scheme`); foco visible con `:focus-visible`.
- **Idioma:** `<html lang>` sigue al idioma elegido (es/en).
- **Sesión vencida:** si el backend responde 401 o cierra con 4001, se vuelve al login con el
  aviso en `role="alert"`.
- **Repeticiones:** cuando la entrevistadora repite una pregunta, la etiqueta visible agrega
  "Repetición"; la presentación y el cierre no muestran "Pregunta N de M".

## Pendiente o no verificado

- axe corre solo con el esquema claro y solo en Chromium.
- No se probó con lectores de pantalla reales (NVDA, VoiceOver).
