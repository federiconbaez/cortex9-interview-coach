// Package speechmock implementa el modo mock determinista del servicio de voz
// (docs/contract.md §6.7) como http.Handler. Lo usan los tests del backend y
// el subcomando `coach speech-mock` para desarrollar sin /speech.
package speechmock

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxAudio = 20 << 20

func problem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "urn:cx9:problem:" + strings.ToLower(strings.ReplaceAll(code, "_", "-")),
		"title": code, "status": status, "code": code, "detail": detail,
	})
}

// Handler devuelve el mock. Los resultados son idénticos byte a byte para la misma entrada.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "version": "mock", "mode": "mock",
			"stt": map[string]any{"ready": true, "engine": "mock", "model": "mock", "languages": []string{"es", "en"},
				"input_formats": []string{"audio/L16;rate=16000;channels=1", "audio/wav"}},
			"tts": map[string]any{"ready": true, "engine": "mock", "fallback": "", "output_formats": []string{"wav", "pcm16le"}},
		})
	})
	mux.HandleFunc("GET /voices", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
			{"id": "mock-es", "language": "es", "engine": "mock", "default": true},
			{"id": "mock-en", "language": "en", "engine": "mock", "default": true},
		}})
	})
	mux.HandleFunc("POST /stt", stt)
	mux.HandleFunc("POST /tts", tts)
	return mux
}

func forcedFailure(w http.ResponseWriter, r *http.Request) bool {
	switch r.Header.Get("X-Mock-Fail") {
	case "503":
		w.Header().Set("Retry-After", "1")
		problem(w, 503, "DEPENDENCY_UNAVAILABLE", "falla forzada")
		return true
	case "504":
		problem(w, 504, "DEPENDENCY_TIMEOUT", "falla forzada")
		return true
	case "timeout":
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Minute):
		}
		return true
	}
	return false
}

func stt(w http.ResponseWriter, r *http.Request) {
	if forcedFailure(w, r) {
		return
	}
	lang := r.URL.Query().Get("language")
	if lang != "es" && lang != "en" && lang != "auto" {
		problem(w, 422, "UNSUPPORTED_LANGUAGE", "language debe ser es, en o auto")
		return
	}
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		problem(w, 415, "UNSUPPORTED_MEDIA_TYPE", "Content-Type inválido")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAudio+1))
	if err != nil {
		problem(w, 400, "BAD_REQUEST", "cuerpo ilegible")
		return
	}
	if len(body) > maxAudio {
		problem(w, 413, "PAYLOAD_TOO_LARGE", "máximo 20 MB")
		return
	}
	var dur float64
	switch mt {
	case "audio/l16":
		rate, _ := strconv.Atoi(params["rate"])
		ch, _ := strconv.Atoi(params["channels"])
		if rate == 0 {
			rate = 16000
		}
		if ch == 0 {
			ch = 1
		}
		dur = float64(len(body)) / float64(rate*2*ch)
	case "audio/wav", "audio/x-wav", "audio/wave":
		d, ok := wavDuration(body)
		if !ok {
			problem(w, 422, "VALIDATION_FAILED", "WAV inválido")
			return
		}
		dur = d
	default:
		problem(w, 415, "UNSUPPORTED_MEDIA_TYPE", "formato no soportado en mock")
		return
	}
	if dur < 0.1 {
		problem(w, 422, "NO_AUDIO", "audio vacío o menor a 100 ms")
		return
	}
	if dur > 600 {
		problem(w, 413, "PAYLOAD_TOO_LARGE", "máximo 600 s")
		return
	}
	dur = math.Round(dur*1000) / 1000
	text := ""
	if h := r.Header.Get("X-Mock-Transcript"); h != "" {
		text, _ = url.QueryUnescape(h)
	} else {
		sum := sha256.Sum256(body)
		prefix := "respuesta simulada "
		if lang == "en" {
			prefix = "simulated answer "
		}
		text = prefix + hex.EncodeToString(sum[:])[:8]
	}
	toks := strings.Fields(text)
	var fillers []map[string]any
	words := make([]map[string]any, 0, len(toks))
	clean := make([]string, 0, len(toks))
	var sum float64
	for i, tok := range toks {
		conf := 0.9
		if strings.HasPrefix(tok, "?") {
			tok, conf = strings.TrimPrefix(tok, "?"), 0.3
		}
		clean = append(clean, tok)
		sum += conf
		step := dur / float64(len(toks))
		isF := hesitation.MatchString(strings.ToLower(strings.Trim(tok, ".,…")))
		if isF {
			fillers = append(fillers, map[string]any{"text": strings.ToLower(strings.Trim(tok, ".,…")), "start_s": round3(step * float64(i)), "end_s": round3(step * float64(i+1))})
		}
		words = append(words, map[string]any{"word": tok, "start_s": round3(step * float64(i)), "end_s": round3(step * float64(i+1)), "confidence": conf, "is_filler": isF})
	}
	var conf any
	if len(toks) > 0 {
		conf = round3(sum / float64(len(toks)))
	}
	finalText := strings.Join(clean, " ")
	outLang := lang
	if outLang == "auto" {
		outLang = "es"
	}
	out := map[string]any{
		"text": finalText, "language": outLang, "duration_s": dur, "confidence": conf,
		"segments": []map[string]any{{"start_s": 0.0, "end_s": dur, "text": finalText, "confidence": conf, "words": words}},
		"engine":   "mock", "model": "mock", "processing_ms": 0,
		"fillers": nonNil(fillers), "filler_count": len(fillers), "pauses": []any{},
	}
	// Endpointing (v1.2): en parciales el mock informa speech_ended_ms y
	// trailing_silence_ms con un VAD de energía determinista sobre el PCM
	// (X-Mock-VAD: off los omite, como un servicio viejo; X-Mock-Speech-Ended-Ms
	// y X-Mock-Trailing-Silence-Ms los fijan).
	if r.URL.Query().Get("partial") == "true" && r.Header.Get("X-Mock-VAD") != "off" {
		ended, trailing := mockVAD(body, mt == "audio/l16", dur)
		if v, err := strconv.Atoi(r.Header.Get("X-Mock-Speech-Ended-Ms")); err == nil {
			ended = v
		}
		if v, err := strconv.Atoi(r.Header.Get("X-Mock-Trailing-Silence-Ms")); err == nil {
			trailing = v
		}
		if ended >= 0 {
			out["speech_ended_ms"] = ended
		}
		out["trailing_silence_ms"] = trailing
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// mockVAD: frames de 20 ms con RMS >= 500 son voz. Devuelve el fin de la última
// voz (-1 si no hubo) y el silencio final, en ms.
func mockVAD(body []byte, l16 bool, dur float64) (endedMs, trailingMs int) {
	pcm := body
	if !l16 && len(pcm) >= 44 {
		pcm = pcm[44:]
	}
	const frame = 640
	last := -1
	for i := 0; i+frame <= len(pcm); i += frame {
		var sum float64
		for j := i; j < i+frame; j += 2 {
			v := float64(int16(binary.LittleEndian.Uint16(pcm[j:])))
			sum += v * v
		}
		if math.Sqrt(sum/(frame/2)) >= 500 {
			last = (i + frame) / 32
		}
	}
	total := int(dur*1000 + 0.5)
	if last < 0 {
		return -1, total
	}
	return last, total - last
}

var hesitation = regexp.MustCompile(`^(?:e+h+|e+m+|m+|u+m+|u+h+|e+r+)$`)

func nonNil(x []map[string]any) []map[string]any {
	if x == nil {
		return []map[string]any{}
	}
	return x
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func wavDuration(b []byte) (float64, bool) {
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0, false
	}
	ch := binary.LittleEndian.Uint16(b[22:24])
	rate := binary.LittleEndian.Uint32(b[24:28])
	bits := binary.LittleEndian.Uint16(b[34:36])
	if ch == 0 || rate == 0 || bits == 0 {
		return 0, false
	}
	data := len(b) - 44
	return float64(data) / float64(uint32(ch)*rate*uint32(bits/8)), true
}

func tts(w http.ResponseWriter, r *http.Request) {
	if forcedFailure(w, r) {
		return
	}
	var req struct {
		Text       string  `json:"text"`
		Language   string  `json:"language"`
		Voice      string  `json:"voice"`
		Format     string  `json:"format"`
		SampleRate int     `json:"sample_rate"`
		Speed      float64 `json:"speed"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		problem(w, 400, "BAD_REQUEST", "JSON inválido")
		return
	}
	n := len([]rune(req.Text))
	if n == 0 || n > 2000 {
		problem(w, 422, "VALIDATION_FAILED", "text debe tener 1-2000 caracteres")
		return
	}
	if req.Language != "es" && req.Language != "en" {
		problem(w, 422, "UNSUPPORTED_LANGUAGE", "language debe ser es o en")
		return
	}
	if req.Voice != "" && req.Voice != "mock-es" && req.Voice != "mock-en" {
		problem(w, 422, "VOICE_NOT_FOUND", "voz desconocida")
		return
	}
	if req.Format == "" {
		req.Format = "wav"
	}
	durMs := 300 + 60*len(strings.Fields(req.Text))
	samples := 16000 * durMs / 1000
	pcm := make([]byte, samples*2) // silencio
	voice := req.Voice
	if voice == "" {
		voice = "mock-" + req.Language
	}
	w.Header().Set("X-Audio-Duration-Ms", strconv.Itoa(durMs))
	w.Header().Set("X-Sample-Rate", "16000")
	w.Header().Set("X-Voice", voice)
	w.Header().Set("X-Engine", "mock")
	switch req.Format {
	case "pcm16le":
		w.Header().Set("Content-Type", "audio/L16; rate=16000; channels=1")
		_, _ = w.Write(pcm)
	case "wav":
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(WAV(pcm, 16000))
	default:
		problem(w, 422, "VALIDATION_FAILED", "format no soportado en mock")
	}
}

// WAV envuelve PCM16LE mono en un contenedor WAV.
func WAV(pcm []byte, rate int) []byte {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	return append(h, pcm...)
}
