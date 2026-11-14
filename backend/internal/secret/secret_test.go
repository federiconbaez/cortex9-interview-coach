package secret

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestNeverLeaks(t *testing.T) {
	s := New("sk-super-secreta-123")
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "key", s, "obj", struct{ K String }{s})
	j, _ := json.Marshal(map[string]any{"k": s})
	out := buf.String() + string(j) + fmt.Sprintf("%v %+v %#v %s", s, s, s, s)
	if strings.Contains(out, "sk-super") {
		t.Fatalf("filtró el secreto: %s", out)
	}
	if s.Reveal() != "sk-super-secreta-123" || !s.IsSet() {
		t.Fatal("reveal")
	}
}
