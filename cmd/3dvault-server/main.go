// Comando 3dvault-server: la API y la web de 3D-Vault, y sus comandos de
// administración.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // La imagen no incluye zonas horarias y la variable TZ las necesita.

	"3d-vault/internal/auth"
	"3d-vault/internal/db"
	"3d-vault/internal/events"
	"3d-vault/internal/maintenance"
	"3d-vault/internal/server"
	"3d-vault/internal/store"
	"3d-vault/internal/thumbs"
	"3d-vault/internal/vault"
)

const usage = `Uso: 3dvault-server [comando]

Comandos:
  serve                       inicia el servidor (por defecto)
  create-admin                crea una cuenta de administrador
  backup --to RUTA            copia la base y los trozos, con el servidor en marcha
  restore --from RUTA         restaura una copia, con el servidor detenido
  check                       verifica el hash de todos los trozos
  purge [--keep-last N] [--older-than EDAD] [--apply]
                              borra versiones viejas y archivos borrados hace tiempo,
                              con el servidor detenido. Conserva la versión actual,
                              las N últimas (10) y las más nuevas que EDAD (180d).
                              Sin --apply solo muestra lo que haría
  gc [--apply]                borra los trozos que ya no usa ninguna versión, con el
                              servidor detenido. Sin --apply solo muestra lo que haría
  healthcheck                 comprueba que el servidor responde
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "serve"
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch command {
	case "serve":
		return serve(ctx)
	case "create-admin":
		return createAdmin(ctx, args)
	case "backup":
		return backup(ctx, args)
	case "restore":
		return restore(ctx, args)
	case "check":
		return check(ctx)
	case "purge":
		return purge(ctx, args)
	case "gc":
		return gc(ctx, args)
	case "healthcheck":
		return healthcheck(ctx)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("comando desconocido %q\n\n%s", command, usage)
	}
}

// config es la configuración del .env (sección 11 de PLAN.md).
type config struct {
	secretKey       string
	dataDir         string
	listenAddr      string
	maxFileSize     int64
	sessionTTL      time.Duration
	allowedIPs      []netip.Prefix
	trustedProxies  []netip.Prefix
	insecureCookies bool
}

const minSecretKey = 32

func dataDir() string {
	if dir := os.Getenv("DATA_DIR"); dir != "" {
		return dir
	}
	return "/data"
}

func listenAddr() string {
	if addr := os.Getenv("LISTEN_ADDR"); addr != "" {
		return addr
	}
	return ":8080"
}

func loadConfig() (config, error) {
	cfg := config{dataDir: dataDir(), listenAddr: listenAddr(), secretKey: os.Getenv("SECRET_KEY")}
	if len(cfg.secretKey) < minSecretKey {
		return config{}, fmt.Errorf("SECRET_KEY debe tener al menos %d caracteres", minSecretKey)
	}

	gigabytes, err := envInt("MAX_FILE_SIZE_GB", 20)
	if err != nil {
		return config{}, err
	}
	cfg.maxFileSize = gigabytes << 30

	hours, err := envInt("SESSION_HOURS", 12)
	if err != nil {
		return config{}, err
	}
	cfg.sessionTTL = time.Duration(hours) * time.Hour

	if cfg.allowedIPs, err = parsePrefixes(os.Getenv("ALLOWED_IPS")); err != nil {
		return config{}, fmt.Errorf("ALLOWED_IPS: %w", err)
	}
	if cfg.trustedProxies, err = parsePrefixes(os.Getenv("TRUSTED_PROXIES")); err != nil {
		return config{}, fmt.Errorf("TRUSTED_PROXIES: %w", err)
	}
	cfg.insecureCookies = os.Getenv("INSECURE_COOKIES") == "true"
	return cfg, nil
}

func envInt(name string, fallback int64) (int64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s debe ser un número positivo", name)
	}
	return value, nil
}

// parsePrefixes acepta IP sueltas y rangos separados por comas.
func parsePrefixes(raw string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for item := range strings.SplitSeq(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(item); err == nil {
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(item)
		if err != nil {
			return nil, fmt.Errorf("%q no es una IP ni un rango", item)
		}
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes, nil
}

// openData abre la base y el almacén de dir.
func openData(ctx context.Context, dir string) (*db.DB, *store.Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, fmt.Errorf("crear la carpeta de datos: %w", err)
	}
	database, err := db.Open(ctx, filepath.Join(dir, maintenance.DatabaseFile))
	if err != nil {
		return nil, nil, err
	}
	chunks, err := store.New(filepath.Join(dir, maintenance.ChunksDir))
	if err != nil {
		_ = database.Close()
		return nil, nil, err
	}
	return database, chunks, nil
}

const shutdownTimeout = 30 * time.Second

// thumbsDir guarda las miniaturas dentro de DATA_DIR. Es una caché: no va en los
// backups y se regenera al pedirlas.
const thumbsDir = "thumbs"

func serve(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Mientras dure, restore y las purgas se niegan a tocar los datos.
	lock, err := maintenance.LockDataDir(cfg.dataDir)
	if err != nil {
		return err
	}
	defer lock.Close()

	database, chunks, err := openData(ctx, cfg.dataDir)
	if err != nil {
		return err
	}
	defer database.Close()
	defer chunks.Close()
	if err := chunks.CleanTemp(); err != nil {
		return err
	}

	thumbCache, err := thumbs.NewCache(filepath.Join(cfg.dataDir, thumbsDir))
	if err != nil {
		return err
	}
	svc := vault.New(vault.Config{
		DB:          database,
		Store:       chunks,
		Hub:         events.NewHub(),
		MaxFileSize: cfg.maxFileSize,
		Argon:       auth.DefaultParams,
		Thumbs:      thumbCache,
	})
	// Antes de cerrar el almacén: las miniaturas en curso leen trozos.
	defer svc.WaitBackground()

	hasAdmin, err := svc.HasAdmin(ctx)
	if err != nil {
		return err
	}
	if !hasAdmin {
		logger.WarnContext(ctx, "no hay ningún administrador: créalo con create-admin")
	}

	handler, err := server.New(server.Config{
		Service:         svc,
		Sessions:        auth.NewSessions([]byte(cfg.secretKey), cfg.sessionTTL),
		Limiter:         auth.NewLimiter(),
		Logger:          logger,
		AllowedIPs:      cfg.allowedIPs,
		TrustedProxies:  cfg.trustedProxies,
		InsecureCookies: cfg.insecureCookies,
	})
	if err != nil {
		return err
	}

	// Cancelar este contexto corta las conexiones SSE, que nunca terminan solas.
	baseCtx, cancelBase := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelBase()

	// Sin plazos globales de lectura o escritura: SSE, las descargas y la
	// verificación de un archivo grande duran mucho. Cada ruta fija los suyos.
	srv := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
		BaseContext:       func(_ net.Listener) context.Context { return baseCtx },
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.InfoContext(ctx, "servidor iniciado", slog.String("dirección", cfg.listenAddr))
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("el servidor se detuvo: %w", err)
	case <-ctx.Done():
	}

	logger.InfoContext(baseCtx, "deteniendo el servidor")
	cancelBase()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("detener el servidor: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("el servidor se detuvo: %w", err)
	}
	return nil
}

func createAdmin(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("create-admin", flag.ContinueOnError)
	username := flags.String("username", "", "nombre de usuario")
	display := flags.String("name", "", "nombre visible")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		return errors.New("falta --username")
	}
	password, err := readPassword("Contraseña: ")
	if err != nil {
		return err
	}

	database, chunks, err := openData(ctx, dataDir())
	if err != nil {
		return err
	}
	defer database.Close()
	defer chunks.Close()

	svc := vault.New(vault.Config{DB: database, Store: chunks, Hub: events.NewHub(), Argon: auth.DefaultParams})
	user, err := svc.CreateUser(ctx, 0, vault.NewUserInput{Username: *username, DisplayName: *display, Password: password, IsAdmin: true})
	if err != nil {
		return err
	}
	fmt.Printf("Administrador «%s» creado.\n", user.Username)
	return nil
}

func backup(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	to := flags.String("to", "", "carpeta de destino")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *to == "" {
		return errors.New("falta --to")
	}
	database, chunks, err := openData(ctx, dataDir())
	if err != nil {
		return err
	}
	defer database.Close()
	defer chunks.Close()

	stats, err := maintenance.Backup(ctx, database, chunks, *to)
	if err != nil {
		return err
	}
	fmt.Printf("Copia terminada en %s: %d trozos copiados, %d ya estaban.\n", *to, stats.Copied, stats.Skipped)
	return nil
}

func restore(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	from := flags.String("from", "", "carpeta de la copia")
	force := flags.Bool("force", false, "aparta la base existente y restaura")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *from == "" {
		return errors.New("falta --from")
	}
	dir := dataDir()
	lock, err := maintenance.LockDataDir(dir)
	if err != nil {
		return err
	}
	defer lock.Close()

	stats, err := maintenance.Restore(ctx, *from, dir, *force, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("Copia restaurada: %d trozos copiados, %d ya estaban. Comprueba el resultado con check.\n", stats.Copied, stats.Skipped)
	return nil
}

func check(ctx context.Context) error {
	database, chunks, err := openData(ctx, dataDir())
	if err != nil {
		return err
	}
	defer database.Close()
	defer chunks.Close()

	report, err := maintenance.Check(ctx, database, chunks)
	if err != nil {
		return err
	}
	fmt.Printf("Trozos verificados: %d\nCorruptos: %d\nFaltantes: %d\nSin registrar (inofensivos): %d\n",
		report.Checked, len(report.Corrupt), len(report.Missing), report.Orphans)
	for _, hash := range report.Corrupt {
		fmt.Println("corrupto:", hash)
	}
	for _, hash := range report.Missing {
		fmt.Println("faltante:", hash)
	}
	if !report.OK() {
		return errors.New("hay trozos dañados: restaura una copia de seguridad")
	}
	return nil
}

// openLocked abre los datos con el servidor detenido, como piden purge y gc.
func openLocked(ctx context.Context) (*db.DB, *store.Store, func(), error) {
	dir := dataDir()
	lock, err := maintenance.LockDataDir(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	database, chunks, err := openData(ctx, dir)
	if err != nil {
		_ = lock.Close()
		return nil, nil, nil, err
	}
	return database, chunks, func() {
		_ = chunks.Close()
		_ = database.Close()
		_ = lock.Close()
	}, nil
}

func purge(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("purge", flag.ContinueOnError)
	keepLast := flags.Int("keep-last", 10, "versiones más nuevas que se conservan siempre")
	olderThan := flags.String("older-than", "180d", "solo borra lo más viejo que esto, en días (180d) u horas (12h)")
	apply := flags.Bool("apply", false, "borra de verdad; sin esto solo simula")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *keepLast < 0 {
		return errors.New("--keep-last no puede ser negativo")
	}
	age, err := parseAge(*olderThan)
	if err != nil {
		return err
	}

	// Purge solo toca la base: los trozos los borra gc.
	database, _, closeAll, err := openLocked(ctx)
	if err != nil {
		return err
	}
	defer closeAll()

	report, err := maintenance.Purge(ctx, database, *keepLast, time.Now().Add(-age), *apply)
	if err != nil {
		return err
	}
	verb := "Se quitarían"
	if *apply {
		verb = "Quitadas"
	}
	fmt.Printf("%s: %d versiones, %d archivos borrados y %d manifiestos.\n", verb, report.Versions, report.Files, report.Manifests)
	fmt.Printf("Trozos sin usar después: %d, unos %s. Los borra gc --apply.\n", report.Chunks, gib(report.Bytes))
	if !*apply {
		fmt.Println("Es una simulación: no se ha cambiado nada. Repite con --apply para purgar.")
	}
	return nil
}

func gc(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("gc", flag.ContinueOnError)
	apply := flags.Bool("apply", false, "borra de verdad; sin esto solo simula")
	if err := flags.Parse(args); err != nil {
		return err
	}
	database, chunks, closeAll, err := openLocked(ctx)
	if err != nil {
		return err
	}
	defer closeAll()

	report, err := maintenance.GC(ctx, database, chunks, *apply)
	if err != nil {
		return err
	}
	if *apply {
		fmt.Printf("Trozos borrados: %d, %s liberados.\n", report.Chunks, gib(report.Bytes))
		return nil
	}
	fmt.Printf("Trozos sin usar: %d, %s. Es una simulación: repite con --apply para borrarlos.\n", report.Chunks, gib(report.Bytes))
	return nil
}

// parseAge acepta días ("180d") además de las duraciones de Go ("12h").
func parseAge(text string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(text, "d"); ok {
		n, err := strconv.Atoi(days)
		if err == nil && n >= 0 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	} else if d, err := time.ParseDuration(text); err == nil && d >= 0 {
		return d, nil
	}
	return 0, fmt.Errorf("--older-than %q no es válido: usa días, como 180d", text)
}

func gib(n int64) string {
	return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
}

func healthcheck(ctx context.Context) error {
	_, port, err := net.SplitHostPort(listenAddr())
	if err != nil {
		return fmt.Errorf("LISTEN_ADDR no es válida: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("respuesta inesperada: %d", resp.StatusCode)
	}
	return nil
}
