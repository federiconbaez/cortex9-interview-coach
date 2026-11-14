// Estado de la vista de subtítulos. Solo en memoria: nada se guarda en localStorage,
// IndexedDB, cookies ni en el servidor (ver docs/SUBTITULOS.md).
import type { CapConnInfo, CapConnState } from './connection';
import {
  CONFIG_REJECTIONS,
  isFatalError,
  type CaptureState,
  type ClientMsg,
  type Device,
  type FinalMsg,
  type Provider,
  type ProviderId,
  type ServerMsg,
  type Translation,
} from './protocol';

export interface Segment {
  /** `${epoch}:${segment_id}`: los segment_id no se reusan dentro de una conexión, pero sí pueden repetirse entre conexiones. */
  key: string;
  segmentId: string;
  lang: string;
  langProb: number;
  text: string;
  final: boolean;
  translation: FinalMsg['translation'];
}

export interface Applied {
  deviceId: string;
  translation: Translation;
}

export type ConfigReq = Extract<ClientMsg, { type: 'config' }>;

export interface CaptionsState {
  conn: CapConnState;
  connInfo: CapConnInfo;
  /** Sube con cada conexión abierta; sirve para separar segmentos de conexiones distintas. */
  epoch: number;
  version: string | null;
  targetLangs: string[];
  providers: Provider[];
  devices: Device[];
  /** Lo que el servidor confirmó (hello o config.applied). La UI muestra esto, no lo elegido. */
  applied: Applied | null;
  pending: ConfigReq | null;
  capture: CaptureState | null;
  levelDbfs: number | null;
  segments: Segment[];
  closed: Set<string>;
  lastFinal: Segment | null;
  error: { code: string; detail: string } | null;
  fatal: boolean;
}

/** Tope de segmentos en memoria (los más viejos se descartan; no se persisten). */
export const MAX_SEGMENTS = 500;

export const initialCaptions = (): CaptionsState => ({
  conn: 'idle',
  connInfo: { attempt: 0, delayMs: null, code: null },
  epoch: 0,
  version: null,
  targetLangs: [],
  providers: [],
  devices: [],
  applied: null,
  pending: null,
  capture: null,
  levelDbfs: null,
  segments: [],
  closed: new Set(),
  lastFinal: null,
  error: null,
  fatal: false,
});

export type CaptionsAction =
  | { type: 'conn'; state: CapConnState; info: CapConnInfo }
  | { type: 'msg'; m: ServerMsg }
  | { type: 'configSent'; req: ConfigReq }
  | { type: 'clear' }
  | { type: 'dismissError' };

function upsert(segments: Segment[], seg: Segment): Segment[] {
  const i = segments.findIndex((s) => s.key === seg.key);
  const next = i === -1 ? [...segments, seg] : segments.map((s, j) => (j === i ? seg : s));
  return next.length > MAX_SEGMENTS ? next.slice(next.length - MAX_SEGMENTS) : next;
}

export function captionsReducer(s: CaptionsState, a: CaptionsAction): CaptionsState {
  switch (a.type) {
    case 'conn': {
      if (a.state === 'open') {
        // Sin replay: la conexión nueva manda hello + capabilities + devices desde cero y
        // la configuración vuelve a la última aplicada por el servidor.
        return {
          ...s,
          conn: 'open',
          connInfo: a.info,
          epoch: s.epoch + 1,
          applied: null,
          pending: null,
          capture: null,
          levelDbfs: null,
          error: null,
          fatal: false,
        };
      }
      return { ...s, conn: a.state, connInfo: a.info, pending: a.state === 'connecting' ? s.pending : null };
    }
    case 'configSent':
      return { ...s, pending: a.req, error: null };
    case 'clear':
      return { ...s, segments: [], lastFinal: null };
    case 'dismissError':
      return s.fatal ? s : { ...s, error: null };
    case 'msg':
      return onMsg(s, a.m);
  }
}

function onMsg(s: CaptionsState, m: ServerMsg): CaptionsState {
  switch (m.type) {
    case 'hello':
      // SUPUESTO: source.device es el id del dispositivo (el mismo que devices[].id y config.device_id).
      return { ...s, version: m.version, applied: { deviceId: m.source.device, translation: m.translation } };
    case 'capabilities':
      return { ...s, targetLangs: m.target_langs, providers: m.providers };
    case 'devices':
      return { ...s, devices: m.devices };
    case 'config.applied':
      return { ...s, applied: { deviceId: m.device_id, translation: m.translation }, pending: null };
    case 'caption.partial': {
      const key = `${s.epoch}:${m.segment_id}`;
      if (s.closed.has(key)) return s; // parcial tardío de un segmento ya cerrado
      return { ...s, segments: upsert(s.segments, { key, segmentId: m.segment_id, lang: m.lang, langProb: m.lang_prob, text: m.text, final: false, translation: null }) };
    }
    case 'caption.final': {
      const key = `${s.epoch}:${m.segment_id}`;
      if (s.closed.has(key)) return s; // un final cierra el segmento para siempre
      const seg: Segment = { key, segmentId: m.segment_id, lang: m.lang, langProb: m.lang_prob, text: m.text, final: true, translation: m.translation };
      const closed = new Set(s.closed);
      closed.add(key);
      return { ...s, segments: upsert(s.segments, seg), closed, lastFinal: seg };
    }
    case 'status': {
      // CAPTURE_FAILED deja state 'error'; si la captura vuelve (p. ej. con otro dispositivo), se limpia.
      const fatal = m.state === 'error';
      return { ...s, capture: m.state, levelDbfs: m.level_dbfs, fatal, error: s.fatal && !fatal ? null : s.error };
    }
    case 'error': {
      const fatal = isFatalError(m.code);
      return {
        ...s,
        error: { code: m.code, detail: m.detail ?? '' },
        fatal: s.fatal || fatal,
        capture: fatal ? 'error' : s.capture,
        // Un config rechazado no tiene config.applied: se descarta lo pendiente y queda lo anterior.
        pending: CONFIG_REJECTIONS.has(m.code) ? null : s.pending,
      };
    }
    case 'pong':
      return s;
  }
}

// ───────────── reglas de la UI (puras, testeables) ─────────────

export const providerById = (providers: Provider[], id: ProviderId) => providers.find((p) => p.id === id) ?? null;

/** El aviso persistente se muestra mientras la traducción aplicada sea remota. */
export const remoteNoticeVisible = (s: Pick<CaptionsState, 'applied'>) => s.applied?.translation.remote === true;

/** Hace falta confirmar antes de pedir un proveedor remoto que no esté ya aplicado. */
export function needsRemoteConfirm(s: Pick<CaptionsState, 'applied' | 'providers'>, req: Pick<ConfigReq, 'provider' | 'translate'>): boolean {
  if (!req.translate) return false;
  const p = providerById(s.providers, req.provider);
  if (!p?.remote) return false;
  return !(s.applied?.translation.remote && s.applied.translation.provider === req.provider);
}

export const providerSelectable = (p: Provider) => p.available;

/** Un idioma destino es elegible si está en target_langs y en los langs del proveedor. */
export const langSelectable = (s: Pick<CaptionsState, 'targetLangs'>, provider: Provider | null, lang: string) =>
  s.targetLangs.includes(lang) && (provider === null || provider.id === 'none' || provider.langs.includes(lang));

/** Arma un config completo a partir de lo aplicado + el cambio pedido. */
export function buildConfig(s: Pick<CaptionsState, 'applied' | 'devices' | 'targetLangs'>, change: Partial<Omit<ConfigReq, 'type'>>): ConfigReq | null {
  const base = s.applied;
  const deviceId = change.device_id ?? base?.deviceId ?? s.devices.find((d) => d.default)?.id;
  if (!deviceId) return null;
  const t = base?.translation;
  return {
    type: 'config',
    device_id: deviceId,
    translate: change.translate ?? t?.enabled ?? false,
    target_lang: change.target_lang ?? t?.target_lang ?? s.targetLangs[0] ?? 'es',
    provider: change.provider ?? t?.provider ?? 'none',
  };
}
