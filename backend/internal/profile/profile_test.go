package profile

import (
	"strings"
	"testing"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/llm"
)

func TestMergeRequiresLiteralEvidence(t *testing.T) {
	p := Empty()
	said := "Trabajé en Telecom como desarrollador backend desde 2021 hasta 2024. Usaba go y redis."
	ex := llm.Extraction{Experiences: []llm.ExtractedExperience{
		{Company: "Telecom", Role: "desarrollador backend", Period: "2021-2024", Technologies: []string{"go", "redis", "kafka"}, Evidence: "Trabajé en Telecom como desarrollador backend"},
		{Company: "Google", Evidence: "trabajé en Google"},
		{Company: "Telecom2", Evidence: "Usaba go y redis"},
	}, Education: []llm.ExtractedEducation{{Institution: "UTN", Evidence: "inventado de la nada"}}}
	changes, rejected := Merge(p, ex, said, "", "s1", "t-1")
	if len(p.Experiences) != 1 || p.Experiences[0].Company != "Telecom" {
		t.Fatalf("%+v", p.Experiences)
	}
	if strings.Join(p.Experiences[0].Technologies, ",") != "go,redis" {
		t.Fatalf("kafka no lo dijo: %v", p.Experiences[0].Technologies)
	}
	if len(p.Education) != 0 || len(rejected) < 3 || len(changes) == 0 {
		t.Fatalf("changes=%v rejected=%v", changes, rejected)
	}
	if *p.Experiences[0].Evidence[0].SessionID != "s1" {
		t.Fatal("evidencia con sesión")
	}
}

func TestGapsAndSummary(t *testing.T) {
	g := Gaps(Empty(), "es")
	if len(g) != 2 || g[0].ID != "h-recorrido" || g[1].ID != "h-formacion" {
		t.Fatalf("%+v", g)
	}
	p := Empty()
	p.Experiences = []Experience{{Company: "Mercado Pago", Role: "dev"}}
	g = Gaps(p, "en")
	if g[0].ID != "h-mercado-pago-role" || !strings.HasPrefix(g[0].Text, "At Mercado Pago") || *g[0].Company != "Mercado Pago" {
		t.Fatalf("%+v", g[0])
	}
	if Summary(p) != "Mercado Pago | dev" {
		t.Fatal(Summary(p))
	}
	bad := &Profile{Experiences: []Experience{{Company: "", Evidence: []Evidence{{Quote: "corta"}}}}}
	if len(bad.Validate()) != 2 {
		t.Fatal("validate")
	}
}
