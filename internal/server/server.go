// Package server expone 3D-Vault por HTTP: la API JSON del agente y la web de
// consulta y administración. La lógica de negocio vive en internal/vault.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"3d-vault/internal/auth"
	"3d-vault/internal/rules"
	"3d-vault/internal/store"
	"3d-vault/internal/vault"
)

const (
	sessionCookie = "3dvault_session"

	defaultHeartbeat    = 25 * time.Second
	defaultMaxStreamAge = time.Hour
)

// Config configura el servidor HTTP.
type Config struct {
	Service  *vault.Service
	Sessions *auth.Sessions
	Limiter  *auth.Limiter
	Logger   *slog.Logger
	// AllowedIPs limita desde dónde se accede. Vacío permite cualquier origen.
	AllowedIPs []netip.Prefix
	// TrustedProxies son los proxies de los que se acepta X-Forwarded-For.
	TrustedProxies []netip.Prefix
	// InsecureCookies quita Secure de la cookie. Solo para pruebas sin HTTPS.
	InsecureCookies bool
	// SSE: intervalo de latido y duración máxima de una conexión. Limitar la
	// duración obliga a reautenticar, por si el token se revocó entretanto.
	Heartbeat    time.Duration
	MaxStreamAge time.Duration
	// Now permite fijar el reloj en tests. Por defecto, time.Now.
	Now func() time.Time
	// Backups describe las copias automáticas, para la página Espacio.
	Backups BackupConfig
	// LowDisk decide si avisar a los admins de que queda poco disco. Por
	// defecto, menos del 15 % o de 20 GB libres.
	LowDisk func(free, total int64) bool
}

// Server contiene las dependencias de los manejadores.
type Server struct {
	svc             *vault.Service
	sessions        *auth.Sessions
	limiter         *auth.Limiter
	log             *slog.Logger
	allowedIPs      []netip.Prefix
	trustedProxies  []netip.Prefix
	insecureCookies bool
	heartbeat       time.Duration
	maxStreamAge    time.Duration
	now             func() time.Time
	pages           *pageSet
	backups         BackupConfig
	lowDisk         func(free, total int64) bool
	disk            diskCache
}

// New devuelve el manejador HTTP completo.
func New(cfg Config) (http.Handler, error) {
	pages, err := loadPages()
	if err != nil {
		return nil, err
	}
	s := &Server{
		svc:             cfg.Service,
		sessions:        cfg.Sessions,
		limiter:         cfg.Limiter,
		log:             cfg.Logger,
		allowedIPs:      cfg.AllowedIPs,
		trustedProxies:  cfg.TrustedProxies,
		insecureCookies: cfg.InsecureCookies,
		heartbeat:       cfg.Heartbeat,
		maxStreamAge:    cfg.MaxStreamAge,
		now:             cfg.Now,
		pages:           pages,
		backups:         cfg.Backups,
		lowDisk:         cfg.LowDisk,
	}
	if s.lowDisk == nil {
		s.lowDisk = defaultLowDisk
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.heartbeat == 0 {
		s.heartbeat = defaultHeartbeat
	}
	if s.maxStreamAge == 0 {
		s.maxStreamAge = defaultMaxStreamAge
	}
	return s.routes(), nil
}

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer, s.logRequests, securityHeaders)

	// Sin restricción de IP: lo consulta el propio contenedor.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	r.Group(func(r chi.Router) {
		r.Use(s.resolveClientIP, s.restrictIPs)
		// Rechaza formularios y peticiones enviadas desde otro sitio, usando las
		// cabeceras del navegador. Los clientes con token no llevan esas cabeceras.
		r.Use(http.NewCrossOriginProtection().Handler)
		r.Use(s.identify)

		s.apiRoutes(r)
		s.webRoutes(r)
	})
	return r
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		// blob: y data: en img-src y connect-src: el visor 3D carga así las texturas
		// y los datos incluidos dentro de un .glb, .gltf o .fbx.
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; connect-src 'self' data: blob:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// statusRecorder guarda el código de respuesta. Unwrap permite que
// http.ResponseController llegue a la conexión real.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// Solo la ruta: nada de cabeceras ni cuerpo, donde van contraseñas y tokens.
		s.log.LogAttrs(r.Context(), slog.LevelInfo, "petición",
			slog.String("método", r.Method),
			slog.String("ruta", r.URL.Path),
			slog.Int("estado", rec.status),
			slog.Duration("duración", time.Since(start)),
		)
	})
}

type contextKey int

const (
	clientIPKey contextKey = iota
	userKey
	authMethodKey
)

// resolveClientIP solo cree X-Forwarded-For si la petición viene de un proxy de
// confianza. Caddy añade la IP real al final y descarta lo que envíe el cliente.
func (s *Server) resolveClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := remoteIP(r.RemoteAddr)
		if ip.IsValid() && containsIP(s.trustedProxies, ip) {
			if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
				parts := strings.Split(forwarded, ",")
				if parsed, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
					ip = parsed.Unmap()
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey, ip)))
	})
}

func remoteIP(addr string) netip.Addr {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return netip.Addr{}
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return ip.Unmap()
}

func containsIP(prefixes []netip.Prefix, ip netip.Addr) bool {
	return slices.ContainsFunc(prefixes, func(p netip.Prefix) bool { return p.Contains(ip) })
}

func clientIP(r *http.Request) netip.Addr {
	ip, _ := r.Context().Value(clientIPKey).(netip.Addr)
	return ip
}

func (s *Server) restrictIPs(next http.Handler) http.Handler {
	if len(s.allowedIPs) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !containsIP(s.allowedIPs, clientIP(r)) {
			http.Error(w, "Acceso no permitido desde esta dirección.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type authMethod int

const (
	authSession authMethod = iota + 1
	authToken
)

// identify averigua quién hace la petición, sin rechazar a nadie: eso lo deciden
// requireAPIUser y requireWebUser según la ruta.
func (s *Server) identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, method, err := s.authenticate(r)
		if err != nil {
			s.log.ErrorContext(r.Context(), "error al autenticar", slog.Any("error", err))
			http.Error(w, "Error interno.", http.StatusInternalServerError)
			return
		}
		ctx := r.Context()
		if method != 0 {
			ctx = context.WithValue(ctx, userKey, user)
			ctx = context.WithValue(ctx, authMethodKey, method)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) authenticate(r *http.Request) (vault.User, authMethod, error) {
	if header := r.Header.Get("Authorization"); header != "" {
		plain, ok := strings.CutPrefix(header, "Bearer ")
		if !ok {
			return vault.User{}, 0, nil
		}
		user, found, err := s.svc.UserByToken(r.Context(), strings.TrimSpace(plain))
		if err != nil || !found {
			return vault.User{}, 0, err
		}
		return user, authToken, nil
	}

	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return vault.User{}, 0, nil
	}
	id, ok := s.sessions.UserID(cookie.Value)
	if !ok {
		return vault.User{}, 0, nil
	}
	user, err := s.svc.GetUser(r.Context(), id)
	if err != nil {
		var ruleErr *rules.Error
		if errors.As(err, &ruleErr) && ruleErr.Code == rules.CodeNotFound {
			return vault.User{}, 0, nil
		}
		return vault.User{}, 0, err
	}
	if !user.IsActive || !s.sessions.Verify(cookie.Value, user.PasswordHash, s.now()) {
		return vault.User{}, 0, nil
	}
	return user, authSession, nil
}

func currentUser(r *http.Request) (vault.User, bool) {
	user, ok := r.Context().Value(userKey).(vault.User)
	return user, ok
}

func (s *Server) setSessionCookie(w http.ResponseWriter, user vault.User) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.sessions.Issue(user.ID, user.PasswordHash, s.now()),
		Path:     "/",
		MaxAge:   int(s.sessions.TTL().Seconds()),
		HttpOnly: true,
		Secure:   !s.insecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   !s.insecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

// login comprueba credenciales aplicando el límite de intentos. El mensaje no
// revela si existe el usuario.
func (s *Server) login(r *http.Request, username, password string) (vault.User, error) {
	ip := clientIP(r).String()
	now := s.now()
	if !s.limiter.Allow(ip, username, now) {
		return vault.User{}, errTooManyAttempts
	}
	user, ok, err := s.svc.Authenticate(r.Context(), username, password)
	if err != nil {
		return vault.User{}, fmt.Errorf("autenticar: %w", err)
	}
	if !ok {
		s.limiter.Fail(ip, username, now)
		return vault.User{}, errBadCredentials
	}
	s.limiter.Succeed(ip, username)
	return user, nil
}

var (
	errTooManyAttempts = errors.New("Demasiados intentos. Espera unos minutos.") //nolint:staticcheck // Se muestra al usuario.
	errBadCredentials  = errors.New("Usuario o contraseña incorrectos.")         //nolint:staticcheck // Se muestra al usuario.
)

// statusForCode traduce un rechazo de negocio a un código HTTP.
func statusForCode(code rules.Code) int {
	switch code {
	case rules.CodeNotFound:
		return http.StatusNotFound
	case rules.CodeForbidden, rules.CodeNotLockOwner:
		return http.StatusForbidden
	case rules.CodeLocked, rules.CodeLockedOtherClient, rules.CodeStaleVersion,
		rules.CodePathExists, rules.CodeNameExists, rules.CodeLastAdmin, rules.CodeProjectArchived:
		return http.StatusConflict
	case rules.CodeMissingChunks, rules.CodeManifestMismatch, rules.CodeInvalidManifest,
		rules.CodeInvalidComment, rules.CodeInvalidPath, rules.CodeExtensionChanged, rules.CodeInvalidInput:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadRequest
	}
}

// isCorruption indica un problema de datos que el administrador debe conocer.
func isCorruption(err error) bool {
	return errors.Is(err, store.ErrCorrupt) || errors.Is(err, store.ErrNotFound)
}
