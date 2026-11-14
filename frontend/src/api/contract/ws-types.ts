// Tipos de referencia del WebSocket /v1/sessions/{id}/live (subprotocolo cx9.live.v1).
// Fuente de verdad: docs/contract.md §5 y docs/openapi.yaml (components.schemas.Ws*).
// Solo práctica: no hay ningún tipo para escuchar a un entrevistador real ni para
// sugerir respuestas en vivo. El servidor rechaza cualquier `type` no listado acá.
// License: Apache-2.0

export type Lang = "es" | "en";
export type Probe = "numbers" | "example" | "why" | "ownership" | "result" | "other";

export type SessionState =
  | "ready" | "intro" | "asking" | "listening" | "processing" | "paused" | "closing" | "ended";

export interface AudioFormat {
  /** Base fija y obligatoria: pcm16le @ 16000 Hz mono. opus es opcional (48000 Hz) y el servidor v1 no lo elige. */
  codec: "pcm16le" | "opus";
  sample_rate: 16000 | 48000;
  channels: 1;
  frame_ms?: 20 | 40 | 60 | 100;
}

export const BASELINE_AUDIO: AudioFormat = { codec: "pcm16le", sample_rate: 16000, channels: 1, frame_ms: 100 };
export const HEARTBEAT_MS = 15_000;
export const IDLE_TIMEOUT_MS = 45_000;
export const RECONNECT_GRACE_MS = 120_000;
export const SUBPROTOCOL = "cx9.live.v1";

/**
 * Sobre común de todo mensaje JSON. `seq` es monótono por dirección y empieza en 1.
 * Excepciones con `seq: 0` (fuera de la secuencia, no se deduplican ni se reenvían):
 * `hello` (cliente) y `ack` (servidor). Ver contract.md §5.2.
 * Un mensaje reenviado tras reconectar conserva su seq original; nunca se renumera.
 * `session.ready` (y cualquier mensaje nuevo) puede llegar con un seq nuevo ANTES de
 * los reenvíos: el cliente ordena y deduplica por seq.
 */
export interface Envelope<T extends string, D> {
  v: 1;
  type: T;
  seq: number;
  ts: string; // RFC 3339 UTC
  session_id: string;
  /** Solo servidor→cliente: último seq del cliente procesado (incluye frames binarios). */
  ack?: number;
  data: D;
}

// ───────────────────────── cliente → servidor ─────────────────────────

/** Siempre con `seq: 0`: no consume secuencia ni cuenta para el dedupe. */
export type Hello = Envelope<"hello", {
  client: string;
  audio_formats: AudioFormat[]; // en orden de preferencia; debe incluir BASELINE_AUDIO
  tts_delivery: Array<"binary" | "url" | "none">;
  /** Solo wav y pcm16le. opus no existe para TTS en v1. */
  tts_formats?: Array<"pcm16le" | "wav">;
  partials?: boolean;
  resume?: { last_server_seq: number } | null;
}>;

/** Fallback NO recomendado (si no se pueden mandar binarios). Normalmente el audio va en frames binarios. */
export type AudioChunkJson = Envelope<"audio.chunk", { turn_id: string; chunk_index: number; audio_b64: string }>;
export type AudioEnd = Envelope<"audio.end", { turn_id: string; chunks: number; bytes: number; duration_ms: number }>;
export type AnswerText = Envelope<"answer.text", { turn_id: string; text: string }>;
export type ControlPause = Envelope<"control.pause", { turn_id?: string }>;
export type ControlResume = Envelope<"control.resume", { turn_id?: string }>;
export type ControlSkip = Envelope<"control.skip", { turn_id: string }>;
export type ControlEnd = Envelope<"control.end", Record<string, never>>;
export type ControlRepeat = Envelope<"control.repeat", { turn_id: string }>;
/** Solo con tts_delivery binary|url. Con tts_delivery=none el cliente NO lo manda y el servidor no lo espera. */
export type TtsPlayed = Envelope<"tts.played", { utterance_id: string }>;
export type Ping = Envelope<"ping", { nonce: string }>;
export type Pong = Envelope<"pong", { nonce: string }>;

export type ClientMessage =
  | Hello | AudioChunkJson | AudioEnd | AnswerText
  | ControlPause | ControlResume | ControlSkip | ControlEnd | ControlRepeat
  | TtsPlayed | Ping | Pong;

// ───────────────────────── servidor → cliente ─────────────────────────

export type SessionReady = Envelope<"session.ready", {
  state: SessionState;
  audio_format: AudioFormat;
  tts_delivery: "binary" | "url" | "none";
  tts_format?: "pcm16le" | "wav";
  heartbeat_ms: 15000;
  idle_timeout_ms: 45000;
  max_inflight_bytes: number;
  max_turn_ms: number;
  resumed: boolean;
  last_client_seq: number;
  question_index?: number;
  question_count?: number;
}>;

export type Question = Envelope<"question", {
  turn_id: string;
  question_id: string;
  /** 1-based. kind=intro: 0 y turn_id "t-0" (no espera respuesta); kind=closing: total y turn_id "t-0". */
  index: number;
  total: number;
  text: string;
  lang: Lang;
  utterance_id: string;
  kind?: "intro" | "main" | "closing" | "repeat";
  /**
   * true si es una re-emisión. Por control.repeat: mismo turn_id y question_id, seq nuevo,
   * sin turn.end. Tras NO_AUDIO o falla de STT: turn.end next=repeat y turn_id nuevo.
   */
  repeat?: boolean;
}>;

export type Followup = Envelope<"followup", {
  turn_id: string;
  question_id: string;
  followup_index: 1 | 2;
  max_followups: 0 | 1 | 2;
  probe: Probe;
  text: string;
  utterance_id: string;
  /** true si es una re-emisión (mismas reglas que Question). Nunca consume repregunta. */
  repeat?: boolean;
}>;

export type TtsAudio = Envelope<"tts.audio", {
  utterance_id: string;
  format: "pcm16le" | "wav";
  sample_rate: number;
  duration_ms: number;
  chunks: number;
  bytes: number;
  sha256?: string;
}>;

export type TtsUrl = Envelope<"tts.url", {
  utterance_id: string;
  url: string;
  format: "wav";
  duration_ms: number;
  expires_at: string;
}>;

export type TranscriptPartial = Envelope<"transcript.partial", {
  turn_id: string; text: string; revision: number; audio_ms: number;
}>;

export type TranscriptFinal = Envelope<"transcript.final", {
  turn_id: string;
  text: string;
  duration_ms: number;
  confidence: number | null;
  low_confidence: string[];
  source: "stt" | "text";
}>;

export type Pause = Envelope<"pause", {
  reason: "user" | "think_time" | "reconnect";
  resume_after_ms?: number | null;
  max_pause_ms?: number;
}>;

export type Resume = Envelope<"resume", { state: SessionState }>;

export type TurnEnd = Envelope<"turn.end", {
  turn_id: string;
  reason: "audio_end" | "text" | "skip" | "max_duration" | "no_audio" | "repeat";
  next: "followup" | "question" | "repeat" | "end";
  followups_used: 0 | 1 | 2;
  max_followups: 0 | 1 | 2;
}>;

export type SessionEnd = Envelope<"session.end", {
  reason: "completed" | "user" | "disconnected" | "timeout" | "error";
  report_url: string;
  summary: {
    questions_asked: number;
    questions_answered: number;
    followups_asked: number;
    skipped: number;
    total_answer_ms: number;
    report_status: "scoring" | "completed" | "failed";
  };
}>;

/**
 * Ack periódico de flujo: mientras el estado es `listening` el servidor lo manda cada
 * 250 ms o cada 32 KB recibidos (lo que pase primero), aunque partials esté apagado.
 * Siempre `seq: 0`: no entra en la secuencia ni en el replay. `ack` == `data.last_client_seq`.
 */
export type Ack = Envelope<"ack", { last_client_seq: number }>;

export type WsErrorCode =
  | "AUTH_FAILED" | "SESSION_NOT_FOUND" | "SESSION_ENDED" | "ALREADY_CONNECTED"
  | "HELLO_REQUIRED" | "INVALID_MESSAGE" | "UNSUPPORTED_MESSAGE" | "UNSUPPORTED_FORMAT"
  | "SEQ_OUT_OF_ORDER" | "RESUME_GAP" | "STATE_CONFLICT" | "BACKPRESSURE"
  | "AUDIO_TOO_LONG" | "NO_AUDIO" | "STT_UNAVAILABLE" | "STT_TIMEOUT"
  | "TTS_UNAVAILABLE" | "LLM_DEGRADED" | "INTERNAL";

export type WsError = Envelope<"error", {
  code: WsErrorCode;
  message: string;
  fatal: boolean;
  retryable: boolean;
  ref_seq?: number | null;
  close_code?: number | null;
}>;

export type ServerMessage =
  | SessionReady | Question | Followup | TtsAudio | TtsUrl
  | TranscriptPartial | TranscriptFinal | Pause | Resume
  | TurnEnd | SessionEnd | WsError | Ack | Ping | Pong;

/** Códigos de cierre WebSocket propios (4000-4999). */
export const CloseCode = {
  NORMAL: 1000,
  AUTH_FAILED: 4001,
  FORBIDDEN_ORIGIN: 4003,
  SESSION_NOT_FOUND: 4004,
  BACKPRESSURE: 4008,
  REPLACED: 4009,
  SESSION_ENDED: 4010,
  IDLE_TIMEOUT: 4011,
  PROTOCOL_ERROR: 4400,
  INTERNAL: 4500,
} as const;

// ───────────────────────── frames binarios ─────────────────────────
// Header fijo de 20 bytes, big-endian (orden de red), seguido del payload.
//  0  u8[2] magic "CX" (0x43 0x58)
//  2  u8    version = 1
//  3  u8    kind: 0x01 audio.chunk (cliente→servidor) | 0x02 tts.audio (servidor→cliente)
//  4  u32   seq (misma secuencia que los mensajes JSON de esa dirección)
//  8  u32   turn_no (cliente: nº de turno; servidor: nº de utterance)
// 12  u32   chunk_index (desde 0 dentro del turno/utterance)
// 16  u8    codec: 0x01 pcm16le@16k | 0x02 opus@48k | 0x03 wav (solo tts)
// 17  u8    flags: bit0 = último chunk
// 18  u16   payload_len (bytes; ≤ 32000)

export const BIN_HEADER_LEN = 20;
export const BinKind = { AUDIO_CHUNK: 0x01, TTS_AUDIO: 0x02 } as const;
export const BinCodec = { PCM16LE_16K: 0x01, OPUS_48K: 0x02, WAV: 0x03 } as const;

export interface BinaryFrameHeader {
  kind: number;
  seq: number;
  turnNo: number;
  chunkIndex: number;
  codec: number;
  last: boolean;
  payloadLen: number;
}

export function encodeFrame(h: Omit<BinaryFrameHeader, "payloadLen">, payload: Uint8Array): ArrayBuffer {
  if (payload.byteLength > 32000) throw new RangeError("payload > 32000 bytes");
  const buf = new ArrayBuffer(BIN_HEADER_LEN + payload.byteLength);
  const dv = new DataView(buf);
  dv.setUint8(0, 0x43); dv.setUint8(1, 0x58); dv.setUint8(2, 1); dv.setUint8(3, h.kind);
  dv.setUint32(4, h.seq); dv.setUint32(8, h.turnNo); dv.setUint32(12, h.chunkIndex);
  dv.setUint8(16, h.codec); dv.setUint8(17, h.last ? 1 : 0); dv.setUint16(18, payload.byteLength);
  new Uint8Array(buf, BIN_HEADER_LEN).set(payload);
  return buf;
}

export function decodeFrame(buf: ArrayBuffer): { header: BinaryFrameHeader; payload: Uint8Array } {
  const dv = new DataView(buf);
  if (buf.byteLength < BIN_HEADER_LEN || dv.getUint8(0) !== 0x43 || dv.getUint8(1) !== 0x58 || dv.getUint8(2) !== 1) {
    throw new Error("frame binario inválido");
  }
  const payloadLen = dv.getUint16(18);
  if (BIN_HEADER_LEN + payloadLen !== buf.byteLength) throw new Error("payload_len no coincide");
  return {
    header: {
      kind: dv.getUint8(3), seq: dv.getUint32(4), turnNo: dv.getUint32(8), chunkIndex: dv.getUint32(12),
      codec: dv.getUint8(16), last: (dv.getUint8(17) & 1) === 1, payloadLen,
    },
    payload: new Uint8Array(buf, BIN_HEADER_LEN, payloadLen),
  };
}
