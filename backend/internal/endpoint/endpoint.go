// Package endpoint detecta el fin de la respuesta del usuario por silencio
// (endpointing) sobre el audio PCM16LE 16 kHz mono de SU micrófono. No toca la
// red ni el reloj: el runtime le pasa el audio y los resultados de /stt parcial
// y decide cuándo confirmar. Todos los tiempos son de audio del turno (ms desde
// el primer byte del turno), no de reloj.
//
// Fuentes de la decisión:
//   - stt: /stt?partial=true devuelve speech_ended_ms (relativo al inicio de la
//     ventana mandada) y trailing_silence_ms (VAD silero/vad_filter del servicio).
//   - energy: VAD de energía RMS propio sobre frames de 20 ms (respaldo).
//
// Modos: off, stt, energy, auto (stt si el servicio manda los campos; si no, o si
// el parcial falla, energy).
package endpoint

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"unicode"
)

const (
	bytesPerMs = 32 // PCM16LE 16 kHz mono
	frameMs    = 20
	frameBytes = frameMs * bytesPerMs
)

// Modos válidos.
const (
	Off    = "off"
	STT    = "stt"
	Energy = "energy"
	Auto   = "auto"
)

// Config del detector.
type Config struct {
	Mode       string  // off | stt | energy | auto
	SilenceMs  int     // silencio final que cierra el turno (CX9_ENDPOINT_SILENCE_MS, 700)
	RMS        float64 // umbral de voz del VAD de energía (CX9_ENDPOINT_RMS, 500 sobre 32767)
	GraceMs    int     // ventana de gracia antes de confirmar (CX9_ENDPOINT_GRACE_MS, 200)
	MinVoiceMs int     // voz mínima para considerar que "hubo voz" por energía (CX9_ENDPOINT_MIN_VOICE_MS, 150)
}

// Default es la configuración por defecto.
func Default() Config {
	return Config{Mode: Auto, SilenceMs: 700, RMS: 500, GraceMs: 200, MinVoiceMs: 150}
}

// Validate revisa la configuración.
func (c Config) Validate() error {
	switch c.Mode {
	case Off, STT, Energy, Auto:
	default:
		return fmt.Errorf("CX9_ENDPOINT_MODE inválido %q (off|stt|energy|auto)", c.Mode)
	}
	if c.SilenceMs < 100 || c.SilenceMs > 10000 {
		return fmt.Errorf("CX9_ENDPOINT_SILENCE_MS fuera de rango (100-10000): %d", c.SilenceMs)
	}
	if c.RMS <= 0 || c.RMS > 32767 {
		return fmt.Errorf("CX9_ENDPOINT_RMS fuera de rango (0-32767]: %v", c.RMS)
	}
	if c.GraceMs < 0 || c.GraceMs > 5000 {
		return fmt.Errorf("CX9_ENDPOINT_GRACE_MS fuera de rango (0-5000): %d", c.GraceMs)
	}
	if c.MinVoiceMs < 0 || c.MinVoiceMs > 5000 {
		return fmt.Errorf("CX9_ENDPOINT_MIN_VOICE_MS fuera de rango (0-5000): %d", c.MinVoiceMs)
	}
	return nil
}

// Candidate es un posible fin de voz.
type Candidate struct {
	Source        string // stt | energy
	SpeechEndedMs int    // fin de voz, absoluto en el turno
	MarkMs        int    // audio recibido cuando se detectó (para la gracia)
}

// Detector lleva el estado de un turno.
type Detector struct {
	cfg        Config
	pending    []byte
	totalBytes int
	voicedMs   int
	lastVoice  int // fin (ms) del último frame con voz; -1 si no hubo
	sttVoice   bool
	sttState   int // 0 desconocido, 1 el servicio manda los campos, -1 no los manda
	lastSTTEnd int // último fin de voz (abs) informado por el STT; -1 si no hubo
	probedFor  int // lastVoice para el que ya se pidió un parcial de confirmación
}

// New crea un detector para un turno.
func New(cfg Config) *Detector {
	return &Detector{cfg: cfg, lastVoice: -1, lastSTTEnd: -1, probedFor: -2}
}

// Enabled dice si el modo cierra turnos.
func (d *Detector) Enabled() bool { return d != nil && d.cfg.Mode != Off }

// UsesSTT dice si el detector necesita parciales de /stt.
func (d *Detector) UsesSTT() bool {
	return d.Enabled() && (d.cfg.Mode == STT || (d.cfg.Mode == Auto && d.sttState >= 0))
}

// Feed procesa audio nuevo del turno (en orden).
func (d *Detector) Feed(pcm []byte) {
	d.pending = append(d.pending, pcm...)
	for len(d.pending) >= frameBytes {
		f := d.pending[:frameBytes]
		d.totalBytes += frameBytes
		if RMS(f) >= d.cfg.RMS {
			d.voicedMs += frameMs
			d.lastVoice = d.totalBytes / bytesPerMs
		}
		d.pending = d.pending[frameBytes:]
	}
}

// TotalMs es el audio del turno ya analizado (frames completos).
func (d *Detector) TotalMs() int { return d.totalBytes / bytesPerMs }

// EnergyVoice dice si el VAD de energía vio voz suficiente en el turno.
func (d *Detector) EnergyVoice() bool { return d.lastVoice >= 0 && d.voicedMs >= d.cfg.MinVoiceMs }

// HadVoice dice si ya hubo voz en el turno (por energía o por el STT).
func (d *Detector) HadVoice() bool { return d.EnergyVoice() || d.sttVoice }

// EnergyTrailingMs es el silencio al final del audio según energía.
func (d *Detector) EnergyTrailingMs() int {
	if d.lastVoice < 0 {
		return 0
	}
	return d.TotalMs() - d.lastVoice
}

// LastVoiceMs es el fin del último frame con voz (energía); -1 si no hubo.
func (d *Detector) LastVoiceMs() int { return d.lastVoice }

// VoiceAfter dice si hubo voz (energía) que terminó después de ms.
func (d *Detector) VoiceAfter(ms int) bool { return d.lastVoice > ms }

func (d *Detector) energySilence() bool {
	return d.EnergyVoice() && d.EnergyTrailingMs() >= d.cfg.SilenceMs
}

// EnergyCandidate devuelve un candidato por energía si el modo lo usa ahora
// (energy, o auto con un servicio que no manda los campos).
func (d *Detector) EnergyCandidate() (Candidate, bool) {
	if !d.Enabled() || !(d.cfg.Mode == Energy || (d.cfg.Mode == Auto && d.sttState < 0)) {
		return Candidate{}, false
	}
	return d.energyNow()
}

// EnergyFallback se usa en auto cuando el parcial de confirmación falló.
func (d *Detector) EnergyFallback() (Candidate, bool) {
	if !d.Enabled() || d.cfg.Mode != Auto {
		return Candidate{}, false
	}
	return d.energyNow()
}

func (d *Detector) energyNow() (Candidate, bool) {
	if !d.energySilence() {
		return Candidate{}, false
	}
	return Candidate{Source: Energy, SpeechEndedMs: d.lastVoice, MarkMs: d.TotalMs()}, true
}

// ProbeWanted dice si conviene pedir ya un /stt parcial para confirmar un
// silencio que vio la energía (una vez por silencio). Baja la latencia del modo
// stt sin esperar al próximo parcial periódico.
func (d *Detector) ProbeWanted() bool {
	if !d.UsesSTT() || !d.energySilence() || d.probedFor == d.lastVoice {
		return false
	}
	d.probedFor = d.lastVoice
	return true
}

// STTResult aplica la respuesta de un /stt parcial sobre la ventana
// [winStartMs, winEndMs) del turno. speechEnded es relativo al inicio de la
// ventana. Devuelve un candidato si corresponde cerrar.
func (d *Detector) STTResult(winStartMs, winEndMs int, speechEnded, trailing *int, text string) (Candidate, bool) {
	if !d.Enabled() || d.cfg.Mode == Energy {
		return Candidate{}, false
	}
	if speechEnded == nil && trailing == nil {
		if d.cfg.Mode == Auto {
			d.sttState = -1 // servicio viejo o mock sin VAD: respaldo de energía
		}
		return Candidate{}, false
	}
	d.sttState = 1
	end := -1
	switch {
	case speechEnded != nil:
		end = winStartMs + *speechEnded
	case *trailing < winEndMs-winStartMs:
		end = winEndMs - *trailing
	}
	if end >= 0 {
		d.sttVoice = true
		if end > d.lastSTTEnd {
			d.lastSTTEnd = end
		}
	} else if strings.TrimSpace(text) != "" {
		d.sttVoice = true
	}
	if !d.HadVoice() || (d.lastSTTEnd < 0 && end < 0) {
		return Candidate{}, false
	}
	if end < 0 {
		end = d.lastSTTEnd // ventana sin voz después de voz anterior
	}
	// Voz que volvió en el audio que llegó después de la ventana: no se corta.
	if d.VoiceAfter(winEndMs) {
		return Candidate{}, false
	}
	now := d.TotalMs()
	if winEndMs > now {
		now = winEndMs
	}
	if now-end < d.cfg.SilenceMs {
		return Candidate{}, false
	}
	return Candidate{Source: STT, SpeechEndedMs: end, MarkMs: now}, true
}

// Confirm se llama al terminar la ventana de gracia: true si no volvió la voz
// desde que se detectó el candidato.
func (d *Detector) Confirm(c Candidate) bool { return !d.VoiceAfter(c.MarkMs) }

// RMS de un bloque PCM16LE.
func RMS(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:])))
		sum += v * v
	}
	return math.Sqrt(sum / float64(n))
}

// Similarity compara dos transcripciones por palabras normalizadas: 1 - distancia
// de edición / largo mayor. Se usa para aceptar o descartar la decisión
// especulativa de repregunta.
func Similarity(a, b string) float64 {
	wa, wb := words(a), words(b)
	if len(wa) == 0 && len(wb) == 0 {
		return 1
	}
	m := max(len(wa), len(wb))
	return 1 - float64(editDistance(wa, wb))/float64(m)
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func editDistance(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
