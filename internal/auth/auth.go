// Package auth contiene las primitivas de autenticación: contraseñas con
// argon2id, tokens de API, cookies de sesión firmadas y límite de intentos.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	saltBytes   = 16
	keyBytes    = 32
	tokenBytes  = 32
	tokenPrefix = "3dv_"

	maxArgonMemory = 1 << 21 // 2 GiB en KiB
	maxArgonTime   = 64
)

// Params configura argon2id.
type Params struct {
	Memory  uint32 // en KiB
	Time    uint32
	Threads uint8
}

// DefaultParams es el coste para producción: unos 64 MiB por intento de login.
var DefaultParams = Params{Memory: 64 * 1024, Time: 3, Threads: 2}

// ErrInvalidHash indica un hash de contraseña con formato incorrecto.
var ErrInvalidHash = errors.New("hash de contraseña no válido")

// HashPassword devuelve el hash de una contraseña en formato PHC.
func HashPassword(p Params, password string) (string, error) {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generar sal: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, keyBytes)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// VerifyPassword comprueba una contraseña en tiempo constante.
func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrInvalidHash
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return false, ErrInvalidHash
	}
	// Los límites evitan un consumo desmedido si el hash guardado estuviera dañado.
	if p.Memory == 0 || p.Memory > maxArgonMemory || p.Time == 0 || p.Time > maxArgonTime || p.Threads == 0 {
		return false, ErrInvalidHash
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) != keyBytes {
		return false, ErrInvalidHash
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, keyBytes)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NewToken genera un token de API y su hash. Solo se guarda el hash: basta
// SHA-256 porque el token tiene 256 bits de azar y no se puede adivinar.
func NewToken() (plain, hash string, err error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generar token: %w", err)
	}
	plain = tokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return plain, HashToken(plain), nil
}

// HashToken devuelve el valor guardado para un token.
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// Sessions firma y verifica cookies de sesión. La firma incluye el hash de la
// contraseña: cambiarla invalida todas las sesiones del usuario sin guardar nada.
type Sessions struct {
	secret []byte
	ttl    time.Duration
}

// NewSessions devuelve un firmador. secret debe ser largo y aleatorio.
func NewSessions(secret []byte, ttl time.Duration) *Sessions {
	return &Sessions{secret: secret, ttl: ttl}
}

// Issue devuelve el valor de la cookie para un usuario.
func (s *Sessions) Issue(userID int64, passwordHash string, now time.Time) string {
	expires := now.Add(s.ttl).Unix()
	return fmt.Sprintf("%d.%d.%s", userID, expires, s.sign(userID, expires, passwordHash))
}

// UserID extrae el usuario de una cookie sin verificarla. Hace falta para buscar
// su contraseña, y siempre hay que llamar a Verify antes de confiar en él.
func (s *Sessions) UserID(cookie string) (int64, bool) {
	id, _, ok := strings.Cut(cookie, ".")
	if !ok {
		return 0, false
	}
	userID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, false
	}
	return userID, true
}

// Verify comprueba la firma y la caducidad de una cookie.
func (s *Sessions) Verify(cookie, passwordHash string, now time.Time) bool {
	parts := strings.Split(cookie, ".")
	if len(parts) != 3 {
		return false
	}
	userID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	want := s.sign(userID, expires, passwordHash)
	if !hmac.Equal([]byte(parts[2]), []byte(want)) {
		return false
	}
	return now.Unix() < expires
}

// TTL devuelve la duración de una sesión.
func (s *Sessions) TTL() time.Duration {
	return s.ttl
}

func (s *Sessions) sign(userID, expires int64, passwordHash string) string {
	mac := hmac.New(sha256.New, s.secret)
	fmt.Fprintf(mac, "%d.%d.%s", userID, expires, passwordHash)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Límites de intentos de login. Contar por usuario e IP impide adivinar la clave
// de alguien; contar también por IP frena probar muchos usuarios. No se cuenta
// solo por usuario, porque cualquiera podría bloquear a un compañero.
const (
	maxFailsUserIP = 5
	maxFailsIP     = 30
	limiterWindow  = 15 * time.Minute
	limiterMaxKeys = 10_000
)

type attempts struct {
	count   int
	expires time.Time
}

// Limiter cuenta intentos de login fallidos en memoria. Un reinicio lo vacía, lo
// cual es aceptable en un servicio interno. Es seguro para uso concurrente.
type Limiter struct {
	mu   sync.Mutex
	keys map[string]attempts
}

// NewLimiter devuelve un Limiter vacío.
func NewLimiter() *Limiter {
	return &Limiter{keys: make(map[string]attempts)}
}

func userIPKey(ip, username string) string {
	return ip + "\x00" + strings.ToLower(username)
}

// Allow indica si ip puede intentar entrar como username.
func (l *Limiter) Allow(ip, username string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.current(userIPKey(ip, username), now) < maxFailsUserIP && l.current(ip, now) < maxFailsIP
}

func (l *Limiter) current(key string, now time.Time) int {
	entry, ok := l.keys[key]
	if !ok {
		return 0
	}
	if !now.Before(entry.expires) {
		delete(l.keys, key)
		return 0
	}
	return entry.count
}

// Fail registra un intento fallido.
func (l *Limiter) Fail(ip, username string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.keys) >= limiterMaxKeys {
		l.evict(now)
	}
	for _, key := range []string{userIPKey(ip, username), ip} {
		l.keys[key] = attempts{count: l.current(key, now) + 1, expires: now.Add(limiterWindow)}
	}
}

// Succeed borra los fallos de ese usuario e IP.
func (l *Limiter) Succeed(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.keys, userIPKey(ip, username))
}

// evict borra lo caducado y, si no basta, una décima parte de las entradas. Evita
// que nombres de usuario inventados agoten la memoria, sin recorrer el mapa en
// cada intento una vez alcanzado el límite.
func (l *Limiter) evict(now time.Time) {
	for key, entry := range l.keys {
		if !now.Before(entry.expires) {
			delete(l.keys, key)
		}
	}
	if len(l.keys) < limiterMaxKeys {
		return
	}
	target := limiterMaxKeys - limiterMaxKeys/10
	for key := range l.keys {
		if len(l.keys) <= target {
			return
		}
		delete(l.keys, key)
	}
}
