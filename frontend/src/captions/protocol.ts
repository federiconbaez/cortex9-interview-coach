// Protocolo WS de subtítulos (servicio `captions` de Apex), versión 0.2.
// FUENTE: resumen del contrato v0.2 recibido el 14/11/2026. captions/CONTRACT.md todavía no
// está en el repo; cuando exista, manda sobre este archivo (TODO(CX9-6174): revisar contra él).
// Este módulo es independiente del motor de entrevistas: no importa nada de ../api ni ../state.

export const CAPTIONS_SUBPROTOCOL = 'cx9.captions.v1';
/** Ruta same-origin que expone nginx (y el proxy de Vite en desarrollo). */
export const CAPTIONS_PATH = '/captions/v1/live';

export type ProviderId = 'none' | 'argos' | 'nllb' | 'llm';
export type CaptureState = 'listening' | 'paused' | 'no_audio' | 'error';
export type DeviceKind = 'monitor' | 'input';

export interface Translation {
  enabled: boolean;
  target_lang: string;
  provider: ProviderId;
  remote: boolean;
}
export interface HelloMsg {
  type: 'hello';
  seq: number;
  version: string;
  source: { device: string; kind: DeviceKind };
  translation: Translation;
}
export interface Provider {
  id: ProviderId;
  label: string;
  remote: boolean;
  available: boolean;
  langs: string[];
}
export interface CapabilitiesMsg {
  type: 'capabilities';
  seq: number;
  target_langs: string[];
  providers: Provider[];
}
export interface Device {
  id: string;
  label: string;
  kind: DeviceKind;
  default: boolean;
}
export interface DevicesMsg {
  type: 'devices';
  seq: number;
  devices: Device[];
}
export interface ConfigAppliedMsg {
  type: 'config.applied';
  seq: number;
  device_id: string;
  translation: Translation;
}
export interface PartialMsg {
  type: 'caption.partial';
  seq: number;
  segment_id: string;
  t_start_ms: number;
  t_end_ms: number;
  lang: string;
  lang_prob: number;
  text: string;
}
export interface FinalMsg {
  type: 'caption.final';
  seq: number;
  segment_id: string;
  t_start_ms: number;
  t_end_ms: number;
  lang: string;
  lang_prob: number;
  text: string;
  translation: { text: string; target_lang: string; provider: ProviderId } | null;
  latency_ms: { stt: number; translate: number; total: number };
}
export interface StatusMsg {
  type: 'status';
  seq: number;
  state: CaptureState;
  level_dbfs: number;
}
export const ERROR_CODES = [
  'BAD_MESSAGE',
  'DEVICE_NOT_FOUND',
  'PROVIDER_UNAVAILABLE',
  'LANG_UNSUPPORTED',
  'CAPTURE_FAILED',
  'STT_FAILED',
  'TRANSLATE_FAILED',
] as const;
export type ErrorCode = (typeof ERROR_CODES)[number];
export interface ErrorMsg {
  type: 'error';
  seq: number;
  code: string;
  detail?: string;
}
export interface PongMsg {
  type: 'pong';
  seq: number;
}
export type ServerMsg =
  | HelloMsg
  | CapabilitiesMsg
  | DevicesMsg
  | ConfigAppliedMsg
  | PartialMsg
  | FinalMsg
  | StatusMsg
  | ErrorMsg
  | PongMsg;

/** Mensajes del cliente. `seq` lo agrega la conexión (ver ClientMsgOut). */
export type ClientMsg =
  | { type: 'config'; device_id: string; translate: boolean; target_lang: string; provider: ProviderId }
  | { type: 'pause' }
  | { type: 'resume' }
  | { type: 'ping' }
  | { type: 'devices.list' };

/** Todos los errores son recuperables salvo CAPTURE_FAILED (deja status.state = 'error'). */
export const isFatalError = (code: string) => code === 'CAPTURE_FAILED';
/** Errores que rechazan un `config` (no llega config.applied). */
export const CONFIG_REJECTIONS = new Set(['BAD_MESSAGE', 'DEVICE_NOT_FOUND', 'PROVIDER_UNAVAILABLE', 'LANG_UNSUPPORTED']);

export const CloseCodes = {
  NORMAL: 1000,
  GOING_AWAY: 1001,
  ORIGIN: 1008,
  INTERNAL: 1011,
  BUSY: 4409,
} as const;

// ───────────── validación (los datos del socket no son confiables) ─────────────

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);
const str = (v: unknown): v is string => typeof v === 'string';
const num = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v);
const bool = (v: unknown): v is boolean => typeof v === 'boolean';
const PROVIDERS = new Set<string>(['none', 'argos', 'nllb', 'llm']);
const STATES = new Set<string>(['listening', 'paused', 'no_audio', 'error']);
const KINDS = new Set<string>(['monitor', 'input']);

const isTranslation = (v: unknown): v is Translation =>
  isObj(v) && bool(v.enabled) && str(v.target_lang) && str(v.provider) && PROVIDERS.has(v.provider) && bool(v.remote);
const isProvider = (v: unknown): v is Provider =>
  isObj(v) && str(v.id) && PROVIDERS.has(v.id) && str(v.label) && bool(v.remote) && bool(v.available) && Array.isArray(v.langs) && v.langs.every(str);
const isDevice = (v: unknown): v is Device => isObj(v) && str(v.id) && str(v.label) && str(v.kind) && KINDS.has(v.kind) && bool(v.default);
const isSegment = (v: Record<string, unknown>) =>
  str(v.segment_id) && num(v.t_start_ms) && num(v.t_end_ms) && str(v.lang) && num(v.lang_prob) && str(v.text);

/** Devuelve el mensaje tipado o null si no respeta el contrato v0.2. */
export function parseServerMsg(raw: unknown): ServerMsg | null {
  let v: unknown;
  try {
    v = typeof raw === 'string' ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
  if (!isObj(v) || !str(v.type) || !num(v.seq) || !Number.isInteger(v.seq) || v.seq < 1) return null;
  switch (v.type) {
    case 'hello':
      return str(v.version) && isObj(v.source) && str(v.source.device) && str(v.source.kind) && isTranslation(v.translation) ? (v as unknown as HelloMsg) : null;
    case 'capabilities':
      return Array.isArray(v.target_langs) && v.target_langs.every(str) && Array.isArray(v.providers) && v.providers.every(isProvider) ? (v as unknown as CapabilitiesMsg) : null;
    case 'devices':
      return Array.isArray(v.devices) && v.devices.every(isDevice) ? (v as unknown as DevicesMsg) : null;
    case 'config.applied':
      return str(v.device_id) && isTranslation(v.translation) ? (v as unknown as ConfigAppliedMsg) : null;
    case 'caption.partial':
      return isSegment(v) ? (v as unknown as PartialMsg) : null;
    case 'caption.final': {
      const tr = v.translation;
      const trOk = tr === null || (isObj(tr) && str(tr.text) && str(tr.target_lang) && str(tr.provider));
      const lat = v.latency_ms;
      const latOk = isObj(lat) && num(lat.stt) && num(lat.translate) && num(lat.total);
      return isSegment(v) && trOk && latOk ? (v as unknown as FinalMsg) : null;
    }
    case 'status':
      return str(v.state) && STATES.has(v.state) && num(v.level_dbfs) ? (v as unknown as StatusMsg) : null;
    case 'error':
      return str(v.code) && (v.detail === undefined || str(v.detail)) ? (v as unknown as ErrorMsg) : null;
    case 'pong':
      return v as unknown as PongMsg;
    default:
      return null;
  }
}
