package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHealthReadyMetricsWithoutAuth(t *testing.T) {
	e := newEnv(t, nil)
	if r := e.do("GET", "/v1/health", nil); r.status != 200 || r.json(t)["status"] != "ok" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r := e.do("GET", "/v1/ready", nil)
	if r.status != 200 || r.json(t)["status"] != "ok" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := e.do("GET", "/metrics", nil); r.status != 200 || !strings.Contains(string(r.body), "cx9_http_requests_total") || !strings.Contains(string(r.body), "cx9_sessions_active") {
		t.Fatalf("métricas: %d", r.status)
	}
	if r := e.do("GET", "/v1/nada", nil); r.status != 404 || r.header.Get("Content-Type") != "application/problem+json" {
		t.Fatal("404 problem")
	}
	if r.header.Get("X-Request-Id") == "" {
		t.Fatal("X-Request-Id")
	}
	if r := e.do("GET", "/v1/health", nil, "X-Request-Id", "abc-12345678"); r.header.Get("X-Request-Id") != "abc-12345678" {
		t.Fatal("respeta X-Request-Id")
	}
}

func TestReadyDegradedAndDown(t *testing.T) {
	e := newEnv(t, nil)
	e.app.Svc.Speech.Base = "http://127.0.0.1:1"
	if r := e.do("GET", "/v1/ready", nil); r.status != 503 || r.json(t)["status"] != "down" {
		t.Fatalf("voz caída = 503: %d %s", r.status, r.body)
	}
	srv := degradedSpeech(t)
	e.app.Svc.Speech.Base = srv
	r := e.do("GET", "/v1/ready", nil)
	if r.status != 200 || r.json(t)["status"] != "degraded" {
		t.Fatalf("degraded es usable: %d %s", r.status, r.body)
	}
}

func TestAuthFlow(t *testing.T) {
	e := newEnv(t, nil)
	r := e.do("GET", "/v1/languages", nil)
	if r.status != 401 || r.json(t)["code"] != "UNAUTHENTICATED" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r = e.do("POST", "/v1/auth/login", map[string]string{"password": testPassword})
	if r.status != 200 {
		t.Fatal(r.status)
	}
	ck := r.header.Get("Set-Cookie")
	if !strings.Contains(ck, "cx9_session=") || !strings.Contains(ck, "HttpOnly") || !strings.Contains(ck, "SameSite=Strict") || strings.Contains(ck, "Secure") {
		t.Fatalf("cookie: %s", ck)
	}
	tok := r.json(t)["token"].(string)
	// la cookie sola autentica
	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "cx9_session", Value: tok})
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 200 {
		t.Fatal("cookie")
	}
	res.Body.Close()
	e.token = tok
	if m := e.do("GET", "/v1/auth/me", nil).json(t); m["user"] != "local" {
		t.Fatal(m)
	}
	if r := e.do("POST", "/v1/auth/logout", nil); r.status != 204 {
		t.Fatal(r.status)
	}
	if r := e.do("GET", "/v1/auth/me", nil); r.status != 401 {
		t.Fatal("token revocado")
	}
	e.token = ""
	for i := 0; i < 5; i++ {
		if r := e.do("POST", "/v1/auth/login", map[string]string{"password": "mal"}); r.status != 401 {
			t.Fatal(r.status)
		}
	}
	r = e.do("POST", "/v1/auth/login", map[string]string{"password": testPassword})
	if r.status != 429 || r.header.Get("Retry-After") == "" || r.json(t)["code"] != "RATE_LIMITED" {
		t.Fatalf("rate limit %d", r.status)
	}
	if strings.Contains(string(r.body), testPassword) {
		t.Fatal("no se devuelve la contraseña")
	}
}

func TestCSRFAndContentType(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	if r := e.do("POST", "/v1/sessions", map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia"}, "Origin", "https://evil.example"); r.status != 403 || r.json(t)["code"] != "ORIGIN_NOT_ALLOWED" {
		t.Fatalf("%d", r.status)
	}
	if r := e.do("POST", "/v1/sessions", map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia"}, "Origin", "http://localhost:5173"); r.status != 201 {
		t.Fatalf("origen dev permitido: %d", r.status)
	}
	if r := e.do("POST", "/v1/sessions", "a=b", "Content-Type", "application/x-www-form-urlencoded"); r.status != 415 {
		t.Fatalf("form post: %d", r.status)
	}
	// CORS
	req, _ := http.NewRequest("OPTIONS", e.srv.URL+"/v1/sessions", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "POST")
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != 204 || res.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" || res.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("preflight %d %v", res.StatusCode, res.Header)
	}
}

func TestLanguagesAndProviders(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-no-se-devuelve")
	e := newEnv(t, nil)
	e.login()
	l := e.do("GET", "/v1/languages", nil).json(t)["items"].([]any)
	es := l[0].(map[string]any)
	if es["code"] != "es" || es["stt"] != true || es["scenarios"].(float64) != 3 || es["default_voice"] != "mock-es" {
		t.Fatal(es)
	}
	r := e.do("GET", "/v1/llm/providers", nil)
	if strings.Contains(string(r.body), "sk-no-se-devuelve") {
		t.Fatal("clave en la respuesta")
	}
	m := r.json(t)
	if m["default"] != "mock" || len(m["items"].([]any)) != 5 || m["items"].([]any)[0].(map[string]any)["configured"] != true {
		t.Fatal(m)
	}
}

const myYAML = `id: mi-screening
title: { es: "Mi screening", en: "My screening" }
max_followups: 2
questions:
  - id: recorrido
    text: { es: "Contame tu recorrido.", en: "Tell me about your background." }
    probes: [example, numbers]
    keywords: [experiencia, años]
    star: false
    expects_numbers: true
`

func TestScenariosCRUD(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	r := e.do("GET", "/v1/scenarios?lang=es", nil)
	items := r.json(t)["items"].([]any)
	if len(items) != 3 {
		t.Fatal(len(items))
	}
	if r := e.do("GET", "/v1/scenarios", nil); !strings.Contains(string(r.body), `"title":{"en"`) {
		t.Fatal("sin lang el título es {es,en}")
	}
	r = e.do("POST", "/v1/scenarios", myYAML)
	if r.status != 201 || r.header.Get("Location") != "/v1/scenarios/mi-screening" || r.header.Get("ETag") == "" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	etag := r.header.Get("ETag")
	if r := e.do("POST", "/v1/scenarios", myYAML); r.status != 409 {
		t.Fatal("conflicto")
	}
	bad := strings.Replace(myYAML, "probes: [example, numbers]", "probes: [example, bogus]", 1)
	r = e.do("POST", "/v1/scenarios/validate", bad)
	if r.status != 422 || !strings.Contains(string(r.body), "/questions/0/probes/1") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := e.do("POST", "/v1/scenarios/validate", strings.Replace(myYAML, "max_followups: 2", "max_followups: 3", 1)); r.status != 422 {
		t.Fatal("max_followups ≤ 2")
	}
	if r := e.do("POST", "/v1/scenarios", strings.Repeat("#", 300<<10)); r.status != 413 {
		t.Fatalf("413: %d", r.status)
	}
	if r := e.do("GET", "/v1/scenarios/screening-ia", nil, "Accept", "application/yaml"); r.status != 200 || !strings.Contains(string(r.body), "id: screening-ia") {
		t.Fatal("yaml")
	}
	if r := e.do("PUT", "/v1/scenarios/screening-ia", myYAML); r.status != 403 || r.json(t)["code"] != "READ_ONLY" {
		t.Fatal("solo lectura")
	}
	if r := e.do("DELETE", "/v1/scenarios/screening-ia", nil); r.status != 403 {
		t.Fatal("solo lectura")
	}
	upd := strings.Replace(myYAML, "Mi screening", "Mi screening 2", 1)
	if r := e.do("PUT", "/v1/scenarios/mi-screening", upd, "If-Match", `"viejo"`); r.status != 412 {
		t.Fatalf("412: %d", r.status)
	}
	if r := e.do("PUT", "/v1/scenarios/mi-screening", upd, "If-Match", etag); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := e.do("GET", "/v1/scenarios/mi-screening", nil); r.json(t)["title"].(map[string]any)["es"] != "Mi screening 2" {
		t.Fatal("actualizado")
	}
	if r := e.do("DELETE", "/v1/scenarios/mi-screening", nil); r.status != 204 {
		t.Fatal(r.status)
	}
	if r := e.do("GET", "/v1/scenarios/mi-screening", nil); r.status != 404 {
		t.Fatal(r.status)
	}
}

func TestProfileCRUD(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	if r := e.do("GET", "/v1/profile", nil); r.status != 404 {
		t.Fatal(r.status)
	}
	p := map[string]any{"experiences": []map[string]any{{"company": "Telecom", "role": "Backend", "technologies": []string{"Go"}}}, "education": []any{}, "skills": []string{"Go"}}
	r := e.do("POST", "/v1/profile", p)
	if r.status != 201 || r.header.Get("ETag") == "" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	etag := r.header.Get("ETag")
	if r := e.do("POST", "/v1/profile", p); r.status != 409 {
		t.Fatal("409")
	}
	if r := e.do("POST", "/v1/profile", map[string]any{"experiences": []map[string]any{{"company": ""}}}); r.status != 422 {
		t.Fatal("422")
	}
	if r := e.do("PUT", "/v1/profile", map[string]any{"campo": 1}); r.status != 422 {
		t.Fatal("desconocido 422")
	}
	patch := []byte(`{"skills":["Go","PostgreSQL"],"education":null}`)
	if r := e.do("PATCH", "/v1/profile", patch); r.status != 415 {
		t.Fatal("PATCH exige merge-patch")
	}
	if r := e.do("PATCH", "/v1/profile", patch, "Content-Type", "application/merge-patch+json", "If-Match", `"x"`); r.status != 412 {
		t.Fatal("412")
	}
	r = e.do("PATCH", "/v1/profile", patch, "Content-Type", "application/merge-patch+json", "If-Match", etag)
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	m := r.json(t)
	if len(m["skills"].([]any)) != 2 || len(m["experiences"].([]any)) != 1 || len(m["education"].([]any)) != 0 {
		t.Fatal(m)
	}
	g := e.do("GET", "/v1/profile/gaps?lang=es", nil).json(t)["items"].([]any)
	if len(g) == 0 {
		t.Fatal("huecos")
	}
	if r := e.do("DELETE", "/v1/profile", nil); r.status != 204 {
		t.Fatal(r.status)
	}
	if r := e.do("GET", "/v1/profile", nil); r.status != 404 {
		t.Fatal(r.status)
	}
}

func TestSessionsCreateValidateIdempotencyAndHistory(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	r := e.do("POST", "/v1/sessions", map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia", "max_followups": 3})
	if r.status != 422 || !strings.Contains(string(r.body), "/max_followups") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := e.do("POST", "/v1/sessions", map[string]any{"lang": "es", "mode": "practice", "scenario_id": "no-existe"}); r.status != 404 {
		t.Fatal(r.status)
	}
	if r := e.do("POST", "/v1/sessions", map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia", "llm_provider": "anthropic"}); r.status != 422 {
		t.Fatal("proveedor no configurado → 422")
	}
	if r := e.do("POST", "/v1/sessions", map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia", "typo": 1}); r.status != 422 {
		t.Fatal("campo desconocido → 422")
	}
	body := map[string]any{"lang": "es", "mode": "practice", "scenario_id": "screening-ia", "num_questions": 3}
	a := e.do("POST", "/v1/sessions", body, "Idempotency-Key", "clave-0001")
	b := e.do("POST", "/v1/sessions", body, "Idempotency-Key", "clave-0001")
	if a.status != 201 || b.status != 201 || string(a.body) != string(b.body) || b.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay %d %d", a.status, b.status)
	}
	body["num_questions"] = 2
	if r := e.do("POST", "/v1/sessions", body, "Idempotency-Key", "clave-0001"); r.status != 422 || r.json(t)["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatal("reuse")
	}
	if r := e.do("POST", "/v1/sessions", body, "Idempotency-Key", "x"); r.status != 400 {
		t.Fatal("clave corta")
	}
	s := a.json(t)
	if s["status"] != "created" || s["question_count"].(float64) != 3 || s["max_followups"].(float64) != 2 || s["report_url"] != nil {
		t.Fatal(s)
	}
	for i := 0; i < 4; i++ {
		e.createSession(map[string]any{"lang": "en", "mode": "practice", "scenario_id": "behavioral-star", "input": "text"})
	}
	r = e.do("GET", "/v1/sessions?lang=en&limit=3", nil)
	page := r.json(t)
	if page["total"].(float64) != 4 || len(page["items"].([]any)) != 3 || page["next_cursor"] == nil {
		t.Fatal(page)
	}
	r = e.do("GET", "/v1/sessions?lang=en&limit=3&cursor="+page["next_cursor"].(string), nil)
	page2 := r.json(t)
	if len(page2["items"].([]any)) != 1 || page2["next_cursor"] != nil {
		t.Fatal(page2)
	}
	for _, bad := range []string{"limit=0", "status=x", "from=ayer", "cursor=bm8tanNvbg", "sort=x"} {
		if r := e.do("GET", "/v1/sessions?"+bad, nil); r.status != 400 {
			t.Fatalf("%s: %d", bad, r.status)
		}
	}
	id := s["id"].(string)
	if r := e.do("GET", "/v1/sessions/"+id+"/report", nil); r.status != 409 || r.json(t)["code"] != "SESSION_NOT_ENDED" {
		t.Fatal("409 antes de terminar")
	}
	r = e.do("POST", "/v1/sessions/"+id+"/live-ticket", nil)
	if r.status != 201 || !strings.Contains(r.json(t)["url"].(string), "?ticket=") {
		t.Fatal(r.status)
	}
	r = e.do("POST", "/v1/sessions/"+id+"/end", map[string]any{"reason": "user"})
	if r.status != 200 || r.json(t)["status"] == "created" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	again := e.do("POST", "/v1/sessions/"+id+"/end", nil)
	if again.status != 200 {
		t.Fatal("idempotente")
	}
	rep := waitReport(t, e, id)
	if len(rep["top3"].([]any)) != 3 || rep["top3"].([]any)[0].(map[string]any)["dimension"] != "[FALTA]" {
		t.Fatalf("top3 exacto con [FALTA]: %v", rep["top3"])
	}
	if r := e.do("POST", "/v1/sessions/"+id+"/live-ticket", nil); r.status != 409 {
		t.Fatal("ticket de sesión terminada")
	}
	if r := e.do("DELETE", "/v1/sessions/"+id, nil); r.status != 204 {
		t.Fatal(r.status)
	}
	if r := e.do("GET", "/v1/sessions/"+id, nil); r.status != 404 {
		t.Fatal(r.status)
	}
}

func TestStoryModeNeedsGaps(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	s := e.do("POST", "/v1/sessions", map[string]any{"lang": "es", "mode": "story"}).json(t)
	if s["mode"] != "story" || s["scenario_id"] != nil || s["question_count"].(float64) < 1 {
		t.Fatal(s)
	}
}

func waitReport(t *testing.T, e *env, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		r := e.do("GET", "/v1/sessions/"+id+"/report", nil)
		switch r.status {
		case 200:
			var m map[string]any
			if err := json.Unmarshal(r.body, &m); err != nil {
				t.Fatal(err)
			}
			if md := e.do("GET", "/v1/sessions/"+id+"/report", nil, "Accept", "text/markdown"); md.status != 200 || !strings.HasPrefix(md.header.Get("Content-Type"), "text/markdown") {
				t.Fatal("markdown")
			}
			return m
		case 202:
			if r.header.Get("Retry-After") != "2" {
				t.Fatal("Retry-After")
			}
			time.Sleep(50 * time.Millisecond)
		default:
			t.Fatalf("report %d %s", r.status, r.body)
		}
	}
	t.Fatal("el informe no llegó")
	return nil
}
