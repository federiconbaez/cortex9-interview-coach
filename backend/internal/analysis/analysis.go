// Package analysis porta el análisis determinista de interview-sim: muletillas,
// números, marcadores STAR, duración estimada y la barrera anti-invención.
// Todo funciona sin LLM y sin red.
package analysis

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

type bounded struct {
	re          *regexp.Regexp
	left, right bool // exigir borde de palabra (Unicode) a izquierda/derecha
}

func b(p string, left, right bool) bounded {
	return bounded{regexp.MustCompile(p), left, right}
}

var fillers = map[string][]bounded{
	"es": {
		b(`eh+`, true, true), b(`em+`, true, true), b(`mm+`, true, true),
		b(`este\s*(?:,|\.\.\.|…)`, true, false), b(`o sea`, true, true), b(`digamos`, true, true),
		b(`tipo\s*,`, true, false), b(`viste`, true, true), b(`bueno\s*,`, true, false),
		b(`básicamente`, true, true), b(`como que`, true, true), b(`nada\s*,`, true, false),
		b(`en plan`, true, true),
	},
	"en": {
		b(`u+m+`, true, true), b(`u+h+`, true, true), b(`er+`, true, true), b(`like\s*,`, true, false),
		b(`you know`, true, true), b(`basically`, true, true), b(`i mean`, true, true),
		b(`kind of`, true, true), b(`sort of`, true, true), b(`actually`, true, true),
		b(`well\s*,`, true, false),
	},
}

var numberWords = map[string][]string{
	"es": {"dos", "tres", "cuatro", "cinco", "seis", "siete", "ocho", "nueve", "diez",
		"quince", "veinte", "treinta", "cuarenta", "cincuenta", "cien", "cientos",
		"doscientos", "trescientos", "quinientos", "mil", "miles", "millón", "millones",
		"doble", "triple", "mitad", "por ciento", "docena"},
	"en": {"two", "three", "four", "five", "six", "seven", "eight", "nine", "ten",
		"fifteen", "twenty", "thirty", "forty", "fifty", "hundred", "hundreds",
		"thousand", "thousands", "million", "millions", "twice", "double", "half",
		"percent", "dozen"},
}

var starMarkers = map[string]map[string][]string{
	"es": {
		"S": {"cuando", "en ese momento", "el contexto", "trabajaba", "estaba en", "teníamos",
			"había un", "había una", "el problema", "en el proyecto", "en la empresa"},
		"T": {"tenía que", "mi rol", "mi responsabilidad", "el objetivo", "me pidieron",
			"necesitábamos", "me tocó", "la tarea", "el desafío", "teníamos que"},
		"A": {"hice", "implementé", "armé", "decidí", "propuse", "desarrollé", "cambié",
			"migré", "analicé", "escribí", "configuré", "diseñé", "agregué", "investigué",
			"medí", "lo que hice", "programé", "automaticé", "optimicé", "refactoricé",
			"usé", "busqué", "reproduje"},
		"R": {"logramos", "logré", "resultado", "redujimos", "reduje", "bajó", "bajamos",
			"subió", "mejoró", "aumentó", "terminamos", "quedó", "ahorramos", "aprendí",
			"al final", "conseguimos", "pasó de", "pasamos de"},
	},
	"en": {
		"S": {"when", "at that time", "the context", "i was working", "we had", "there was",
			"the problem", "on the project", "at the company"},
		"T": {"i had to", "my role", "my responsibility", "the goal", "i was asked",
			"we needed", "the task", "the challenge", "we had to"},
		"A": {"i built", "i implemented", "i designed", "i decided", "i proposed",
			"i developed", "i changed", "i migrated", "i analyzed", "i wrote",
			"i configured", "i added", "i investigated", "i measured", "i used",
			"i automated", "i optimized", "i refactored", "what i did", "i reproduced"},
		"R": {"we achieved", "i achieved", "result", "we reduced", "i reduced", "dropped",
			"improved", "increased", "we finished", "we saved", "i learned", "in the end",
			"went from", "we went from"},
	},
}

var wpm = map[string]float64{"es": 130, "en": 110}

// TechLexicon son tecnologías conocidas: si aparecen en un texto "mejorado"
// y no en lo que dijo el candidato, son invención.
var TechLexicon = map[string]bool{}

func init() {
	for _, t := range strings.Fields(`kafka redis docker kubernetes k8s java go golang rust
python spring react angular vue node nodejs express postgres postgresql mysql oracle mongodb aws gcp azure grafana
datadog prometheus terraform jenkins gitlab github linux c# .net dotnet kotlin scala rabbitmq elasticsearch graphql grpc
hibernate django flask fastapi laravel php typescript javascript sql nosql microservicios microservices lambda s3 ec2 kvm
ebpf xdp nginx apache tomcat jboss openshift helm ansible`) {
		TechLexicon[t] = true
	}
}

func lang(l string) string {
	if l == "en" {
		return "en"
	}
	return "es"
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

func runeBefore(s string, i int) (rune, bool) {
	if i <= 0 {
		return 0, false
	}
	r := []rune(s[:i])
	return r[len(r)-1], true
}

func runeAfter(s string, i int) (rune, bool) {
	if i >= len(s) {
		return 0, false
	}
	for _, r := range s[i:] {
		return r, true
	}
	return 0, false
}

func findBounded(p bounded, s string) [][]int {
	var out [][]int
	for _, m := range p.re.FindAllStringIndex(s, -1) {
		if p.left {
			if r, ok := runeBefore(s, m[0]); ok && isWordRune(r) {
				continue
			}
		}
		if p.right {
			if r, ok := runeAfter(s, m[1]); ok && isWordRune(r) {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// containsPhrase busca la frase con bordes de palabra Unicode.
func containsPhrase(text, phrase string) bool {
	return len(findBounded(bounded{regexp.MustCompile(regexp.QuoteMeta(phrase)), true, true}, text)) > 0
}

var spaces = regexp.MustCompile(`\s+`)

// Norm: NFKC, minúsculas y espacios colapsados.
func Norm(s string) string {
	s = strings.ToLower(norm.NFKC.String(s))
	return strings.TrimSpace(spaces.ReplaceAllString(s, " "))
}

// StripAccents quita marcas diacríticas.
func StripAccents(s string) string {
	var sb strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}_#+.%-]+`)
var hasWord = regexp.MustCompile(`[\p{L}\p{N}_]`)

func Words(text string) []string { return wordRe.FindAllString(text, -1) }

func WordCount(text string) int {
	n := 0
	for _, w := range Words(text) {
		if hasWord.MatchString(w) {
			n++
		}
	}
	return n
}

var sentEnd = regexp.MustCompile(`([.!?…])\s+`)
var newlines = regexp.MustCompile(`\n+`)

func Sentences(text string) []string {
	t := sentEnd.ReplaceAllString(strings.TrimSpace(text), "$1\n")
	var out []string
	for _, p := range newlines.Split(t, -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func FindFillers(text, l string) []string {
	t := Norm(text)
	out := []string{}
	for _, p := range fillers[lang(l)] {
		for _, m := range findBounded(p, t) {
			out = append(out, strings.Trim(t[m[0]:m[1]], " ,.…"))
		}
	}
	return out
}

func FillerRate(text, l string) float64 {
	n := WordCount(text)
	if n == 0 {
		return 0
	}
	return 100 * float64(len(FindFillers(text, l))) / float64(n)
}

var numRe = regexp.MustCompile(`\d+(?:[.,]\d+)?\s*%?`)
var digitRe = regexp.MustCompile(`\d+(?:[.,]\d+)?`)
var pctRe = regexp.MustCompile(`\d+\s*%`)

func FindNumbers(text, l string) []string {
	t := Norm(text)
	out := []string{}
	for _, n := range numRe.FindAllString(t, -1) {
		out = append(out, strings.TrimSpace(n))
	}
	for _, w := range numberWords[lang(l)] {
		p := bounded{regexp.MustCompile(regexp.QuoteMeta(w)), true, true}
		for range findBounded(p, t) {
			out = append(out, w)
		}
	}
	return out
}

func DigitNumbers(text string) map[string]bool {
	out := map[string]bool{}
	for _, n := range digitRe.FindAllString(text, -1) {
		out[strings.ReplaceAll(n, ",", ".")] = true
	}
	return out
}

// Star son los marcadores S/T/A/R detectados.
type Star struct {
	S bool `json:"S"`
	T bool `json:"T"`
	A bool `json:"A"`
	R bool `json:"R"`
}

func (s Star) Count() int {
	n := 0
	for _, v := range []bool{s.S, s.T, s.A, s.R} {
		if v {
			n++
		}
	}
	return n
}

func StarParts(text, l string) Star {
	t := Norm(text)
	m := starMarkers[lang(l)]
	has := func(k string) bool {
		for _, ph := range m[k] {
			if containsPhrase(t, ph) {
				return true
			}
		}
		return false
	}
	s := Star{S: has("S"), T: has("T"), A: has("A"), R: has("R")}
	if !s.R && pctRe.MatchString(t) {
		s.R = true
	}
	return s
}

func classifySentence(s, l string) string {
	p := StarParts(s, l)
	switch {
	case p.R:
		return "R"
	case p.A:
		return "A"
	case p.T:
		return "T"
	case p.S:
		return "S"
	}
	return "A"
}

func EstimateSeconds(text, l string) float64 {
	return 60 * float64(WordCount(text)) / wpm[lang(l)]
}

// OneQuestion deja una sola pregunta (la entrevistadora pregunta de a una).
func OneQuestion(text string) string {
	t := strings.TrimSpace(spaces.ReplaceAllString(strings.Trim(strings.TrimSpace(text), `"`), " "))
	t = strings.TrimSpace(t)
	if strings.Count(t, "?") > 1 {
		t = t[:strings.Index(t, "?")+1]
	}
	if len(t) > 300 {
		cut := strings.LastIndex(t[:len(t)-1], ". ")
		if c2 := strings.LastIndex(t, "¿"); c2 > cut {
			cut = c2
		}
		if cut > 0 {
			t = strings.TrimSpace(strings.TrimLeft(t[cut:], ". "))
		}
	}
	return t
}

var (
	spaceBeforePunct = regexp.MustCompile(`\s+([,.;:])(\s|$)`)
	leadingPunct     = regexp.MustCompile(`(^|[.!?]\s+)[,;]\s*`)
	doubleComma      = regexp.MustCompile(`,\s*,`)
	multiSpace       = regexp.MustCompile(`\s{2,}`)
)

// RemoveFillers quita muletillas sin tocar el contenido.
func RemoveFillers(text, l string) string {
	out := text
	for _, p := range fillers[lang(l)] {
		ci := bounded{regexp.MustCompile(`(?i)` + p.re.String()), p.left, p.right}
		ms := findBounded(ci, out)
		for i := len(ms) - 1; i >= 0; i-- {
			out = out[:ms[i][0]] + " " + out[ms[i][1]:]
		}
	}
	out = spaceBeforePunct.ReplaceAllString(out, "$1$2")
	out = leadingPunct.ReplaceAllString(out, "$1")
	out = doubleComma.ReplaceAllString(out, ",")
	out = strings.Trim(multiSpace.ReplaceAllString(out, " "), " ,")
	if out == "" {
		return out
	}
	r := []rune(out)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

var starLabels = map[string]bool{"situacion": true, "tarea": true, "accion": true, "resultado": true,
	"situation": true, "task": true, "action": true, "result": true, "star": true}

// InventedTerms devuelve los términos del candidato "mejorado" que no aparecen
// en lo que dijo el usuario: números, tecnologías y nombres propios nuevos.
func InventedTerms(source, candidate string) []string {
	src := StripAccents(Norm(source))
	srcWords := map[string]bool{}
	for _, w := range Words(src) {
		srcWords[w] = true
	}
	var bad []string
	srcNums := DigitNumbers(source)
	var cand []string
	for n := range DigitNumbers(candidate) {
		if !srcNums[n] {
			cand = append(cand, n)
		}
	}
	sort.Strings(cand)
	bad = append(bad, cand...)
	for _, sent := range Sentences(candidate) {
		toks := Words(sent)
		for i, tok := range toks {
			low := strings.Trim(StripAccents(strings.ToLower(tok)), ".-")
			if low == "" || starLabels[low] || srcWords[low] {
				continue
			}
			first := []rune(tok)
			isTech := TechLexicon[low]
			isProper := i > 0 && unicode.IsUpper(first[0]) && len(first) > 2
			if (isTech || isProper) && !strings.Contains(src, low) {
				bad = append(bad, strings.Trim(tok, ".,;:-"))
			}
		}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, x := range bad {
		k := strings.ToLower(x)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, x)
	}
	return out
}

// DeterministicImprovement reordena las frases del usuario (S-T-A-R si aplica)
// y quita muletillas. No agrega ni una palabra de contenido.
func DeterministicImprovement(answers []string, l string, star bool) string {
	var clean []string
	for _, a := range answers {
		for _, s := range Sentences(RemoveFillers(a, l)) {
			if WordCount(s) < 2 {
				continue
			}
			s = strings.TrimRight(s, " ,")
			if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "!") && !strings.HasSuffix(s, "?") {
				s += "."
			}
			clean = append(clean, s)
		}
	}
	if !star {
		return strings.Join(clean, " ")
	}
	buckets := map[string][]string{}
	for _, s := range clean {
		k := classifySentence(s, l)
		buckets[k] = append(buckets[k], s)
	}
	labels := map[string]string{"S": "Situación", "T": "Tarea", "A": "Acción", "R": "Resultado"}
	if lang(l) == "en" {
		labels = map[string]string{"S": "Situation", "T": "Task", "A": "Action", "R": "Result"}
	}
	var lines []string
	for _, k := range []string{"S", "T", "A", "R"} {
		if v := buckets[k]; len(v) > 0 {
			lines = append(lines, "**"+labels[k]+":** "+strings.Join(v, " "))
		}
	}
	return strings.Join(lines, "\n")
}

// Round redondea a n decimales.
func Round(v float64, n int) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', n, 64), 64)
	return f
}
