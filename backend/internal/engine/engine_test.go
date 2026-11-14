package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/protocol"
)

func cfg() Config {
	return Config{Lang: "es", MaxFollowups: 2, Intro: "Hola", Closing: "Gracias", Questions: []Question{
		{ID: "q1", Text: "¿Recorrido?", Probes: []string{"numbers", "why"}},
		{ID: "q2", Text: "¿Rol actual?"},
	}}
}

func types(outs []Out) []string {
	var t []string
	for _, o := range outs {
		t = append(t, o.Type)
	}
	return t
}

func mustTypes(t *testing.T, outs []Out, want ...string) {
	t.Helper()
	got := types(outs)
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func answer(t *testing.T, e *Engine, text string) ([]Out, bool) {
	t.Helper()
	e.PromptDelivered(0)
	if o := e.EndOfAudio(e.CurrentTurnID(), "audio_end"); o != nil {
		t.Fatalf("end of audio: %v", o)
	}
	return e.Final(Answer{TurnID: e.CurrentTurnID(), Text: text, DurationS: 12.5, Source: "stt"})
}

func TestFullFlowWithTwoFollowupsCap(t *testing.T) {
	e := New(cfg())
	outs := e.Start()
	mustTypes(t, outs, protocol.TQuestion)
	if q := outs[0].Data.(protocol.Question); q.Kind != "intro" || q.TurnID != "t-0" || e.State() != Intro {
		t.Fatalf("%+v", q)
	}
	outs = e.PromptDelivered(1)
	mustTypes(t, outs, protocol.TQuestion)
	q := outs[0].Data.(protocol.Question)
	if q.TurnID != "t-1" || q.Index != 1 || q.Total != 2 || q.UtteranceID != "u-2" || e.State() != Asking {
		t.Fatalf("%+v %s", q, e.State())
	}
	if e.AcceptAudio(1) {
		t.Fatal("no se acepta audio en asking")
	}
	e.PromptDelivered(2)
	if !e.AcceptAudio(1) || e.AcceptAudio(2) {
		t.Fatal("audio solo del turno vigente en listening")
	}
	_, need := answer(t, e, "Trabajé en backend.")
	if !need || e.State() != Processing {
		t.Fatal("debe pedir decisión")
	}
	outs = e.ApplyDecision(Decision{Followup: true, Text: "¿Cuántos años?", Probe: "numbers"})
	mustTypes(t, outs, protocol.TTurnEnd, protocol.TFollowup)
	te := outs[0].Data.(protocol.TurnEnd)
	f := outs[1].Data.(protocol.Followup)
	if te.Next != "followup" || te.FollowupsUsed != 0 || f.FollowupIndex != 1 || f.TurnID != "t-2" || f.MaxFollowups != 2 {
		t.Fatalf("%+v %+v", te, f)
	}
	_, need = answer(t, e, "Tres años.")
	if !need {
		t.Fatal("segunda decisión")
	}
	outs = e.ApplyDecision(Decision{Followup: true, Text: "¿Por qué?", Probe: "why"})
	if f := outs[1].Data.(protocol.Followup); f.FollowupIndex != 2 {
		t.Fatalf("%+v", f)
	}
	// tras 2 repreguntas el motor no pide más decisiones y avanza solo
	outs, need = answer(t, e, "Porque sí.")
	if need {
		t.Fatal("no debe haber tercera repregunta")
	}
	mustTypes(t, outs, protocol.TTranscriptFinal, protocol.TTurnEnd, protocol.TQuestion)
	if te := outs[1].Data.(protocol.TurnEnd); te.FollowupsUsed != 2 || te.Next != "question" {
		t.Fatalf("%+v", te)
	}
	// aun si el LLM insiste, ApplyDecision respeta el tope
	_, need = answer(t, e, "Soy líder técnico.")
	outs = e.ApplyDecision(Decision{Followup: false})
	mustTypes(t, outs, protocol.TTurnEnd, protocol.TQuestion, protocol.TSessionEnd)
	if outs[1].Data.(protocol.Question).Kind != "closing" || e.State() != Ended {
		t.Fatal("cierre")
	}
	s := outs[2].Data.(protocol.SessionEnd)
	if s.Reason != "completed" || s.Summary.QuestionsAsked != 2 || s.Summary.QuestionsAnswered != 2 || s.Summary.FollowupsAsked != 2 || s.Summary.TotalAnswerMs != 50000 {
		t.Fatalf("%+v", s)
	}
	if len(e.Turns) != 4 || e.Turns[1].Kind != "followup" || *e.Turns[1].Probe != "numbers" || e.Turns[1].FollowupIndex != 1 {
		t.Fatalf("%+v", e.Turns)
	}
}

func TestMaxFollowupsClampAndZero(t *testing.T) {
	c := cfg()
	c.MaxFollowups = 7
	if New(c).MaxFollowups() != 2 {
		t.Fatal("tope 2")
	}
	c.MaxFollowups = 0
	c.Intro = ""
	e := New(c)
	e.Start()
	outs, need := answer(t, e, "algo")
	if need || outs[1].Type != protocol.TTurnEnd {
		t.Fatal("sin repreguntas con max 0")
	}
}

func TestDuplicateFollowupIsIgnored(t *testing.T) {
	c := cfg()
	c.Intro = ""
	e := New(c)
	e.Start()
	answer(t, e, "x")
	outs := e.ApplyDecision(Decision{Followup: true, Text: "¿Recorrido?"})
	if outs[0].Data.(protocol.TurnEnd).Next != "question" {
		t.Fatal("repregunta igual a la pregunta no se hace")
	}
}

func TestNoAudioRepeatsThenGivesUp(t *testing.T) {
	c := cfg()
	c.Intro = ""
	e := New(c)
	e.Start()
	for i := 0; i < MaxNoAudioRepeats; i++ {
		outs, need := answer(t, e, "  ")
		mustTypes(t, outs, protocol.TError, protocol.TTurnEnd, protocol.TQuestion)
		if need || outs[2].Data.(protocol.Question).Kind != "repeat" || outs[1].Data.(protocol.TurnEnd).Next != "repeat" {
			t.Fatalf("%+v", outs)
		}
	}
	outs, _ := answer(t, e, "")
	mustTypes(t, outs, protocol.TTurnEnd, protocol.TQuestion)
	if outs[1].Data.(protocol.Question).QuestionID != "q2" || e.Turns[0].Answer != "" {
		t.Fatal("tras 2 silencios registra vacío y avanza")
	}
}

func TestSkipRepeatPauseResumeEnd(t *testing.T) {
	c := cfg()
	c.Intro = ""
	e := New(c)
	e.Start()
	if o := e.Resume(); o[0].Type != protocol.TError {
		t.Fatal("resume sin pausa = conflicto")
	}
	outs := e.Repeat("t-1")
	if outs[0].Data.(protocol.Question).Kind != "repeat" || outs[0].Data.(protocol.Question).TurnID != "t-1" {
		t.Fatal("repeat mismo turno")
	}
	e.PromptDelivered(0)
	outs = e.Pause()
	if outs[0].Type != protocol.TPause || e.State() != Paused {
		t.Fatal("pause")
	}
	if o := e.Skip("t-1"); o[0].Type != protocol.TError {
		t.Fatal("skip en pausa = conflicto")
	}
	outs = e.Resume()
	mustTypes(t, outs, protocol.TResume, protocol.TQuestion)
	if e.State() != Asking {
		t.Fatal("resume vuelve a asking")
	}
	if o := e.Skip("t-9"); o[0].Type != protocol.TError {
		t.Fatal("skip de otro turno")
	}
	outs = e.Skip("t-1")
	mustTypes(t, outs, protocol.TTurnEnd, protocol.TQuestion)
	if !e.Turns[0].Skipped || outs[0].Data.(protocol.TurnEnd).Reason != "skip" {
		t.Fatal("skip registrado")
	}
	// resultado STT tardío del turno saltado se descarta
	if o, _ := e.Final(Answer{TurnID: "t-1", Text: "tarde"}); o != nil {
		t.Fatal("resultado viejo")
	}
	outs = e.End("user")
	s := outs[0].Data.(protocol.SessionEnd)
	if s.Reason != "user" || s.Summary.Skipped != 1 || e.State() != Ended {
		t.Fatalf("%+v", s)
	}
	if e.End("user") != nil {
		t.Fatal("end idempotente")
	}
}

func TestThinkPauseAndText(t *testing.T) {
	c := cfg()
	c.Intro = ""
	c.ThinkPauseMs = 2000
	e := New(c)
	outs := e.Start()
	p := outs[0].Data.(protocol.Pause)
	if p.Reason != "think_time" || *p.ResumeAfterMs != 2000 {
		t.Fatal("think pause")
	}
	outs = e.ThinkElapsed()
	mustTypes(t, outs, protocol.TResume, protocol.TQuestion)
	if o := e.AcceptText("t-1"); o != nil {
		t.Fatal(o)
	}
	outs, need := e.Final(Answer{TurnID: "t-1", Text: "Trabajé tres años", DurationS: 1.4, Estimated: true, Source: "text"})
	if !need || outs[0].Data.(protocol.TranscriptFinal).Source != "text" || !e.Turns[0].DurationEstimated {
		t.Fatal("texto")
	}
	if o := e.AcceptText("t-1"); o[0].Type != protocol.TError {
		t.Fatal("texto en processing = conflicto")
	}
}

func TestStateConflicts(t *testing.T) {
	e := New(cfg())
	e.Start()
	if o := e.EndOfAudio("t-1", "audio_end"); o[0].Type != protocol.TError {
		t.Fatal("audio.end en intro")
	}
	if o := e.Skip(""); o[0].Type != protocol.TError {
		t.Fatal("skip en intro")
	}
	if o := e.Pause(); o[0].Type != protocol.TError {
		t.Fatal("pause en intro")
	}
	if e.PromptDelivered(99) != nil || e.State() != Intro {
		t.Fatal("tts.played viejo")
	}
}

func TestRetryAfterSTTFailure(t *testing.T) {
	c := cfg()
	c.Intro = ""
	e := New(c)
	e.Start()
	e.PromptDelivered(1)
	e.EndOfAudio("t-1", "audio_end")
	if out := e.Retry("t-9"); out != nil {
		t.Fatal("turno equivocado")
	}
	out := e.Retry("t-1")
	if len(out) != 2 || out[0].Data.(protocol.TurnEnd).Next != "repeat" || out[1].Data.(protocol.Question).Kind != "repeat" || e.CurrentTurnID() != "t-2" {
		t.Fatalf("%+v", out)
	}
	if e.State() != Asking || len(e.Turns) != 0 {
		t.Fatal("no registra turno")
	}
}

func TestRepeatFollowupKeepsTurnAndCount(t *testing.T) {
	e := New(cfg())
	e.Start()
	e.PromptDelivered(0) // intro
	e.PromptDelivered(0)
	e.EndOfAudio("t-1", "audio_end")
	if _, need := e.Final(Answer{TurnID: "t-1", Text: "trabajé en backend", Source: "stt"}); !need {
		t.Fatal("debía decidir")
	}
	outs := e.ApplyDecision(Decision{Followup: true, Text: "¿Cuántos años?", Probe: "numbers"})
	f := outs[1].Data.(protocol.Followup)
	rep := e.Repeat(f.TurnID)
	if len(rep) != 1 {
		t.Fatalf("sin turn.end: %+v", rep)
	}
	r := rep[0].Data.(protocol.Followup)
	if !r.Repeat || r.TurnID != f.TurnID || r.QuestionID != f.QuestionID || r.FollowupIndex != 1 || r.UtteranceID == f.UtteranceID {
		t.Fatalf("%+v", r)
	}
	q := e.Repeat("")
	if q[0].Data.(protocol.Followup).FollowupIndex != 1 {
		t.Fatal("no consume repregunta")
	}
}

// stt_fillers distingue ausente (se omite) de vacío ([]), y sobrevive al JSON con
// el que se persiste el turno en SQLite y en Postgres.
func TestSTTFillersAbsentVsEmptyRoundTrip(t *testing.T) {
	for _, c := range []struct {
		in      []string
		want    string
		present bool
	}{{nil, "", false}, {[]string{}, `"stt_fillers":[]`, true}, {[]string{"eh"}, `"stt_fillers":["eh"]`, true}} {
		tu := Turn{ID: "t-1", STTFillers: sttFillers(c.in)}
		b, err := json.Marshal(tu)
		if err != nil {
			t.Fatal(err)
		}
		if c.present != strings.Contains(string(b), `"stt_fillers"`) || (c.want != "" && !strings.Contains(string(b), c.want)) {
			t.Fatalf("%v → %s", c.in, b)
		}
		var back Turn
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		if (back.STTFillers != nil) != c.present || (c.present && len(*back.STTFillers) != len(c.in)) {
			t.Fatalf("ida y vuelta %v → %v", c.in, back.STTFillers)
		}
	}
}
