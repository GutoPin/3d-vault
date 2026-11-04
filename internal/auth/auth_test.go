package auth

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

var testParams = Params{Memory: 1024, Time: 1, Threads: 1}

func TestPasswordRoundTrip(t *testing.T) {
	t.Parallel()

	hash, err := HashPassword(testParams, "contraseña correcta")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("formato inesperado: %s", hash)
	}
	if ok, err := VerifyPassword(hash, "contraseña correcta"); err != nil || !ok {
		t.Fatalf("correcta: ok=%v err=%v", ok, err)
	}
	if ok, err := VerifyPassword(hash, "otra"); err != nil || ok {
		t.Fatalf("incorrecta: ok=%v err=%v", ok, err)
	}
}

func TestHashPasswordUsesRandomSalt(t *testing.T) {
	t.Parallel()

	a, err := HashPassword(testParams, "igual")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword(testParams, "igual")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("dos hashes de la misma contraseña no deben coincidir")
	}
}

func TestVerifyPasswordRejectsBadHashes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		hash string
	}{
		{"vacío", ""},
		{"otro algoritmo", "$bcrypt$v=19$m=1,t=1,p=1$AAAA$AAAA"},
		{"sin partes", "$argon2id$"},
		{"parámetros rotos", "$argon2id$v=19$m=x,t=1,p=1$AAAA$AAAA"},
		{"memoria cero", "$argon2id$v=19$m=0,t=1,p=1$AAAA$" + strings.Repeat("A", 43)},
		{"memoria enorme", "$argon2id$v=19$m=4294967295,t=1,p=1$AAAA$" + strings.Repeat("A", 43)},
		{"tiempo enorme", "$argon2id$v=19$m=8,t=99999,p=1$AAAA$" + strings.Repeat("A", 43)},
		{"sal rota", "$argon2id$v=19$m=8,t=1,p=1$!!!$" + strings.Repeat("A", 43)},
		{"clave corta", "$argon2id$v=19$m=8,t=1,p=1$AAAA$AAAA"},
		{"versión distinta", "$argon2id$v=1$m=8,t=1,p=1$AAAA$" + strings.Repeat("A", 43)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := VerifyPassword(tt.hash, "x"); !errors.Is(err, ErrInvalidHash) {
				t.Fatalf("VerifyPassword = %v, quiero ErrInvalidHash", err)
			}
		})
	}
}

func TestNewToken(t *testing.T) {
	t.Parallel()

	plain, hash, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if !strings.HasPrefix(plain, tokenPrefix) {
		t.Errorf("token sin prefijo: %s", plain)
	}
	if strings.Contains(hash, plain) || HashToken(plain) != hash {
		t.Error("el hash no corresponde al token")
	}
	other, _, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if plain == other {
		t.Error("dos tokens no deben coincidir")
	}
}

func TestSessions(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s := NewSessions([]byte("secreto-de-prueba"), time.Hour)
	cookie := s.Issue(42, "hash-1", now)

	if id, ok := s.UserID(cookie); !ok || id != 42 {
		t.Fatalf("UserID = %d, %v", id, ok)
	}

	tests := []struct {
		name   string
		cookie string
		hash   string
		at     time.Time
		want   bool
	}{
		{"válida", cookie, "hash-1", now.Add(time.Minute), true},
		{"caducada", cookie, "hash-1", now.Add(time.Hour), false},
		{"contraseña cambiada", cookie, "hash-2", now, false},
		{"otro usuario en la cookie", strings.Replace(cookie, "42.", "43.", 1), "hash-1", now, false},
		{"caducidad alterada", tamperExpiry(cookie), "hash-1", now, false},
		{"firma cortada", cookie[:len(cookie)-1], "hash-1", now, false},
		{"vacía", "", "hash-1", now, false},
		{"sin firma", "42.123", "hash-1", now, false},
		{"basura", "a.b.c", "hash-1", now, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := s.Verify(tt.cookie, tt.hash, tt.at); got != tt.want {
				t.Fatalf("Verify = %v, quiero %v", got, tt.want)
			}
		})
	}

	t.Run("otro secreto", func(t *testing.T) {
		t.Parallel()

		other := NewSessions([]byte("otro-secreto"), time.Hour)
		if other.Verify(cookie, "hash-1", now) {
			t.Fatal("no debe aceptar una cookie firmada con otro secreto")
		}
	})
}

func tamperExpiry(cookie string) string {
	parts := strings.Split(cookie, ".")
	parts[1] = "9999999999"
	return strings.Join(parts, ".")
}

func TestUserIDRejectsGarbage(t *testing.T) {
	t.Parallel()

	s := NewSessions([]byte("x"), time.Hour)
	for _, cookie := range []string{"", "abc", "abc.def"} {
		if _, ok := s.UserID(cookie); ok {
			t.Errorf("UserID(%q) debería fallar", cookie)
		}
	}
}

func TestLimiterBlocksPerUserAndIP(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	l := NewLimiter()

	for range maxFailsUserIP {
		if !l.Allow("1.1.1.1", "juan", now) {
			t.Fatal("bloqueado antes de tiempo")
		}
		l.Fail("1.1.1.1", "juan", now)
	}
	if l.Allow("1.1.1.1", "juan", now) {
		t.Fatal("debería estar bloqueado")
	}
	if l.Allow("1.1.1.1", "JUAN", now) {
		t.Fatal("no debe distinguir mayúsculas en el usuario")
	}
	if !l.Allow("1.1.1.1", "ana", now) {
		t.Fatal("no debe bloquear a otro usuario")
	}
	if !l.Allow("2.2.2.2", "juan", now) {
		t.Fatal("no debe bloquear a juan desde otra IP: cualquiera podría dejarlo fuera")
	}
	if !l.Allow("1.1.1.1", "juan", now.Add(limiterWindow)) {
		t.Fatal("debería desbloquearse al pasar la ventana")
	}
}

func TestLimiterBlocksManyUsersFromOneIP(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	l := NewLimiter()
	for i := range maxFailsIP {
		l.Fail("1.1.1.1", fmt.Sprintf("usuario%d", i), now)
	}
	if l.Allow("1.1.1.1", "nuevo", now) {
		t.Fatal("debería bloquear la IP tras muchos fallos")
	}
	if !l.Allow("2.2.2.2", "nuevo", now) {
		t.Fatal("no debe afectar a otras IP")
	}
}

func TestLimiterSucceedClearsUserFails(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	l := NewLimiter()
	for range maxFailsUserIP {
		l.Fail("1.1.1.1", "juan", now)
	}
	l.Succeed("1.1.1.1", "juan")
	if !l.Allow("1.1.1.1", "juan", now) {
		t.Fatal("un acceso correcto debe limpiar los fallos")
	}
}

func TestLimiterMemoryIsBounded(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	l := NewLimiter()
	for i := range limiterMaxKeys * 2 {
		l.Fail(fmt.Sprintf("10.0.%d.%d", i/250, i%250), "x", now)
	}
	if len(l.keys) > limiterMaxKeys+2 {
		t.Fatalf("el límite creció a %d entradas", len(l.keys))
	}
}

func TestLimiterConcurrentUse(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	l := NewLimiter()
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			ip := fmt.Sprintf("10.0.0.%d", i)
			for range 100 {
				l.Allow(ip, "juan", now)
				l.Fail(ip, "juan", now)
				l.Succeed(ip, "juan")
			}
		})
	}
	wg.Wait()
}
