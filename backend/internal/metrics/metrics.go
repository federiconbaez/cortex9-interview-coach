// Package metrics expone métricas Prometheus: RED de HTTP, WebSocket,
// sesiones activas, latencia de LLM y del servicio de voz. Ninguna etiqueta
// lleva texto del usuario, IDs de sesión ni claves.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/resilience"
)

type M struct {
	Reg            *prometheus.Registry
	HTTPRequests   *prometheus.CounterVec
	HTTPDuration   *prometheus.HistogramVec
	WSConnections  *prometheus.GaugeVec
	WSMessages     *prometheus.CounterVec
	WSBackpressure *prometheus.CounterVec
	SessionsActive prometheus.Gauge
	Sessions       *prometheus.CounterVec
	Followups      *prometheus.CounterVec
	SpeechDuration *prometheus.HistogramVec
	SpeechErrors   *prometheus.CounterVec
	LLMDuration    *prometheus.HistogramVec
	LLMBreakerG    *prometheus.GaugeVec
	LLMFallbacks   *prometheus.CounterVec
	InventedTerms  prometheus.Counter
	// Endpointing y latencia de turno (v1.2).
	EndpointToTTS     *prometheus.HistogramVec // cx9_endpoint_to_followup_tts_seconds{mode,provider}
	TurnStage         *prometheus.HistogramVec // cx9_turn_stage_seconds{stage}
	Endpoints         *prometheus.CounterVec   // cx9_endpoints_total{source}
	Speculative       *prometheus.CounterVec   // cx9_speculative_followups_total{result}
	TTSPlayedTimeouts prometheus.Counter       // cx9_tts_played_timeouts_total
}

func New() *M {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	lat := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
	m := &M{Reg: r,
		HTTPRequests:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "cx9_http_requests_total", Help: "Peticiones HTTP"}, []string{"method", "route", "status"}),
		HTTPDuration:   prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "cx9_http_request_duration_seconds", Help: "Duración HTTP", Buckets: lat}, []string{"method", "route"}),
		WSConnections:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "cx9_ws_connections", Help: "Conexiones WS"}, []string{"state"}),
		WSMessages:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "cx9_ws_messages_total", Help: "Mensajes WS"}, []string{"direction", "type"}),
		WSBackpressure: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "cx9_ws_backpressure_total", Help: "Eventos de backpressure"}, []string{"action"}),
		SessionsActive: prometheus.NewGauge(prometheus.GaugeOpts{Name: "cx9_sessions_active", Help: "Sesiones en vivo (live o detached)"}),
		Sessions:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "cx9_sessions_total", Help: "Sesiones terminadas"}, []string{"mode", "lang", "ended_reason"}),
		Followups:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "cx9_followups_total", Help: "Repreguntas"}, []string{"probe"}),
		SpeechDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "cx9_speech_request_duration_seconds", Help: "Latencia del servicio de voz", Buckets: lat}, []string{"op", "outcome"}),
		SpeechErrors:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "cx9_speech_errors_total", Help: "Errores del servicio de voz"}, []string{"op", "code"}),
		LLMDuration:    prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "cx9_llm_request_duration_seconds", Help: "Latencia LLM", Buckets: lat}, []string{"provider", "outcome"}),
		LLMBreakerG:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "cx9_llm_breaker_state", Help: "0 closed, 1 half_open, 2 open"}, []string{"provider"}),
		LLMFallbacks:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "cx9_llm_fallbacks_total", Help: "Usos del respaldo local"}, []string{"provider"}),
		InventedTerms:  prometheus.NewCounter(prometheus.CounterOpts{Name: "cx9_report_invented_terms_rejected_total", Help: "Términos inventados rechazados en mejoras"}),
	}
	turnLat := []float64{0.05, 0.1, 0.2, 0.3, 0.5, 0.75, 1, 1.5, 2, 3, 5, 10, 30}
	m.EndpointToTTS = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "cx9_endpoint_to_followup_tts_seconds",
		Help: "Desde el fin de voz detectado (endpoint) o audio.end (explicit) hasta el primer frame/URL de TTS de la siguiente intervención", Buckets: turnLat}, []string{"mode", "provider"})
	m.TurnStage = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "cx9_turn_stage_seconds",
		Help: "Etapas del cierre de turno: stt_final, llm_followup, tts_first_byte", Buckets: turnLat}, []string{"stage"})
	m.Endpoints = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "cx9_endpoints_total", Help: "Turnos cerrados por silencio"}, []string{"source"})
	m.Speculative = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "cx9_speculative_followups_total", Help: "Decisiones especulativas de repregunta"}, []string{"result"})
	m.TTSPlayedTimeouts = prometheus.NewCounter(prometheus.CounterOpts{Name: "cx9_tts_played_timeouts_total", Help: "Intervenciones en las que tts.played no llegó a tiempo y el servidor avanzó igual"})
	r.MustRegister(m.EndpointToTTS, m.TurnStage, m.Endpoints, m.Speculative, m.TTSPlayedTimeouts)
	r.MustRegister(m.HTTPRequests, m.HTTPDuration, m.WSConnections, m.WSMessages, m.WSBackpressure, m.SessionsActive, m.Sessions,
		m.Followups, m.SpeechDuration, m.SpeechErrors, m.LLMDuration, m.LLMBreakerG, m.LLMFallbacks, m.InventedTerms)
	return m
}

func (m *M) Handler() http.Handler { return promhttp.HandlerFor(m.Reg, promhttp.HandlerOpts{}) }

// Observadores de llm y speech.
func (m *M) LLMCall(p, outcome string, d time.Duration) {
	m.LLMDuration.WithLabelValues(p, outcome).Observe(d.Seconds())
}
func (m *M) LLMBreaker(p string, s resilience.State) {
	m.LLMBreakerG.WithLabelValues(p).Set(float64(s))
}
func (m *M) SpeechCall(op, outcome string, d time.Duration) {
	m.SpeechDuration.WithLabelValues(op, outcome).Observe(d.Seconds())
}
func (m *M) SpeechError(op, code string) { m.SpeechErrors.WithLabelValues(op, code).Inc() }
