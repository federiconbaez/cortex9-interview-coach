package api_test

import (
	"context"
	"os"
	"testing"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/config"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/live"
	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/store"
)

// Regresión (integración de Apex sobre a1be862): /speech informa cero muletillas
// (fillers: [] y filler_count: 0). La lista vacía tiene que persistirse y el
// informe tiene que decir muletillas_fuente "stt" con 0 muletillas, aunque el texto
// tenga palabras que el detector por texto contaría ("bueno", "o sea").
// Corre en SQLite y, con CX9_TEST_DATABASE_URL, también en Postgres.
func TestRegressionEmptySTTFillersKeepSTTSource(t *testing.T) {
	dbs := map[string]string{"sqlite": ""}
	if u := os.Getenv("CX9_TEST_DATABASE_URL"); u != "" {
		dbs["postgres"] = u
	}
	for name, url := range dbs {
		t.Run(name, func(t *testing.T) {
			if url != "" {
				resetPostgres(t, url)
			}
			e := newEnv(t, func(c *config.Config, _ *live.Options) {
				if url != "" {
					c.DatabaseURL = url
				}
			})
			e.speech.noFillers = true
			e.login()
			sid := e.createSession(map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia", "num_questions": 1, "max_followups": 0})
			e.speech.say("Bueno, o sea, trabajé tres años en Telecom con Go.")
			w, _, err := e.dial(sid)
			if err != nil {
				t.Fatal(err)
			}
			w.hello(map[string]any{"partials": false, "tts_delivery": []string{"none"}})
			w.expect("session.ready")
			for {
				typ, d, _ := w.prompt()
				if typ == "session.end" {
					break
				}
				if k, _ := d["kind"].(string); k == "intro" || k == "closing" {
					continue
				}
				tid := d["turn_id"].(string)
				w.answer(tid, 1500)
				w.expect("transcript.final")
				w.expect("turn.end")
			}
			w.waitClose()

			det := e.do("GET", "/v1/sessions/"+sid, nil).json(t)
			turns := det["turns"].([]any)
			if len(turns) != 1 {
				t.Fatal(det)
			}
			f, ok := turns[0].(map[string]any)["stt_fillers"]
			if l, isList := f.([]any); !ok || !isList || len(l) != 0 {
				t.Fatalf("stt_fillers persistido como %#v (presente=%v)", f, ok)
			}
			rep := waitReport(t, e, sid)
			tr := rep["blocks"].([]any)[0].(map[string]any)["turns"].([]any)[0].(map[string]any)
			d := tr["details"].(map[string]any)
			if d["muletillas_fuente"] != "stt" {
				t.Fatalf("muletillas_fuente = %v", d["muletillas_fuente"])
			}
			if m, ok := d["muletillas"].([]any); !ok || len(m) != 0 || d["muletillas_cada_100"].(float64) != 0 {
				t.Fatalf("muletillas = %#v (%v cada 100)", d["muletillas"], d["muletillas_cada_100"])
			}
		})
	}
}

func resetPostgres(t *testing.T, url string) {
	t.Helper()
	st, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, tb := range []string{"turns", "reports", "sessions", "scenarios", "profile", "auth_tokens", "idempotency", "schema_migrations"} {
		if _, err := st.DB.Exec("DROP TABLE IF EXISTS " + tb + " CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
}
