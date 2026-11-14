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
