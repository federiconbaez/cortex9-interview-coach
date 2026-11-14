// Package store persiste en SQLite (dev y tests, modernc.org/sqlite sin cgo)
// o Postgres (pgx), elegido por el esquema de CX9_DATABASE_URL. Las
// migraciones van embebidas y se aplican al arrancar.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var (
	ErrNotFound     = errors.New("no existe")
	ErrConflict     = errors.New("ya existe")
	ErrPrecondition = errors.New("etag no coincide")
)

const tsLayout = "2006-01-02T15:04:05.000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(tsLayout, s)
	return t
}

func nts(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func pts(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

// Store envuelve *sql.DB con el dialecto.
type Store struct {
	DB      *sql.DB
	Dialect string // sqlite | postgres
}

// Open abre según el esquema: sqlite://ruta, sqlite://:memory:, postgres://…
func Open(ctx context.Context, url string) (*Store, error) {
	var s Store
	var err error
	switch {
	case strings.HasPrefix(url, "postgres://"), strings.HasPrefix(url, "postgresql://"):
		s.Dialect = "postgres"
		s.DB, err = sql.Open("pgx", url)
		if err == nil {
			s.DB.SetMaxOpenConns(10)
			s.DB.SetConnMaxIdleTime(5 * time.Minute)
		}
	case strings.HasPrefix(url, "sqlite://"), strings.HasPrefix(url, "sqlite:"):
		path := strings.TrimPrefix(strings.TrimPrefix(url, "sqlite://"), "sqlite:")
		if path == "" {
			path = "cx9.db"
		}
		s.Dialect = "sqlite"
		dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
		if path != ":memory:" {
			dsn += "&_pragma=journal_mode(WAL)"
		}
		s.DB, err = sql.Open("sqlite", dsn)
		if err == nil {
			// Una sola conexión: SQLite serializa escrituras y :memory: es por conexión.
			s.DB.SetMaxOpenConns(1)
		}
	default:
		return nil, fmt.Errorf("CX9_DATABASE_URL con esquema no soportado (usá sqlite:// o postgres://)")
	}
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.DB.PingContext(pctx); err != nil {
		s.DB.Close()
		return nil, fmt.Errorf("no se pudo conectar a la base (%s): %w", s.Dialect, err)
	}
	return &s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

// q reescribe ? → $n para Postgres.
func (s *Store) q(query string) string {
	if s.Dialect != "postgres" {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.DB.ExecContext(ctx, s.q(query), args...)
}

func (s *Store) row(ctx context.Context, query string, args ...any) *sql.Row {
	return s.DB.QueryRowContext(ctx, s.q(query), args...)
}

// Migrate aplica las migraciones pendientes, cada una en su transacción.
func (s *Store) Migrate(ctx context.Context) ([]int, error) {
	if _, err := s.exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var applied []int
	for _, e := range entries {
		v, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return nil, fmt.Errorf("migración mal nombrada: %s", e.Name())
		}
		var one int
		if err := s.row(ctx, `SELECT 1 FROM schema_migrations WHERE version = ?`, v).Scan(&one); err == nil {
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		body, _ := migrationsFS.ReadFile("migrations/" + e.Name())
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		for _, stmt := range splitSQL(string(body)) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return nil, fmt.Errorf("migración %s: %w", e.Name(), err)
			}
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`), v, ts(time.Now())); err != nil {
			tx.Rollback()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		applied = append(applied, v)
	}
	return applied, nil
}

func splitSQL(body string) []string {
	var lines []string
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "--") {
			lines = append(lines, l)
		}
	}
	var out []string
	for _, st := range strings.Split(strings.Join(lines, "\n"), ";") {
		if st = strings.TrimSpace(st); st != "" {
			out = append(out, st)
		}
	}
	return out
}

func isUnique(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "unique") || strings.Contains(m, "duplicate key") || strings.Contains(m, "23505")
}

// ---- tokens y tickets ----

func (s *Store) CreateToken(ctx context.Context, hash, kind string, sessionID *string, expires time.Time) error {
	_, err := s.exec(ctx, `INSERT INTO auth_tokens (token_hash, kind, session_id, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		hash, kind, sessionID, ts(expires), ts(time.Now()))
	return err
}

// TokenExpiry devuelve el vencimiento de un token de sesión vigente.
func (s *Store) TokenExpiry(ctx context.Context, hash string, now time.Time) (time.Time, error) {
	var exp string
	err := s.row(ctx, `SELECT expires_at FROM auth_tokens WHERE token_hash = ? AND kind = 'session' AND expires_at > ?`, hash, ts(now)).Scan(&exp)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	return parseTS(exp), err
}

func (s *Store) DeleteToken(ctx context.Context, hash string) error {
	_, err := s.exec(ctx, `DELETE FROM auth_tokens WHERE token_hash = ?`, hash)
	return err
}

// UseTicket consume un ticket de un solo uso (atómico).
func (s *Store) UseTicket(ctx context.Context, hash, sessionID string, now time.Time) (bool, error) {
	res, err := s.exec(ctx, `UPDATE auth_tokens SET used = 1 WHERE token_hash = ? AND kind = 'ticket' AND session_id = ? AND used = 0 AND expires_at > ?`,
		hash, sessionID, ts(now))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) PurgeExpired(ctx context.Context, now time.Time) error {
	if _, err := s.exec(ctx, `DELETE FROM auth_tokens WHERE expires_at <= ?`, ts(now)); err != nil {
		return err
	}
	_, err := s.exec(ctx, `DELETE FROM idempotency WHERE created_at <= ?`, ts(now.Add(-24*time.Hour)))
	return err
}

// ---- escenarios propios ----

type ScenarioRow struct {
	ID, YAML, JSON, ETag string
	UpdatedAt            time.Time
}

func (s *Store) ListScenarios(ctx context.Context) ([]ScenarioRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, yaml, json, etag, updated_at FROM scenarios ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScenarioRow
	for rows.Next() {
		var r ScenarioRow
		var u string
		if err := rows.Scan(&r.ID, &r.YAML, &r.JSON, &r.ETag, &u); err != nil {
			return nil, err
		}
		r.UpdatedAt = parseTS(u)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetScenario(ctx context.Context, id string) (*ScenarioRow, error) {
	var r ScenarioRow
	var u string
	err := s.row(ctx, `SELECT id, yaml, json, etag, updated_at FROM scenarios WHERE id = ?`, id).Scan(&r.ID, &r.YAML, &r.JSON, &r.ETag, &u)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	r.UpdatedAt = parseTS(u)
	return &r, err
}

func (s *Store) InsertScenario(ctx context.Context, r ScenarioRow) error {
	now := ts(r.UpdatedAt)
	_, err := s.exec(ctx, `INSERT INTO scenarios (id, yaml, json, etag, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, r.ID, r.YAML, r.JSON, r.ETag, now, now)
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

// UpdateScenario reemplaza si existe; ifMatch vacío = sin control de concurrencia.
func (s *Store) UpdateScenario(ctx context.Context, r ScenarioRow, ifMatch string) error {
	query := `UPDATE scenarios SET yaml = ?, json = ?, etag = ?, updated_at = ? WHERE id = ?`
	args := []any{r.YAML, r.JSON, r.ETag, ts(r.UpdatedAt), r.ID}
	if ifMatch != "" {
		query += ` AND etag = ?`
		args = append(args, ifMatch)
	}
	res, err := s.exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetScenario(ctx, r.ID); err != nil {
			return err
		}
		return ErrPrecondition
	}
	return nil
}

func (s *Store) DeleteScenario(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM scenarios WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- perfil (uno solo, id = 1) ----

func (s *Store) GetProfile(ctx context.Context) (data, etag string, err error) {
	err = s.row(ctx, `SELECT json, etag FROM profile WHERE id = 1`).Scan(&data, &etag)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return data, etag, err
}

// PutProfile crea o reemplaza. mustCreate=true falla con ErrConflict si existe.
func (s *Store) PutProfile(ctx context.Context, data, etag, ifMatch string, mustCreate bool, now time.Time) error {
	if mustCreate {
		_, err := s.exec(ctx, `INSERT INTO profile (id, json, etag, updated_at) VALUES (1, ?, ?, ?)`, data, etag, ts(now))
		if isUnique(err) {
			return ErrConflict
		}
		return err
	}
	if ifMatch != "" {
		res, err := s.exec(ctx, `UPDATE profile SET json = ?, etag = ?, updated_at = ? WHERE id = 1 AND etag = ?`, data, etag, ts(now), ifMatch)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrPrecondition
		}
		return nil
	}
	_, err := s.exec(ctx, `INSERT INTO profile (id, json, etag, updated_at) VALUES (1, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET json = excluded.json, etag = excluded.etag, updated_at = excluded.updated_at`, data, etag, ts(now))
	return err
}

func (s *Store) DeleteProfile(ctx context.Context) error {
	_, err := s.exec(ctx, `DELETE FROM profile WHERE id = 1`)
	return err
}

// ---- sesiones ----

type Session struct {
	ID                string
	Status            string
	Mode              string
	ScenarioID        *string
	ScenarioTitle     *string
	Lang              string
	Input             string
	LLMProvider       string
	LLMModel          string
	MaxFollowups      int
	QuestionCount     int
	QuestionsAnswered int
	EndedReason       *string
	OverallScore      *float64
	Config            json.RawMessage
	LLMErrors         []string
	CreatedAt         time.Time
	StartedAt         *time.Time
	EndedAt           *time.Time
}

const sessionCols = `id, status, mode, scenario_id, scenario_title, lang, input, llm_provider, llm_model, max_followups,
	question_count, questions_answered, ended_reason, overall_score, config, llm_errors, created_at, started_at, ended_at`

func scanSession(sc interface{ Scan(...any) error }) (*Session, error) {
	var x Session
	var sid, stitle, er sql.NullString
	var ov sql.NullFloat64
	var cfg, errs, created string
	var started, ended sql.NullString
	if err := sc.Scan(&x.ID, &x.Status, &x.Mode, &sid, &stitle, &x.Lang, &x.Input, &x.LLMProvider, &x.LLMModel, &x.MaxFollowups,
		&x.QuestionCount, &x.QuestionsAnswered, &er, &ov, &cfg, &errs, &created, &started, &ended); err != nil {
		return nil, err
	}
	if sid.Valid {
		x.ScenarioID = &sid.String
	}
	if stitle.Valid {
		x.ScenarioTitle = &stitle.String
	}
	if er.Valid {
		x.EndedReason = &er.String
	}
	if ov.Valid {
		x.OverallScore = &ov.Float64
	}
	x.Config = json.RawMessage(cfg)
	_ = json.Unmarshal([]byte(errs), &x.LLMErrors)
	if x.LLMErrors == nil {
		x.LLMErrors = []string{}
	}
	x.CreatedAt = parseTS(created)
	x.StartedAt, x.EndedAt = pts(started), pts(ended)
	return &x, nil
}

func (s *Store) InsertSession(ctx context.Context, x *Session) error {
	errs, _ := json.Marshal(x.LLMErrors)
	if x.LLMErrors == nil {
		errs = []byte("[]")
	}
	_, err := s.exec(ctx, `INSERT INTO sessions (`+sessionCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.ID, x.Status, x.Mode, x.ScenarioID, x.ScenarioTitle, x.Lang, x.Input, x.LLMProvider, x.LLMModel, x.MaxFollowups,
		x.QuestionCount, x.QuestionsAnswered, x.EndedReason, x.OverallScore, string(x.Config), string(errs), ts(x.CreatedAt), nts(x.StartedAt), nts(x.EndedAt))
	return err
}

func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	x, err := scanSession(s.row(ctx, `SELECT `+sessionCols+` FROM sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return x, err
}

// SaveSession actualiza los campos mutables.
func (s *Store) SaveSession(ctx context.Context, x *Session) error {
	errs, _ := json.Marshal(x.LLMErrors)
	if x.LLMErrors == nil {
		errs = []byte("[]")
	}
	res, err := s.exec(ctx, `UPDATE sessions SET status = ?, questions_answered = ?, ended_reason = ?, overall_score = ?, config = ?,
		llm_errors = ?, started_at = ?, ended_at = ? WHERE id = ?`,
		x.Status, x.QuestionsAnswered, x.EndedReason, x.OverallScore, string(x.Config), string(errs), nts(x.StartedAt), nts(x.EndedAt), x.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetStatus cambia el estado solo si el actual está en from (vacío = cualquiera).
func (s *Store) SetStatus(ctx context.Context, id, status string, from ...string) (bool, error) {
	query := `UPDATE sessions SET status = ? WHERE id = ?`
	args := []any{status, id}
	if len(from) > 0 {
		query += ` AND status IN (` + strings.TrimSuffix(strings.Repeat("?,", len(from)), ",") + `)`
		for _, f := range from {
			args = append(args, f)
		}
	}
	res, err := s.exec(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	for _, q := range []string{`DELETE FROM turns WHERE session_id = ?`, `DELETE FROM reports WHERE session_id = ?`} {
		if _, err := s.exec(ctx, q, id); err != nil {
			return err
		}
	}
	res, err := s.exec(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SessionsByStatus devuelve ids en esos estados (para recuperar tras reinicio).
func (s *Store) SessionsByStatus(ctx context.Context, statuses ...string) ([]string, error) {
	args := make([]any, len(statuses))
	for i, st := range statuses {
		args[i] = st
	}
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT id FROM sessions WHERE status IN (`+strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")+`) ORDER BY created_at`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Filter del historial.
type Filter struct {
	Status, ScenarioID, Lang, Mode string
	From, To                       *time.Time
	Limit                          int
	Cursor                         string
	Asc                            bool
}

type cursor struct {
	C  string `json:"c"`
	ID string `json:"id"`
}

var ErrBadCursor = errors.New("cursor inválido")

// ListSessions pagina por keyset sobre (created_at, id).
func (s *Store) ListSessions(ctx context.Context, f Filter) (items []*Session, next *string, total int, err error) {
	var where []string
	var args []any
	add := func(cond string, v any) { where = append(where, cond); args = append(args, v) }
	if f.Status != "" {
		add("status = ?", f.Status)
	}
	if f.ScenarioID != "" {
		add("scenario_id = ?", f.ScenarioID)
	}
	if f.Lang != "" {
		add("lang = ?", f.Lang)
	}
	if f.Mode != "" {
		add("mode = ?", f.Mode)
	}
	if f.From != nil {
		add("created_at >= ?", ts(*f.From))
	}
	if f.To != nil {
		add("created_at < ?", ts(*f.To))
	}
	cw := strings.Join(where, " AND ")
	if cw == "" {
		cw = "1=1"
	}
	if err = s.row(ctx, `SELECT COUNT(*) FROM sessions WHERE `+cw, args...).Scan(&total); err != nil {
		return
	}
	pageWhere, pageArgs := cw, append([]any(nil), args...)
	if f.Cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(f.Cursor)
		var c cursor
		if e != nil || json.Unmarshal(raw, &c) != nil || c.C == "" {
			return nil, nil, 0, ErrBadCursor
		}
		op := "<"
		if f.Asc {
			op = ">"
		}
		pageWhere += fmt.Sprintf(" AND (created_at %s ? OR (created_at = ? AND id %s ?))", op, op)
		pageArgs = append(pageArgs, c.C, c.C, c.ID)
	}
	dir := "DESC"
	if f.Asc {
		dir = "ASC"
	}
	pageArgs = append(pageArgs, f.Limit+1)
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT `+sessionCols+` FROM sessions WHERE `+pageWhere+
		` ORDER BY created_at `+dir+`, id `+dir+` LIMIT ?`), pageArgs...)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		x, e := scanSession(rows)
		if e != nil {
			return nil, nil, 0, e
		}
		items = append(items, x)
	}
	if err = rows.Err(); err != nil {
		return
	}
	if len(items) > f.Limit {
		items = items[:f.Limit]
		last := items[len(items)-1]
		b, _ := json.Marshal(cursor{C: ts(last.CreatedAt), ID: last.ID})
		c := base64.RawURLEncoding.EncodeToString(b)
		next = &c
	}
	return
}

// ---- turnos ----

func (s *Store) PutTurn(ctx context.Context, sessionID string, idx int, turnID string, data []byte) error {
	_, err := s.exec(ctx, `INSERT INTO turns (session_id, idx, turn_id, json) VALUES (?, ?, ?, ?)
		ON CONFLICT (session_id, idx) DO UPDATE SET turn_id = excluded.turn_id, json = excluded.json`, sessionID, idx, turnID, string(data))
	return err
}

func (s *Store) Turns(ctx context.Context, sessionID string) ([]json.RawMessage, error) {
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT json FROM turns WHERE session_id = ? ORDER BY idx`), sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var j string
		if err := rows.Scan(&j); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(j))
	}
	return out, rows.Err()
}

// ---- informes ----

func (s *Store) PutReport(ctx context.Context, sessionID string, data []byte, md string) error {
	_, err := s.exec(ctx, `INSERT INTO reports (session_id, json, markdown, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (session_id) DO UPDATE SET json = excluded.json, markdown = excluded.markdown, created_at = excluded.created_at`,
		sessionID, string(data), md, ts(time.Now()))
	return err
}

func (s *Store) GetReport(ctx context.Context, sessionID string) (data, md string, err error) {
	err = s.row(ctx, `SELECT json, markdown FROM reports WHERE session_id = ?`, sessionID).Scan(&data, &md)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return
}

// ---- idempotencia ----

type IdemRecord struct {
	BodyHash string
	State    string // in_progress | done
	Status   int
	Headers  map[string]string
	Body     string
}

// IdemBegin reserva la clave. Si ya existía devuelve el registro (created=false).
func (s *Store) IdemBegin(ctx context.Context, scope, bodyHash string, now time.Time) (*IdemRecord, bool, error) {
	_, err := s.exec(ctx, `INSERT INTO idempotency (scope, body_hash, state, created_at) VALUES (?, ?, 'in_progress', ?)`, scope, bodyHash, ts(now))
	if err == nil {
		return nil, true, nil
	}
	if !isUnique(err) {
		return nil, false, err
	}
	var r IdemRecord
	var h string
	if err := s.row(ctx, `SELECT body_hash, state, status, headers, body FROM idempotency WHERE scope = ?`, scope).Scan(&r.BodyHash, &r.State, &r.Status, &h, &r.Body); err != nil {
		return nil, false, err
	}
	_ = json.Unmarshal([]byte(h), &r.Headers)
	return &r, false, nil
}

func (s *Store) IdemComplete(ctx context.Context, scope string, status int, headers map[string]string, body string) error {
	h, _ := json.Marshal(headers)
	_, err := s.exec(ctx, `UPDATE idempotency SET state = 'done', status = ?, headers = ?, body = ? WHERE scope = ?`, status, string(h), body, scope)
	return err
}

func (s *Store) IdemRelease(ctx context.Context, scope string) error {
	_, err := s.exec(ctx, `DELETE FROM idempotency WHERE scope = ?`, scope)
	return err
}
