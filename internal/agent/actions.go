package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"3d-vault/internal/client"
	"3d-vault/internal/rules"
	"3d-vault/internal/vault"
)

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("abrir el archivo: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("leer el archivo: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FileView es un archivo tal como lo muestra el panel.
type FileView struct {
	AssetID      int64
	ProjectID    int64
	ProjectName  string
	Path         string
	LocalPath    string
	LocalVersion int64
	HeadVersion  int64
	SizeBytes    int64
	Status       string
	Lock         *rules.Lock
	// LockedHere: bloqueado por este usuario en esta PC.
	LockedHere bool
	// LockedElsewhere: bloqueado por este usuario en otra PC.
	LockedElsewhere bool
	Busy            bool
}

// Files devuelve los archivos conocidos de los proyectos elegidos.
func (e *Engine) Files(ctx context.Context) ([]FileView, error) {
	cfg, err := e.store.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	projects, err := e.store.Projects(ctx)
	if err != nil {
		return nil, err
	}

	views := []FileView{}
	for _, p := range projects {
		if !p.Selected {
			continue
		}
		e.mu.Lock()
		snap, hasSnapshot := e.snapshots[p.ID]
		e.mu.Unlock()

		records, err := e.store.Files(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		byID := make(map[int64]FileRecord, len(records))
		for _, rec := range records {
			byID[rec.AssetID] = rec
		}

		dir, err := projectDir(cfg, p.Name)
		if err != nil {
			continue
		}
		seen := make(map[int64]bool)
		if hasSnapshot {
			for _, asset := range snap.Assets {
				seen[asset.ID] = true
				views = append(views, e.viewOf(cfg, p, dir, asset, byID))
			}
		}
		// Sin conexión todavía no hay estado del servidor, pero el trabajo local se
		// sigue mostrando.
		for _, rec := range records {
			if seen[rec.AssetID] {
				continue
			}
			views = append(views, FileView{
				AssetID: rec.AssetID, ProjectID: p.ID, ProjectName: p.Name, Path: rec.Path,
				LocalPath: filepath.Join(dir, filepath.FromSlash(rec.Path)), LocalVersion: rec.Version,
				HeadVersion: rec.Version, SizeBytes: rec.Size, Status: rec.Status, Busy: e.isBusy(rec.AssetID),
			})
		}
	}
	slices.SortFunc(views, func(a, b FileView) int {
		if c := strings.Compare(a.ProjectName, b.ProjectName); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(a.Path), strings.ToLower(b.Path))
	})
	return views, nil
}

func (e *Engine) viewOf(cfg Config, p Project, dir string, asset vault.Asset, records map[int64]FileRecord) FileView {
	view := FileView{
		AssetID:     asset.ID,
		ProjectID:   p.ID,
		ProjectName: p.Name,
		Path:        asset.Path,
		LocalPath:   filepath.Join(dir, filepath.FromSlash(asset.Path)),
		HeadVersion: asset.HeadVersion,
		SizeBytes:   asset.SizeBytes,
		Lock:        asset.Lock,
		Busy:        e.isBusy(asset.ID),
	}
	if rec, ok := records[asset.ID]; ok {
		view.LocalVersion = rec.Version
		view.Status = rec.Status
	}
	if asset.Lock != nil && asset.Lock.UserID == cfg.UserID {
		view.LockedHere = asset.Lock.ClientID == cfg.ClientID
		view.LockedElsewhere = !view.LockedHere
	}
	return view
}

// NewFiles devuelve los archivos locales que el servidor no conoce.
func (e *Engine) NewFiles() []NewFile {
	e.mu.Lock()
	defer e.mu.Unlock()
	all := []NewFile{}
	for _, files := range e.newFiles {
		all = append(all, files...)
	}
	slices.SortFunc(all, func(a, b NewFile) int {
		if c := strings.Compare(a.ProjectName, b.ProjectName); c != 0 {
			return c
		}
		return strings.Compare(a.Path, b.Path)
	})
	return all
}

// CurrentStatus devuelve el resumen para el panel y el ícono.
func (e *Engine) CurrentStatus(ctx context.Context) Status {
	cfg, err := e.store.LoadConfig(ctx)
	status := Status{}
	e.mu.Lock()
	status.Online = e.online
	status.CurrentFile = e.current
	status.LastSync = e.lastSync
	status.Errors = slices.Clone(e.syncErrors)
	status.Activity = slices.Clone(e.activity)
	busy := len(e.busy) + len(e.busyPaths)
	e.mu.Unlock()
	status.DoneBytes = e.doneBytes.Load()
	status.TotalBytes = e.totalBytes.Load()

	pending := 0
	if files, err := e.Files(ctx); err == nil {
		for _, f := range files {
			if f.Status == StatusChanged || f.Status == StatusMissing {
				pending++
			}
		}
	}
	pending += len(e.NewFiles())
	if conflicts, err := e.store.Conflicts(ctx); err == nil {
		pending += len(conflicts)
	}

	switch {
	case err != nil:
		status.State, status.Message = StateError, err.Error()
	case !cfg.Ready():
		status.State, status.Message = StateSetup, "Falta configurar esta PC."
	case cfg.Paused:
		status.State, status.Message = StatePaused, "Sincronización en pausa."
	case len(status.Errors) > 0:
		status.State, status.Message = StateError, "Hay problemas para sincronizar."
	case status.CurrentFile != "" || busy > 0:
		status.State, status.Message = StateTransfer, "Transfiriendo archivos."
	case pending > 0:
		status.State, status.Message = StatePending, "Todo sincronizado, con cambios pendientes de revisar."
	default:
		status.State, status.Message = StateOK, "Todo sincronizado."
	}
	return status
}

// clientFor devuelve el cliente de la cuenta conectada.
func (e *Engine) clientFor(ctx context.Context) (Config, *client.Client, error) {
	cfg, err := e.store.LoadConfig(ctx)
	if err != nil {
		return Config{}, nil, err
	}
	if !cfg.Ready() {
		return Config{}, nil, errors.New("esta PC aún no está configurada")
	}
	return cfg, e.newClient(cfg), nil
}

// LockResult es el resultado de bloquear.
type LockResult struct {
	// Warning es un aviso para el usuario, si hay algo que debe tener presente.
	Warning string
}

// recentUpdate es cuánto tiempo se considera que Blender puede tener abierta una
// versión anterior de un archivo que se acaba de actualizar.
const recentUpdate = 8 * time.Hour

// Lock bloquea un archivo y sincroniza para dejarlo editable en su última versión.
func (e *Engine) Lock(ctx context.Context, assetID int64, takeover bool) (LockResult, error) {
	_, c, err := e.clientFor(ctx)
	if err != nil {
		return LockResult{}, err
	}
	if _, err := c.Lock(ctx, assetID, takeover); err != nil {
		return LockResult{}, err
	}
	if err := e.SyncNow(ctx); err != nil {
		e.log.WarnContext(ctx, "fallo al sincronizar tras bloquear")
	}

	var result LockResult
	rec, found, err := e.store.File(ctx, assetID)
	if err != nil {
		return result, err
	}
	// Blender no nota que el archivo cambió: guardar desde una sesión abierta antes
	// de la última actualización perdería esa versión.
	if found && e.now().Sub(rec.UpdatedAt) < recentUpdate {
		result.Warning = "Este archivo se actualizó hace poco. Si ya lo tenías abierto en Blender, usa Archivo → Revertir antes de editar."
	}
	return result, nil
}

// Unlock libera un bloqueo propio y vuelve a dejar el archivo en solo lectura.
func (e *Engine) Unlock(ctx context.Context, assetID int64) error {
	_, c, err := e.clientFor(ctx)
	if err != nil {
		return err
	}
	if err := c.Unlock(ctx, assetID); err != nil {
		return err
	}
	if err := e.SyncNow(ctx); err != nil {
		e.log.WarnContext(ctx, "fallo al sincronizar tras liberar")
	}
	return nil
}

func (e *Engine) claim(assetID int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.busy[assetID] {
		return false
	}
	e.busy[assetID] = true
	return true
}

func (e *Engine) release(assetID int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.busy, assetID)
}

// ErrBusy indica que el archivo ya se está transfiriendo.
var ErrBusy = errors.New("este archivo ya se está transfiriendo")

// Upload sube los cambios de un archivo como una versión nueva. Parte de la versión
// que esta PC tenía: si otro subió entretanto, el servidor lo rechaza en lugar de
// reemplazar su trabajo.
func (e *Engine) Upload(ctx context.Context, assetID int64, comment string, release bool) (vault.CommitResult, error) {
	if _, err := rules.CheckComment(comment); err != nil {
		return vault.CommitResult{}, err
	}
	if !e.claim(assetID) {
		return vault.CommitResult{}, ErrBusy
	}
	defer e.release(assetID)

	cfg, c, err := e.clientFor(ctx)
	if err != nil {
		return vault.CommitResult{}, err
	}
	rec, found, err := e.store.File(ctx, assetID)
	if err != nil {
		return vault.CommitResult{}, err
	}
	if !found {
		return vault.CommitResult{}, errors.New("este archivo no está en esta PC")
	}
	dir, target, err := e.pathsOf(ctx, cfg, rec)
	if err != nil {
		return vault.CommitResult{}, err
	}
	_ = dir

	// Se mira antes de leer: si cambia durante la subida, la fecha ya no coincidirá
	// y el archivo volverá a marcarse como pendiente.
	info, err := os.Stat(target)
	if err != nil {
		return vault.CommitResult{}, fmt.Errorf("consultar el archivo: %w", err)
	}
	scan, err := client.ScanFile(target)
	if err != nil {
		return vault.CommitResult{}, err
	}
	e.totalBytes.Add(scan.Manifest.Size())
	defer e.finishTransfer(scan.Manifest.Size())
	if _, err := c.UploadChunks(ctx, target, scan, func(n int64) { e.doneBytes.Add(n) }); err != nil {
		return vault.CommitResult{}, err
	}
	result, err := c.CommitVersion(ctx, client.CommitInput{
		AssetID:     assetID,
		BaseVersion: rec.Version,
		Manifest:    scan.Manifest,
		Comment:     comment,
		ReleaseLock: release,
	})
	if err != nil {
		return vault.CommitResult{}, err
	}

	sha := scan.Manifest.FileSHA256.String()
	rec.Version, rec.VersionID = result.Asset.HeadVersion, result.Asset.HeadVersionID
	rec.SHA256, rec.Size = sha, scan.Manifest.Size()
	rec.SeenSize, rec.SeenMTime, rec.LocalSHA = info.Size(), info.ModTime().UnixNano(), sha
	rec.Status, rec.UpdatedAt = StatusSynced, e.now()
	if err := e.store.PutFile(ctx, rec); err != nil {
		return vault.CommitResult{}, err
	}
	if result.NoChanges {
		e.note(ctx, "%s: no hay cambios que subir", rec.Path)
	} else {
		e.note(ctx, "%s subido como v%d", rec.Path, result.Version.Number)
	}
	e.Wake()
	return result, nil
}

// finishTransfer deja el progreso limpio tras una subida, aunque falle.
func (e *Engine) finishTransfer(size int64) {
	e.totalBytes.Add(-size)
	if e.totalBytes.Load() <= 0 {
		e.totalBytes.Store(0)
		e.doneBytes.Store(0)
	}
}

func (e *Engine) pathsOf(ctx context.Context, cfg Config, rec FileRecord) (dir, target string, err error) {
	name, err := e.projectName(ctx, rec.ProjectID)
	if err != nil {
		return "", "", err
	}
	dir, err = projectDir(cfg, name)
	if err != nil {
		return "", "", err
	}
	target, err = localPath(dir, rec.Path)
	return dir, target, err
}

func (e *Engine) projectName(ctx context.Context, id int64) (string, error) {
	projects, err := e.store.Projects(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range projects {
		if p.ID == id {
			return p.Name, nil
		}
	}
	return "", errors.New("proyecto desconocido")
}

// AddNew sube un archivo nuevo de la carpeta del proyecto.
func (e *Engine) AddNew(ctx context.Context, projectID int64, relPath, comment string) (vault.CommitResult, error) {
	if _, err := rules.CheckComment(comment); err != nil {
		return vault.CommitResult{}, err
	}
	path, err := rules.NormalizePath(relPath)
	if err != nil {
		return vault.CommitResult{}, err
	}
	cfg, c, err := e.clientFor(ctx)
	if err != nil {
		return vault.CommitResult{}, err
	}
	name, err := e.projectName(ctx, projectID)
	if err != nil {
		return vault.CommitResult{}, err
	}
	dir, err := projectDir(cfg, name)
	if err != nil {
		return vault.CommitResult{}, err
	}
	// La ruta llega del formulario del panel: solo se aceptan archivos de la carpeta.
	target, err := localPath(dir, relPath)
	if err != nil {
		return vault.CommitResult{}, err
	}

	e.mu.Lock()
	if e.busyPaths[path.Key] {
		e.mu.Unlock()
		return vault.CommitResult{}, ErrBusy
	}
	e.busyPaths[path.Key] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.busyPaths, path.Key)
		e.mu.Unlock()
	}()

	info, err := os.Stat(target)
	if err != nil {
		return vault.CommitResult{}, fmt.Errorf("consultar el archivo: %w", err)
	}
	scan, err := client.ScanFile(target)
	if err != nil {
		return vault.CommitResult{}, err
	}
	e.totalBytes.Add(scan.Manifest.Size())
	defer e.finishTransfer(scan.Manifest.Size())
	if _, err := c.UploadChunks(ctx, target, scan, func(n int64) { e.doneBytes.Add(n) }); err != nil {
		return vault.CommitResult{}, err
	}
	result, err := c.CreateAsset(ctx, projectID, path.Clean, scan.Manifest, comment)
	if err != nil {
		return vault.CommitResult{}, err
	}

	rec := e.recordFor(Project{ID: projectID}, result.Asset)
	rec.SeenSize, rec.SeenMTime, rec.LocalSHA = info.Size(), info.ModTime().UnixNano(), scan.Manifest.FileSHA256.String()
	if err := e.store.PutFile(ctx, rec); err != nil {
		return vault.CommitResult{}, err
	}
	e.note(ctx, "%s agregado", path.Clean)
	if err := e.SyncNow(ctx); err != nil {
		e.log.WarnContext(ctx, "fallo al sincronizar tras agregar")
	}
	return result, nil
}

// RecoverMissing vuelve a descargar un archivo que falta en la carpeta.
func (e *Engine) RecoverMissing(ctx context.Context, assetID int64) error {
	rec, found, err := e.store.File(ctx, assetID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("este archivo no está en esta PC")
	}
	// Sin registro, el archivo se trata como nuevo en esta PC y se descarga. Si
	// mientras tanto reapareció con otro contenido, se aparta en lugar de pisarlo.
	if err := e.store.DeleteFile(ctx, rec.AssetID); err != nil {
		return err
	}
	return e.SyncNow(ctx)
}

// DeleteOnServer borra en el servidor un archivo que falta en la carpeta. Solo se
// hace a petición del usuario: un borrado local nunca se propaga solo.
func (e *Engine) DeleteOnServer(ctx context.Context, assetID int64) error {
	_, c, err := e.clientFor(ctx)
	if err != nil {
		return err
	}
	rec, found, err := e.store.File(ctx, assetID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("este archivo no está en esta PC")
	}
	if err := c.Delete(ctx, assetID, rec.Version); err != nil {
		return err
	}
	e.note(ctx, "%s borrado en el servidor", rec.Path)
	return e.SyncNow(ctx)
}

// MoveOnServer registra en el servidor un renombrado hecho en el Explorador, y así
// el archivo conserva su historial en lugar de empezar uno nuevo.
func (e *Engine) MoveOnServer(ctx context.Context, assetID int64, newPath string) error {
	_, c, err := e.clientFor(ctx)
	if err != nil {
		return err
	}
	rec, found, err := e.store.File(ctx, assetID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("este archivo no está en esta PC")
	}
	asset, err := c.Move(ctx, assetID, rec.Version, newPath)
	if err != nil {
		return err
	}
	// La ruta local ya es la nueva: se anota para que sincronizar no intente moverlo.
	oldPath := rec.Path
	rec.Path, rec.Status = asset.Path, StatusSynced
	rec.SeenSize, rec.SeenMTime, rec.LocalSHA = 0, 0, ""
	if err := e.store.PutFile(ctx, rec); err != nil {
		return err
	}
	e.note(ctx, "%s renombrado a %s en el servidor", oldPath, asset.Path)
	return e.SyncNow(ctx)
}

// FindLocal busca un archivo por su ruta en el disco. Lo usa la API local, que es
// por donde hablará el complemento de Blender.
func (e *Engine) FindLocal(ctx context.Context, path string) (FileView, bool, error) {
	files, err := e.Files(ctx)
	if err != nil {
		return FileView{}, false, err
	}
	want := filepath.Clean(path)
	for _, f := range files {
		if strings.EqualFold(filepath.Clean(f.LocalPath), want) {
			return f, true, nil
		}
	}
	return FileView{}, false, nil
}

// SetRoot cambia la carpeta raíz moviendo su contenido.
func (e *Engine) SetRoot(ctx context.Context, root string) error {
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) {
		return errors.New("indica una ruta completa, por ejemplo D:\\3D-Vault")
	}
	root = filepath.Clean(root)

	e.syncMu.Lock()
	defer e.syncMu.Unlock()
	cfg, err := e.store.LoadConfig(ctx)
	if err != nil {
		return err
	}
	if cfg.Root == root {
		return nil
	}
	if cfg.Root != "" {
		e.mu.Lock()
		transferring := len(e.busy)+len(e.busyPaths) > 0
		e.mu.Unlock()
		if transferring {
			return errors.New("espera a que terminen las subidas para cambiar la carpeta")
		}
		if _, err := os.Stat(cfg.Root); err == nil {
			if _, err := os.Stat(root); err == nil {
				return errors.New("esa carpeta ya existe: elige una que no exista, y el contenido se moverá allí")
			}
			if err := os.MkdirAll(filepath.Dir(root), 0o750); err != nil {
				return fmt.Errorf("crear la carpeta: %w", err)
			}
			if err := os.Rename(cfg.Root, root); err != nil {
				return fmt.Errorf("mover la carpeta (entre discos distintos hay que moverla a mano): %w", err)
			}
		}
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return fmt.Errorf("crear la carpeta: %w", err)
	}
	cfg.Root = root
	if err := e.store.SaveConfig(ctx, cfg); err != nil {
		return err
	}
	e.mu.Lock()
	e.watchedDirs = make(map[string]bool)
	e.mu.Unlock()
	e.Reconnect()
	e.Wake()
	return nil
}

// SetProjectSelection guarda los proyectos elegidos.
func (e *Engine) SetProjectSelection(ctx context.Context, selected []int64) error {
	projects, err := e.store.Projects(ctx)
	if err != nil {
		return err
	}
	for _, p := range projects {
		if err := e.store.SetSelected(ctx, p.ID, slices.Contains(selected, p.ID)); err != nil {
			return err
		}
	}
	e.Reconnect()
	e.Wake()
	return nil
}
