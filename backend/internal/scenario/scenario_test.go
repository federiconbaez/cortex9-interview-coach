package scenario

import (
	"errors"
	"strings"
	"testing"
)

func TestBuiltinScenarios(t *testing.T) {
	b, err := LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"screening-ia": 10, "behavioral-star": 7, "backend-tecnica": 8}
	for id, n := range want {
		s, ok := b[id]
		if !ok {
			t.Fatalf("falta %s", id)
		}
		if len(s.Scenario.Questions) != n {
			t.Fatalf("%s: %d preguntas, quiero %d", id, len(s.Scenario.Questions), n)
		}
		if strings.Join(s.Scenario.Languages, ",") != "es,en" {
			t.Fatalf("%s: idiomas %v", id, s.Scenario.Languages)
		}
		if s.Scenario.MaxF() != 2 {
			t.Fatalf("%s: max_followups", id)
		}
	}
	if !strings.Contains(b["screening-ia"].Scenario.Title.Get("es"), "Valeria") {
		t.Fatal("screening-ia debe ser estilo Valeria")
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]string{
		"/questions":            "id: x1\ntitle: {es: X}\nquestions: []\n",
		"/max_followups":        "id: x1\ntitle: {es: X}\nmax_followups: 3\nquestions:\n  - {id: a1, text: {es: hola}}\n",
		"/questions/1/id":       "id: x1\ntitle: {es: X}\nquestions:\n  - {id: a1, text: {es: hola}}\n  - {id: a1, text: {es: chau}}\n",
		"/questions/0/probes/0": "id: x1\ntitle: {es: X}\nquestions:\n  - {id: a1, text: {es: hola}, probes: [magia]}\n",
		"/id":                   "id: X!\ntitle: {es: X}\nquestions:\n  - {id: a1, text: {es: hola}}\n",
		"/questions/0/text":     "id: x1\ntitle: {es: X}\nquestions:\n  - {id: a1, text: {fr: salut}}\n",
	}
	for ptr, y := range cases {
		_, err := ParseYAML([]byte(y))
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("%s: esperaba ValidationError, got %v", ptr, err)
		}
		found := false
		for _, e := range ve.Errors {
			if e.Pointer == ptr {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: errores %v", ptr, ve.Errors)
		}
	}
	if _, err := ParseYAML([]byte("id: x1\ntitle: {es: X}\nfoo: 1\nquestions:\n  - {id: a1, text: {es: hola}}\n")); err == nil {
		t.Fatal("campo desconocido debe fallar")
	}
	if _, err := ParseJSON([]byte(`{"id":"x1","title":{"es":"X"},"questions":[{"id":"a1","text":{"es":"h"}}],"extra":1}`)); err == nil {
		t.Fatal("campo desconocido JSON debe fallar")
	}
}

func TestRoundTripYAML(t *testing.T) {
	s, err := ParseYAML([]byte("id: mi-screening\ntitle: {es: Mi, en: My}\nquestions:\n  - id: recorrido\n    text: {es: Contame, en: Tell me}\n    probes: [example, numbers]\n    star: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := ParseYAML(s.ToYAML())
	if err != nil {
		t.Fatal(err)
	}
	if s2.Questions[0].IsStar() || !s2.Questions[0].WantsNumbers() || s2.Questions[0].Text.Get("en") != "Tell me" {
		t.Fatalf("round trip: %+v", s2.Questions[0])
	}
	if got := s2.Questions[0].Text.Get("fr"); got != "Contame" {
		t.Fatal("fallback es")
	}
}
