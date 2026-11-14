package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/analysis"
)

// FollowupText son las repreguntas fijas del mock (y del respaldo local).
var FollowupText = map[string]map[string]string{
	"es": {
		"numbers":   "¿Podés darme un número concreto? Por ejemplo cuántos usuarios, cuánto tiempo o cuánto mejoró.",
		"why":       "¿Y por qué tomaste esa decisión?",
		"example":   "¿Me contás un ejemplo concreto de eso?",
		"ownership": "¿Qué hiciste vos personalmente, más allá del equipo?",
		"result":    "¿Cuál fue el resultado al final?",
	},
	"en": {
		"numbers":   "Could you give me a concrete number? For example, how many users or how much it improved.",
		"why":       "And why did you make that decision?",
		"example":   "Could you tell me a concrete example?",
		"ownership": "What did you do personally, beyond the team?",
		"result":    "What was the final result?",
	},
}

var whyWords = map[string][]string{
	"es": {"porque", "por eso", "ya que", "debido", "para que", "el motivo"},
	"en": {"because", "so that", "since", "the reason", "due to"},
}

// Mock es un LLM determinista y sin red, portado de interview-sim. Sirve para
// tests y como respaldo local cuando el proveedor real falla.
type Mock struct {
	mu    sync.Mutex
	Calls []Task
}

func NewMock() *Mock          { return &Mock{} }
func (m *Mock) ID() string    { return "mock" }
func (m *Mock) Model() string { return "mock" }

func (m *Mock) Complete(ctx context.Context, req Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.mu.Lock()
	m.Calls = append(m.Calls, req.Task)
	m.mu.Unlock()
	var out any
	switch req.Task {
	case TaskFollowup:
		var p FollowupPayload
		_ = json.Unmarshal([]byte(req.User), &p)
		out = MockFollowup(p)
	case TaskScore:
		var p ScorePayload
		_ = json.Unmarshal([]byte(req.User), &p)
		out = mockScore(p)
	case TaskImprove:
		var p ImprovePayload
		_ = json.Unmarshal([]byte(req.User), &p)
		out = mockImprove(p)
	case TaskProfile:
		var p ProfilePayload
		_ = json.Unmarshal([]byte(req.User), &p)
		out = MockProfile(p)
	case TaskEnglish:
		var p EnglishPayload
		_ = json.Unmarshal([]byte(req.User), &p)
		out = MockEnglish(p.Answer)
	default:
		return "", &Error{Provider: "mock", Msg: fmt.Sprintf("tarea desconocida %q", req.Task)}
	}
	return mustJSON(out), nil
}

func lg(l string) string {
	if l == "en" {
		return "en"
	}
	return "es"
}

var yoRe = regexp.MustCompile(`(^|[^\p{L}])(yo|i)([^\p{L}]|$)`)

// MockFollowup decide la repregunta con heurísticas (igual que interview-sim).
func MockFollowup(p FollowupPayload) FollowupResult {
	l := lg(p.Lang)
	answer := strings.Join(p.Answers, " ")
	asked := map[string]bool{}
	for _, a := range p.AskedProbes {
		asked[a] = true
	}
	probes := p.Probes
	if probes == nil {
		probes = []string{"numbers", "example", "why"}
	}
	if p.FollowupsDone >= p.MaxFollowups {
		return FollowupResult{Action: "next", Reason: "límite de repreguntas"}
	}
	t := analysis.Norm(answer)
	hasWhy := false
	for _, w := range whyWords[l] {
		if strings.Contains(t, w) {
			hasWhy = true
		}
	}
	checks := map[string]bool{
		"numbers":   len(analysis.FindNumbers(answer, l)) == 0,
		"why":       !hasWhy,
		"example":   analysis.WordCount(answer) < 25,
		"ownership": (strings.Contains(t, "nosotros") || strings.Contains(" "+t+" ", " we ")) && !yoRe.MatchString(t),
		"result":    !analysis.StarParts(answer, l).R,
	}
	for _, pr := range probes {
		if !asked[pr] && checks[pr] {
			return FollowupResult{Action: "followup", Probe: pr, Question: FollowupText[l][pr], Reason: "falta " + pr}
		}
	}
	return FollowupResult{Action: "next", Reason: "respuesta suficiente"}
}

func mockScore(p ScorePayload) ScoreResult {
	l := lg(p.Lang)
	ans := p.Answer
	parts := analysis.StarParts(ans, l).Count()
	sents := analysis.Sentences(ans)
	if len(sents) == 0 {
		sents = []string{ans}
	}
	avg := float64(analysis.WordCount(ans)) / float64(len(sents))
	clar := 2
	switch {
	case avg <= 22:
		clar = 5
	case avg <= 32:
		clar = 4
	case avg <= 45:
		clar = 3
	}
	rel := 2
	if len(p.Keywords) == 0 {
		rel = 4
	}
	for _, k := range p.Keywords {
		if strings.Contains(analysis.Norm(ans), strings.ToLower(k)) {
			rel = 4
		}
	}
	if analysis.WordCount(ans) < 6 {
		clar, rel = min(clar, 2), min(rel, 2)
	}
	return ScoreResult{Claridad: clar, Estructura: max(1, min(5, parts+1)), Relevancia: rel,
		Comentario: "Evaluación simulada (mock) basada en heurísticas."}
}

func mockImprove(p ImprovePayload) ImproveResult {
	l := lg(p.Lang)
	joined := strings.Join(p.Answers, " ")
	var missing []string
	if len(analysis.FindNumbers(joined, l)) == 0 {
		if l == "en" {
			missing = append(missing, "[FALTA: concrete number]")
		} else {
			missing = append(missing, "[FALTA: número concreto]")
		}
	}
	text := analysis.DeterministicImprovement(p.Answers, l, p.Star)
	if len(missing) > 0 {
		text += "\n" + missing[0]
	}
	return ImproveResult{Mejorada: text, Faltantes: missing}
}

var (
	companyRe = regexp.MustCompile(`(?:^|\s)en ([A-ZÁÉÍÓÚÑ][\p{L}\p{N}_&]+(?: [A-ZÁÉÍÓÚÑ][\p{L}\p{N}_&]+)*)`)
	workRe    = regexp.MustCompile(`trabaj|estuve|fui|era |soy |desarroll`)
	roleRe    = regexp.MustCompile(`(?:^|\s)como ([\p{L}\p{N}_ ]+?)(?:[,.]| en | con | usando| desde | entre | hasta |$)`)
	periodRe  = regexp.MustCompile(`(?:^|\s)(?:desde|entre|de) (\d{4})(?:\s*(?:a|hasta|y|-)\s*(\d{4}))?`)
	reasonRe  = regexp.MustCompile(`(?i)(?:me fui|cambié|me cambié|dejé)[^.]*?porque ([^.]+)`)
	achRe     = regexp.MustCompile(`(^|[^\p{L}])(logré|logramos|reduje|redujimos|bajé|bajamos|mejoré)([^\p{L}]|$)`)
	probRe    = regexp.MustCompile(`(^|[^\p{L}])(resolví|arreglé|solucioné|depuré)([^\p{L}]|$)`)
	uniRe     = regexp.MustCompile(`(UTN|FIUBA|UBA|Universidad [\p{L} ]+)`)
	studyRe   = regexp.MustCompile(`estudi|carrera|ingenier|recib`)
)

// MockProfile extrae hechos con reglas simples; la evidencia siempre es una
// frase literal del usuario.
func MockProfile(p ProfilePayload) Extraction {
	text := p.Answer
	out := Extraction{Experiences: []ExtractedExperience{}, Education: []ExtractedEducation{}, Skills: []string{}}
	sents := analysis.Sentences(text)
	for _, c := range p.KnownCompanies {
		if c != "" && strings.Contains(strings.ToLower(p.Question), strings.ToLower(c)) && len(sents) > 0 {
			out.Experiences = append(out.Experiences, ExtractedExperience{Company: c, Technologies: []string{}, Frameworks: []string{}, Achievements: []string{}, Problems: []string{}, Evidence: sents[0]})
			break
		}
	}
	for _, sent := range sents {
		low := analysis.Norm(sent)
		techSet := map[string]bool{}
		for _, w := range analysis.Words(low) {
			w = strings.Trim(w, ".,")
			if analysis.TechLexicon[w] {
				techSet[w] = true
			}
		}
		techs := keys(techSet)
		m := companyRe.FindStringSubmatch(sent)
		if m != nil && workRe.MatchString(low) {
			e := ExtractedExperience{Company: m[1], Technologies: techs, Frameworks: []string{}, Achievements: []string{}, Problems: []string{}, Evidence: sent}
			if r := roleRe.FindStringSubmatch(sent); r != nil {
				e.Role = strings.TrimSpace(r[1])
			}
			if pm := periodRe.FindStringSubmatch(sent); pm != nil {
				e.Period = pm[1]
				if pm[2] != "" {
					e.Period += "-" + pm[2]
				}
			}
			if rm := reasonRe.FindStringSubmatch(sent); rm != nil {
				e.ReasonForChange = strings.TrimSpace(rm[1])
			}
			out.Experiences = append(out.Experiences, e)
		} else if n := len(out.Experiences); n > 0 {
			e := &out.Experiences[n-1]
			for _, t := range techs {
				if !contains(e.Technologies, t) {
					e.Technologies = append(e.Technologies, t)
				}
			}
			sort.Strings(e.Technologies)
			if achRe.MatchString(low) {
				e.Achievements = append(e.Achievements, strings.TrimRight(sent, "."))
			}
			if probRe.MatchString(low) {
				e.Problems = append(e.Problems, strings.TrimRight(sent, "."))
			}
			if rm := reasonRe.FindStringSubmatch(sent); rm != nil && e.ReasonForChange == "" {
				e.ReasonForChange = strings.TrimSpace(rm[1])
				e.Evidence += " " + sent
			}
		}
		if um := uniRe.FindStringSubmatch(sent); um != nil && studyRe.MatchString(low) {
			st := ""
			if strings.Contains(low, "curso") || strings.Contains(low, "estoy") {
				st = "en curso"
			}
			out.Education = append(out.Education, ExtractedEducation{Institution: strings.TrimSpace(um[1]), Status: st, Evidence: sent})
		}
	}
	return out
}

var englishRules = []struct {
	re       *regexp.Regexp
	rep, why string
}{
	{regexp.MustCompile(`(^|[^\p{L}])i([^\p{L}]|$)`), "${1}I${2}", "El pronombre 'I' siempre va en mayúscula."},
	{regexp.MustCompile(`people is`), "people are", "'people' es plural: 'people are'."},
	{regexp.MustCompile(`more better`), "better", "'better' ya es comparativo."},
	{regexp.MustCompile(`I am agree`), "I agree", "'agree' es verbo: 'I agree'."},
	{regexp.MustCompile(`depends of`), "depends on", "Se dice 'depends on'."},
}

// MockEnglish marca errores frecuentes con reglas fijas.
func MockEnglish(text string) EnglishResult {
	out := EnglishResult{Grammar: []Grammar{}, Tips: []string{"Usá frases cortas: sujeto + verbo + objeto."}}
	for _, r := range englishRules {
		if m := r.re.FindString(text); m != "" {
			out.Grammar = append(out.Grammar, Grammar{Original: strings.TrimSpace(m), Corrected: strings.TrimSpace(r.re.ReplaceAllString(m, r.rep)), Explanation: r.why})
		}
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
