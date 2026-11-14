import { describe, expect, it } from 'vitest';
import { msgs, PROVIDERS } from '../test/captions';
import { parseServerMsg } from './protocol';
import {
  buildConfig,
  captionsReducer,
  initialCaptions,
  langSelectable,
  MAX_SEGMENTS,
  needsRemoteConfirm,
  providerById,
  remoteNoticeVisible,
  type CaptionsAction,
  type CaptionsState,
} from './state';

const run = (actions: CaptionsAction[], s: CaptionsState = initialCaptions()) => actions.reduce(captionsReducer, s);
const open: CaptionsAction = { type: 'conn', state: 'open', info: { attempt: 0, delayMs: null, code: null } };

describe('captions state (contrato v0.2)', () => {
  it('los parciales se reemplazan por segment_id y el final cierra el segmento', () => {
    const m = msgs();
    const s = run([open, { type: 'msg', m: m.partial('a', 'Hel') }, { type: 'msg', m: m.partial('a', 'Hello wor') }]);
    expect(s.segments).toHaveLength(1);
    expect(s.segments[0]).toMatchObject({ text: 'Hello wor', final: false, translation: null });
    const f = run([{ type: 'msg', m: m.final('a', 'Hello world.') }, { type: 'msg', m: m.partial('a', 'tardío') }], s);
    expect(f.segments).toHaveLength(1);
    expect(f.segments[0]).toMatchObject({ text: 'Hello world.', final: true });
    expect(f.lastFinal?.text).toBe('Hello world.');
    // un segundo final del mismo segmento también se ignora
    expect(run([{ type: 'msg', m: m.final('a', 'otro') }], f).segments[0]?.text).toBe('Hello world.');
  });

  it('final con y sin traducción, y final sin parciales previos', () => {
    const m = msgs();
    const s = run([open, { type: 'msg', m: m.final('x', 'No translation') }, { type: 'msg', m: m.final('y', 'Hello', 'Hola') }]);
    expect(s.segments.map((g) => [g.segmentId, g.final, g.translation?.text ?? null])).toEqual([
      ['x', true, null],
      ['y', true, 'Hola'],
    ]);
    expect(s.lastFinal?.translation?.provider).toBe('argos');
  });

  it('los ids de segmento se separan por conexión (no hay replay al reconectar)', () => {
    const m = msgs();
    let s = run([open, { type: 'msg', m: m.hello() }, { type: 'msg', m: m.final('seg-1', 'uno') }]);
    s = run([{ type: 'conn', state: 'reconnecting', info: { attempt: 1, delayMs: 500, code: 1011 } }, open], s);
    expect(s.applied).toBeNull();
    m.reset();
    s = run([{ type: 'msg', m: m.hello(false, 'argos') }, { type: 'msg', m: m.final('seg-1', 'uno otra vez') }], s);
    expect(s.segments.map((g) => g.text)).toEqual(['uno', 'uno otra vez']);
    expect(s.applied?.translation.provider).toBe('argos'); // vuelve a la última config aplicada por el servidor
    expect(s.epoch).toBe(2);
  });

  it('guarda como máximo MAX_SEGMENTS en memoria y se puede borrar', () => {
    const m = msgs();
    const acts: CaptionsAction[] = [open];
    for (let i = 0; i < MAX_SEGMENTS + 5; i += 1) acts.push({ type: 'msg', m: m.final(`s${i}`, `t${i}`) });
    const s = run(acts);
    expect(s.segments).toHaveLength(MAX_SEGMENTS);
    expect(s.segments[0]?.text).toBe('t5');
    expect(run([{ type: 'clear' }], s).segments).toEqual([]);
  });

  it('la UI refleja config.applied, no lo elegido; un rechazo descarta lo pendiente', () => {
    const m = msgs();
    let s = run([open, { type: 'msg', m: m.hello() }, { type: 'msg', m: m.caps() }, { type: 'msg', m: m.devices() }]);
    const req = buildConfig(s, { provider: 'argos', translate: true });
    if (!req) throw new Error('sin config');
    expect(req).toEqual({ type: 'config', device_id: 'monitor-0', translate: true, target_lang: 'es', provider: 'argos' });
    s = run([{ type: 'configSent', req }], s);
    expect(s.pending).not.toBeNull();
    expect(s.applied?.translation.provider).toBe('none');
    s = run([{ type: 'msg', m: m.applied('argos', false) }], s);
    expect(s.pending).toBeNull();
    expect(s.applied?.translation.provider).toBe('argos');
    s = run([{ type: 'configSent', req: { ...req, device_id: 'gone' } }, { type: 'msg', m: m.error('DEVICE_NOT_FOUND') }], s);
    expect(s.pending).toBeNull();
    expect(s.applied?.deviceId).toBe('monitor-0');
    expect(s.error?.code).toBe('DEVICE_NOT_FOUND');
    expect(s.fatal).toBe(false);
    // errores que no rechazan config (p. ej. TRANSLATE_FAILED) no tocan lo pendiente
    s = run([{ type: 'configSent', req }, { type: 'msg', m: m.error('TRANSLATE_FAILED') }], s);
    expect(s.pending).not.toBeNull();
    expect(run([{ type: 'dismissError' }], s).error).toBeNull();
  });

  it('status: estados, nivel y CAPTURE_FAILED como único error no recuperable', () => {
    const m = msgs();
    let s = run([open, { type: 'msg', m: m.status('listening', -18) }]);
    expect([s.capture, s.levelDbfs]).toEqual(['listening', -18]);
    s = run([{ type: 'msg', m: m.status('paused', -90) }], s);
    expect(s.capture).toBe('paused');
    s = run([{ type: 'msg', m: m.status('no_audio', -80) }], s);
    expect(s.capture).toBe('no_audio');
    s = run([{ type: 'msg', m: m.error('CAPTURE_FAILED') }], s);
    expect([s.capture, s.fatal, s.error?.code]).toEqual(['error', true, 'CAPTURE_FAILED']);
    expect(run([{ type: 'dismissError' }], s).error).not.toBeNull(); // no se descarta
    s = run([{ type: 'msg', m: m.status('error') }, { type: 'msg', m: m.status('listening') }], s);
    expect([s.fatal, s.error]).toEqual([false, null]);
  });

  it('aviso remoto: confirmación antes de elegir un proveedor remoto y aviso persistente mientras remote=true', () => {
    const m = msgs();
    let s = run([open, { type: 'msg', m: m.hello() }, { type: 'msg', m: m.caps() }]);
    expect(remoteNoticeVisible(s)).toBe(false);
    expect(needsRemoteConfirm(s, { provider: 'llm', translate: true })).toBe(true);
    expect(needsRemoteConfirm(s, { provider: 'llm', translate: false })).toBe(false);
    expect(needsRemoteConfirm(s, { provider: 'argos', translate: true })).toBe(false);
    s = run([{ type: 'msg', m: m.applied('llm', true) }], s);
    expect(remoteNoticeVisible(s)).toBe(true);
    // ya aplicado: cambiar solo el idioma no vuelve a pedir confirmación
    expect(needsRemoteConfirm(s, { provider: 'llm', translate: true })).toBe(false);
    s = run([{ type: 'msg', m: m.applied('argos', false) }], s);
    expect(remoteNoticeVisible(s)).toBe(false);
    // hello con remote=true también muestra el aviso (p. ej. al reconectar)
    expect(remoteNoticeVisible(run([open, { type: 'msg', m: m.hello(true) }]))).toBe(true);
  });

  it('proveedores no disponibles e idiomas fuera de los langs del proveedor', () => {
    const s = { targetLangs: ['es', 'en', 'fr'] };
    expect(langSelectable(s, providerById(PROVIDERS, 'argos'), 'fr')).toBe(false);
    expect(langSelectable(s, providerById(PROVIDERS, 'argos'), 'en')).toBe(true);
    expect(langSelectable(s, providerById(PROVIDERS, 'none'), 'fr')).toBe(true);
    expect(langSelectable(s, null, 'de')).toBe(false);
    expect(providerById(PROVIDERS, 'nllb')?.available).toBe(false);
    expect(buildConfig({ applied: null, devices: [], targetLangs: [] }, {})).toBeNull();
  });

  it('parseServerMsg valida cada tipo y descarta lo que no cumple el contrato', () => {
    const m = msgs();
    for (const x of [m.hello(), m.caps(), m.devices(), m.applied('none', false), m.partial('a', 'x'), m.final('a', 'y', 'z'), m.status('listening'), m.error('STT_FAILED')])
      expect(parseServerMsg(JSON.stringify(x))).toEqual(x);
    expect(parseServerMsg(JSON.stringify({ type: 'pong', seq: 3 }))).toEqual({ type: 'pong', seq: 3 });
    expect(parseServerMsg(JSON.stringify({ type: 'error', seq: 1, code: 'X' }))).toMatchObject({ code: 'X' });
    for (const bad of [
      'no json',
      { type: 'hello', seq: 0 },
      { type: 'status', seq: 1, state: 'dormido', level_dbfs: -1 },
      { type: 'caption.final', seq: 1, segment_id: 'a', t_start_ms: 0, t_end_ms: 1, lang: 'en', lang_prob: 1, text: 'x', translation: null },
      { type: 'capabilities', seq: 1, target_langs: ['es'], providers: [{ id: 'deepl', label: 'x', remote: true, available: true, langs: [] }] },
      { type: 'devices', seq: 1, devices: [{ id: 'a', label: 'b', kind: 'usb', default: false }] },
      { type: 'otro', seq: 1 },
      { type: 'caption.partial', seq: 1.5 },
    ])
      expect(parseServerMsg(typeof bad === 'string' ? bad : JSON.stringify(bad))).toBeNull();
    expect(parseServerMsg(42)).toBeNull();
  });
});
