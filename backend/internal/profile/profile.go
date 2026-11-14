// Package profile tiene el perfil REAL del candidato (modo historia): tipos,
// fusión de hechos extraídos con evidencia literal, huecos y resumen.
package profile

import (
	"fmt"
	"strings"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/analysis"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/llm"
)

type Evidence struct {
	Quote     string  `json:"quote"`
	SessionID *string `json:"session_id"`
	TurnID    *string `json:"turn_id"`
}

type Experience struct {
	Company         string     `json:"company"`
	Role            string     `json:"role,omitempty"`
	Period          string     `json:"period,omitempty"`
	Technologies    []string   `json:"technologies,omitempty"`
	Frameworks      []string   `json:"frameworks,omitempty"`
	Achievements    []string   `json:"achievements,omitempty"`
	Problems        []string   `json:"problems,omitempty"`
	ReasonForChange string     `json:"reason_for_change,omitempty"`
	Evidence        []Evidence `json:"evidence,omitempty"`
}

type Education struct {
	Institution string     `json:"institution"`
	Degree      string     `json:"degree,omitempty"`
	Status      string     `json:"status,omitempty"`
	Evidence    []Evidence `json:"evidence,omitempty"`
}

// Input es el cuerpo de POST/PUT.
type Input struct {
	Experiences []Experience `json:"experiences"`
	Education   []Education  `json:"education"`
	Skills      []string     `json:"skills"`
}

// Profile es el perfil almacenado.
type Profile struct {
	Version     int          `json:"version"`
	UpdatedAt   *time.Time   `json:"updated_at"`
	Experiences []Experience `json:"experiences"`
	Education   []Education  `json:"education"`
	Skills      []string     `json:"skills"`
}

// Empty es un perfil vacío (sin persistir).
func Empty() *Profile {
	return &Profile{Version: 1, Experiences: []Experience{}, Education: []Education{}, Skills: []string{}}
}

// FromInput normaliza listas nulas.
func FromInput(in Input) *Profile {
	p := Empty()
	if in.Experiences != nil {
		p.Experiences = in.Experiences
	}
	if in.Education != nil {
		p.Education = in.Education
	}
	if in.Skills != nil {
		p.Skills = in.Skills
	}
	return p
}

// Validate revisa campos obligatorios y evidencias mínimas.
func (p *Profile) Validate() []string {
	var errs []string
	for i, e := range p.Experiences {
		if strings.TrimSpace(e.Company) == "" {
			errs = append(errs, fmt.Sprintf("/experiences/%d/company", i))
		}
		for j, ev := range e.Evidence {
			if len([]rune(ev.Quote)) < 8 {
				errs = append(errs, fmt.Sprintf("/experiences/%d/evidence/%d/quote", i, j))
			}
		}
	}
	for i, e := range p.Education {
		if strings.TrimSpace(e.Institution) == "" {
			errs = append(errs, fmt.Sprintf("/education/%d/institution", i))
		}
	}
	return errs
}

func supported(evidence, said string) bool {
	ev := strings.Trim(analysis.StripAccents(analysis.Norm(evidence)), " .\"'")
	return len([]rune(ev)) >= 8 && strings.Contains(analysis.StripAccents(analysis.Norm(said)), ev)
}

func mergeList(dst []string, src []string) ([]string, bool) {
	low := map[string]bool{}
	for _, x := range dst {
		low[strings.ToLower(x)] = true
	}
	added := false
	for _, x := range src {
		if x != "" && !low[strings.ToLower(x)] {
			dst = append(dst, x)
			low[strings.ToLower(x)] = true
			added = true
		}
	}
	return dst, added
}

// Merge fusiona lo extraído por el LLM. Solo entra lo respaldado por una cita
// literal de lo que dijo el usuario; devuelve cambios aplicados y descartados.
func Merge(p *Profile, ex llm.Extraction, said, question string, sessionID, turnID string) (changes, rejected []string) {
	ctx := analysis.Norm(said + " " + question)
	ev := func(q string) Evidence {
		s, t := sessionID, turnID
		return Evidence{Quote: strings.TrimSpace(q), SessionID: &s, TurnID: &t}
	}
	saidNorm := analysis.Norm(said)
	for _, x := range ex.Experiences {
		company := strings.TrimSpace(x.Company)
		if company == "" {
			continue
		}
		if !supported(x.Evidence, said) || !strings.Contains(ctx, strings.ToLower(company)) {
			rejected = append(rejected, "descartado (sin cita literal): "+company)
			continue
		}
		var cur *Experience
		for i := range p.Experiences {
			if strings.EqualFold(p.Experiences[i].Company, company) {
				cur = &p.Experiences[i]
			}
		}
		if cur == nil {
			p.Experiences = append(p.Experiences, Experience{Company: company})
			cur = &p.Experiences[len(p.Experiences)-1]
			changes = append(changes, "nueva experiencia: "+company)
		}
		filter := func(vals []string, literal bool) []string {
			var ok []string
			for _, v := range vals {
				if literal {
					if strings.Contains(saidNorm, analysis.Norm(v)) {
						ok = append(ok, v)
					} else {
						rejected = append(rejected, fmt.Sprintf("descartado (no lo dijiste): %s", v))
					}
				} else if len(analysis.InventedTerms(said, v)) == 0 {
					ok = append(ok, v)
				} else {
					rejected = append(rejected, fmt.Sprintf("descartado (agrega datos): %s", v))
				}
			}
			return ok
		}
		var added bool
		if cur.Technologies, added = mergeList(cur.Technologies, filter(x.Technologies, true)); added {
			changes = append(changes, company+": +technologies")
		}
		if cur.Frameworks, added = mergeList(cur.Frameworks, filter(x.Frameworks, true)); added {
			changes = append(changes, company+": +frameworks")
		}
		if cur.Achievements, added = mergeList(cur.Achievements, filter(x.Achievements, false)); added {
			changes = append(changes, company+": +achievements")
		}
		if cur.Problems, added = mergeList(cur.Problems, filter(x.Problems, false)); added {
			changes = append(changes, company+": +problems")
		}
		for _, f := range []struct {
			dst *string
			val string
			nm  string
		}{{&cur.Role, x.Role, "role"}, {&cur.Period, x.Period, "period"}, {&cur.ReasonForChange, x.ReasonForChange, "reason_for_change"}} {
			if f.val != "" && *f.dst == "" && len(analysis.InventedTerms(said, f.val)) == 0 {
				*f.dst = f.val
				changes = append(changes, fmt.Sprintf("%s: %s = %s", company, f.nm, f.val))
			}
		}
		cur.Evidence = append(cur.Evidence, ev(x.Evidence))
	}
	for _, e := range ex.Education {
		inst := strings.TrimSpace(e.Institution)
		if inst == "" {
			continue
		}
		if !supported(e.Evidence, said) {
			rejected = append(rejected, "descartado (sin cita literal): "+inst)
			continue
		}
		exists := false
		for _, cur := range p.Education {
			if strings.EqualFold(cur.Institution, inst) {
				exists = true
			}
		}
		if !exists {
			p.Education = append(p.Education, Education{Institution: inst, Degree: e.Degree, Status: e.Status, Evidence: []Evidence{ev(e.Evidence)}})
			changes = append(changes, "formación: "+inst)
		}
	}
	if len(ex.Skills) > 0 && supported(ex.SkillsEvidence, said) {
		var added bool
		if p.Skills, added = mergeList(p.Skills, ex.Skills); added {
			changes = append(changes, "skills actualizadas")
		}
	}
	return changes, rejected
}

// Gap es una pregunta para completar el perfil.
type Gap struct {
	ID      string  `json:"id"`
	Company *string `json:"company"`
	Field   string  `json:"field"`
	Text    string  `json:"text"`
}

func slug(s string) string {
	return strings.ReplaceAll(analysis.StripAccents(strings.ToLower(strings.TrimSpace(s))), " ", "-")
}

// Gaps devuelve las preguntas para completar datos faltantes (portado de interview-sim).
func Gaps(p *Profile, lang string) []Gap {
	es := lang != "en"
	pick := func(a, b string) string {
		if es {
			return a
		}
		return b
	}
	out := []Gap{}
	if len(p.Experiences) == 0 {
		out = append(out, Gap{ID: "h-recorrido", Field: "experiences", Text: pick(
			"Contame tu recorrido profesional, empresa por empresa, empezando por la más reciente.",
			"Tell me about your career, company by company, starting with the most recent one.")})
	}
	for _, e := range p.Experiences {
		c := e.Company
		cc := c
		checks := []struct {
			field, es, en string
			missing       bool
		}{
			{"role", fmt.Sprintf("En %s, ¿cuál era tu rol exactamente y en qué años estuviste?", c), fmt.Sprintf("At %s, what exactly was your role, and which years were you there?", c), e.Role == "" || e.Period == ""},
			{"technologies", fmt.Sprintf("En %s, ¿con qué tecnologías y frameworks trabajabas día a día?", c), fmt.Sprintf("At %s, which technologies and frameworks did you use every day?", c), len(e.Technologies) == 0},
			{"achievements", fmt.Sprintf("¿Qué logro concreto tuviste en %s? Si podés, con un número.", c), fmt.Sprintf("What concrete achievement did you have at %s? With a number, if you can.", c), len(e.Achievements) == 0},
			{"problems", fmt.Sprintf("¿Qué problema difícil resolviste en %s?", c), fmt.Sprintf("What difficult problem did you solve at %s?", c), len(e.Problems) == 0},
			{"reason_for_change", fmt.Sprintf("¿Por qué te fuiste de %s, o por qué querés cambiar?", c), fmt.Sprintf("Why did you leave %s, or why do you want to change?", c), e.ReasonForChange == ""},
		}
		for _, ch := range checks {
			if ch.missing {
				out = append(out, Gap{ID: "h-" + slug(c) + "-" + ch.field, Company: &cc, Field: ch.field, Text: pick(ch.es, ch.en)})
			}
		}
	}
	if len(p.Education) == 0 {
		out = append(out, Gap{ID: "h-formacion", Field: "education", Text: pick(
			"Contame tu formación: qué estudiaste, dónde y hasta dónde llegaste.",
			"Tell me about your education: what you studied, where, and how far you got.")})
	}
	return out
}

// Summary es el resumen compacto que recibe la entrevistadora.
func Summary(p *Profile) string {
	var lines []string
	for _, e := range p.Experiences {
		bits := []string{e.Company}
		if e.Role != "" {
			bits = append(bits, e.Role)
		}
		if e.Period != "" {
			bits = append(bits, e.Period)
		}
		if len(e.Technologies) > 0 {
			bits = append(bits, "tec: "+strings.Join(e.Technologies, ", "))
		}
		if len(e.Achievements) > 0 {
			bits = append(bits, "logros: "+strings.Join(e.Achievements, "; "))
		}
		if len(e.Problems) > 0 {
			bits = append(bits, "problemas: "+strings.Join(e.Problems, "; "))
		}
		if e.ReasonForChange != "" {
			bits = append(bits, "motivo de cambio: "+e.ReasonForChange)
		}
		lines = append(lines, strings.Join(bits, " | "))
	}
	for _, ed := range p.Education {
		lines = append(lines, strings.TrimSpace("Formación: "+ed.Institution+" "+ed.Degree+" "+ed.Status))
	}
	if len(p.Skills) > 0 {
		lines = append(lines, "Skills: "+strings.Join(p.Skills, ", "))
	}
	return strings.Join(lines, "\n")
}
