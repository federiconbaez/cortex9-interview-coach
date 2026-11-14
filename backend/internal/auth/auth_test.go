package auth

import (
	"testing"
	"time"
)

func TestHashVerify(t *testing.T) {
	h, err := HashPassword("correcta-horse")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPassword(h, "correcta-horse"); !ok || err != nil {
		t.Fatal("debía verificar")
	}
	if ok, _ := VerifyPassword(h, "otra"); ok {
		t.Fatal("contraseña incorrecta")
	}
	for _, bad := range []string{"", "$bcrypt$x", "$argon2id$v=19$m=0,t=1,p=1$AAAA$AAAA", "$argon2id$v=19$m=65536,t=3,p=2$***$***"} {
		if _, err := VerifyPassword(bad, "x"); err != ErrBadHash {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

func TestTokens(t *testing.T) {
	a, ha, _ := NewToken()
	b, _, _ := NewToken()
	if a == b || len(a) != 43 || HashToken(a) != ha || len(ha) != 64 {
		t.Fatal("tokens")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter()
	l.Now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if l.Blocked() != 0 {
			t.Fatal("todavía no")
		}
		l.Fail()
	}
	if d := l.Blocked(); d != time.Minute {
		t.Fatalf("bloqueado %v", d)
	}
	now = now.Add(61 * time.Second)
	if l.Blocked() != 0 {
		t.Fatal("ventana")
	}
	l.Fail()
	l.Reset()
	if l.Blocked() != 0 {
		t.Fatal("reset")
	}
}
