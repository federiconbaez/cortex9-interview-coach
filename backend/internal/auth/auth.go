// Package auth tiene la autenticación local de un solo usuario: contraseña
// argon2id (PHC) desde el entorno, tokens opacos (se guarda solo su SHA-256),
// tickets WS de un solo uso y límite de intentos de login.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// Parámetros por defecto (RFC 9106, perfil de memoria moderada).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

var ErrBadHash = errors.New("CX9_PASSWORD_HASH no es un hash argon2id válido")

// HashPassword devuelve $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// VerifyPassword compara en tiempo constante.
func VerifyPassword(phc, pw string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrBadHash
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false, ErrBadHash
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 || m > 1<<22 || t > 20 {
		return false, ErrBadHash
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[4])
	want, err2 := enc.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) < 16 {
		return false, ErrBadHash
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NewToken genera 32 bytes aleatorios (base64url) y su hash para guardar.
func NewToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken: lo único que toca la base es el SHA-256 del token.
func HashToken(token string) string {
	s := sha256.Sum256([]byte(token))
	return hex.EncodeToString(s[:])
}

// Limiter: 5 intentos fallidos por minuto (ventana deslizante, en memoria).
type Limiter struct {
	Max    int
	Window time.Duration
	Now    func() time.Time
	mu     sync.Mutex
	fails  []time.Time
}

func NewLimiter() *Limiter { return &Limiter{Max: 5, Window: time.Minute, Now: time.Now} }

// Blocked devuelve cuánto falta para volver a intentar (0 = permitido).
func (l *Limiter) Blocked() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	l.prune(now)
	if len(l.fails) < l.Max {
		return 0
	}
	return l.fails[0].Add(l.Window).Sub(now)
}

func (l *Limiter) Fail() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(l.Now())
	l.fails = append(l.fails, l.Now())
}

func (l *Limiter) Reset() {
	l.mu.Lock()
	l.fails = nil
	l.mu.Unlock()
}

func (l *Limiter) prune(now time.Time) {
	i := 0
	for i < len(l.fails) && now.Sub(l.fails[i]) >= l.Window {
		i++
	}
	l.fails = l.fails[i:]
}

type ctxKey struct{}

// WithUser marca el contexto como autenticado.
func WithUser(ctx context.Context, exp time.Time) context.Context {
	return context.WithValue(ctx, ctxKey{}, exp)
}

// Expiry devuelve el vencimiento del token del contexto.
func Expiry(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(ctxKey{}).(time.Time)
	return t, ok
}
