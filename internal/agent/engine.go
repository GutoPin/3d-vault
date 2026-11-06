package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"

	"3d-vault/internal/client"
	"3d-vault/internal/rules"
	"3d-vault/internal/vault"
)

const (
	defaultStableFor = 5 * time.Second
	defaultPollEvery = 30 * time.Second
	defaultRescan    = 5 * time.Minute

	maxRecentActivity = 50
	renameAttempts    = 8
)

// Notifier muestra avisos al usuario. Un fallo al mostrarlos nunca afecta a la
// sincronización.
type Notifier interface {
	Notify(title, message string)
}

// Options configura un Engine.
type Options struct {
	Store    *Store
	Notifier Notifier
	Logger   *slog.Logger
	// HTTP permite sustituir el cliente HTTP en tests.
	HTTP *http.Client
	// StableFor es cuánto debe llevar un archivo sin cambiar para leerlo. Evita
	// tomar un archivo a medio guardar. Por defecto, 5 s.
	StableFor time.Duration
	// PollEvery es cada cuánto se consulta el servidor sin conexión de eventos.
	PollEvery time.Duration
	// Rescan es cada cuánto se revisa todo aunque no haya avisos: fsnotify puede
	// perder eventos.
	Rescan time.Duration
	// Rename permite simular archivos en uso en tests. Por defecto, os.Rename.
	Rename func(oldPath, newPath string) error
	// FreeSpace permite simular un disco lleno. Por defecto, diskFree.
	FreeSpace func(path string) (uint64, error)
	Now       func() time.Time
}

// State es el resumen de lo que hace el agente, para el ícono y el panel.
type State string

// Estados del agente.
const (
	StateSetup    State = "setup"
	StatePaused   State = "paused"
	StateOK       State = "ok"
	StateTransfer State = "transfer"
	StatePending  State = "pending"
	StateError    State = "error"
)

// Status es lo que muestra el panel.
type Status struct {
	State       State
	Message     string
	Online      bool
	CurrentFile string
	DoneBytes   int64
	TotalBytes  int64
	LastSync    time.Time
	Errors      []string
	Activity    []Activity
}

// Activity es una línea del registro reciente.
type Activity struct {
	At      time.Time
	Message string
}

// NewFile es un archivo de la carpeta que el servidor no conoce.
type NewFile struct {
	ProjectID   int64
	ProjectName string
	Path        string
	Size        int64
	// Problem explica por qué no se puede subir tal como está.
	Problem string
	// RenamedFrom es el archivo que falta y tiene el mismo contenido: probablemente
	// este sea el mismo archivo, renombrado desde el Explorador.
	RenamedFrom *FileRecord
}

// Engine mantiene la carpeta local igual que el servidor. Es seguro para uso
// concurrente: sincronizar se hace de uno en uno, y subir marca los archivos como
// ocupados para que la sincronización no los toque.
type Engine struct {
	store     *Store
	notifier  Notifier
	log       *slog.Logger
	http      *http.Client
	stableFor time.Duration
	pollEvery time.Duration
	rescan    time.Duration
	rename    func(oldPath, newPath string) error
	freeSpace func(path string) (uint64, error)
	now       func() time.Time

	wake      chan struct{}
	reconnect chan struct{}
	syncMu    sync.Mutex

	mu          sync.Mutex
	online      bool
	current     string
	lastSync    time.Time
	syncErrors  []string
	activity    []Activity
	busy        map[int64]bool
	busyPaths   map[string]bool
	snapshots   map[int64]vault.Snapshot
	newFiles    map[int64][]NewFile
	watchedDirs map[string]bool
	watcher     *fsnotify.Watcher
	doneBytes   atomic.Int64
	totalBytes  atomic.Int64
}

// NewEngine devuelve un Engine.
func NewEngine(opts Options) *Engine {
	e := &Engine{
		store:       opts.Store,
		notifier:    opts.Notifier,
		log:         opts.Logger,
		http:        opts.HTTP,
		stableFor:   opts.StableFor,
		pollEvery:   opts.PollEvery,
		rescan:      opts.Rescan,
		rename:      opts.Rename,
		freeSpace:   opts.FreeSpace,
		now:         opts.Now,
		wake:        make(chan struct{}, 1),
		reconnect:   make(chan struct{}, 1),
		busy:        make(map[int64]bool),
		busyPaths:   make(map[string]bool),
		snapshots:   make(map[int64]vault.Snapshot),
		newFiles:    make(map[int64][]NewFile),
		watchedDirs: make(map[string]bool),
	}
	if e.log == nil {
		e.log = slog.New(slog.DiscardHandler)
	}
	if e.stableFor == 0 {
		e.stableFor = defaultStableFor
	}
	if e.pollEvery == 0 {
		e.pollEvery = defaultPollEvery
	}
	if e.rescan == 0 {
		e.rescan = defaultRescan
	}
	if e.rename == nil {
		e.rename = os.Rename
	}
	if e.freeSpace == nil {
		e.freeSpace = diskFree
	}
	if e.now == nil {
		e.now = time.Now
	}
	return e
}

// Wake pide una sincronización. Varias peticiones seguidas cuentan como una.
func (e *Engine) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Reconnect reabre la conexión de eventos, por ejemplo al cambiar de proyectos.
func (e *Engine) Reconnect() {
	select {
	case e.reconnect <- struct{}{}:
	default:
	}
}

// Run sincroniza hasta que ctx termina.
func (e *Engine) Run(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		// Sin vigilancia del disco se sigue funcionando con los escaneos periódicos.
		e.log.WarnContext(ctx, "no se pudo vigilar la carpeta", slog.Any("error", err))
	} else {
		defer watcher.Close()
		e.mu.Lock()
		e.watcher = watcher
		e.mu.Unlock()
	}

	var wg sync.WaitGroup
	wg.Go(func() { e.streamLoop(ctx) })
	if watcher != nil {
		wg.Go(func() { e.watchLoop(ctx, watcher) })
	}
	wg.Go(func() { e.syncLoop(ctx) })
	e.Wake()
	wg.Wait()
	return ctx.Err()
}

func (e *Engine) syncLoop(ctx context.Context) {
	timer := time.NewTimer(e.rescan)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
		case <-timer.C:
		}
		if err := e.SyncNow(ctx); err != nil && ctx.Err() == nil {
			e.log.WarnContext(ctx, "fallo al sincronizar", slog.Any("error", err))
		}
		timer.Reset(e.interval())
	}
}

func (e *Engine) interval() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.online {
		return e.rescan
	}
	return e.pollEvery
}

// streamLoop mantiene abierta la conexión de eventos. Los eventos son solo avisos:
// cada uno provoca pedir el estado completo, de modo que perder uno no pierde nada.
func (e *Engine) streamLoop(ctx context.Context) {
	for ctx.Err() == nil {
		cfg, err := e.store.LoadConfig(ctx)
		if err != nil || !cfg.Ready() || cfg.Paused {
			e.setOnline(false)
			if !e.waitReconnect(ctx, e.pollEvery) {
				return
			}
			continue
		}
		ids, err := e.selectedIDs(ctx)
		if err != nil || len(ids) == 0 {
			e.setOnline(false)
			if !e.waitReconnect(ctx, e.pollEvery) {
				return
			}
			continue
		}

		streamCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			select {
			case <-e.reconnect:
				cancel()
			case <-streamCtx.Done():
			}
		}()
		// El saludo también despierta: lo ocurrido antes de conectar se recoge al
		// pedir el estado, y lo posterior llega por aquí.
		err = e.newClient(cfg).Stream(streamCtx, ids, func(string) {
			e.setOnline(true)
			e.Wake()
		})
		reconnectAsked := streamCtx.Err() != nil && ctx.Err() == nil
		cancel()
		<-done
		e.setOnline(false)
		if ctx.Err() != nil {
			return
		}
		if reconnectAsked {
			continue
		}
		e.log.InfoContext(ctx, "conexión de eventos cortada", slog.Any("error", err))
		e.Wake()
		if !e.waitReconnect(ctx, e.pollEvery) {
			return
		}
	}
}

func (e *Engine) waitReconnect(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
	case <-e.reconnect:
	}
	return true
}

func (e *Engine) watchLoop(ctx context.Context, watcher *fsnotify.Watcher) {
	var debounce <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if strings.Contains(filepath.ToSlash(event.Name), "/"+tmpDirName) {
				continue
			}
			debounce = time.After(e.stableFor + 100*time.Millisecond)
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			// Un desbordamiento pierde eventos: toca revisar todo.
			e.log.InfoContext(ctx, "aviso al vigilar la carpeta", slog.Any("error", err))
			e.Wake()
		case <-debounce:
			debounce = nil
			e.Wake()
		}
	}
}

func (e *Engine) selectedIDs(ctx context.Context) ([]int64, error) {
	projects, err := e.store.Projects(ctx)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, p := range projects {
		if p.Selected {
			ids = append(ids, p.ID)
		}
	}
	return ids, nil
}

func (e *Engine) setOnline(online bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.online = online
}

func (e *Engine) newClient(cfg Config) *client.Client {
	return client.New(client.Config{
		Server:     cfg.Server,
		Token:      cfg.Token,
		ClientID:   cfg.ClientID,
		ClientName: cfg.ClientName,
		HTTP:       e.http,
	})
}

func (e *Engine) note(ctx context.Context, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	e.log.InfoContext(ctx, message)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activity = append([]Activity{{At: e.now(), Message: message}}, e.activity...)
	if len(e.activity) > maxRecentActivity {
		e.activity = e.activity[:maxRecentActivity]
	}
}

func (e *Engine) notify(title, message string) {
	if e.notifier != nil {
		e.notifier.Notify(title, message)
	}
}

// Connect obtiene un token y lo guarda. La contraseña no se guarda en ningún sitio.
func (e *Engine) Connect(ctx context.Context, server, username, password string) error {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if err := checkServerURL(server); err != nil {
		return err
	}
	cfg, err := e.store.LoadConfig(ctx)
	if err != nil {
		return err
	}
	if cfg.ClientID == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return fmt.Errorf("generar el identificador de la PC: %w", err)
		}
		cfg.ClientID = hex.EncodeToString(raw[:])
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "PC"
	}

	anon := client.New(client.Config{Server: server, HTTP: e.http})
	token, user, err := anon.Login(ctx, username, password, hostname)
	if err != nil {
		return err
	}
	if cfg.UserID != 0 && cfg.UserID != user.ID {
		// Los registros de esta PC parten de lo que descargó otra cuenta, pero siguen
		// describiendo los mismos archivos: no hace falta borrarlos.
		e.log.InfoContext(ctx, "cambio de cuenta en esta PC")
	}
	cfg.Server, cfg.Token, cfg.ClientName = server, token, hostname
	cfg.UserID, cfg.Username, cfg.DisplayName = user.ID, user.Username, user.DisplayName
	if err := e.store.SaveConfig(ctx, cfg); err != nil {
		return err
	}
	e.Reconnect()
	e.Wake()
	return nil
}

// checkServerURL exige HTTPS, salvo en esta misma máquina: la contraseña y el token
// viajarían a la vista de cualquiera.
func checkServerURL(server string) error {
	parsed, err := url.Parse(server)
	if err != nil || parsed.Host == "" {
		return errors.New("la dirección del servidor no es válida; debe ser como https://3dvault.miempresa.com")
	}
	host := parsed.Hostname()
	local := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && local) {
		return errors.New("la dirección del servidor debe empezar por https://")
	}
	return nil
}

// RefreshProjects actualiza la lista de proyectos del servidor.
func (e *Engine) RefreshProjects(ctx context.Context) ([]Project, error) {
	cfg, err := e.store.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Connected() {
		return nil, errors.New("esta PC no está conectada")
	}
	remote, err := e.newClient(cfg).Projects(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range remote {
		if err := e.store.UpsertProject(ctx, p.ID, p.Name); err != nil {
			return nil, err
		}
	}
	return e.store.Projects(ctx)
}

// ProjectSize calcula lo que ocuparía sincronizar un proyecto.
func (e *Engine) ProjectSize(ctx context.Context, projectID int64) (files int, size int64, err error) {
	cfg, err := e.store.LoadConfig(ctx)
	if err != nil {
		return 0, 0, err
	}
	snap, err := e.newClient(cfg).Snapshot(ctx, projectID)
	if err != nil {
		return 0, 0, err
	}
	for _, a := range snap.Assets {
		size += a.SizeBytes
	}
	return len(snap.Assets), size, nil
}

// SetPaused pausa o reanuda la sincronización. Lo elegido se mantiene al reiniciar.
func (e *Engine) SetPaused(ctx context.Context, paused bool) error {
	cfg, err := e.store.LoadConfig(ctx)
	if err != nil {
		return err
	}
	cfg.Paused = paused
	if err := e.store.SaveConfig(ctx, cfg); err != nil {
		return err
	}
	e.Reconnect()
	e.Wake()
	return nil
}

// SyncNow sincroniza todos los proyectos elegidos y espera a que termine.
func (e *Engine) SyncNow(ctx context.Context) error {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()

	cfg, err := e.store.LoadConfig(ctx)
	if err != nil {
		return err
	}
	if !cfg.Ready() || cfg.Paused {
		return nil
	}
	c := e.newClient(cfg)
	projects, err := e.store.Projects(ctx)
	if err != nil {
		return err
	}

	var problems []string
	for _, p := range projects {
		if !p.Selected {
			continue
		}
		problems = append(problems, e.syncProject(ctx, cfg, c, p)...)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	e.mu.Lock()
	e.syncErrors = problems
	e.current = ""
	if len(problems) == 0 {
		e.lastSync = e.now()
	}
	e.mu.Unlock()
	e.doneBytes.Store(0)
	e.totalBytes.Store(0)
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// describeProblem convierte un error en un mensaje para el panel.
func describeProblem(err error) string {
	switch {
	case client.IsUnauthorized(err):
		return "La sesión ya no es válida. Vuelve a conectar esta PC en Ajustes."
	case errors.Is(err, context.Canceled):
		return "Operación cancelada."
	}
	var ruleErr *rules.Error
	if errors.As(err, &ruleErr) {
		return ruleErr.Message
	}
	var serverErr *client.ServerError
	if errors.As(err, &serverErr) {
		return serverErr.Error()
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "No se pudo conectar con el servidor. Se reintentará en un momento."
	}
	return err.Error()
}

// projectDir valida lo que envía el servidor antes de usarlo como ruta local.
func projectDir(cfg Config, name string) (string, error) {
	parsed, err := rules.NormalizePath(name)
	if err != nil || strings.Contains(parsed.Clean, "/") {
		return "", fmt.Errorf("el nombre de proyecto %q no se puede usar como carpeta", name)
	}
	return filepath.Join(cfg.Root, filepath.FromSlash(parsed.Clean)), nil
}

// localPath valida una ruta del servidor. Aunque el servidor ya las valida, un
// servidor comprometido no debe poder escribir fuera de la carpeta del proyecto.
func localPath(dir, remote string) (string, error) {
	parsed, err := rules.NormalizePath(remote)
	if err != nil {
		return "", fmt.Errorf("la ruta %q no es válida: %w", remote, err)
	}
	native := filepath.FromSlash(parsed.Clean)
	if !filepath.IsLocal(native) {
		return "", fmt.Errorf("la ruta %q no es válida", remote)
	}
	return filepath.Join(dir, native), nil
}

func (e *Engine) syncProject(ctx context.Context, cfg Config, c *client.Client, p Project) []string {
	dir, err := projectDir(cfg, p.Name)
	if err != nil {
		return []string{err.Error()}
	}
	snap, err := c.Snapshot(ctx, p.ID)
	if err != nil {
		return []string{describeProblem(err)}
	}
	if err := os.MkdirAll(filepath.Join(dir, tmpDirName), 0o750); err != nil {
		return []string{"No se pudo crear la carpeta del proyecto: " + err.Error()}
	}
	markHidden(filepath.Join(dir, tmpDirName))
	e.cleanTemp(dir)

	e.prepareProgress(ctx, snap)

	var problems []string
	report := func(name string, err error) {
		if ctx.Err() == nil {
			problem := name + ": " + describeProblem(err)
			problems = append(problems, problem)
			e.log.WarnContext(ctx, "fallo al sincronizar un archivo", slog.String("archivo", name), slog.Any("error", err))
		}
	}

	remote := make(map[int64]bool, len(snap.Assets))
	for _, asset := range snap.Assets {
		remote[asset.ID] = true
		if e.isBusy(asset.ID) {
			continue
		}
		if err := e.reconcileAsset(ctx, cfg, c, dir, p, asset); err != nil {
			report(asset.Path, err)
		}
		if ctx.Err() != nil {
			return problems
		}
	}

	records, err := e.store.Files(ctx, p.ID)
	if err != nil {
		return append(problems, err.Error())
	}
	for _, rec := range records {
		if remote[rec.AssetID] || e.isBusy(rec.AssetID) {
			continue
		}
		if err := e.reconcileRemoved(ctx, cfg, dir, p, rec); err != nil {
			report(rec.Path, err)
		}
	}

	if err := e.scanNewFiles(ctx, dir, p); err != nil {
		problems = append(problems, "No se pudo revisar la carpeta: "+err.Error())
	}

	e.mu.Lock()
	e.snapshots[p.ID] = snap
	e.mu.Unlock()
	return problems
}

// cleanTemp borra temporales de descargas interrumpidas. Si hay una subida o
// descarga en curso, su temporal no se puede borrar en Windows y aquí se ignora.
func (e *Engine) cleanTemp(dir string) {
	tmp := filepath.Join(dir, tmpDirName)
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return
	}
	for _, entry := range entries {
		_ = os.Remove(filepath.Join(tmp, entry.Name()))
	}
}

// prepareProgress calcula cuánto falta por bajar, para mostrar el total desde el
// principio.
func (e *Engine) prepareProgress(ctx context.Context, snap vault.Snapshot) {
	var total int64
	for _, asset := range snap.Assets {
		rec, found, err := e.store.File(ctx, asset.ID)
		if err == nil && (!found || rec.SHA256 != asset.FileSHA256) {
			total += asset.SizeBytes
		}
	}
	e.totalBytes.Add(total)
}

func (e *Engine) isBusy(assetID int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.busy[assetID]
}

func (e *Engine) mineHere(cfg Config, lock *rules.Lock) bool {
	return lock != nil && lock.UserID == cfg.UserID && lock.ClientID == cfg.ClientID
}

// observation es lo que se sabe del archivo en este momento.
type observation struct {
	exists bool
	// stable es false si el archivo cambió hace muy poco y puede estar guardándose.
	stable bool
	sha    string
	size   int64
	mtime  int64
}

// observe lee el estado del archivo. Solo calcula el hash si cambió desde la última
// vez, para que revisar la carpeta sea barato aunque tenga archivos de varios GB.
func (e *Engine) observe(target string, rec *FileRecord) (observation, error) {
	info, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return observation{stable: true}, nil
	}
	if err != nil {
		return observation{}, fmt.Errorf("consultar el archivo: %w", err)
	}
	obs := observation{exists: true, stable: true, size: info.Size(), mtime: info.ModTime().UnixNano()}
	if rec != nil && rec.LocalSHA != "" && rec.SeenSize == obs.size && rec.SeenMTime == obs.mtime {
		obs.sha = rec.LocalSHA
		return obs, nil
	}
	if e.now().Sub(info.ModTime()) < e.stableFor {
		obs.stable = false
		return obs, nil
	}
	obs.sha, err = hashFile(target)
	if err != nil {
		return observation{}, err
	}
	return obs, nil
}

// applyAccess deja el archivo editable solo si está bloqueado por este usuario en
// esta PC. Es una ayuda visual, no seguridad: evita editar sin bloquear por descuido.
func (e *Engine) applyAccess(target string, writable bool) {
	info, err := os.Stat(target)
	if err != nil {
		return
	}
	isWritable := info.Mode().Perm()&0o200 != 0
	if isWritable == writable {
		return
	}
	mode := os.FileMode(0o444)
	if writable {
		mode = 0o644
	}
	if err := os.Chmod(target, mode); err != nil {
		e.log.Warn("no se pudo cambiar el modo de solo lectura", slog.String("archivo", target), slog.Any("error", err))
	}
}

// reconcileAsset deja un archivo igual que en el servidor sin perder trabajo local.
func (e *Engine) reconcileAsset(ctx context.Context, cfg Config, c *client.Client, dir string, p Project, asset vault.Asset) error {
	target, err := localPath(dir, asset.Path)
	if err != nil {
		return err
	}
	rec, known, err := e.store.File(ctx, asset.ID)
	if err != nil {
		return err
	}
	writable := e.mineHere(cfg, asset.Lock)

	if !known {
		obs, err := e.observe(target, nil)
		if err != nil {
			return err
		}
		if !obs.stable {
			e.wakeLater()
			return nil
		}
		if obs.exists {
			if obs.sha == asset.FileSHA256 {
				// Ya estaba, por ejemplo tras reinstalar el agente: se adopta sin descargar.
				rec := e.recordFor(p, asset)
				rec.SeenSize, rec.SeenMTime, rec.LocalSHA = obs.size, obs.mtime, obs.sha
				e.applyAccess(target, writable)
				return e.store.PutFile(ctx, rec)
			}
			if err := e.keepCopy(ctx, cfg, p, asset.Path, target, "Ya había un archivo distinto en esa ruta."); err != nil {
				return err
			}
		}
		return e.download(ctx, cfg, c, dir, p, asset, target, nil, writable)
	}

	if rec.Path != asset.Path {
		if err := e.moveLocal(ctx, cfg, dir, p, &rec, asset.Path, target); err != nil {
			return err
		}
	}

	obs, err := e.observe(target, &rec)
	if err != nil {
		return err
	}
	if !obs.stable {
		e.wakeLater()
		return nil
	}

	if !obs.exists {
		if rec.SHA256 != asset.FileSHA256 {
			return e.download(ctx, cfg, c, dir, p, asset, target, &rec, writable)
		}
		// Nunca se propaga un borrado local: se avisa y el usuario decide.
		rec.Status = StatusMissing
		rec.Version, rec.VersionID = asset.HeadVersion, asset.HeadVersionID
		return e.store.PutFile(ctx, rec)
	}

	if rec.SHA256 == asset.FileSHA256 {
		rec.Version, rec.VersionID, rec.Size = asset.HeadVersion, asset.HeadVersionID, asset.SizeBytes
		e.recordObservation(&rec, obs)
		e.applyAccess(target, writable)
		return e.store.PutFile(ctx, rec)
	}

	// El servidor tiene otro contenido.
	if obs.sha == asset.FileSHA256 {
		rec = e.recordFor(p, asset)
		rec.SeenSize, rec.SeenMTime, rec.LocalSHA = obs.size, obs.mtime, obs.sha
		e.applyAccess(target, writable)
		return e.store.PutFile(ctx, rec)
	}
	modified := obs.sha != rec.SHA256
	// Un número de versión que no avanza con otro contenido indica que el servidor
	// volvió atrás, por ejemplo al restaurar un backup. Lo local puede ser más nuevo.
	rollback := asset.HeadVersion <= rec.Version
	switch {
	case modified:
		reason := fmt.Sprintf("Tenías cambios sin subir y llegó la v%d de %s.", asset.HeadVersion, asset.UploadedBy)
		if err := e.keepCopy(ctx, cfg, p, asset.Path, target, reason); err != nil {
			return err
		}
	case rollback:
		reason := fmt.Sprintf("El servidor volvió a la v%d y tu copia era de la v%d.", asset.HeadVersion, rec.Version)
		if err := e.keepCopy(ctx, cfg, p, asset.Path, target, reason); err != nil {
			return err
		}
	}
	return e.download(ctx, cfg, c, dir, p, asset, target, &rec, writable)
}

func (e *Engine) recordFor(p Project, asset vault.Asset) FileRecord {
	return FileRecord{
		AssetID:   asset.ID,
		ProjectID: p.ID,
		Path:      asset.Path,
		Version:   asset.HeadVersion,
		VersionID: asset.HeadVersionID,
		SHA256:    asset.FileSHA256,
		Size:      asset.SizeBytes,
		Status:    StatusSynced,
		UpdatedAt: e.now(),
	}
}

func (e *Engine) recordObservation(rec *FileRecord, obs observation) {
	rec.SeenSize, rec.SeenMTime, rec.LocalSHA = obs.size, obs.mtime, obs.sha
	if obs.sha == rec.SHA256 {
		rec.Status = StatusSynced
	} else {
		rec.Status = StatusChanged
	}
}

// wakeLater vuelve a revisar cuando el archivo debería haber dejado de cambiar.
func (e *Engine) wakeLater() {
	time.AfterFunc(e.stableFor+100*time.Millisecond, e.Wake)
}

// keepCopy aparta el archivo local con otro nombre. Es lo único que el agente hace
// con trabajo local que el servidor no tiene: nunca se sobrescribe ni se borra.
func (e *Engine) keepCopy(ctx context.Context, cfg Config, p Project, remotePath, target, reason string) error {
	copyPath := e.conflictName(target, cfg.Username)
	if err := e.chmodWritable(target); err != nil {
		return err
	}
	if err := e.renameRetry(ctx, target, copyPath); err != nil {
		return fmt.Errorf("apartar la copia local (si está abierta en otro programa, ciérralo): %w", err)
	}
	rel, err := filepath.Rel(filepath.Join(cfg.Root, p.Name), copyPath)
	if err != nil {
		rel = copyPath
	}
	if err := e.store.AddConflict(ctx, Conflict{
		ProjectID: p.ID,
		Path:      remotePath,
		CopyPath:  copyPath,
		Reason:    reason,
		CreatedAt: e.now(),
	}); err != nil {
		return err
	}
	e.note(ctx, "Conflicto en %s: tu copia se guardó como %s", remotePath, filepath.ToSlash(rel))
	e.notify("Conflicto en "+filepath.Base(target), "Tu copia se guardó como "+filepath.Base(copyPath)+". "+reason)
	return nil
}

// conflictName sigue el formato nombre.conflicto-<usuario>-<fecha>.ext y añade un
// número si ya existe, porque nunca se reemplaza una copia anterior.
func (e *Engine) conflictName(target, username string) string {
	dir, base := filepath.Split(target)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	stamp := e.now().Format("20060102-150405")
	safeUser := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, username)
	candidate := filepath.Join(dir, fmt.Sprintf("%s.conflicto-%s-%s%s", stem, safeUser, stamp, ext))
	for i := 2; ; i++ {
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s.conflicto-%s-%s-%d%s", stem, safeUser, stamp, i, ext))
	}
}

func (e *Engine) chmodWritable(target string) error {
	if err := os.Chmod(target, 0o644); err != nil {
		return fmt.Errorf("quitar solo lectura: %w", err)
	}
	return nil
}

// renameRetry reintenta porque en Windows un renombrado falla mientras algo tiene el
// archivo abierto, como la vista previa del Explorador o el antivirus.
func (e *Engine) renameRetry(ctx context.Context, oldPath, newPath string) error {
	delay := 100 * time.Millisecond
	var err error
	for attempt := 1; attempt <= renameAttempts; attempt++ {
		err = e.rename(oldPath, newPath)
		if err == nil {
			return nil
		}
		if attempt == renameAttempts {
			break
		}
		select {
		case <-time.After(delay):
			delay *= 2
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// moveLocal sigue un movimiento hecho en el servidor. Conserva lo que hubiera en el
// destino y, si el archivo tiene cambios sin subir, se mueven con él.
func (e *Engine) moveLocal(ctx context.Context, cfg Config, dir string, p Project, rec *FileRecord, newPath, target string) error {
	oldTarget, err := localPath(dir, rec.Path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(oldTarget); err == nil {
		sameFile := strings.EqualFold(oldTarget, target)
		if _, err := os.Stat(target); err == nil && !sameFile {
			if err := e.keepCopy(ctx, cfg, p, newPath, target, "Otro archivo se movió a esta ruta."); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return fmt.Errorf("crear la carpeta: %w", err)
		}
		if err := e.renameRetry(ctx, oldTarget, target); err != nil {
			return fmt.Errorf("mover el archivo: %w", err)
		}
		pruneEmptyDirs(filepath.Dir(oldTarget), dir)
		e.note(ctx, "%s se movió a %s", rec.Path, newPath)
	}
	rec.Path = newPath
	return e.store.PutFile(ctx, *rec)
}

// pruneEmptyDirs borra las carpetas vacías desde dir hacia arriba, sin pasar de stop.
func pruneEmptyDirs(dir, stop string) {
	for dir != stop && strings.HasPrefix(dir, stop) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// reconcileRemoved atiende un archivo que ya no está en el servidor. Solo se borra
// si coincide con la última versión conocida, que sigue guardada en el servidor.
func (e *Engine) reconcileRemoved(ctx context.Context, cfg Config, dir string, p Project, rec FileRecord) error {
	target, err := localPath(dir, rec.Path)
	if err != nil {
		return err
	}
	obs, err := e.observe(target, &rec)
	if err != nil {
		return err
	}
	if !obs.stable {
		e.wakeLater()
		return nil
	}
	if obs.exists {
		if obs.sha == rec.SHA256 {
			if err := e.chmodWritable(target); err != nil {
				return err
			}
			if err := os.Remove(target); err != nil {
				return fmt.Errorf("borrar el archivo: %w", err)
			}
			pruneEmptyDirs(filepath.Dir(target), dir)
			e.note(ctx, "%s se borró en el servidor", rec.Path)
		} else {
			reason := "El archivo se borró en el servidor y tenías cambios sin subir."
			if err := e.keepCopy(ctx, cfg, p, rec.Path, target, reason); err != nil {
				return err
			}
		}
	}
	return e.store.DeleteFile(ctx, rec.AssetID)
}

// minFreeMargin es el espacio que se deja libre además del archivo.
const minFreeMargin = 512 << 20

// download trae una versión y reemplaza el archivo solo tras verificarla.
func (e *Engine) download(ctx context.Context, cfg Config, c *client.Client, dir string, p Project, asset vault.Asset, target string, previous *FileRecord, writable bool) error {
	e.mu.Lock()
	e.current = asset.Path
	e.mu.Unlock()

	free, err := e.freeSpace(dir)
	if err == nil && free < uint64(asset.SizeBytes)+minFreeMargin {
		return fmt.Errorf("no hay espacio en el disco: faltan %s", formatSize(int64(uint64(asset.SizeBytes)+minFreeMargin-free)))
	}

	// El archivo local, si sigue ahí, sirve para no descargar lo que ya se tiene.
	var base *client.Scan
	var before os.FileInfo
	if info, statErr := os.Stat(target); statErr == nil {
		scan, err := client.ScanFile(target)
		if err != nil {
			return err
		}
		base, before = &scan, info
	}

	m, err := c.Manifest(ctx, asset.HeadVersionID)
	if err != nil {
		return err
	}
	tmpDir := filepath.Join(dir, tmpDirName)
	tmpPath, _, err := c.Assemble(ctx, m, tmpDir, target, base, func(n int64) { e.doneBytes.Add(n) })
	if err != nil {
		return err
	}
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	// Última comprobación, justo antes de reemplazar: alguien pudo guardar mientras
	// se descargaba. Sin esto, ese trabajo se perdería.
	if before != nil {
		now, err := os.Stat(target)
		if err == nil && (now.Size() != before.Size() || !now.ModTime().Equal(before.ModTime())) {
			reason := fmt.Sprintf("Guardaste el archivo mientras llegaba la v%d.", asset.HeadVersion)
			if err := e.keepCopy(ctx, cfg, p, asset.Path, target, reason); err != nil {
				return err
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("crear la carpeta: %w", err)
	}
	// En Windows no se puede reemplazar un archivo de solo lectura.
	if _, err := os.Stat(target); err == nil {
		if err := e.chmodWritable(target); err != nil {
			return err
		}
	}
	if err := e.renameRetry(ctx, tmpPath, target); err != nil {
		return fmt.Errorf("reemplazar el archivo (si está abierto en otro programa, ciérralo): %w", err)
	}
	removeTmp = false
	e.applyAccess(target, writable)

	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("consultar el archivo: %w", err)
	}
	rec := e.recordFor(p, asset)
	rec.SeenSize, rec.SeenMTime, rec.LocalSHA = info.Size(), info.ModTime().UnixNano(), asset.FileSHA256
	if err := e.store.PutFile(ctx, rec); err != nil {
		return err
	}

	if previous != nil {
		name := filepath.Base(target)
		message := fmt.Sprintf("%s actualizado a v%d por %s", name, asset.HeadVersion, asset.UploadedBy)
		e.note(ctx, "%s", message)
		e.notify(message, "Si lo tienes abierto, usa Archivo → Revertir.")
	} else {
		e.note(ctx, "%s descargado (v%d)", asset.Path, asset.HeadVersion)
	}
	return nil
}

// scanNewFiles busca archivos que el servidor no conoce y registra las carpetas para
// vigilarlas, porque fsnotify no vigila de forma recursiva.
func (e *Engine) scanNewFiles(ctx context.Context, dir string, p Project) error {
	records, err := e.store.Files(ctx, p.ID)
	if err != nil {
		return err
	}
	tracked := make(map[string]bool, len(records))
	var missing []FileRecord
	for _, rec := range records {
		if parsed, err := rules.NormalizePath(rec.Path); err == nil {
			tracked[parsed.Key] = true
		}
		if rec.Status == StatusMissing {
			missing = append(missing, rec)
		}
	}

	var found []NewFile
	ignore := loadIgnoreRules(dir)
	err = walkProject(dir, ignore, e.watchDir, func(rel string, info os.FileInfo) {
		parsed, parseErr := rules.NormalizePath(rel)
		file := NewFile{ProjectID: p.ID, ProjectName: p.Name, Path: rel, Size: info.Size()}
		if parseErr != nil {
			var ruleErr *rules.Error
			if errors.As(parseErr, &ruleErr) {
				file.Problem = ruleErr.Message
			} else {
				file.Problem = parseErr.Error()
			}
			found = append(found, file)
			return
		}
		if tracked[parsed.Key] {
			return
		}
		for i := range missing {
			if missing[i].Size != info.Size() {
				continue
			}
			sha, err := hashFile(filepath.Join(dir, filepath.FromSlash(rel)))
			if err == nil && sha == missing[i].SHA256 {
				file.RenamedFrom = &missing[i]
				break
			}
		}
		found = append(found, file)
	})
	if err != nil {
		return err
	}
	slices.SortFunc(found, func(a, b NewFile) int { return strings.Compare(a.Path, b.Path) })

	e.mu.Lock()
	e.newFiles[p.ID] = found
	e.mu.Unlock()
	return nil
}

func (e *Engine) watchDir(dir string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.watcher == nil || e.watchedDirs[dir] {
		return
	}
	if err := e.watcher.Add(dir); err != nil {
		e.log.Info("no se pudo vigilar una carpeta", slog.String("carpeta", dir), slog.Any("error", err))
		return
	}
	e.watchedDirs[dir] = true
}

func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	value := fmt.Sprintf("%.1f", float64(n)/float64(div))
	return strings.Replace(value, ".", ",", 1) + " " + string("KMGT"[exp]) + "B"
}
