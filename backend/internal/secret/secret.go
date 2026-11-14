// Package secret envuelve valores sensibles (claves de API, tokens) para que
// nunca salgan en logs, JSON ni fmt.
package secret

import "log/slog"

// String es un secreto. Su valor solo se obtiene con Reveal().
type String struct{ v string }

func New(v string) String                     { return String{v} }
func (s String) Reveal() string               { return s.v }
func (s String) IsSet() bool                  { return s.v != "" }
func (s String) String() string               { return "[REDACTED]" }
func (s String) GoString() string             { return "secret.String{[REDACTED]}" }
func (s String) LogValue() slog.Value         { return slog.StringValue("[REDACTED]") }
func (s String) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }
func (s String) MarshalText() ([]byte, error) { return []byte("[REDACTED]"), nil }
