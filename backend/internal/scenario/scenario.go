// Package scenario carga, valida y serializa escenarios de entrevista con el
// mismo esquema YAML que interview-sim. Los incluidos van embebidos con go:embed.
package scenario

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

// MaxFollowupsCap es el tope duro del contrato: 2 repreguntas por pregunta.
const MaxFollowupsCap = 2

var validProbes = map[string]bool{"numbers": true, "example": true, "why": true, "ownership": true, "result": true}
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// I18n es texto por idioma (es/en).
type I18n map[string]string

// Get devuelve el texto del idioma, con respaldo en es y luego en cualquiera.
func (t I18n) Get(lang string) string {
	if v := t[lang]; v != "" {
		return v
	}
	if v := t["es"]; v != "" {
		return v
	}
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if t[k] != "" {
			return t[k]
		}
	}
	return ""
}

type Question struct {
	ID             string   `json:"id" yaml:"id"`
	Text           I18n     `json:"text" yaml:"text"`
	Probes         []string `json:"probes,omitempty" yaml:"probes,omitempty"`
	Keywords       []string `json:"keywords,omitempty" yaml:"keywords,omitempty"`
	Star           *bool    `json:"star,omitempty" yaml:"star,omitempty"`
	ExpectsNumbers *bool    `json:"expects_numbers,omitempty" yaml:"expects_numbers,omitempty"`
}

func (q Question) IsStar() bool       { return q.Star == nil || *q.Star }
func (q Question) WantsNumbers() bool { return q.ExpectsNumbers == nil || *q.ExpectsNumbers }
func (q Question) ProbeList() []string {
	if q.Probes == nil {
		return []string{"numbers", "example", "why"}
	}
	return q.Probes
}

type Scenario struct {
	ID                   string     `json:"id" yaml:"id"`
	Title                I18n       `json:"title" yaml:"title"`
	Style                string     `json:"style,omitempty" yaml:"style,omitempty"`
	MaxFollowups         *int       `json:"max_followups,omitempty" yaml:"max_followups,omitempty"`
	IdealSeconds         []float64  `json:"ideal_seconds,omitempty" yaml:"ideal_seconds,omitempty"`
	FollowupIdealSeconds []float64  `json:"followup_ideal_seconds,omitempty" yaml:"followup_ideal_seconds,omitempty"`
	Intro                I18n       `json:"intro,omitempty" yaml:"intro,omitempty"`
	Closing              I18n       `json:"closing,omitempty" yaml:"closing,omitempty"`
	Questions            []Question `json:"questions" yaml:"questions"`
	Builtin              bool       `json:"builtin" yaml:"-"`
	Languages            []string   `json:"languages" yaml:"-"`
	UpdatedAt            *time.Time `json:"updated_at,omitempty" yaml:"-"`
}

func (s *Scenario) StyleOr() string {
	if s.Style == "" {
		return "entrevista"
	}
	return s.Style
}

func (s *Scenario) MaxF() int {
	if s.MaxFollowups == nil {
		return 2
	}
	return *s.MaxFollowups
}

func (s *Scenario) Ideal() [2]float64 {
	if len(s.IdealSeconds) == 2 {
		return [2]float64{s.IdealSeconds[0], s.IdealSeconds[1]}
	}
	return [2]float64{40, 120}
}

func (s *Scenario) FollowupIdeal() [2]float64 {
	if len(s.FollowupIdealSeconds) == 2 {
		return [2]float64{s.FollowupIdealSeconds[0], s.FollowupIdealSeconds[1]}
	}
	return [2]float64{15, 75}
}

// FieldError es un error de validación con JSON Pointer (RFC 6901).
type FieldError struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

// ValidationError agrupa errores por campo.
type ValidationError struct{ Errors []FieldError }

func (e *ValidationError) Error() string {
	var parts []string
	for _, f := range e.Errors {
		parts = append(parts, f.Pointer+": "+f.Message)
	}
	return "escenario inválido: " + strings.Join(parts, "; ")
}

func validI18n(t I18n) bool {
	if len(t) == 0 {
		return false
	}
	for k, v := range t {
		if (k != "es" && k != "en") || strings.TrimSpace(v) == "" {
			return false
		}
	}
	return true
}

// Validate aplica las reglas de interview-sim y del contrato y completa Languages.
func (s *Scenario) Validate() error {
	var errs []FieldError
	add := func(p, m string) { errs = append(errs, FieldError{p, m}) }
	if !slugRe.MatchString(s.ID) {
		add("/id", "debe cumplir ^[a-z0-9][a-z0-9-]{1,62}$")
	}
	if !validI18n(s.Title) {
		add("/title", "necesita es o en, no vacíos")
	}
	if s.MaxFollowups != nil && (*s.MaxFollowups < 0 || *s.MaxFollowups > MaxFollowupsCap) {
		add("/max_followups", "debe estar entre 0 y 2")
	}
	for name, v := range map[string][]float64{"ideal_seconds": s.IdealSeconds, "followup_ideal_seconds": s.FollowupIdealSeconds} {
		if v != nil && (len(v) != 2 || v[0] < 0 || v[1] < v[0]) {
			add("/"+name, "debe ser [mínimo, máximo] con 0 ≤ mínimo ≤ máximo")
		}
	}
	if s.Intro != nil && len(s.Intro) > 0 && !validI18n(s.Intro) {
		add("/intro", "solo es/en, no vacíos")
	}
	if s.Closing != nil && len(s.Closing) > 0 && !validI18n(s.Closing) {
		add("/closing", "solo es/en, no vacíos")
	}
	if len(s.Questions) == 0 {
		add("/questions", "no tiene preguntas")
	}
	seen := map[string]bool{}
	langs := map[string]int{}
	for i, q := range s.Questions {
		p := fmt.Sprintf("/questions/%d", i)
		if !slugRe.MatchString(q.ID) {
			add(p+"/id", "id inválido")
		}
		if seen[q.ID] {
			add(p+"/id", "id de pregunta repetido")
		}
		seen[q.ID] = true
		if !validI18n(q.Text) {
			add(p+"/text", "necesita text.es o text.en")
		}
		for k := range q.Text {
			langs[k]++
		}
		for j, pr := range q.Probes {
			if !validProbes[pr] {
				add(fmt.Sprintf("%s/probes/%d", p, j), "probe inválido (numbers, example, why, ownership, result)")
			}
		}
	}
	if len(errs) > 0 {
		return &ValidationError{errs}
	}
	s.Languages = nil
	for _, l := range []string{"es", "en"} {
		if langs[l] == len(s.Questions) && (s.Title[l] != "") {
			s.Languages = append(s.Languages, l)
		}
	}
	if len(s.Languages) == 0 {
		for _, l := range []string{"es", "en"} {
			if langs[l] > 0 {
				s.Languages = append(s.Languages, l)
			}
		}
	}
	return nil
}

// ParseYAML decodifica en modo estricto (rechaza campos desconocidos).
func ParseYAML(data []byte) (*Scenario, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var s Scenario
	if err := dec.Decode(&s); err != nil {
		return nil, &ValidationError{[]FieldError{{"", "YAML inválido: " + err.Error()}}}
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// ParseJSON decodifica en modo estricto (rechaza campos desconocidos y de solo lectura).
func ParseJSON(data []byte) (*Scenario, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &ValidationError{[]FieldError{{"", "JSON inválido: " + err.Error()}}}
	}
	for _, ro := range []string{"builtin", "languages", "updated_at"} {
		delete(raw, ro) // campos readOnly: se ignoran si vienen
	}
	clean, _ := json.Marshal(raw)
	dec := json.NewDecoder(bytes.NewReader(clean))
	dec.DisallowUnknownFields()
	var s Scenario
	if err := dec.Decode(&s); err != nil {
		return nil, &ValidationError{[]FieldError{{"", "JSON inválido: " + err.Error()}}}
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// ToYAML serializa el escenario.
func (s *Scenario) ToYAML() []byte {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	_ = enc.Encode(s)
	return buf.Bytes()
}

// Builtin es un escenario incluido con su YAML original.
type Builtin struct {
	Scenario *Scenario
	YAML     []byte
}

// LoadBuiltin carga los escenarios embebidos. Falla si alguno no valida.
func LoadBuiltin() (map[string]Builtin, error) {
	out := map[string]Builtin{}
	err := fs.WalkDir(builtinFS, "builtin", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := builtinFS.ReadFile(path)
		if err != nil {
			return err
		}
		s, err := ParseYAML(data)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		s.Builtin = true
		out[s.ID] = Builtin{s, data}
		return nil
	})
	return out, err
}
