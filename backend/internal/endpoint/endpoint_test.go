package endpoint

import (
	"encoding/binary"
	"math"
	"testing"
)

// sine genera "voz" sintética: un seno de 220 Hz con amplitud amp, ms milisegundos.
func sine(ms int, amp float64) []byte {
	n := ms * 16
	b := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		v := int16(amp * math.Sin(2*math.Pi*220*float64(i)/16000))
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

func silence(ms int) []byte { return make([]byte, ms*32) }

func feedChunks(d *Detector, pcm []byte) {
	for len(pcm) > 0 { // chunks de 100 ms, como el cliente
		n := min(3200, len(pcm))
		d.Feed(pcm[:n])
		pcm = pcm[n:]
	}
}

func energyCfg() Config {
	c := Default()
	c.Mode = Energy
	return c
}

func TestRMS(t *testing.T) {
	if RMS(silence(20)) != 0 || RMS(nil) != 0 {
		t.Fatal("silencio")
	}
	if r := RMS(sine(20, 8000)); r < 5500 || r > 5800 { // 8000/√2 ≈ 5657
		t.Fatalf("rms %v", r)
	}
}

func TestEnergyFallbackClosesAfterSilence(t *testing.T) {
	d := New(energyCfg())
	feedChunks(d, silence(300)) // silencio inicial: no hubo voz todavía
	if _, ok := d.EnergyCandidate(); ok {
		t.Fatal("sin voz no se cierra")
	}
	feedChunks(d, sine(1200, 8000))
	feedChunks(d, silence(600))
	if _, ok := d.EnergyCandidate(); ok {
		t.Fatal("600 ms < 700 ms")
	}
	feedChunks(d, silence(100))
	c, ok := d.EnergyCandidate()
	if !ok || c.Source != Energy || c.SpeechEndedMs != 1500 || c.MarkMs != 2200 || d.LastVoiceMs() != 1500 {
		t.Fatalf("%+v %v", c, ok)
	}
	if !d.Confirm(c) {
		t.Fatal("sin voz nueva se confirma")
	}
}

func TestVoiceThatReturnsDoesNotCut(t *testing.T) {
	d := New(energyCfg())
	feedChunks(d, sine(800, 8000))
	feedChunks(d, silence(500)) // pausa corta en medio de la respuesta
	feedChunks(d, sine(600, 8000))
	if _, ok := d.EnergyCandidate(); ok {
		t.Fatal("la pausa de 500 ms no cierra")
	}
	feedChunks(d, silence(700))
	c, ok := d.EnergyCandidate()
	if !ok || c.SpeechEndedMs != 1900 {
		t.Fatalf("%+v %v", c, ok)
	}
	feedChunks(d, sine(200, 8000)) // vuelve la voz dentro de la ventana de gracia
	if d.Confirm(c) {
		t.Fatal("la voz volvió durante la gracia")
	}
	if _, ok := d.EnergyCandidate(); ok {
		t.Fatal("ya no hay silencio final")
	}
}

func TestQuietNoiseIsNotVoice(t *testing.T) {
	d := New(energyCfg())
	feedChunks(d, sine(2000, 300)) // ruido de fondo bajo el umbral (RMS ≈ 212)
	if d.EnergyVoice() {
		t.Fatal("ruido bajo no es voz")
	}
	d2 := New(energyCfg())
	feedChunks(d2, sine(100, 8000)) // clic corto: menos que MinVoiceMs
	feedChunks(d2, silence(1000))
	if _, ok := d2.EnergyCandidate(); ok {
		t.Fatal("100 ms de voz no alcanzan")
	}
}

func TestSTTFieldsAndAutoFallback(t *testing.T) {
	ip := func(v int) *int { return &v }
	d := New(Default())
	feedChunks(d, sine(1500, 8000))
	feedChunks(d, silence(900))
	if !d.UsesSTT() {
		t.Fatal("auto empieza probando stt")
	}
	if !d.ProbeWanted() || d.ProbeWanted() {
		t.Fatal("una sola confirmación por silencio")
	}
	c, ok := d.STTResult(0, 2400, ip(1500), ip(900), "hola")
	if !ok || c.Source != STT || c.SpeechEndedMs != 1500 {
		t.Fatalf("%+v %v", c, ok)
	}
	// offset relativo a la ventana (ventana de 12 s desde los 2 s)
	d2 := New(Default())
	feedChunks(d2, sine(13000, 8000))
	feedChunks(d2, silence(1000))
	c, ok = d2.STTResult(2000, 14000, ip(11000), ip(1000), "x")
	if !ok || c.SpeechEndedMs != 13000 {
		t.Fatalf("absoluto %+v %v", c, ok)
	}
	d3 := New(Default())
	feedChunks(d3, sine(1000, 8000))
	feedChunks(d3, silence(300))
	if _, ok := d3.STTResult(0, 1300, ip(1000), ip(300), "x"); ok {
		t.Fatal("300 ms no cierra")
	}
	d4 := New(Default())
	feedChunks(d4, sine(1000, 8000))
	feedChunks(d4, silence(800))
	feedChunks(d4, sine(300, 8000))
	if _, ok := d4.STTResult(0, 1800, ip(1000), ip(800), "x"); ok {
		t.Fatal("la voz volvió después de la ventana")
	}
	d5 := New(Default())
	feedChunks(d5, sine(1000, 8000))
	feedChunks(d5, silence(800))
	if c, ok := d5.STTResult(0, 1800, nil, ip(800), "x"); !ok || c.SpeechEndedMs != 1000 {
		t.Fatalf("trailing solo %+v %v", c, ok)
	}
	// ventana sin voz (trailing = ventana) después de voz anterior informada
	if c, ok := d5.STTResult(1800, 1800, nil, ip(0), ""); !ok || c.SpeechEndedMs != 1000 {
		t.Fatalf("voz anterior %+v %v", c, ok)
	}
	// auto sin campos: pasa a energía
	d6 := New(Default())
	feedChunks(d6, sine(1000, 8000))
	feedChunks(d6, silence(800))
	if _, ok := d6.EnergyCandidate(); ok {
		t.Fatal("auto con estado desconocido confirma primero con stt")
	}
	if _, ok := d6.STTResult(0, 1800, nil, nil, "x"); ok || d6.UsesSTT() {
		t.Fatal("sin campos no cierra por stt y deja de usarlo")
	}
	if c, ok := d6.EnergyCandidate(); !ok || c.Source != Energy {
		t.Fatal("respaldo de energía")
	}
	d7 := New(Default())
	feedChunks(d7, sine(1000, 8000))
	feedChunks(d7, silence(800))
	if c, ok := d7.EnergyFallback(); !ok || c.Source != Energy {
		t.Fatal("fallback")
	}
	cs := Default()
	cs.Mode = STT
	d8 := New(cs)
	feedChunks(d8, sine(1000, 8000))
	feedChunks(d8, silence(2000))
	if _, ok := d8.STTResult(0, 3000, nil, nil, "x"); ok {
		t.Fatal("stt sin campos")
	}
	if _, ok := d8.EnergyCandidate(); ok {
		t.Fatal("stt no usa energía")
	}
	if _, ok := d8.EnergyFallback(); ok {
		t.Fatal("stt no usa energía ni de respaldo")
	}
	ce := energyCfg()
	if _, ok := New(ce).STTResult(0, 1000, ip(100), ip(900), "x"); ok {
		t.Fatal("energy ignora stt")
	}
	co := Default()
	co.Mode = Off
	d9 := New(co)
	feedChunks(d9, sine(1000, 8000))
	feedChunks(d9, silence(2000))
	if d9.Enabled() || d9.UsesSTT() || d9.ProbeWanted() {
		t.Fatal("off")
	}
	if _, ok := d9.STTResult(0, 3000, ip(1000), ip(2000), "x"); ok {
		t.Fatal("off no cierra")
	}
	if _, ok := d9.EnergyCandidate(); ok {
		t.Fatal("off no cierra por energía")
	}
}

func TestConfigValidate(t *testing.T) {
	if Default().Validate() != nil {
		t.Fatal("default")
	}
	for _, c := range []Config{{Mode: "x", SilenceMs: 700, RMS: 1}, {Mode: Auto, SilenceMs: 10, RMS: 1}, {Mode: Auto, SilenceMs: 700, RMS: 0},
		{Mode: Auto, SilenceMs: 700, RMS: 1, GraceMs: -1}, {Mode: Auto, SilenceMs: 700, RMS: 1, MinVoiceMs: -1}} {
		if c.Validate() == nil {
			t.Fatalf("%+v", c)
		}
	}
}

func TestSimilarity(t *testing.T) {
	if Similarity("Trabajé tres años en Telecom.", "trabajé tres años en telecom") != 1 {
		t.Fatal("igual normalizado")
	}
	if s := Similarity("trabajé tres años en telecom con go", "trabajé dos años en telecom con go"); s < 0.85 || s >= 0.9 {
		t.Fatal(s) // un número distinto en 7 palabras: < 0.9, se descarta
	}
	if Similarity("hice apis", "lideré una migración a go") > 0.3 {
		t.Fatal("distintas")
	}
	if Similarity("", "") != 1 {
		t.Fatal("vacías")
	}
}
