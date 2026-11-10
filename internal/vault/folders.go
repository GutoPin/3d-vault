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
)

const (
	actionCreateFolder = "crear_carpeta"
	actionDeleteFolder = "borrar_carpeta"
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

// DeleteFolder borra una carpeta vacía y las carpetas vacías que contenga. Una
// carpeta con archivos no se borra: hay que borrar o mover antes sus archivos,
// cada uno con sus reglas de bloqueo.
func (s *Service) DeleteFolder(ctx context.Context, actor rules.Actor, projectID int64, raw string) error {
	path, err := rules.NormalizePath(strings.Trim(strings.ReplaceAll(raw, `\`, "/"), "/"))
	if err != nil {
		return err
	}
	deleted := false
	err = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := checkProjectWritable(ctx, tx, projectID); err != nil {
			return err
		}
		var files int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM assets WHERE project_id = ? AND deleted_at IS NULL AND `+underKey,
			projectID, path.Key, path.Key).Scan(&files); err != nil {
			return fmt.Errorf("consultar carpeta: %w", err)
		}
		if files > 0 {
			return &rules.Error{Code: rules.CodePathExists, Message: fmt.Sprintf("«%s» tiene %d archivo(s). Solo se puede borrar una carpeta vacía.", path.Clean, files)}
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM folders WHERE project_id = ? AND (path_key = ? OR `+underKey+`)`,
			projectID, path.Key, path.Key, path.Key)
		if err != nil {
			return fmt.Errorf("borrar carpeta: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("La carpeta")
		}
		if err := s.addActivity(ctx, tx, activityEntry{userID: actor.UserID, action: actionDeleteFolder, projectID: projectID, detail: path.Clean}); err != nil {
			return err
		}
		deleted = true
		return s.addEvent(ctx, tx, projectID, EventFolderDeleted, 0, eventPayload{Path: path.Clean})
	})
	if deleted {
		s.hub.Notify()
	}
	return err
}
