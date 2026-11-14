// Mensajes de ejemplo del contrato de subtítulos v0.2 (solo para tests).
import type { Provider, ServerMsg } from '../captions/protocol';

export const PROVIDERS: Provider[] = [
  { id: 'none', label: 'Sin traducción', remote: false, available: true, langs: ['es', 'en', 'fr'] },
  { id: 'argos', label: 'Argos', remote: false, available: true, langs: ['es', 'en'] },
  { id: 'nllb', label: 'NLLB', remote: false, available: false, langs: ['es', 'en', 'fr'] },
  { id: 'llm', label: 'LLM remoto', remote: true, available: true, langs: ['es', 'en', 'fr'] },
];

export function msgs() {
  let seq = 0;
  const next = <T extends Record<string, unknown>>(m: T) => ({ ...m, seq: ++seq }) as unknown as ServerMsg;
  return {
    reset: () => (seq = 0),
    hello: (remote = false, provider: string = remote ? 'llm' : 'none') =>
      next({ type: 'hello', version: '0.2', source: { device: 'monitor-0', kind: 'monitor' }, translation: { enabled: provider !== 'none', target_lang: 'es', provider, remote } }),
    caps: () => next({ type: 'capabilities', target_langs: ['es', 'en', 'fr'], providers: PROVIDERS }),
    devices: () =>
      next({
        type: 'devices',
        devices: [
          { id: 'monitor-0', label: 'Monitor', kind: 'monitor', default: true },
          { id: 'mic-0', label: 'Mic', kind: 'input', default: false },
        ],
      }),
    applied: (provider: string, remote: boolean, device = 'monitor-0', target = 'es') =>
      next({ type: 'config.applied', device_id: device, translation: { enabled: provider !== 'none', target_lang: target, provider, remote } }),
    partial: (id: string, text: string) => next({ type: 'caption.partial', segment_id: id, t_start_ms: 0, t_end_ms: 900, lang: 'en', lang_prob: 0.9, text }),
    final: (id: string, text: string, tr: string | null = null) =>
      next({
        type: 'caption.final',
        segment_id: id,
        t_start_ms: 0,
        t_end_ms: 900,
        lang: 'en',
        lang_prob: 0.97,
        text,
        translation: tr === null ? null : { text: tr, target_lang: 'es', provider: 'argos' },
        latency_ms: { stt: 100, translate: tr === null ? 0 : 30, total: 130 },
      }),
    status: (state: string, level = -20) => next({ type: 'status', state, level_dbfs: level }),
    error: (code: string) => next({ type: 'error', code, detail: 'x' }),
  };
}
