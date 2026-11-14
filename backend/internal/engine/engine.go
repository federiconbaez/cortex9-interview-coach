// Package engine es el motor de la sesión: una máquina de estados pura (sin
// red ni relojes) que decide qué decir y cuándo. La capa live le pasa eventos
// (respuesta transcrita, controles, fin de TTS) y envía los mensajes que
// devuelve. Las decisiones de repregunta las toma el LLM afuera y vuelven con
// ApplyDecision; el motor impone el tope de 2 repreguntas por pregunta.
package engine

import (
	"fmt"
	"strings"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/analysis"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/protocol"
)

type State string

const (
	Ready      State = "ready"
	Intro      State = "intro"
	Asking     State = "asking"
	Listening  State = "listening"
	Processing State = "processing"
	Paused     State = "paused"
	Closing    State = "closing"
	Ended      State = "ended"
)

// MaxNoAudioRepeats: tras 2 repeticiones por silencio se registra la respuesta vacía.
const MaxNoAudioRepeats = 2

// Question es una pregunta ya resuelta al idioma de la sesión.
type Question struct {
	ID             string
	Text           string
	Probes         []string
	Keywords       []string
	Star           bool
	ExpectsNumbers bool
}

type Config struct {
	Lang         string
	Questions    []Question
	MaxFollowups int
	Intro        string
	Closing      string
	ThinkPauseMs int
}

// Turn es un turno registrado (lo que se persiste y se puntúa).
type Turn struct {
	ID                string   `json:"id"`
	QuestionID        string   `json:"question_id"`
	Kind              string   `json:"kind"`
	FollowupIndex     int      `json:"followup_index"`
	Probe             *string  `json:"probe"`
	Question          string   `json:"question"`
	Answer            string   `json:"answer"`
	DurationS         float64  `json:"duration_s"`
	DurationEstimated bool     `json:"duration_estimated"`
	LowConfidence     []string `json:"low_confidence"`
	Skipped           bool     `json:"skipped"`
	// Extensiones (si /speech las mandó): muletillas con contexto de pausas y pausas largas.
	// STTFillers es puntero para distinguir "ausente" (nil, se omite) de "vacío"
	// (/speech informó cero muletillas, se serializa como []). El turno se guarda
	// como JSON, así que la diferencia se conserva igual en SQLite y en Postgres.
	STTFillers *[]string   `json:"stt_fillers,omitempty"`
	Pauses     *PauseStats `json:"pauses,omitempty"`
}

// PauseStats resume las pausas ≥ umbral que detectó /speech.
type PauseStats struct {
	Count    int     `json:"cantidad"`
	TotalS   float64 `json:"total_s"`
	LongestS float64 `json:"mas_larga_s"`
}

// Out es un mensaje servidor→cliente sin sobre.
type Out struct {
	Type string
	Data any
}

type prompt struct {
	turnNo        int
	uttNo         int
	kind          string // main | followup | intro | closing
	text          string
	followupIndex int
	probe         string
}

// Engine no es seguro para uso concurrente: lo maneja una sola goroutine.
type Engine struct {
	cfg   Config
	state State

	pausedFrom  State
	pauseReason string

	qIdx    int
	turnNo  int
	uttNo   int
	cur     prompt
	answerd bool

	followupsUsed int
	askedProbes   []string
	noAudio       int
	blockAnswers  []string
	blockHistory  []string
	lastEndReason string

	Turns          []Turn
	questionsAsked int
	followupsAsked int
	skipped        int
	totalAnswerMs  int
	answeredQ      map[string]bool
	endReason      string
}

func New(cfg Config) *Engine {
	if cfg.MaxFollowups < 0 {
		cfg.MaxFollowups = 0
	}
	if cfg.MaxFollowups > 2 {
		cfg.MaxFollowups = 2
	}
	return &Engine{cfg: cfg, state: Ready, qIdx: -1, answeredQ: map[string]bool{}}
}

func (e *Engine) State() State          { return e.state }
func (e *Engine) EndReason() string     { return e.endReason }
func (e *Engine) QuestionIndex() int    { return e.qIdx + 1 }
func (e *Engine) QuestionCount() int    { return len(e.cfg.Questions) }
func (e *Engine) CurrentTurnNo() int    { return e.cur.turnNo }
func (e *Engine) CurrentTurnID() string { return turnID(e.cur.turnNo) }
func (e *Engine) MaxFollowups() int     { return e.cfg.MaxFollowups }

func turnID(n int) string { return fmt.Sprintf("t-%d", n) }
func uttID(n int) string  { return fmt.Sprintf("u-%d", n) }

// UtteranceNo devuelve el número de la intervención vigente.
func (e *Engine) UtteranceNo() int { return e.cur.uttNo }

func errOut(code, msg string, retry bool) Out {
	return Out{protocol.TError, protocol.Error{Code: code, Message: msg, Retryable: retry}}
}

func (e *Engine) conflict(what string) []Out {
	return []Out{errOut(protocol.ErrStateConflict, fmt.Sprintf("%s no aplica en estado %s", what, e.state), false)}
}

func (e *Engine) question() Question { return e.cfg.Questions[e.qIdx] }

// promptOut arma question/followup para el prompt vigente.
func (e *Engine) promptOut(kindOverride string) Out {
	e.uttNo++
	e.cur.uttNo = e.uttNo
	switch e.cur.kind {
	case "followup":
		return Out{protocol.TFollowup, protocol.Followup{TurnID: turnID(e.cur.turnNo), QuestionID: e.question().ID,
			FollowupIndex: e.cur.followupIndex, MaxFollowups: e.cfg.MaxFollowups, Probe: e.cur.probe,
			Text: e.cur.text, UtteranceID: uttID(e.uttNo), Repeat: kindOverride == "repeat"}}
	case "intro", "closing":
		idx := 0
		if e.cur.kind == "closing" {
			idx = len(e.cfg.Questions)
		}
		return Out{protocol.TQuestion, protocol.Question{TurnID: "t-0", QuestionID: e.cur.kind, Index: idx,
			Total: len(e.cfg.Questions), Text: e.cur.text, Lang: e.cfg.Lang, UtteranceID: uttID(e.uttNo), Kind: e.cur.kind, Repeat: kindOverride == "repeat"}}
	}
	kind := "main"
	if kindOverride != "" {
		kind = kindOverride
	}
	q := e.question()
	return Out{protocol.TQuestion, protocol.Question{TurnID: turnID(e.cur.turnNo), QuestionID: q.ID, Index: e.qIdx + 1,
		Total: len(e.cfg.Questions), Text: e.cur.text, Lang: e.cfg.Lang, UtteranceID: uttID(e.uttNo), Kind: kind, Repeat: kindOverride == "repeat"}}
}

// Start arranca: intro (si hay) o primera pregunta.
func (e *Engine) Start() []Out {
	if e.state != Ready {
		return nil
	}
	if strings.TrimSpace(e.cfg.Intro) != "" {
		e.state = Intro
		e.cur = prompt{kind: "intro", text: e.cfg.Intro}
		return []Out{e.promptOut("")}
	}
	return e.nextQuestion()
}

func (e *Engine) nextQuestion() []Out {
	if e.qIdx+1 >= len(e.cfg.Questions) {
		return e.finish("completed")
	}
	e.qIdx++
	e.followupsUsed, e.askedProbes, e.noAudio = 0, nil, 0
	e.blockAnswers, e.blockHistory = nil, nil
	if e.cfg.ThinkPauseMs > 0 {
		e.state, e.pauseReason, e.pausedFrom = Paused, "think_time", Asking
		ms := e.cfg.ThinkPauseMs
		return []Out{{protocol.TPause, protocol.Pause{Reason: "think_time", ResumeAfterMs: &ms, MaxPauseMs: protocol.MaxPauseMs}}}
	}
	return e.askMain("")
}

func (e *Engine) askMain(kind string) []Out {
	e.turnNo++
	e.cur = prompt{turnNo: e.turnNo, kind: "main", text: e.question().Text}
	e.answerd = false
	e.state = Asking
	if kind == "" {
		e.questionsAsked++
	}
	return []Out{e.promptOut(kind)}
}

// ThinkElapsed termina la pausa para pensar.
func (e *Engine) ThinkElapsed() []Out {
	if e.state != Paused || e.pauseReason != "think_time" {
		return nil
	}
	e.pauseReason = ""
	out := []Out{{protocol.TResume, protocol.ResumeMsg{State: string(Asking)}}}
	return append(out, e.askMain("")...)
}

// PromptDelivered: el cliente terminó de reproducir (tts.played), venció el
// temporizador de TTS o no hay TTS.
func (e *Engine) PromptDelivered(utt int) []Out {
	if utt != 0 && utt != e.cur.uttNo {
		return nil // de una intervención vieja
	}
	switch e.state {
	case Intro:
		return e.nextQuestion()
	case Asking:
		e.state = Listening
	}
	return nil
}

// AcceptAudio dice si se acepta audio para ese turno ahora.
func (e *Engine) AcceptAudio(turnNo int) bool {
	return e.state == Listening && turnNo == e.cur.turnNo
}

// EndOfAudio pasa a processing (audio.end o max_turn_ms).
func (e *Engine) EndOfAudio(tid, reason string) []Out {
	if e.state != Listening {
		return e.conflict("audio.end")
	}
	if tid != turnID(e.cur.turnNo) {
		return []Out{errOut(protocol.ErrStateConflict, "turn_id no es el turno vigente", false)}
	}
	e.state = Processing
	e.lastEndReason = reason
	return nil
}

// AcceptText dice si se acepta answer.text ahora (asking o listening).
func (e *Engine) AcceptText(tid string) []Out {
	if e.state != Asking && e.state != Listening {
		return e.conflict("answer.text")
	}
	if tid != turnID(e.cur.turnNo) || e.cur.kind == "intro" || e.cur.kind == "closing" {
		return []Out{errOut(protocol.ErrStateConflict, "turn_id no es el turno vigente", false)}
	}
	e.state = Processing
	e.lastEndReason = "text"
	return nil
}

// Answer es el resultado final del turno (STT o texto).
type Answer struct {
	TurnID        string
	Text          string
	DurationS     float64
	Estimated     bool
	Confidence    *float64
	LowConfidence []string
	Source        string      // stt | text
	Fillers       []string    // nil = /speech no las detectó
	Pauses        *PauseStats // nil = sin datos (modo texto)
}

// Final registra la respuesta. needDecision=true si hay que consultar al LLM.
func (e *Engine) Final(a Answer) (outs []Out, needDecision bool) {
	if e.state != Processing || a.TurnID != turnID(e.cur.turnNo) {
		return nil, false // resultado viejo (turno saltado o terminado)
	}
	text := strings.TrimSpace(a.Text)
	if text == "" {
		e.noAudio++
		if e.noAudio <= MaxNoAudioRepeats {
			outs = append(outs, errOut(protocol.ErrNoAudio, "No te escuché bien. ¿Podés repetir tu respuesta?", true),
				Out{protocol.TTurnEnd, e.turnEnd("no_audio", "repeat")})
			return append(outs, e.reask()...), false
		}
	}
	if a.LowConfidence == nil {
		a.LowConfidence = []string{}
	}
	if text != "" {
		outs = append(outs, Out{protocol.TTranscriptFinal, protocol.TranscriptFinal{TurnID: a.TurnID, Text: text,
			DurationMs: int(a.DurationS * 1000), Confidence: a.Confidence, LowConfidence: a.LowConfidence, Source: a.Source}})
	}
	e.record(Turn{Answer: text, DurationS: analysis.Round(a.DurationS, 3), DurationEstimated: a.Estimated, LowConfidence: a.LowConfidence,
		STTFillers: sttFillers(a.Fillers), Pauses: a.Pauses})
	if text == "" {
		return append(outs, e.advance(e.lastEndReason)...), false
	}
	e.blockAnswers = append(e.blockAnswers, text)
	e.blockHistory = append(e.blockHistory, analysis.Norm(e.cur.text))
	if e.followupsUsed >= e.cfg.MaxFollowups {
		return append(outs, e.advance(e.lastEndReason)...), false
	}
	return outs, true
}

func (e *Engine) record(t Turn) {
	t.ID = turnID(e.cur.turnNo)
	t.QuestionID = e.question().ID
	t.Kind = e.cur.kind
	t.Question = e.cur.text
	if e.cur.kind == "followup" {
		t.FollowupIndex = e.cur.followupIndex
		p := e.cur.probe
		t.Probe = &p
	}
	if t.LowConfidence == nil {
		t.LowConfidence = []string{}
	}
	e.Turns = append(e.Turns, t)
	e.answerd = true
	if t.Answer != "" {
		e.answeredQ[t.QuestionID] = true
		e.totalAnswerMs += int(t.DurationS * 1000)
	}
}

// Retry descarta el turno en proceso (el STT falló tras su reintento) y pide
// repetir la respuesta con un turno nuevo, sin consumir repreguntas.
func (e *Engine) Retry(tid string) []Out {
	if e.state != Processing || tid != turnID(e.cur.turnNo) {
		return nil
	}
	outs := []Out{{protocol.TTurnEnd, e.turnEnd(e.lastEndReason, "repeat")}}
	return append(outs, e.reask()...)
}

// LastTurn devuelve el último turno registrado.
func (e *Engine) LastTurn() *Turn {
	if len(e.Turns) == 0 {
		return nil
	}
	return &e.Turns[len(e.Turns)-1]
}

// reask repite el prompt vigente con un turno nuevo (tras silencio).
func (e *Engine) reask() []Out {
	e.turnNo++
	e.cur.turnNo = e.turnNo
	e.state = Asking
	return []Out{e.promptOut("repeat")}
}

// DecisionInput son los datos para el LLM.
type DecisionInput struct {
	Question      Question
	Answers       []string
	AskedProbes   []string
	FollowupsDone int
	MaxFollowups  int
	History       [][2]string
}

func (e *Engine) DecisionInput() DecisionInput {
	var hist [][2]string
	for _, t := range e.Turns {
		if t.QuestionID == e.question().ID {
			hist = append(hist, [2]string{t.Question, t.Answer})
		}
	}
	return DecisionInput{Question: e.question(), Answers: append([]string(nil), e.blockAnswers...),
		AskedProbes: append([]string(nil), e.askedProbes...), FollowupsDone: e.followupsUsed,
		MaxFollowups: e.cfg.MaxFollowups, History: hist}
}

// Decision es lo que resolvió el LLM (o el respaldo local).
type Decision struct {
	Followup bool
	Text     string
	Probe    string
}

var validProbe = map[string]bool{"numbers": true, "example": true, "why": true, "ownership": true, "result": true, "other": true}

// ApplyDecision aplica la decisión respetando el tope de repreguntas.
func (e *Engine) ApplyDecision(d Decision) []Out {
	if e.state != Processing {
		return nil
	}
	q := analysis.OneQuestion(d.Text)
	if w := strings.Fields(q); len(w) > 45 {
		q = strings.TrimRight(strings.Join(w[:45], " "), ",;") + "?"
	}
	dup := false
	for _, h := range e.blockHistory {
		if h == analysis.Norm(q) {
			dup = true
		}
	}
	if !d.Followup || q == "" || dup || e.followupsUsed >= e.cfg.MaxFollowups {
		return e.advance(e.lastEndReason)
	}
	probe := d.Probe
	if !validProbe[probe] {
		probe = "other"
	}
	outs := []Out{{protocol.TTurnEnd, e.turnEnd(e.lastEndReason, "followup")}}
	e.followupsUsed++
	e.followupsAsked++
	e.askedProbes = append(e.askedProbes, probe)
	e.turnNo++
	e.cur = prompt{turnNo: e.turnNo, kind: "followup", text: q, followupIndex: e.followupsUsed, probe: probe}
	e.noAudio = 0
	e.answerd = false
	e.state = Asking
	return append(outs, e.promptOut(""))
}

func (e *Engine) turnEnd(reason, next string) protocol.TurnEnd {
	return protocol.TurnEnd{TurnID: turnID(e.cur.turnNo), Reason: reason, Next: next,
		FollowupsUsed: e.followupsUsed, MaxFollowups: e.cfg.MaxFollowups}
}

func (e *Engine) advance(reason string) []Out {
	next := "question"
	if e.qIdx+1 >= len(e.cfg.Questions) {
		next = "end"
	}
	outs := []Out{{protocol.TTurnEnd, e.turnEnd(reason, next)}}
	return append(outs, e.nextQuestion()...)
}

// Skip salta la pregunta vigente y sus repreguntas.
func (e *Engine) Skip(tid string) []Out {
	if e.state != Asking && e.state != Listening && e.state != Processing {
		return e.conflict("control.skip")
	}
	if e.cur.kind == "intro" {
		return e.conflict("control.skip")
	}
	if tid != "" && tid != turnID(e.cur.turnNo) {
		return []Out{errOut(protocol.ErrStateConflict, "turn_id no es el turno vigente", false)}
	}
	if !e.answerd {
		e.record(Turn{Skipped: true})
	}
	e.skipped++
	return e.advance("skip")
}

// Repeat repite la pregunta vigente (mismo turno, nueva intervención).
func (e *Engine) Repeat(tid string) []Out {
	if e.state != Asking && e.state != Listening {
		return e.conflict("control.repeat")
	}
	if tid != "" && tid != turnID(e.cur.turnNo) {
		return []Out{errOut(protocol.ErrStateConflict, "turn_id no es el turno vigente", false)}
	}
	e.state = Asking
	return []Out{e.promptOut("repeat")}
}

// Pause pausa (descarta el audio parcial del turno, eso lo hace live).
func (e *Engine) Pause() []Out {
	if e.state != Asking && e.state != Listening {
		return e.conflict("control.pause")
	}
	e.pausedFrom, e.pauseReason, e.state = e.state, "user", Paused
	return []Out{{protocol.TPause, protocol.Pause{Reason: "user", MaxPauseMs: protocol.MaxPauseMs}}}
}

// Resume reanuda y repite el prompt vigente.
func (e *Engine) Resume() []Out {
	if e.state != Paused {
		return e.conflict("control.resume")
	}
	if e.pauseReason == "think_time" {
		return e.ThinkElapsed()
	}
	e.pauseReason = ""
	e.state = Asking
	return []Out{{protocol.TResume, protocol.ResumeMsg{State: string(Asking)}}, e.promptOut("repeat")}
}

func (e *Engine) finish(reason string) []Out {
	var outs []Out
	if reason == "completed" && strings.TrimSpace(e.cfg.Closing) != "" {
		e.cur = prompt{kind: "closing", text: e.cfg.Closing}
		outs = append(outs, e.promptOut(""))
	}
	e.state = Closing
	return append(outs, e.end(reason))
}

func (e *Engine) end(reason string) Out {
	e.state = Ended
	e.endReason = reason
	return Out{protocol.TSessionEnd, protocol.SessionEnd{Reason: reason, Summary: e.Summary()}}
}

// End termina por control.end, timeout, desconexión o error.
func (e *Engine) End(reason string) []Out {
	if e.state == Ended {
		return nil
	}
	e.state = Closing
	return []Out{e.end(reason)}
}

// Summary es el resumen para session.end.
func (e *Engine) Summary() protocol.Summary {
	return protocol.Summary{QuestionsAsked: e.questionsAsked, QuestionsAnswered: len(e.answeredQ),
		FollowupsAsked: e.followupsAsked, Skipped: e.skipped, TotalAnswerMs: e.totalAnswerMs, ReportStatus: "scoring"}
}

// CurrentPrompt re-emite el prompt vigente (para RESUME_GAP).
func (e *Engine) CurrentPrompt() []Out {
	switch e.state {
	case Intro:
		return []Out{e.promptOut("repeat")}
	case Asking, Listening:
		e.state = Asking
		return []Out{e.promptOut("repeat")}
	}
	return nil
}

// sttFillers conserva la diferencia entre nil (/speech no mandó la extensión) y
// lista vacía (/speech informó cero muletillas).
func sttFillers(f []string) *[]string {
	if f == nil {
		return nil
	}
	c := append([]string{}, f...)
	return &c
}
