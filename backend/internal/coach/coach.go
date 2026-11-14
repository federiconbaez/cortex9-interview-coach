// Package coach tiene la lógica de aplicación compartida por la API REST y el
// WebSocket: dependencias, creación de sesiones, cola de scoring, caché de TTS
// y recuperación tras reinicio.
package coach

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/config"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/engine"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/llm"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/metrics"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/profile"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/scenario"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/scoring"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/speech"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/store"
)

// Estados REST de la sesión.
const (
	StCreated   = "created"
	StLive      = "live"
	StDetached  = "detached"
	StEnded     = "ended"
	StScoring   = "scoring"
	StCompleted = "completed"
	StFailed    = "failed"
)

// Service agrupa las dependencias.
type Service struct {
	Cfg     *config.Config
	Store   *store.Store
	LLM     *llm.Registry
	Speech  *speech.Client
	M       *metrics.M
	Log     *slog.Logger
	Builtin map[string]scenario.Builtin
	TTS     *TTSCache
	Now     func() time.Time

	scoreQ    chan string
	scoreWG   sync.WaitGroup
	stopScore context.CancelFunc
	scoreCtx  context.Context
	pending   sync.Map // sesiones en cola o en curso (evita duplicados)
	profMu    sync.Mutex
}

func New(cfg *config.Config, st *store.Store, reg *llm.Registry, sp *speech.Client, m *metrics.M, log *slog.Logger) (*Service, error) {
	b, err := scenario.LoadBuiltin()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{Cfg: cfg, Store: st, LLM: reg, Speech: sp, M: m, Log: log, Builtin: b, TTS: NewTTSCache(time.Hour, 64<<20),
		Now: time.Now, scoreQ: make(chan string, 1024), scoreCtx: ctx, stopScore: cancel}, nil
}

// SessionConfig es la copia congelada de lo necesario para correr y puntuar
// la sesión (se guarda en sessions.config).
type SessionConfig struct {
	ScenarioID     *string           `json:"scenario_id"`
	Questions      []engine.Question `json:"questions"`
	Style          string            `json:"style"`
	Intro          string            `json:"intro"`
	Closing        string            `json:"closing"`
	Ideal          [2]float64        `json:"ideal"`
	FollowupIdeal  [2]float64        `json:"followup_ideal"`
	ThinkPauseMs   int               `json:"think_pause_ms"`
	LearnProfile   bool              `json:"learn_profile"`
	SaveAudio      bool              `json:"save_audio"`
	Voice          string            `json:"voice,omitempty"`
	LLMModel       string            `json:"llm_model,omitempty"`
	ProfileChanges []string          `json:"profile_changes,omitempty"`
	Summary        *SummarySnapshot  `json:"summary,omitempty"`
}

type SummarySnapshot struct {
	QuestionsAsked int `json:"questions_asked"`
	FollowupsAsked int `json:"followups_asked"`
	Skipped        int `json:"skipped"`
	TotalAnswerMs  int `json:"total_answer_ms"`
}

func ParseConfig(raw json.RawMessage) (*SessionConfig, error) {
	var c SessionConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// CreateRequest es el cuerpo de POST /v1/sessions.
type CreateRequest struct {
	ScenarioID   *string `json:"scenario_id"`
	Lang         string  `json:"lang"`
	Mode         string  `json:"mode"`
	Input        *string `json:"input"`
	LLMProvider  *string `json:"llm_provider"`
	LLMModel     *string `json:"llm_model"`
	NumQuestions *int    `json:"num_questions"`
	Shuffle      bool    `json:"shuffle"`
	MaxFollowups *int    `json:"max_followups"`
	ThinkPauseMs *int    `json:"think_pause_ms"`
	LearnProfile bool    `json:"learn_profile"`
	SaveAudio    bool    `json:"save_audio"`
	Voice        *string `json:"voice"`
}

// FieldError y errores tipados que la API traduce a problem+json.
type FieldError = scenario.FieldError

type ValidationError struct{ Errors []FieldError }

func (e *ValidationError) Error() string {
	var b []string
	for _, f := range e.Errors {
		b = append(b, f.Pointer+": "+f.Message)
	}
	return strings.Join(b, "; ")
}

var (
	ErrNotFound = errors.New("no encontrado")
	ErrConflict = errors.New("conflicto")
)

// Scenario busca un escenario incluido o propio.
func (s *Service) Scenario(ctx context.Context, id string) (*scenario.Scenario, []byte, string, error) {
	if b, ok := s.Builtin[id]; ok {
		cp := *b.Scenario
		return &cp, b.YAML, ETag(b.YAML), nil
	}
	row, err := s.Store.GetScenario(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, "", ErrNotFound
	}
	if err != nil {
		return nil, nil, "", err
	}
	sc, err := scenario.ParseJSON([]byte(row.JSON))
	if err != nil {
		return nil, nil, "", err
	}
	u := row.UpdatedAt
	sc.UpdatedAt = &u
	return sc, []byte(row.YAML), row.ETag, nil
}

// Scenarios devuelve todos (incluidos + propios) ordenados por id.
func (s *Service) Scenarios(ctx context.Context) ([]*scenario.Scenario, error) {
	var out []*scenario.Scenario
	for _, b := range s.Builtin {
		cp := *b.Scenario
		out = append(out, &cp)
	}
	rows, err := s.Store.ListScenarios(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		sc, err := scenario.ParseJSON([]byte(r.JSON))
		if err != nil {
			continue
		}
		u := r.UpdatedAt
		sc.UpdatedAt = &u
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ETag fuerte a partir del contenido.
func ETag(b []byte) string {
	h := sha256.Sum256(b)
	return `"` + hex.EncodeToString(h[:12]) + `"`
}

// Profile lee el perfil (nil si no existe).
func (s *Service) Profile(ctx context.Context) (*profile.Profile, string, error) {
	data, etag, err := s.Store.GetProfile(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var p profile.Profile
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return nil, "", err
	}
	return &p, etag, nil
}

// SaveProfile guarda el perfil y devuelve el ETag nuevo.
func (s *Service) SaveProfile(ctx context.Context, p *profile.Profile, ifMatch string, mustCreate bool) (string, error) {
	now := s.Now().UTC().Truncate(time.Second)
	p.UpdatedAt = &now
	if p.Version == 0 {
		p.Version = 1
	}
	b, _ := json.Marshal(p)
	etag := ETag(b)
	return etag, s.Store.PutProfile(ctx, string(b), etag, ifMatch, mustCreate, now)
}

var story = map[string]string{"es": "Modo historia: completar tu perfil", "en": "Story mode: complete your profile"}

// CreateSession valida y crea una sesión (estado created).
func (s *Service) CreateSession(ctx context.Context, r CreateRequest) (*store.Session, error) {
	var fe []FieldError
	bad := func(p, m string) { fe = append(fe, FieldError{Pointer: p, Message: m}) }
	if r.Lang != "es" && r.Lang != "en" {
		bad("/lang", "debe ser es o en")
	}
	if r.Mode != "practice" && r.Mode != "story" {
		bad("/mode", "debe ser practice o story")
	}
	input := "voice"
	if r.Input != nil {
		input = *r.Input
		if input != "voice" && input != "text" {
			bad("/input", "debe ser voice o text")
		}
	}
	maxF := 2
	if r.MaxFollowups != nil {
		maxF = *r.MaxFollowups
		if maxF < 0 || maxF > 2 {
			bad("/max_followups", "máximo 2")
		}
	}
	think := 0
	if r.ThinkPauseMs != nil {
		think = *r.ThinkPauseMs
		if think < 0 || think > 30000 {
			bad("/think_pause_ms", "entre 0 y 30000")
		}
	}
	if r.NumQuestions != nil && (*r.NumQuestions < 1 || *r.NumQuestions > 50) {
		bad("/num_questions", "entre 1 y 50")
	}
	if r.Mode == "practice" && (r.ScenarioID == nil || *r.ScenarioID == "") {
		bad("/scenario_id", "obligatorio en mode=practice")
	}
	provID := s.LLM.Default()
	if r.LLMProvider != nil {
		provID = *r.LLMProvider
	}
	prov, perr := s.LLM.Get(provID)
	if perr != nil {
		bad("/llm_provider", perr.Error())
	}
	if len(fe) > 0 {
		return nil, &ValidationError{fe}
	}
	model := prov.Model()
	if r.LLMModel != nil && *r.LLMModel != "" && provID != "mock" {
		model = *r.LLMModel
	}
	cfg := &SessionConfig{ThinkPauseMs: think, LearnProfile: r.LearnProfile, SaveAudio: r.SaveAudio, LLMModel: model}
	if r.Voice != nil {
		cfg.Voice = *r.Voice
	}
	var title string
	var scID *string
	if r.Mode == "practice" {
		sc, _, _, err := s.Scenario(ctx, *r.ScenarioID)
		if err != nil {
			return nil, err
		}
		if sc.MaxF() < maxF {
			maxF = sc.MaxF()
		}
		title = sc.Title.Get(r.Lang)
		id := sc.ID
		scID = &id
		cfg.ScenarioID = scID
		cfg.Intro, cfg.Closing, cfg.Style = sc.Intro.Get(r.Lang), sc.Closing.Get(r.Lang), sc.StyleOr()
		cfg.Ideal, cfg.FollowupIdeal = sc.Ideal(), sc.FollowupIdeal()
		for _, q := range sc.Questions {
			cfg.Questions = append(cfg.Questions, engine.Question{ID: q.ID, Text: q.Text.Get(r.Lang), Probes: q.ProbeList(),
				Keywords: q.Keywords, Star: q.IsStar(), ExpectsNumbers: q.WantsNumbers()})
		}
	} else {
		p, _, err := s.Profile(ctx)
		if err != nil {
			return nil, err
		}
		if p == nil {
			p = profile.Empty()
		}
		gaps := profile.Gaps(p, r.Lang)
		if len(gaps) == 0 {
			return nil, fmt.Errorf("%w: el perfil no tiene huecos para completar", ErrConflict)
		}
		title = story[r.Lang]
		cfg.Style = "entrevista para conocer tu experiencia real"
		cfg.Ideal, cfg.FollowupIdeal = [2]float64{40, 120}, [2]float64{15, 75}
		for _, g := range gaps {
			cfg.Questions = append(cfg.Questions, engine.Question{ID: g.ID, Text: g.Text, Probes: []string{"example", "numbers"}})
		}
		cfg.LearnProfile = true
	}
	if r.Shuffle {
		rand.Shuffle(len(cfg.Questions), func(i, j int) { cfg.Questions[i], cfg.Questions[j] = cfg.Questions[j], cfg.Questions[i] })
	}
	if r.NumQuestions != nil && *r.NumQuestions < len(cfg.Questions) {
		cfg.Questions = cfg.Questions[:*r.NumQuestions]
	}
	raw, _ := json.Marshal(cfg)
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	sess := &store.Session{ID: id.String(), Status: StCreated, Mode: r.Mode, ScenarioID: scID, ScenarioTitle: &title, Lang: r.Lang,
		Input: input, LLMProvider: provID, LLMModel: model, MaxFollowups: maxF, QuestionCount: len(cfg.Questions),
		Config: raw, LLMErrors: []string{}, CreatedAt: s.Now().UTC().Truncate(time.Millisecond)}
	if err := s.Store.InsertSession(ctx, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// Provider devuelve el proveedor de la sesión (o mock si ya no está configurado).
func (s *Service) Provider(sess *store.Session) (llm.Provider, bool) {
	p, err := s.LLM.Get(sess.LLMProvider)
	if err != nil {
		mock, _ := s.LLM.Get("mock")
		return mock, false
	}
	return llm.WithModel(p, sess.LLMModel), true
}

// ---- scoring ----

// StartScoring arranca los workers de informes.
func (s *Service) StartScoring(workers int) {
	for i := 0; i < workers; i++ {
		s.scoreWG.Add(1)
		go func() {
			defer s.scoreWG.Done()
			for {
				select {
				case <-s.scoreCtx.Done():
					return
				case id := <-s.scoreQ:
					s.score(id)
					s.pending.Delete(id)
				}
			}
		}()
	}
}

// Enqueue encola el informe de una sesión terminada (no duplica).
func (s *Service) Enqueue(id string) {
	if _, dup := s.pending.LoadOrStore(id, true); dup {
		return
	}
	select {
	case s.scoreQ <- id:
	default:
		s.pending.Delete(id)
		s.Log.Warn("cola de informes llena", "session_id", id)
	}
}

// StopScoring espera a que terminen los informes en curso (o vence ctx).
func (s *Service) StopScoring(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		// deja vaciar lo que ya está en curso; lo encolado y no empezado se retoma al arrancar
		s.stopScore()
		s.scoreWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (s *Service) score(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	log := s.Log.With("session_id", id)
	ok, err := s.Store.SetStatus(ctx, id, StScoring, StEnded, StFailed, StScoring)
	if err != nil || !ok {
		return
	}
	fail := func(why string, err error) {
		log.Error("informe falló", "paso", why, "error", err)
		_, _ = s.Store.SetStatus(ctx, id, StFailed, StScoring)
	}
	defer func() {
		if r := recover(); r != nil {
			fail("panic", fmt.Errorf("%v", r))
		}
	}()
	sess, err := s.Store.GetSession(ctx, id)
	if err != nil {
		fail("sesión", err)
		return
	}
	cfg, err := ParseConfig(sess.Config)
	if err != nil {
		fail("config", err)
		return
	}
	raws, err := s.Store.Turns(ctx, id)
	if err != nil {
		fail("turnos", err)
		return
	}
	turns := make([]engine.Turn, 0, len(raws))
	for _, r := range raws {
		var t engine.Turn
		if json.Unmarshal(r, &t) == nil {
			turns = append(turns, t)
		}
	}
	prov, _ := s.Provider(sess)
	started := sess.CreatedAt
	if sess.StartedAt != nil {
		started = *sess.StartedAt
	}
	ended := s.Now().UTC()
	if sess.EndedAt != nil {
		ended = *sess.EndedAt
	}
	reason := ""
	if sess.EndedReason != nil {
		reason = *sess.EndedReason
	}
	title := ""
	if sess.ScenarioTitle != nil {
		title = *sess.ScenarioTitle
	}
	sc := &scoring.Scorer{LLM: prov}
	rep := sc.Evaluate(ctx, scoring.Input{SessionID: id, ScenarioID: sess.ScenarioID, ScenarioTitle: title, Lang: sess.Lang, Mode: sess.Mode,
		StartedAt: started, EndedAt: ended, EndedReason: reason, Questions: cfg.Questions, Ideal: cfg.Ideal, FollowupIdeal: cfg.FollowupIdeal,
		Turns: turns, ProfileChanges: cfg.ProfileChanges, LLMErrors: sess.LLMErrors})
	rep.LLM = sess.LLMProvider + "/" + sess.LLMModel
	for _, b := range rep.Blocks {
		if n := len(b.Improved.RejectedTerms); n > 0 {
			s.M.InventedTerms.Add(float64(n))
		}
	}
	if len(sc.Errors) > 0 {
		s.M.LLMFallbacks.WithLabelValues(sess.LLMProvider).Add(float64(len(sc.Errors)))
	}
	data, _ := json.Marshal(rep)
	if err := s.Store.PutReport(ctx, id, data, scoring.Markdown(rep)); err != nil {
		fail("guardar", err)
		return
	}
	sess.OverallScore = rep.Overall
	sess.Status = StCompleted
	if err := s.Store.SaveSession(ctx, sess); err != nil {
		fail("estado", err)
		return
	}
	log.Info("informe listo", "turnos", len(turns))
}

// Recover deja consistentes las sesiones tras un reinicio: las que estaban en
// vivo terminan como disconnected y se re-encolan los informes pendientes.
func (s *Service) Recover(ctx context.Context) error {
	ids, err := s.Store.SessionsByStatus(ctx, StLive, StDetached)
	if err != nil {
		return err
	}
	for _, id := range ids {
		sess, err := s.Store.GetSession(ctx, id)
		if err != nil {
			continue
		}
		now := s.Now().UTC()
		r := "disconnected"
		sess.Status, sess.EndedReason, sess.EndedAt = StEnded, &r, &now
		_ = s.Store.SaveSession(ctx, sess)
	}
	pend, err := s.Store.SessionsByStatus(ctx, StEnded, StScoring)
	if err != nil {
		return err
	}
	for _, id := range pend {
		_, _ = s.Store.SetStatus(ctx, id, StEnded, StScoring)
		s.Enqueue(id)
	}
	if len(ids)+len(pend) > 0 {
		s.Log.Info("recuperación al arrancar", "cerradas", len(ids), "informes_reencolados", len(pend))
	}
	return nil
}

// LearnProfile extrae hechos de una respuesta y los fusiona con evidencia literal.
func (s *Service) LearnProfile(ctx context.Context, prov llm.Provider, sessionID, turnID, question, answer string) (changes, rejected []string, err error) {
	s.profMu.Lock()
	defer s.profMu.Unlock()
	p, etag, err := s.Profile(ctx)
	if err != nil {
		return nil, nil, err
	}
	create := p == nil
	if create {
		p = profile.Empty()
	}
	var known []string
	for _, e := range p.Experiences {
		known = append(known, e.Company)
	}
	pl := llm.ProfilePayload{Answer: answer, Question: question, KnownCompanies: known}
	ex, lerr := llm.Call[llm.Extraction](ctx, prov, llm.TaskProfile, llm.ProfileSystem, pl, 0.1)
	if lerr != nil {
		ex = llm.MockProfile(pl)
	}
	changes, rejected = profile.Merge(p, ex, answer, question, sessionID, turnID)
	if len(changes) == 0 {
		return nil, rejected, lerr
	}
	if create {
		_, err = s.SaveProfile(ctx, p, "", false)
	} else {
		_, err = s.SaveProfile(ctx, p, etag, false)
	}
	return changes, rejected, err
}
