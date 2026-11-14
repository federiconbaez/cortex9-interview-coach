package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// stores devuelve SQLite en memoria y, si CX9_TEST_DATABASE_URL apunta a un
// Postgres, también Postgres (el CI sin red usa solo SQLite).
func stores(t *testing.T) map[string]*Store {
	out := map[string]*Store{}
	s, err := Open(context.Background(), "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	out["sqlite"] = s
	if u := os.Getenv("CX9_TEST_DATABASE_URL"); u != "" {
		p, err := Open(context.Background(), u)
		if err != nil {
			t.Fatal(err)
		}
		for _, tb := range []string{"turns", "reports", "sessions", "scenarios", "profile", "auth_tokens", "idempotency", "schema_migrations"} {
			_, _ = p.DB.Exec("DROP TABLE IF EXISTS " + tb + " CASCADE")
		}
		out["postgres"] = p
	}
	for name, st := range out {
		if _, err := st.Migrate(context.Background()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Cleanup(func() { st.Close() })
	}
	return out
}

func TestStoreAllDialects(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now()
			if v, err := s.Migrate(ctx); err != nil || len(v) != 0 {
				t.Fatalf("migrar dos veces es no-op: %v %v", v, err)
			}
			// tokens y tickets
			sid := "s-1"
			if err := s.CreateToken(ctx, "h1", "session", nil, now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.TokenExpiry(ctx, "h1", now); err != nil {
				t.Fatal(err)
			}
			if _, err := s.TokenExpiry(ctx, "h1", now.Add(2*time.Hour)); err != ErrNotFound {
				t.Fatal("token vencido")
			}
			_ = s.CreateToken(ctx, "tk", "ticket", &sid, now.Add(time.Minute))
			if ok, _ := s.UseTicket(ctx, "tk", "otra", now); ok {
				t.Fatal("ticket de otra sesión")
			}
			if ok, _ := s.UseTicket(ctx, "tk", sid, now); !ok {
				t.Fatal("primer uso")
			}
			if ok, _ := s.UseTicket(ctx, "tk", sid, now); ok {
				t.Fatal("un solo uso")
			}
			// escenarios
			r := ScenarioRow{ID: "mio", YAML: "y", JSON: "{}", ETag: `"a"`, UpdatedAt: now}
			if err := s.InsertScenario(ctx, r); err != nil {
				t.Fatal(err)
			}
			if err := s.InsertScenario(ctx, r); err != ErrConflict {
				t.Fatalf("conflict: %v", err)
			}
			r.ETag = `"b"`
			if err := s.UpdateScenario(ctx, r, `"zzz"`); err != ErrPrecondition {
				t.Fatalf("precondition: %v", err)
			}
			if err := s.UpdateScenario(ctx, r, `"a"`); err != nil {
				t.Fatal(err)
			}
			if err := s.DeleteScenario(ctx, "mio"); err != nil || s.DeleteScenario(ctx, "mio") != ErrNotFound {
				t.Fatal("delete")
			}
			// perfil
			if _, _, err := s.GetProfile(ctx); err != ErrNotFound {
				t.Fatal("sin perfil")
			}
			if err := s.PutProfile(ctx, "{}", `"1"`, "", true, now); err != nil {
				t.Fatal(err)
			}
			if err := s.PutProfile(ctx, "{}", `"1"`, "", true, now); err != ErrConflict {
				t.Fatalf("crear dos veces: %v", err)
			}
			if err := s.PutProfile(ctx, `{"a":1}`, `"2"`, `"x"`, false, now); err != ErrPrecondition {
				t.Fatal("if-match")
			}
			if err := s.PutProfile(ctx, `{"a":1}`, `"2"`, "", false, now); err != nil {
				t.Fatal(err)
			}
			// sesiones + paginación
			for i := 0; i < 5; i++ {
				x := &Session{ID: fmt.Sprintf("id-%d", i), Status: "created", Mode: "practice", Lang: "es", Input: "voice", LLMProvider: "mock", LLMModel: "mock",
					MaxFollowups: 2, QuestionCount: 3, Config: json.RawMessage(`{}`), CreatedAt: now.Add(time.Duration(i) * time.Second)}
				if i == 4 {
					x.Lang = "en"
				}
				if err := s.InsertSession(ctx, x); err != nil {
					t.Fatal(err)
				}
			}
			items, next, total, err := s.ListSessions(ctx, Filter{Limit: 2})
			if err != nil || len(items) != 2 || next == nil || total != 5 || items[0].ID != "id-4" {
				t.Fatalf("%v %v %d", err, next, total)
			}
			items, next, _, _ = s.ListSessions(ctx, Filter{Limit: 2, Cursor: *next})
			if items[0].ID != "id-2" || next == nil {
				t.Fatal("página 2")
			}
			items, next, _, _ = s.ListSessions(ctx, Filter{Limit: 2, Cursor: *next})
			if len(items) != 1 || next != nil {
				t.Fatal("última página")
			}
			if _, _, n, _ := s.ListSessions(ctx, Filter{Limit: 10, Lang: "en"}); n != 1 {
				t.Fatal("filtro lang")
			}
			if _, _, _, err := s.ListSessions(ctx, Filter{Limit: 2, Cursor: "%%%"}); err != ErrBadCursor {
				t.Fatal("cursor inválido")
			}
			x, _ := s.GetSession(ctx, "id-1")
			ov := 3.5
			x.OverallScore, x.Status, x.StartedAt = &ov, "completed", &now
			if err := s.SaveSession(ctx, x); err != nil {
				t.Fatal(err)
			}
			if ok, _ := s.SetStatus(ctx, "id-1", "scoring", "live"); ok {
				t.Fatal("SetStatus con from")
			}
			if ids, _ := s.SessionsByStatus(ctx, "completed"); len(ids) != 1 {
				t.Fatal("by status")
			}
			_ = s.PutTurn(ctx, "id-1", 0, "t-1", []byte(`{"id":"t-1"}`))
			_ = s.PutTurn(ctx, "id-1", 0, "t-1", []byte(`{"id":"t-1","answer":"x"}`))
			turns, _ := s.Turns(ctx, "id-1")
			if len(turns) != 1 {
				t.Fatal("upsert turno")
			}
			_ = s.PutReport(ctx, "id-1", []byte(`{}`), "# md")
			if _, md, err := s.GetReport(ctx, "id-1"); err != nil || md != "# md" {
				t.Fatal("report")
			}
			if err := s.DeleteSession(ctx, "id-1"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.GetReport(ctx, "id-1"); err != ErrNotFound {
				t.Fatal("cascade")
			}
			// idempotencia
			if _, created, _ := s.IdemBegin(ctx, "k", "h", now); !created {
				t.Fatal("primero")
			}
			rec, created, _ := s.IdemBegin(ctx, "k", "h", now)
			if created || rec.State != "in_progress" {
				t.Fatal("en curso")
			}
			_ = s.IdemComplete(ctx, "k", 201, map[string]string{"Location": "/x"}, "{}")
			rec, _, _ = s.IdemBegin(ctx, "k", "h", now)
			if rec.Status != 201 || rec.Headers["Location"] != "/x" {
				t.Fatal("done")
			}
			if err := s.PurgeExpired(ctx, now.Add(25*time.Hour)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenBadScheme(t *testing.T) {
	if _, err := Open(context.Background(), "mysql://x"); err == nil {
		t.Fatal("esquema no soportado")
	}
}
