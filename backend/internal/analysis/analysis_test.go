package analysis

import (
	"reflect"
	"testing"
)

func TestFillersES(t *testing.T) {
	got := FindFillers("Eh, bueno, trabajé en Telecom, o sea, varios años. Este, digamos que sí.", "es")
	want := []string{"eh", "este", "o sea", "digamos", "bueno"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestFillersNoFalsePositiveInsideWord(t *testing.T) {
	if got := FindFillers("Programé en Behh y en Remmo", "es"); len(got) != 0 {
		t.Fatalf("falso positivo: %v", got)
	}
	if got := FindFillers("I was a member of the team", "en"); len(got) != 0 {
		t.Fatalf("falso positivo en: %v", got)
	}
}

func TestNumbers(t *testing.T) {
	got := FindNumbers("Bajamos la latencia un 30% en tres meses con 2,5 servidores", "es")
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	if FindNumbers("nada concreto", "es") == nil || len(FindNumbers("nada concreto", "es")) != 0 {
		t.Fatal("debe ser lista vacía")
	}
}

func TestStarAccentBoundaries(t *testing.T) {
	s := StarParts("Cuando estaba en Telecom tenía que migrar; implementé colas y logré bajar el tiempo.", "es")
	if !(s.S && s.T && s.A && s.R) {
		t.Fatalf("STAR incompleto: %+v", s)
	}
	if StarParts("Bajamos 20 %", "es").R != true {
		t.Fatal("porcentaje debe contar como resultado")
	}
}

func TestInventedTerms(t *testing.T) {
	src := "Trabajé en Telecom con Go, bajamos la latencia."
	if bad := InventedTerms(src, "Trabajé en Telecom con Go y bajamos la latencia."); len(bad) != 0 {
		t.Fatalf("no debía rechazar: %v", bad)
	}
	bad := InventedTerms(src, "Trabajé en Telecom con Go y Kafka y bajamos la latencia un 40%. Lo hice en Mercado.")
	want := []string{"40", "Kafka", "Mercado"}
	if !reflect.DeepEqual(bad, want) {
		t.Fatalf("got %v want %v", bad, want)
	}
}

func TestRemoveFillersAndImprovement(t *testing.T) {
	got := RemoveFillers("eh, bueno, trabajé en backend, o sea, con Go.", "es")
	if got != "Trabajé en backend, con Go." {
		t.Fatalf("got %q", got)
	}
	imp := DeterministicImprovement([]string{"Cuando estaba en Telecom había un problema. Implementé colas. Logré bajar el tiempo."}, "es", true)
	want := "**Situación:** Cuando estaba en Telecom había un problema.\n**Acción:** Implementé colas.\n**Resultado:** Logré bajar el tiempo."
	if imp != want {
		t.Fatalf("got %q", imp)
	}
	if InventedTerms("Cuando estaba en Telecom había un problema. Implementé colas. Logré bajar el tiempo.", imp) != nil &&
		len(InventedTerms("Cuando estaba en Telecom había un problema. Implementé colas. Logré bajar el tiempo.", imp)) != 0 {
		t.Fatal("la mejora determinista no puede inventar")
	}
}

func TestOneQuestion(t *testing.T) {
	if got := OneQuestion(`"¿Cuántos usuarios? ¿Y por qué?"`); got != "¿Cuántos usuarios?" {
		t.Fatalf("got %q", got)
	}
}

func TestSentencesAndCounts(t *testing.T) {
	if n := len(Sentences("Hola. Chau! ¿Qué?\nOtra")); n != 4 {
		t.Fatalf("got %d", n)
	}
	if WordCount("—, ... hola mundo 30%") != 3 {
		t.Fatal("word count")
	}
	if EstimateSeconds("uno dos tres", "en") <= 0 {
		t.Fatal("estimate")
	}
}
