// Package scoring puntúa cada respuesta en 6 dimensiones, arma la versión
// mejorada (solo con lo que dijo el usuario) y el top3 exacto. Cuando faltan
// datos usa [FALTA: …] en texto y null en números, sin inventar nada.
package scoring

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/analysis"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/engine"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/llm"
)

var Dims = []string{"claridad", "estructura", "concrecion", "duracion", "muletillas", "relevancia"}

var DimLabel = map[string]string{"claridad": "Claridad", "estructura": "Estructura STAR", "concrecion": "Concreción (números)",
	"duracion": "Duración", "muletillas": "Muletillas", "relevancia": "Relevancia"}

var Advice = map[string]string{
	"claridad":   "Empezá cada respuesta con la idea principal en una frase corta (\"Trabajé 3 años en X como Y\") y recién después los detalles.",
	"estructura": "Contá las historias en 4 pasos en voz alta: situación (1 frase), tarea (1 frase), lo que hiciste VOS (2-3 frases) y el resultado (1 frase).",
	"concrecion": "Prepará 1 número real por experiencia (usuarios, tiempos, % de mejora, tamaño del equipo) y decilo sin que te lo pidan.",
	"duracion":   "Apuntá a 60-90 segundos por respuesta: practicá con el cronómetro y cortá después del resultado.",
	"muletillas": "Reemplazá la muletilla por una pausa en silencio: pensar callado 2 segundos suena seguro, no inseguro.",
	"relevancia": "Antes de responder, repetí mentalmente la pregunta y contestala en la primera frase; lo demás es apoyo.",
}

const Disclaimer = "Simulador de práctica. Las puntuaciones son orientativas: combinan métricas medibles (duración, muletillas, números) con la opinión del LLM sobre claridad y relevancia. Las versiones mejoradas usan solo lo que dijiste; donde falta un dato aparece [FALTA: …] para que lo completes con tu dato real."

// DimScores tiene las 6 dimensiones; nil = no aplica o sin datos.
type DimScores struct {
	Claridad   *int `json:"claridad"`
	Estructura *int `json:"estructura"`
	Concrecion *int `json:"concrecion"`
	Duracion   *int `json:"duracion"`
	Muletillas *int `json:"muletillas"`
	Relevancia *int `json:"relevancia"`
}

func (d *DimScores) ptr(name string) **int {
	switch name {
	case "claridad":
		return &d.Claridad
	case "estructura":
		return &d.Estructura
	case "concrecion":
		return &d.Concrecion
	case "duracion":
		return &d.Duracion
	case "muletillas":
		return &d.Muletillas
	}
	return &d.Relevancia
}

func (d *DimScores) Get(n string) *int    { return *d.ptr(n) }
func (d *DimScores) Set(n string, v *int) { *d.ptr(n) = v }

type DimMeans struct {
	Claridad   *float64 `json:"claridad"`
	Estructura *float64 `json:"estructura"`
	Concrecion *float64 `json:"concrecion"`
	Duracion   *float64 `json:"duracion"`
	Muletillas *float64 `json:"muletillas"`
	Relevancia *float64 `json:"relevancia"`
}

func (d *DimMeans) set(n string, v *float64) {
	switch n {
	case "claridad":
		d.Claridad = v
	case "estructura":
		d.Estructura = v
	case "concrecion":
		d.Concrecion = v
	case "duracion":
		d.Duracion = v
	case "muletillas":
		d.Muletillas = v
	default:
		d.Relevancia = v
	}
}

type Details struct {
	Segundos          float64       `json:"segundos"`
	SegundosEstimados bool          `json:"segundos_estimados"`
	IdealSegundos     [2]float64    `json:"ideal_segundos"`
	Palabras          int           `json:"palabras"`
	Muletillas        []string      `json:"muletillas"`
	MuletillasCada100 float64       `json:"muletillas_cada_100"`
	Numeros           []string      `json:"numeros"`
	Star              analysis.Star `json:"star"`
	// Pausas ≥ 0,6 s detectadas por /speech; null si no hay datos (modo texto o sin extensión).
	Pausas *engine.PauseStats `json:"pausas"`
	// Fuente del conteo de muletillas: "stt" (con contexto de pausas) o "texto".
	MuletillasFuente string `json:"muletillas_fuente"`
}

type TurnScore struct {
	TurnID        string    `json:"turn_id"`
	Kind          string    `json:"kind"`
	Probe         *string   `json:"probe"`
	Question      string    `json:"question"`
	Answer        string    `json:"answer"`
	Dims          DimScores `json:"dims"`
	Overall       *float64  `json:"overall"`
	ScoreSource   string    `json:"score_source"`
	Comment       string    `json:"comment"`
	Details       Details   `json:"details"`
	LowConfidence []string  `json:"low_confidence"`
}

type Improved struct {
	Text          string   `json:"text"`
	Source        string   `json:"source"`
	Missing       []string `json:"missing"`
	RejectedTerms []string `json:"rejected_terms"`
}

type English struct {
	Grammar       []llm.Grammar `json:"grammar"`
	Tips          []string      `json:"tips"`
	LowConfidence []string      `json:"low_confidence"`
}

type Block struct {
	QuestionID string      `json:"question_id"`
	Question   string      `json:"question"`
	Turns      []TurnScore `json:"turns"`
	Improved   Improved    `json:"improved"`
	English    *English    `json:"english"`
}

type PracticeItem struct {
	Rank      int      `json:"rank"`
	Dimension string   `json:"dimension"`
	Mean      *float64 `json:"mean"`
	Advice    string   `json:"advice"`
}

type Report struct {
	SessionID         string         `json:"session_id"`
	ScenarioID        *string        `json:"scenario_id"`
	ScenarioTitle     string         `json:"scenario_title"`
	Lang              string         `json:"lang"`
	Mode              string         `json:"mode"`
	LLM               string         `json:"llm"`
	StartedAt         time.Time      `json:"started_at"`
	EndedAt           time.Time      `json:"ended_at"`
	EndedByUser       bool           `json:"ended_by_user"`
	QuestionsAnswered int            `json:"questions_answered"`
	Top3              []PracticeItem `json:"top3"`
	DimMeans          DimMeans       `json:"dim_means"`
	Overall           *float64       `json:"overall"`
	Blocks            []Block        `json:"blocks"`
	ProfileChanges    []string       `json:"profile_changes"`
	LLMErrors         []string       `json:"llm_errors"`
	Faltantes         []string       `json:"faltantes"`
	Disclaimer        string         `json:"disclaimer"`
}

// Input es todo lo que necesita el scoring.
type Input struct {
	SessionID      string
	ScenarioID     *string
	ScenarioTitle  string
	Lang           string
	Mode           string
	StartedAt      time.Time
	EndedAt        time.Time
	EndedReason    string
	Questions      []engine.Question
	Ideal          [2]float64
	FollowupIdeal  [2]float64
	Turns          []engine.Turn
	ProfileChanges []string
	LLMErrors      []string
}

func ip(v int) *int         { return &v }
func fp(v float64) *float64 { return &v }

func clamp(v any) *int {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case int:
		f = float64(x)
	case string:
		if _, err := fmt.Sscanf(x, "%g", &f); err != nil {
			return nil
		}
	default:
		return nil
	}
	if math.IsNaN(f) {
		return nil
	}
	return ip(max(1, min(5, int(math.Round(f)))))
}

func DurationScore(s float64, ideal [2]float64) int {
	lo, hi := ideal[0], ideal[1]
	switch {
	case s >= lo && s <= hi:
		return 5
	case s < lo:
		if lo == 0 {
			return 5
		}
		return max(1, int(math.Round(5*s/lo)))
	}
	return max(1, 5-int((s-hi)/30)-1)
}

func FillerScore(rate float64) int {
	switch {
	case rate <= 1:
		return 5
	case rate <= 3:
		return 4
	case rate <= 5:
		return 3
	case rate <= 8:
		return 2
	}
	return 1
}

func NumberScore(n int) int {
	switch n {
	case 0:
		return 1
	case 1:
		return 3
	case 2:
		return 4
	}
	return 5
}

// Heuristic puntúa sin LLM (portado de interview-sim).
func Heuristic(t engine.Turn, q engine.Question, in *Input) TurnScore {
	l := in.Lang
	ans := t.Answer
	fill := analysis.FindFillers(ans, l)
	rate := analysis.FillerRate(ans, l)
	fsrc := "texto"
	if t.STTFillers != nil && strings.TrimSpace(ans) != "" {
		// /speech marca muletillas con el contexto de las pausas (más preciso que el texto solo).
		fill, fsrc = append([]string{}, t.STTFillers...), "stt"
		if n := analysis.WordCount(ans); n > 0 {
			rate = 100 * float64(len(fill)) / float64(n)
		}
	}
	nums := analysis.FindNumbers(ans, l)
	star := analysis.StarParts(ans, l)
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
	if rate > 8 {
		clar--
	}
	var rel *int
	if len(q.Keywords) > 0 {
		rel = ip(2)
		for _, k := range q.Keywords {
			if strings.Contains(analysis.Norm(ans), strings.ToLower(k)) {
				rel = ip(4)
			}
		}
	}
	wantsNumbers := q.ExpectsNumbers
	if t.Kind == "followup" {
		wantsNumbers = t.Probe != nil && *t.Probe == "numbers"
	}
	wantsStar := q.Star && t.Kind == "main"
	ideal := in.Ideal
	if t.Kind == "followup" {
		ideal = in.FollowupIdeal
	}
	empty := analysis.WordCount(ans) == 0
	var d DimScores
	if !empty {
		d.Claridad = ip(max(1, clar))
		if wantsStar {
			d.Estructura = ip(max(1, min(5, star.Count()+1)))
		}
		if wantsNumbers {
			d.Concrecion = ip(NumberScore(len(nums)))
		}
		d.Duracion = ip(DurationScore(t.DurationS, ideal))
		d.Muletillas = ip(FillerScore(rate))
		d.Relevancia = rel
	}
	if fill == nil {
		fill = []string{}
	}
	return TurnScore{TurnID: t.ID, Kind: t.Kind, Probe: t.Probe, Question: t.Question, Answer: ans, Dims: d,
		ScoreSource: "heuristica", LowConfidence: t.LowConfidence,
		Details: Details{Segundos: analysis.Round(t.DurationS, 1), SegundosEstimados: t.DurationEstimated, IdealSegundos: ideal,
			Palabras: analysis.WordCount(ans), Muletillas: fill, MuletillasCada100: analysis.Round(rate, 1), Numeros: nums, Star: star,
			Pausas: t.Pauses, MuletillasFuente: fsrc}}
}

func overall(d DimScores) *float64 {
	sum, n := 0, 0
	for _, k := range Dims {
		if v := d.Get(k); v != nil {
			sum += *v
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return fp(analysis.Round(float64(sum)/float64(n), 1))
}

// Scorer usa un proveedor LLM con respaldo heurístico.
type Scorer struct {
	LLM    llm.Provider
	Errors []string
}

func (s *Scorer) scoreTurn(ctx context.Context, t engine.Turn, q engine.Question, in *Input) TurnScore {
	ts := Heuristic(t, q, in)
	if strings.TrimSpace(t.Answer) == "" {
		return ts
	}
	out, err := llm.Call[llm.ScoreResult](ctx, s.LLM, llm.TaskScore, llm.ScoreSystem(in.Lang),
		llm.ScorePayload{Lang: in.Lang, Question: t.Question, Answer: t.Answer, Keywords: q.Keywords}, 0.1)
	if err != nil {
		s.Errors = append(s.Errors, "score: "+err.Error())
		return ts
	}
	used := false
	for _, k := range []struct {
		name string
		v    any
	}{{"claridad", out.Claridad}, {"relevancia", out.Relevancia}} {
		if v := clamp(k.v); v != nil {
			ts.Dims.Set(k.name, v)
			used = true
		}
	}
	if ts.Dims.Estructura != nil {
		if v := clamp(out.Estructura); v != nil { // promedio: el LLM no tapa lo que la heurística no ve
			ts.Dims.Estructura = ip(int(math.Round(float64(*v+*ts.Dims.Estructura) / 2)))
			used = true
		}
	}
	if c := strings.TrimSpace(out.Comentario); c != "" {
		if len([]rune(c)) > 400 {
			c = string([]rune(c)[:400])
		}
		ts.Comment = c
	}
	if used {
		ts.ScoreSource = "mixta"
	}
	return ts
}

var gapRe = regexp.MustCompile(`(?i)\[(?:FALTA|MISSING)[^\]]*\]`)

func normalizeMissing(xs []string) []string {
	out := []string{}
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		if !strings.HasPrefix(x, "[FALTA") {
			x = "[FALTA: " + strings.Trim(x, "[]") + "]"
		}
		out = append(out, x)
	}
	return out
}

func (s *Scorer) improve(ctx context.Context, question string, answers []string, lang string, star bool) Improved {
	if len(answers) == 0 {
		return Improved{Text: "[FALTA: sin respuesta]", Source: "falta", Missing: []string{"[FALTA: sin respuesta]"}, RejectedTerms: []string{}}
	}
	src := strings.Join(answers, " ")
	rejected := []string{}
	for i := 0; i < 2; i++ {
		out, err := llm.Call[llm.ImproveResult](ctx, s.LLM, llm.TaskImprove, llm.ImproveSystem(lang),
			llm.ImprovePayload{Lang: lang, Question: question, Answers: answers, Star: star, ForbiddenNewTerms: rejected}, 0.2)
		if err != nil {
			s.Errors = append(s.Errors, "improve: "+err.Error())
			break
		}
		text := strings.TrimSpace(out.Mejorada)
		if text == "" {
			break
		}
		bad := analysis.InventedTerms(src, gapRe.ReplaceAllString(text, " "))
		if len(bad) == 0 {
			missing := normalizeMissing(out.Faltantes)
			for _, g := range gapRe.FindAllString(text, -1) {
				if !containsStr(missing, g) {
					missing = append(missing, g)
				}
			}
			return Improved{Text: text, Source: "llm", Missing: missing, RejectedTerms: rejected}
		}
		for _, b := range bad {
			if !containsStr(rejected, b) {
				rejected = append(rejected, b)
			}
		}
	}
	text := analysis.DeterministicImprovement(answers, lang, star)
	if strings.TrimSpace(text) == "" {
		return Improved{Text: "[FALTA: la respuesta no tiene frases completas para reordenar]", Source: "falta",
			Missing: []string{"[FALTA: la respuesta no tiene frases completas para reordenar]"}, RejectedTerms: rejected}
	}
	return Improved{Text: text, Source: "reordenamiento", Missing: []string{}, RejectedTerms: rejected}
}

func containsStr(xs []string, x string) bool {
	for _, y := range xs {
		if strings.EqualFold(y, x) {
			return true
		}
	}
	return false
}

func (s *Scorer) english(ctx context.Context, answers []string, low []string) *English {
	out := &English{Grammar: []llm.Grammar{}, Tips: []string{}, LowConfidence: low}
	said := strings.Join(answers, " ")
	if strings.TrimSpace(said) == "" {
		return out
	}
	res, err := llm.Call[llm.EnglishResult](ctx, s.LLM, llm.TaskEnglish, llm.EnglishSystem, llm.EnglishPayload{Answer: said}, 0.1)
	if err != nil {
		s.Errors = append(s.Errors, "english: "+err.Error())
		res = llm.MockEnglish(said)
	}
	if len(res.Grammar) > 6 {
		res.Grammar = res.Grammar[:6]
	}
	if len(res.Tips) > 3 {
		res.Tips = res.Tips[:3]
	}
	if res.Grammar != nil {
		out.Grammar = res.Grammar
	}
	if res.Tips != nil {
		out.Tips = res.Tips
	}
	return out
}

// Evaluate arma el informe completo.
func (s *Scorer) Evaluate(ctx context.Context, in Input) *Report {
	qmap := map[string]engine.Question{}
	for _, q := range in.Questions {
		qmap[q.ID] = q
	}
	r := &Report{SessionID: in.SessionID, ScenarioID: in.ScenarioID, ScenarioTitle: in.ScenarioTitle, Lang: in.Lang, Mode: in.Mode,
		StartedAt: in.StartedAt, EndedAt: in.EndedAt, EndedByUser: in.EndedReason == "user", Blocks: []Block{},
		ProfileChanges: nonNil(in.ProfileChanges), Faltantes: []string{}, Disclaimer: Disclaimer}
	r.LLM = s.LLM.ID() + "/" + s.LLM.Model()
	if s.LLM.ID() == "mock" {
		r.LLM = "mock"
	}
	// agrupar turnos por pregunta en orden de aparición
	var order []string
	byQ := map[string][]engine.Turn{}
	for _, t := range in.Turns {
		if _, ok := byQ[t.QuestionID]; !ok {
			order = append(order, t.QuestionID)
		}
		byQ[t.QuestionID] = append(byQ[t.QuestionID], t)
	}
	var all []TurnScore
	answered := 0
	for bi, qid := range order {
		turns := byQ[qid]
		q, ok := qmap[qid]
		if !ok {
			q = engine.Question{ID: qid, Text: turns[0].Question, Star: true, ExpectsNumbers: true}
		}
		b := Block{QuestionID: qid, Question: q.Text, Turns: []TurnScore{}}
		var answers, low []string
		for ti, t := range turns {
			ts := s.scoreTurn(ctx, t, q, &in)
			path := fmt.Sprintf("blocks[%d].turns[%d]", bi, ti)
			if strings.TrimSpace(t.Answer) == "" {
				reason := "sin respuesta"
				if t.Skipped {
					reason = "el usuario saltó la pregunta"
				}
				ts.Answer = "[FALTA: sin respuesta]"
				ts.Comment = "[FALTA: " + reason + "]"
				r.Faltantes = append(r.Faltantes, fmt.Sprintf("[FALTA] %s.dims: %s", path, reason))
			} else {
				answers = append(answers, t.Answer)
				for _, d := range Dims {
					if ts.Dims.Get(d) == nil {
						r.Faltantes = append(r.Faltantes, fmt.Sprintf("[FALTA] %s.dims.%s: %s", path, d, notApplicable(d, t, q)))
					}
				}
				if ts.Comment == "" {
					ts.Comment = "[FALTA: el LLM no dio comentario]"
				}
			}
			for _, w := range t.LowConfidence {
				if !containsStr(low, w) {
					low = append(low, w)
				}
			}
			ts.Overall = overall(ts.Dims)
			b.Turns = append(b.Turns, ts)
			all = append(all, ts)
		}
		if len(answers) > 0 {
			answered++
		}
		b.Improved = s.improve(ctx, q.Text, answers, in.Lang, q.Star)
		if b.Improved.Source == "falta" {
			r.Faltantes = append(r.Faltantes, fmt.Sprintf("[FALTA] blocks[%d].improved: %s", bi, strings.TrimSuffix(strings.TrimPrefix(b.Improved.Text, "[FALTA: "), "]")))
		}
		if in.Lang == "en" {
			if low == nil {
				low = []string{}
			}
			b.English = s.english(ctx, answers, low)
		}
		r.Blocks = append(r.Blocks, b)
	}
	r.QuestionsAnswered = answered
	r.Top3, r.DimMeans, r.Overall = top3(all, &r.Faltantes)
	r.LLMErrors = append(nonNil(in.LLMErrors), s.Errors...)
	return r
}

func notApplicable(d string, t engine.Turn, q engine.Question) string {
	switch d {
	case "estructura":
		return "no aplica: la pregunta no es de historia STAR"
	case "concrecion":
		return "no aplica: la pregunta no pide números"
	case "relevancia":
		return "sin datos: la pregunta no tiene keywords y el LLM no la puntuó"
	}
	return "sin datos"
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func top3(all []TurnScore, faltantes *[]string) ([]PracticeItem, DimMeans, *float64) {
	means := map[string]float64{}
	var dm DimMeans
	for _, d := range Dims {
		sum, n := 0, 0
		for _, s := range all {
			if v := s.Dims.Get(d); v != nil {
				sum += *v
				n++
			}
		}
		if n > 0 {
			m := analysis.Round(float64(sum)/float64(n), 2)
			means[d] = m
			dm.set(d, fp(m))
		} else {
			*faltantes = append(*faltantes, fmt.Sprintf("[FALTA] dim_means.%s: ninguna respuesta tiene datos para este criterio", d))
		}
	}
	order := make([]string, 0, len(means))
	for d := range means {
		order = append(order, d)
	}
	idx := map[string]int{}
	for i, d := range Dims {
		idx[d] = i
	}
	sort.Slice(order, func(i, j int) bool {
		if means[order[i]] != means[order[j]] {
			return means[order[i]] < means[order[j]]
		}
		return idx[order[i]] < idx[order[j]]
	})
	items := make([]PracticeItem, 0, 3)
	for i := 0; i < 3; i++ {
		if i < len(order) {
			d := order[i]
			tip := Advice[d]
			if d == "muletillas" {
				count := map[string]int{}
				best, bn := "", 0
				for _, s := range all {
					for _, f := range s.Details.Muletillas {
						count[f]++
						if count[f] > bn || (count[f] == bn && f < best) {
							best, bn = f, count[f]
						}
					}
				}
				if bn > 0 {
					tip += fmt.Sprintf(" Tu muletilla más repetida fue \"%s\" (%d veces).", best, bn)
				}
			}
			if d == "duracion" {
				longest := 0.0
				for _, s := range all {
					longest = math.Max(longest, s.Details.Segundos)
				}
				tip += fmt.Sprintf(" Tu respuesta más larga duró ~%d s.", int(longest))
			}
			m := means[d]
			items = append(items, PracticeItem{Rank: i + 1, Dimension: d, Mean: &m, Advice: tip})
		} else {
			items = append(items, PracticeItem{Rank: i + 1, Dimension: "[FALTA]", Advice: "[FALTA: no hay suficientes respuestas para elegir un foco]"})
			*faltantes = append(*faltantes, fmt.Sprintf("[FALTA] top3[%d]: no hay suficientes dimensiones con datos", i))
		}
	}
	var ov *float64
	if len(means) > 0 {
		sum, n := 0.0, 0
		for _, s := range all {
			if s.Overall != nil {
				sum += *s.Overall
				n++
			}
		}
		if n > 0 {
			ov = fp(analysis.Round(sum/float64(n), 1))
		}
	}
	return items, dm, ov
}
