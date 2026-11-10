package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"3d-vault/internal/rules"
)

// Folder es una carpeta creada a propósito. Las que tienen archivos existen
// igual por sus rutas; esta lista hace falta para que una vacía no se pierda.
type Folder struct {
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`

	pathKey string
}

// Eventos de carpetas. No tienen archivo: su asset_id es 0.
const (
	EventFolderCreated = "folder_created"
	EventFolderDeleted = "folder_deleted"
	EventFolderRenamed = "folder_renamed"
)

const (
	actionCreateFolder = "crear_carpeta"
	actionDeleteFolder = "borrar_carpeta"
	actionRenameFolder = "renombrar_carpeta"
	actionUndelete     = "recuperar_archivo"
)

// underKey es la condición de SQL para las rutas dentro de una carpeta. Compara
// el principio de la clave, sin LIKE: una ruta puede tener «%» o «_».
const underKey = `substr(path_key, 1, length(?) + 1) = ? || '/'`

func folderExists(path string) error {
	return &rules.Error{Code: rules.CodePathExists, Message: fmt.Sprintf("Ya existe una carpeta en «%s».", path)}
}

// checkFolderFree comprueba que una ruta de archivo no choque con una carpeta
// creada a propósito: en el disco no pueden convivir un archivo y una carpeta
// con el mismo nombre.
func checkFolderFree(ctx context.Context, tx *sql.Tx, projectID int64, path rules.Path) error {
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM folders WHERE project_id = ? AND path_key = ?`, projectID, path.Key).Scan(new(int))
	if err == nil {
		return folderExists(path.Clean)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("consultar carpetas: %w", err)
	}
	return nil
}

func listFolders(ctx context.Context, tx *sql.Tx, projectID int64) ([]Folder, error) {
	rows, err := tx.QueryContext(ctx, `SELECT path, path_key, created_at FROM folders WHERE project_id = ? ORDER BY path_key`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listar carpetas: %w", err)
	}
	defer rows.Close()
	folders := []Folder{}
	for rows.Next() {
		var (
			f  Folder
			at int64
		)
		if err := rows.Scan(&f.Path, &f.pathKey, &at); err != nil {
			return nil, fmt.Errorf("leer carpeta: %w", err)
		}
		f.CreatedAt = fromMillis(at)
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

// ListFolders devuelve las carpetas creadas a propósito de un proyecto.
func (s *Service) ListFolders(ctx context.Context, projectID int64) ([]Folder, error) {
	var folders []Folder
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		folders, err = listFolders(ctx, tx, projectID)
		return err
	})
	return folders, err
}

// CreateFolder crea una carpeta, aunque esté vacía. Si ya existe, con archivos
// dentro o creada antes, no hace nada y devuelve created = false.
func (s *Service) CreateFolder(ctx context.Context, actor rules.Actor, projectID int64, raw string) (folder Folder, created bool, err error) {
	path, err := rules.NormalizePath(strings.Trim(strings.ReplaceAll(raw, `\`, "/"), "/"))
	if err != nil {
		return Folder{}, false, err
	}
	folder = Folder{Path: path.Clean, pathKey: path.Key, CreatedAt: s.now()}
	err = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := checkProjectWritable(ctx, tx, projectID); err != nil {
			return err
		}
		// Ni la carpeta ni ninguna de las que la contienen puede ser un archivo.
		parts := strings.Split(path.Key, "/")
		shown := strings.Split(path.Clean, "/")
		for i := range parts {
			prefix := strings.Join(parts[:i+1], "/")
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM assets WHERE project_id = ? AND path_key = ? AND deleted_at IS NULL`, projectID, prefix).Scan(new(int))
			if err == nil {
				return &rules.Error{Code: rules.CodePathExists, Message: fmt.Sprintf("Ya existe un archivo llamado «%s».", strings.Join(shown[:i+1], "/"))}
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("consultar ruta: %w", err)
			}
		}
		var existing int
		err := tx.QueryRowContext(ctx, `
			SELECT (SELECT COUNT(*) FROM folders WHERE project_id = ? AND path_key = ?)
			     + (SELECT COUNT(*) FROM assets WHERE project_id = ? AND deleted_at IS NULL AND `+underKey+`)`,
			projectID, path.Key, projectID, path.Key, path.Key).Scan(&existing)
		if err != nil {
			return fmt.Errorf("consultar carpeta: %w", err)
		}
		if existing > 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO folders (project_id, path, path_key, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
			projectID, path.Clean, path.Key, actor.UserID, millis(folder.CreatedAt)); err != nil {
			return fmt.Errorf("crear carpeta: %w", err)
		}
		if err := s.addActivity(ctx, tx, activityEntry{userID: actor.UserID, action: actionCreateFolder, projectID: projectID, detail: path.Clean}); err != nil {
			return err
		}
		created = true
		return s.addEvent(ctx, tx, projectID, EventFolderCreated, 0, eventPayload{Path: path.Clean})
	})
	if err != nil {
		return Folder{}, false, err
	}
	if created {
		s.hub.Notify()
	}
	return folder, created, nil
}

// FolderInfo resume qué pasaría al renombrar o borrar una carpeta.
type FolderInfo struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
	// Locked son los archivos de dentro bloqueados por otra persona: impiden
	// renombrar o borrar la carpeta.
	Locked []Asset `json:"locked"`
	// LinkedFrom son los .blend de fuera de la carpeta que usan algo de dentro:
	// al renombrarla o borrarla, sus enlaces se romperán.
	LinkedFrom []Asset `json:"linked_from"`
}

func cleanFolderPath(raw string) (rules.Path, error) {
	return rules.NormalizePath(strings.Trim(strings.ReplaceAll(strings.TrimSpace(raw), `\`, "/"), "/"))
}

func assetsUnder(ctx context.Context, tx *sql.Tx, projectID int64, key string) ([]Asset, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+assetColumns+assetFrom+` AND a.project_id = ? AND substr(a.path_key, 1, length(?) + 1) = ? || '/' ORDER BY a.path_key`,
		projectID, key, key)
	if err != nil {
		return nil, fmt.Errorf("listar archivos de la carpeta: %w", err)
	}
	defer rows.Close()
	var out []Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, fmt.Errorf("leer archivo: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// lockedByOthers devuelve los archivos que bloquea otra persona. Los que
// bloquea el mismo usuario, en cualquier PC, se pueden mover o borrar: el
// bloqueo los acompaña o se libera.
func lockedByOthers(actor rules.Actor, assets []Asset) []Asset {
	var out []Asset
	for _, a := range assets {
		if a.Lock != nil && a.Lock.UserID != actor.UserID {
			out = append(out, a)
		}
	}
	return out
}

func lockedError(what string, locked []Asset) error {
	names := make([]string, 0, 3)
	for _, a := range locked[:min(len(locked), 3)] {
		names = append(names, fmt.Sprintf("%s (%s)", a.Path, a.Lock.DisplayName))
	}
	more := ""
	if len(locked) > 3 {
		more = fmt.Sprintf(" y %d más", len(locked)-3)
	}
	return &rules.Error{Code: rules.CodeLocked, Message: fmt.Sprintf("No se puede %s: hay archivos bloqueados por otra persona: %s%s. Pídeles que los liberen.", what, strings.Join(names, ", "), more)}
}

func folderKnown(ctx context.Context, tx *sql.Tx, projectID int64, key string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM folders WHERE project_id = ? AND (path_key = ? OR `+underKey+`))
		     + (SELECT COUNT(*) FROM assets WHERE project_id = ? AND deleted_at IS NULL AND `+underKey+`)`,
		projectID, key, key, key, projectID, key, key).Scan(&n)
	return n > 0, err
}

// FolderInfo dice cuántos archivos tiene una carpeta, cuáles bloquea otra
// persona y qué .blend de fuera la usan.
func (s *Service) FolderInfo(ctx context.Context, actor rules.Actor, projectID int64, raw string) (FolderInfo, error) {
	path, err := cleanFolderPath(raw)
	if err != nil {
		return FolderInfo{}, err
	}
	info := FolderInfo{Path: path.Clean, Locked: []Asset{}, LinkedFrom: []Asset{}}
	inside := map[int64]bool{}
	err = s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		ok, err := folderKnown(ctx, tx, projectID, path.Key)
		if err != nil {
			return fmt.Errorf("consultar carpeta: %w", err)
		}
		if !ok {
			return notFound("La carpeta")
		}
		assets, err := assetsUnder(ctx, tx, projectID, path.Key)
		if err != nil {
			return err
		}
		info.Files = len(assets)
		for _, a := range assets {
			info.Bytes += a.SizeBytes
			inside[a.ID] = true
		}
		if locked := lockedByOthers(actor, assets); locked != nil {
			info.Locked = locked
		}
		return nil
	})
	if err != nil || info.Files == 0 {
		return info, err
	}
	g, err := s.loadGraph(ctx)
	if err != nil {
		return info, err
	}
	for _, b := range g.blends {
		if inside[b.ID] {
			continue
		}
		links, _, _ := g.linksOf(b)
		for _, l := range links {
			if l.Target != nil && inside[l.Target.ID] {
				info.LinkedFrom = append(info.LinkedFrom, b)
				break
			}
		}
	}
	return info, nil
}

// DeleteFolder borra una carpeta. Si tiene archivos, solo con withFiles: van a
// la papelera del proyecto (borrado suave, regla 8) y se pueden recuperar. Un
// archivo bloqueado por otra persona impide borrar la carpeta entera.
func (s *Service) DeleteFolder(ctx context.Context, actor rules.Actor, projectID int64, raw string, withFiles bool) (deletedFiles int, err error) {
	path, err := cleanFolderPath(raw)
	if err != nil {
		return 0, err
	}
	deleted := false
	err = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := checkProjectWritable(ctx, tx, projectID); err != nil {
			return err
		}
		ok, err := folderKnown(ctx, tx, projectID, path.Key)
		if err != nil {
			return fmt.Errorf("consultar carpeta: %w", err)
		}
		if !ok {
			return notFound("La carpeta")
		}
		assets, err := assetsUnder(ctx, tx, projectID, path.Key)
		if err != nil {
			return err
		}
		if len(assets) > 0 && !withFiles {
			return &rules.Error{Code: rules.CodePathExists, Message: fmt.Sprintf("«%s» tiene %d archivo(s). Confirma que quieres mandarlos a la papelera.", path.Clean, len(assets))}
		}
		if locked := lockedByOthers(actor, assets); locked != nil {
			return lockedError("borrar la carpeta", locked)
		}
		now := millis(s.now())
		for _, a := range assets {
			if _, err := tx.ExecContext(ctx, `UPDATE assets SET deleted_at = ? WHERE id = ?`, now, a.ID); err != nil {
				return fmt.Errorf("borrar archivo: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM locks WHERE asset_id = ?`, a.ID); err != nil {
				return fmt.Errorf("liberar bloqueo: %w", err)
			}
			if err := s.addActivity(ctx, tx, activityEntry{userID: actor.UserID, action: actionDelete, projectID: projectID, assetID: a.ID, detail: a.Path}); err != nil {
				return err
			}
			if err := s.addEvent(ctx, tx, projectID, EventDeleted, a.ID, eventPayload{Path: a.Path}); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM folders WHERE project_id = ? AND (path_key = ? OR `+underKey+`)`,
			projectID, path.Key, path.Key, path.Key); err != nil {
			return fmt.Errorf("borrar carpeta: %w", err)
		}
		detail := path.Clean
		if len(assets) > 0 {
			detail = fmt.Sprintf("%s (%d archivos a la papelera)", path.Clean, len(assets))
		}
		if err := s.addActivity(ctx, tx, activityEntry{userID: actor.UserID, action: actionDeleteFolder, projectID: projectID, detail: detail}); err != nil {
			return err
		}
		deleted = true
		deletedFiles = len(assets)
		return s.addEvent(ctx, tx, projectID, EventFolderDeleted, 0, eventPayload{Path: path.Clean})
	})
	if deleted {
		s.hub.Notify()
	}
	return deletedFiles, err
}

// RenameFolder cambia el nombre o el lugar de una carpeta y mueve con ella
// todos sus archivos, que conservan su historial (regla 9). Si otra persona
// bloquea alguno, no se mueve nada. Devuelve cuántos archivos se movieron.
func (s *Service) RenameFolder(ctx context.Context, actor rules.Actor, projectID int64, rawFrom, rawTo string) (Folder, int, error) {
	from, err := cleanFolderPath(rawFrom)
	if err != nil {
		return Folder{}, 0, err
	}
	to, err := cleanFolderPath(rawTo)
	if err != nil {
		return Folder{}, 0, err
	}
	if strings.HasPrefix(to.Key+"/", from.Key+"/") && to.Key != from.Key {
		return Folder{}, 0, invalidInput("Una carpeta no puede ir dentro de sí misma.")
	}
	moved := 0
	changed := false
	err = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := checkProjectWritable(ctx, tx, projectID); err != nil {
			return err
		}
		ok, err := folderKnown(ctx, tx, projectID, from.Key)
		if err != nil {
			return fmt.Errorf("consultar carpeta: %w", err)
		}
		if !ok {
			return notFound("La carpeta")
		}
		if from.Clean == to.Clean {
			return nil
		}
		if to.Key != from.Key {
			// El destino no puede existir, ni como carpeta ni como archivo.
			taken, err := folderKnown(ctx, tx, projectID, to.Key)
			if err != nil {
				return fmt.Errorf("consultar carpeta: %w", err)
			}
			if taken {
				return folderExists(to.Clean)
			}
			parts, shown := strings.Split(to.Key, "/"), strings.Split(to.Clean, "/")
			for i := range parts {
				err := tx.QueryRowContext(ctx, `SELECT 1 FROM assets WHERE project_id = ? AND path_key = ? AND deleted_at IS NULL`, projectID, strings.Join(parts[:i+1], "/")).Scan(new(int))
				if err == nil {
					return &rules.Error{Code: rules.CodePathExists, Message: fmt.Sprintf("Ya existe un archivo llamado «%s».", strings.Join(shown[:i+1], "/"))}
				}
				if !errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("consultar ruta: %w", err)
				}
			}
		}
		assets, err := assetsUnder(ctx, tx, projectID, from.Key)
		if err != nil {
			return err
		}
		if locked := lockedByOthers(actor, assets); locked != nil {
			return lockedError("renombrar la carpeta", locked)
		}
		for _, a := range assets {
			newPath, err := rules.NormalizePath(to.Clean + a.Path[len(from.Clean):])
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE assets SET path = ?, path_key = ? WHERE id = ?`, newPath.Clean, newPath.Key, a.ID); err != nil {
				return fmt.Errorf("mover archivo: %w", err)
			}
			if err := s.addEvent(ctx, tx, projectID, EventMoved, a.ID, eventPayload{Path: newPath.Clean, OldPath: a.Path}); err != nil {
				return err
			}
		}
		// Las carpetas vacías de dentro van con ella.
		rows, err := tx.QueryContext(ctx, `SELECT id, path FROM folders WHERE project_id = ? AND (path_key = ? OR `+underKey+`)`, projectID, from.Key, from.Key, from.Key)
		if err != nil {
			return fmt.Errorf("listar carpetas: %w", err)
		}
		type row struct {
			id   int64
			path string
		}
		var inner []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.path); err != nil {
				rows.Close()
				return err
			}
			inner = append(inner, r)
		}
		rows.Close()
		for _, r := range inner {
			p, err := rules.NormalizePath(to.Clean + r.path[len(from.Clean):])
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE folders SET path = ?, path_key = ? WHERE id = ?`, p.Clean, p.Key, r.id); err != nil {
				return fmt.Errorf("renombrar carpeta: %w", err)
			}
		}
		if err := s.addActivity(ctx, tx, activityEntry{userID: actor.UserID, action: actionRenameFolder, projectID: projectID, detail: from.Clean + " → " + to.Clean}); err != nil {
			return err
		}
		moved, changed = len(assets), true
		return s.addEvent(ctx, tx, projectID, EventFolderRenamed, 0, eventPayload{Path: to.Clean, OldPath: from.Clean})
	})
	if err != nil {
		return Folder{}, 0, err
	}
	if changed {
		s.hub.Notify()
	}
	return Folder{Path: to.Clean, pathKey: to.Key}, moved, nil
}

// DeletedAsset es un archivo en la papelera de un proyecto.
type DeletedAsset struct {
	ID          int64
	Path        string
	HeadVersion int64
	SizeBytes   int64
	DeletedAt   time.Time
	// Free indica que su ruta está libre, así que se puede recuperar tal cual.
	Free bool
}

// ListDeleted devuelve la papelera de un proyecto: los archivos borrados que
// aún no purgó el admin, el más reciente primero.
func (s *Service) ListDeleted(ctx context.Context, projectID int64) ([]DeletedAsset, error) {
	var out []DeletedAsset
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT a.id, a.path, a.head_version, v.size_bytes, a.deleted_at,
			       NOT EXISTS (SELECT 1 FROM assets o WHERE o.project_id = a.project_id AND o.path_key = a.path_key AND o.deleted_at IS NULL)
			FROM assets a JOIN versions v ON v.asset_id = a.id AND v.number = a.head_version
			WHERE a.project_id = ? AND a.deleted_at IS NOT NULL
			ORDER BY a.deleted_at DESC, a.path_key LIMIT 500`, projectID)
		if err != nil {
			return fmt.Errorf("listar papelera: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				d  DeletedAsset
				at int64
			)
			if err := rows.Scan(&d.ID, &d.Path, &d.HeadVersion, &d.SizeBytes, &at, &d.Free); err != nil {
				return fmt.Errorf("leer papelera: %w", err)
			}
			d.DeletedAt = fromMillis(at)
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// Undelete saca un archivo de la papelera, con su historial, en su ruta de
// antes. Si la ruta ya está ocupada, hay que liberarla primero.
func (s *Service) Undelete(ctx context.Context, actor rules.Actor, assetID int64) (Asset, error) {
	var asset Asset
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var (
			projectID int64
			path, key string
		)
		err := tx.QueryRowContext(ctx, `SELECT project_id, path, path_key FROM assets WHERE id = ? AND deleted_at IS NOT NULL`, assetID).Scan(&projectID, &path, &key)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("El archivo borrado")
		}
		if err != nil {
			return fmt.Errorf("leer archivo: %w", err)
		}
		if err := checkProjectWritable(ctx, tx, projectID); err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM assets WHERE project_id = ? AND path_key = ? AND deleted_at IS NULL`, projectID, key).Scan(new(int))
		if err == nil {
			return &rules.Error{Code: rules.CodePathExists, Message: fmt.Sprintf("Ya hay otro archivo en «%s». Muévelo o bórralo antes de recuperar este.", path)}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("consultar ruta: %w", err)
		}
		p, err := rules.NormalizePath(path)
		if err != nil {
			return err
		}
		if err := checkFolderFree(ctx, tx, projectID, p); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE assets SET deleted_at = NULL WHERE id = ?`, assetID); err != nil {
			return fmt.Errorf("recuperar archivo: %w", err)
		}
		asset, err = loadAsset(ctx, tx, assetID)
		if err != nil {
			return err
		}
		if err := s.addActivity(ctx, tx, activityEntry{userID: actor.UserID, action: actionUndelete, projectID: projectID, assetID: assetID, detail: path}); err != nil {
			return err
		}
		return s.addEvent(ctx, tx, projectID, EventAssetCreated, assetID, eventPayload{
			Path: asset.Path, Version: asset.HeadVersion, VersionID: asset.HeadVersionID, FileSHA256: asset.FileSHA256, SizeBytes: asset.SizeBytes,
		})
	})
	if err != nil {
		return Asset{}, err
	}
	s.hub.Notify()
	return asset, nil
}
