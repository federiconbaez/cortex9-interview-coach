// Package protocol define los mensajes del WebSocket /v1/sessions/{id}/live
// (subprotocolo cx9.live.v1) y el header binario de 20 bytes, tal como están
// en docs/contract.md §5 y docs/ws-types.ts.
//
// Lista cerrada: no existe ningún mensaje para escuchar a un entrevistador
// real ni para sugerir respuestas en vivo.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"
)

const (
	Subprotocol      = "cx9.live.v1"
	HeartbeatMs      = 15000
	IdleTimeoutMs    = 45000
	ReconnectGraceMs = 120000
	MaxPauseMs       = 1800000
	MaxTextMessage   = 64 << 10
	MaxPayload       = 32000
	HeaderLen        = 20
)

// Tipos cliente→servidor (lista cerrada).
const (
	THello         = "hello"
	TAudioChunk    = "audio.chunk"
	TAudioEnd      = "audio.end"
	TAnswerText    = "answer.text"
	TControlPause  = "control.pause"
	TControlResume = "control.resume"
	TControlSkip   = "control.skip"
	TControlEnd    = "control.end"
	TControlRepeat = "control.repeat"
	TTTSPlayed     = "tts.played"
	TPing          = "ping"
	TPong          = "pong"
)

// Tipos servidor→cliente.
const (
	TSessionReady      = "session.ready"
	TQuestion          = "question"
	TFollowup          = "followup"
	TTTSAudio          = "tts.audio"
	TTTSURL            = "tts.url"
	TTranscriptPartial = "transcript.partial"
	TTranscriptFinal   = "transcript.final"
	TPause             = "pause"
	TResume            = "resume"
	TTurnEnd           = "turn.end"
	TSessionEnd        = "session.end"
	TError             = "error"
	// TAck es un ack liviano (seq 0, fuera de la secuencia) mientras el estado es listening.
	TAck = "ack"
)

// ClientTypes es la lista cerrada que acepta el servidor.
var ClientTypes = map[string]bool{THello: true, TAudioChunk: true, TAudioEnd: true, TAnswerText: true,
	TControlPause: true, TControlResume: true, TControlSkip: true, TControlEnd: true, TControlRepeat: true,
	TTTSPlayed: true, TPing: true, TPong: true}

// Códigos de error WS.
const (
	ErrAuthFailed         = "AUTH_FAILED"
	ErrSessionNotFound    = "SESSION_NOT_FOUND"
	ErrSessionEnded       = "SESSION_ENDED"
	ErrAlreadyConnected   = "ALREADY_CONNECTED"
	ErrHelloRequired      = "HELLO_REQUIRED"
	ErrInvalidMessage     = "INVALID_MESSAGE"
	ErrUnsupportedMessage = "UNSUPPORTED_MESSAGE"
	ErrUnsupportedFormat  = "UNSUPPORTED_FORMAT"
	ErrSeqOutOfOrder      = "SEQ_OUT_OF_ORDER"
	ErrResumeGap          = "RESUME_GAP"
	ErrStateConflict      = "STATE_CONFLICT"
	ErrBackpressure       = "BACKPRESSURE"
	ErrAudioTooLong       = "AUDIO_TOO_LONG"
	ErrNoAudio            = "NO_AUDIO"
	ErrSTTUnavailable     = "STT_UNAVAILABLE"
	ErrSTTTimeout         = "STT_TIMEOUT"
	ErrTTSUnavailable     = "TTS_UNAVAILABLE"
	ErrLLMDegraded        = "LLM_DEGRADED"
	ErrInternal           = "INTERNAL"
)

// Códigos de cierre.
const (
	CloseNormal        = 1000
	CloseGoingAway     = 1001
	CloseAuthFailed    = 4001
	CloseForbidden     = 4003
	CloseNotFound      = 4004
	CloseBackpressure  = 4008
	CloseReplaced      = 4009
	CloseSessionEnded  = 4010
	CloseIdleTimeout   = 4011
	CloseProtocolError = 4400
	CloseInternal      = 4500
)

// Envelope es el sobre de todo mensaje JSON.
type Envelope struct {
	V         int             `json:"v"`
	Type      string          `json:"type"`
	Seq       uint32          `json:"seq"`
	Ts        string          `json:"ts"`
	SessionID string          `json:"session_id"`
	Ack       *uint32         `json:"ack,omitempty"`
	Data      json.RawMessage `json:"data"`
}

// Now formatea ts en RFC 3339 UTC con milisegundos.
func Now(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

type AudioFormat struct {
	Codec      string `json:"codec"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	FrameMs    int    `json:"frame_ms,omitempty"`
}

// Baseline es el formato fijo del contrato.
var Baseline = AudioFormat{Codec: "pcm16le", SampleRate: 16000, Channels: 1, FrameMs: 100}

type Resume struct {
	LastServerSeq uint32 `json:"last_server_seq"`
}

type Hello struct {
	Client       string        `json:"client"`
	AudioFormats []AudioFormat `json:"audio_formats"`
	TTSDelivery  []string      `json:"tts_delivery"`
	TTSFormats   []string      `json:"tts_formats,omitempty"`
	Partials     *bool         `json:"partials,omitempty"`
	// Endpointing por silencio para esta conexión (default true; false = solo audio.end).
	Endpointing *bool   `json:"endpointing,omitempty"`
	Resume      *Resume `json:"resume"`
}

type SessionReady struct {
	State            string      `json:"state"`
	AudioFormat      AudioFormat `json:"audio_format"`
	TTSDelivery      string      `json:"tts_delivery"`
	TTSFormat        string      `json:"tts_format,omitempty"`
	HeartbeatMs      int         `json:"heartbeat_ms"`
	IdleTimeoutMs    int         `json:"idle_timeout_ms"`
	MaxInflightBytes int         `json:"max_inflight_bytes"`
	MaxTurnMs        int         `json:"max_turn_ms"`
	Resumed          bool        `json:"resumed"`
	LastClientSeq    uint32      `json:"last_client_seq"`
	QuestionIndex    int         `json:"question_index"`
	QuestionCount    int         `json:"question_count"`
	// Endpointing efectivo de la conexión (v1.2).
	Endpointing *EndpointInfo `json:"endpointing,omitempty"`
}

// EndpointInfo dice si el servidor cierra el turno solo por silencio.
type EndpointInfo struct {
	Enabled   bool   `json:"enabled"`
	Mode      string `json:"mode"` // off | stt | energy | auto
	SilenceMs int    `json:"silence_ms"`
}

type Question struct {
	TurnID      string `json:"turn_id"`
	QuestionID  string `json:"question_id"`
	Index       int    `json:"index"`
	Total       int    `json:"total"`
	Text        string `json:"text"`
	Lang        string `json:"lang"`
	UtteranceID string `json:"utterance_id"`
	Kind        string `json:"kind"`
	Repeat      bool   `json:"repeat,omitempty"`
}

type Followup struct {
	TurnID        string `json:"turn_id"`
	QuestionID    string `json:"question_id"`
	FollowupIndex int    `json:"followup_index"`
	MaxFollowups  int    `json:"max_followups"`
	Probe         string `json:"probe"`
	Text          string `json:"text"`
	UtteranceID   string `json:"utterance_id"`
	Repeat        bool   `json:"repeat,omitempty"`
}

// Ack es el cuerpo del mensaje ack.
type Ack struct {
	LastClientSeq uint32 `json:"last_client_seq"`
}

type TTSAudio struct {
	UtteranceID string `json:"utterance_id"`
	Format      string `json:"format"`
	SampleRate  int    `json:"sample_rate"`
	DurationMs  int    `json:"duration_ms"`
	Chunks      int    `json:"chunks"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256,omitempty"`
}

type TTSURL struct {
	UtteranceID string `json:"utterance_id"`
	URL         string `json:"url"`
	Format      string `json:"format"`
	DurationMs  int    `json:"duration_ms"`
	ExpiresAt   string `json:"expires_at"`
}

type TranscriptPartial struct {
	TurnID   string `json:"turn_id"`
	Text     string `json:"text"`
	Revision int    `json:"revision"`
	AudioMs  int    `json:"audio_ms"`
}

type TranscriptFinal struct {
	TurnID        string   `json:"turn_id"`
	Text          string   `json:"text"`
	DurationMs    int      `json:"duration_ms"`
	Confidence    *float64 `json:"confidence"`
	LowConfidence []string `json:"low_confidence"`
	Source        string   `json:"source"`
}

type Pause struct {
	Reason        string `json:"reason"`
	ResumeAfterMs *int   `json:"resume_after_ms"`
	MaxPauseMs    int    `json:"max_pause_ms"`
}

type ResumeMsg struct {
	State string `json:"state"`
}

type TurnEnd struct {
	TurnID        string `json:"turn_id"`
	Reason        string `json:"reason"`
	Next          string `json:"next"`
	FollowupsUsed int    `json:"followups_used"`
	MaxFollowups  int    `json:"max_followups"`
}

type Summary struct {
	QuestionsAsked    int    `json:"questions_asked"`
	QuestionsAnswered int    `json:"questions_answered"`
	FollowupsAsked    int    `json:"followups_asked"`
	Skipped           int    `json:"skipped"`
	TotalAnswerMs     int    `json:"total_answer_ms"`
	ReportStatus      string `json:"report_status"`
}

type SessionEnd struct {
	Reason    string  `json:"reason"`
	ReportURL string  `json:"report_url"`
	Summary   Summary `json:"summary"`
}

type Error struct {
	Code      string  `json:"code"`
	Message   string  `json:"message"`
	Fatal     bool    `json:"fatal"`
	Retryable bool    `json:"retryable"`
	RefSeq    *uint32 `json:"ref_seq"`
	CloseCode *int    `json:"close_code"`
}

type Nonce struct {
	Nonce string `json:"nonce"`
}

type AudioEnd struct {
	TurnID     string `json:"turn_id"`
	Chunks     int    `json:"chunks"`
	Bytes      int    `json:"bytes"`
	DurationMs int    `json:"duration_ms"`
}

type AudioChunkJSON struct {
	TurnID     string `json:"turn_id"`
	ChunkIndex int    `json:"chunk_index"`
	AudioB64   string `json:"audio_b64"`
}

type AnswerText struct {
	TurnID string `json:"turn_id"`
	Text   string `json:"text"`
}

type Control struct {
	TurnID string `json:"turn_id,omitempty"`
}

type TTSPlayed struct {
	UtteranceID string `json:"utterance_id"`
}

// ---- frames binarios ----

const (
	KindAudioChunk byte = 0x01
	KindTTSAudio   byte = 0x02
	CodecPCM16     byte = 0x01
	CodecOpus      byte = 0x02
	CodecWAV       byte = 0x03
	FlagLast       byte = 0x01
)

type FrameHeader struct {
	Kind       byte
	Seq        uint32
	TurnNo     uint32
	ChunkIndex uint32
	Codec      byte
	Last       bool
	PayloadLen uint16
}

var ErrBadFrame = errors.New("frame binario inválido")

// EncodeFrame arma header (big-endian) + payload.
func EncodeFrame(h FrameHeader, payload []byte) ([]byte, error) {
	if len(payload) > MaxPayload {
		return nil, errors.New("payload > 32000 bytes")
	}
	b := make([]byte, HeaderLen+len(payload))
	b[0], b[1], b[2], b[3] = 0x43, 0x58, 1, h.Kind
	binary.BigEndian.PutUint32(b[4:], h.Seq)
	binary.BigEndian.PutUint32(b[8:], h.TurnNo)
	binary.BigEndian.PutUint32(b[12:], h.ChunkIndex)
	b[16] = h.Codec
	if h.Last {
		b[17] = FlagLast
	}
	binary.BigEndian.PutUint16(b[18:], uint16(len(payload)))
	copy(b[HeaderLen:], payload)
	return b, nil
}

// DecodeFrame valida magic, versión, flags y payload_len.
func DecodeFrame(b []byte) (FrameHeader, []byte, error) {
	if len(b) < HeaderLen || b[0] != 0x43 || b[1] != 0x58 || b[2] != 1 {
		return FrameHeader{}, nil, ErrBadFrame
	}
	h := FrameHeader{Kind: b[3], Seq: binary.BigEndian.Uint32(b[4:]), TurnNo: binary.BigEndian.Uint32(b[8:]),
		ChunkIndex: binary.BigEndian.Uint32(b[12:]), Codec: b[16], Last: b[17]&FlagLast != 0,
		PayloadLen: binary.BigEndian.Uint16(b[18:])}
	if b[17]&^FlagLast != 0 || int(h.PayloadLen) != len(b)-HeaderLen || int(h.PayloadLen) > MaxPayload {
		return FrameHeader{}, nil, ErrBadFrame
	}
	return h, b[HeaderLen:], nil
}
