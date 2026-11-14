package scoring

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/engine"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/llm"
)

type fakeLLM struct{ improve string }

func (f fakeLLM) ID() string    { return "fake" }
func (f fakeLLM) Model() string { return "f1" }
func (f fakeLLM) Complete(ctx context.Context, r llm.Request) (string, error) {
	switch r.Task {
	case llm.TaskImprove:
		return f.improve, nil
	case llm.TaskScore:
		return `{"claridad": 4, "estructura": "3", "relevancia": 9, "comentario": "ok"}`, nil
	}
	return "", &llm.Error{Provider: "fake", Msg: "caído"}
}

func probe(s string) *string { return &s }

func input() Input {
	return Input{SessionID: "s1", ScenarioTitle: "T", Lang: "es", Mode: "practice", StartedAt: time.Unix(0, 0), EndedAt: time.Unix(60, 0),
		Ideal: [2]float64{40, 120}, FollowupIdeal: [2]float64{15, 75},
		Questions: []engine.Question{{ID: "q1", Text: "¿Recorrido?", ExpectsNumbers: true, Keywords: []string{"trabaj"}}, {ID: "q2", Text: "¿Error?", Star: true}},
		Turns: []engine.Turn{
			{ID: "t-1", QuestionID: "q1", Kind: "main", Question: "¿Recorrido?", Answer: "Eh, bueno, trabajé en Telecom haciendo backend con Go, o sea, varios años.", DurationS: 24.6, LowConfidence: []string{"Telecom"}},
			{ID: "t-2", QuestionID: "q1", Kind: "followup", FollowupIndex: 1, Probe: probe("numbers"), Question: "¿Cuántos años?", Skipped: true, LowConfidence: []string{}},
			{ID: "t-3", QuestionID: "q2", Kind: "main", Question: "¿Error?", Skipped: true, LowConfidence: []string{}},
		}}
}

func TestReportFaltaAndTop3(t *testing.T) {
	s := &Scorer{LLM: llm.NewMock()}
	r := s.Evaluate(context.Background(), input())
	if len(r.Top3) != 3 {
		t.Fatalf("top3 debe tener exactamente 3: %d", len(r.Top3))
	}
	b0 := r.Blocks[0]
	if b0.Turns[1].Answer != "[FALTA: sin respuesta]" || b0.Turns[1].Overall != nil || b0.Turns[1].Dims.Claridad != nil {
		t.Fatalf("%+v", b0.Turns[1])
	}
	if !strings.Contains(b0.Improved.Text, "[FALTA: número concreto]") || b0.Improved.Missing[0] != "[FALTA: número concreto]" {
		t.Fatalf("%+v", b0.Improved)
	}
	if r.Blocks[1].Improved.Text != "[FALTA: sin respuesta]" || r.Blocks[1].Improved.Source != "falta" {
		t.Fatalf("%+v", r.Blocks[1].Improved)
	}
	if r.DimMeans.Estructura != nil {
		t.Fatal("estructura: ninguna respuesta STAR contestada")
	}
	for _, f := range r.Faltantes {
		if !strings.HasPrefix(f, "[FALTA]") {
			t.Fatal(f)
		}
	}
	joined := strings.Join(r.Faltantes, "\n")
	for _, want := range []string{"dim_means.estructura", "blocks[0].turns[1].dims", "blocks[1].improved", "blocks[0].turns[0].dims.estructura"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("falta %s en %s", want, joined)
		}
	}
	if r.QuestionsAnswered != 1 || r.LLM != "mock" || r.Overall == nil {
		t.Fatalf("%+v", r)
	}
	j, _ := json.Marshal(r)
	if strings.Contains(string(j), `"top3":null`) || strings.Contains(string(j), `"faltantes":null`) {
		t.Fatal("listas nunca null")
	}
	md := Markdown(r)
	if !strings.Contains(md, "## Las 3 cosas a practicar") || !strings.Contains(md, "[FALTA: sin respuesta]") {
		t.Fatal(md)
	}
}

func TestTop3FillsWithFalta(t *testing.T) {
	in := input()
	in.Questions = []engine.Question{{ID: "q1", Text: "¿x?"}}
	in.Turns = []engine.Turn{{ID: "t-1", QuestionID: "q1", Kind: "main", Question: "¿x?", Skipped: true}}
	r := (&Scorer{LLM: llm.NewMock()}).Evaluate(context.Background(), in)
	if len(r.Top3) != 3 {
		t.Fatal("exactamente 3")
	}
	for _, it := range r.Top3 {
		if it.Dimension != "[FALTA]" || it.Mean != nil || !strings.HasPrefix(it.Advice, "[FALTA") {
			t.Fatalf("%+v", it)
		}
	}
	if r.Overall != nil {
		t.Fatal("sin datos, overall null")
	}
}

func TestImproveRejectsInventedTerms(t *testing.T) {
	f := fakeLLM{improve: `{"mejorada": "Trabajé 5 años en Telecom con Kafka.", "faltantes": []}`}
	s := &Scorer{LLM: f}
	r := s.Evaluate(context.Background(), input())
	imp := r.Blocks[0].Improved
	if imp.Source != "reordenamiento" || !containsStr(imp.RejectedTerms, "Kafka") || !containsStr(imp.RejectedTerms, "5") {
		t.Fatalf("%+v", imp)
	}
	if strings.Contains(imp.Text, "Kafka") {
		t.Fatal("no puede quedar el término inventado")
	}
	sc := r.Blocks[0].Turns[0]
	if *sc.Dims.Relevancia != 5 || *sc.Dims.Claridad != 4 || sc.ScoreSource != "mixta" || sc.Comment != "ok" {
		t.Fatalf("clamp/mixta %+v", sc.Dims)
	}
	// el error de english se registra y no rompe nada
	f2 := fakeLLM{improve: `{"mejorada": "Trabajé en Telecom haciendo backend con Go durante [FALTA: cuántos años].", "faltantes": ["cuántos años"]}`}
	in := input()
	in.Lang = "en"
	r2 := (&Scorer{LLM: f2}).Evaluate(context.Background(), in)
	if r2.Blocks[0].Improved.Source != "llm" || r2.Blocks[0].Improved.Missing[0] != "[FALTA: cuántos años]" || r2.Blocks[0].English == nil {
		t.Fatalf("%+v", r2.Blocks[0].Improved)
	}
	if len(r2.LLMErrors) == 0 {
		t.Fatal("errores de LLM registrados")
	}
}

func TestDurationAndFillerScores(t *testing.T) {
	if DurationScore(60, [2]float64{40, 120}) != 5 || DurationScore(20, [2]float64{40, 120}) != 3 || DurationScore(200, [2]float64{40, 120}) != 2 {
		t.Fatal("duración")
	}
	if FillerScore(0) != 5 || FillerScore(9) != 1 || NumberScore(3) != 5 {
		t.Fatal("escalas")
	}
}

func TestSTTFillersPreferred(t *testing.T) {
	in := input()
	in.Turns[0].STTFillers = &[]string{"eh"}
	in.Turns[0].Pauses = &engine.PauseStats{Count: 2, TotalS: 1.5, LongestS: 0.9}
	r := (&Scorer{LLM: llm.NewMock()}).Evaluate(context.Background(), in)
	d := r.Blocks[0].Turns[0].Details
	if d.MuletillasFuente != "stt" || len(d.Muletillas) != 1 || d.Pausas.Count != 2 {
		t.Fatalf("%+v", d)
	}
	if r.Blocks[1].Turns[0].Details.Pausas != nil || r.Blocks[1].Turns[0].Details.MuletillasFuente != "texto" {
		t.Fatal("sin datos de STT: pausas null y fuente texto")
	}
}

// Regresión: /speech informó cero muletillas (lista vacía). La fuente sigue siendo
// el STT aunque el texto tenga palabras que el detector por texto contaría.
func TestEmptySTTFillersStillSTTSource(t *testing.T) {
	in := input()
	in.Turns[0].Answer = "Bueno, o sea, trabajé tres años en Telecom con Go."
	in.Turns[0].STTFillers = &[]string{}
	r := (&Scorer{LLM: llm.NewMock()}).Evaluate(context.Background(), in)
	d := r.Blocks[0].Turns[0].Details
	if d.MuletillasFuente != "stt" || len(d.Muletillas) != 0 || d.Muletillas == nil || d.MuletillasCada100 != 0 {
		t.Fatalf("%+v", d)
	}
}
