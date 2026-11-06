// Package agent implementa el agente de sincronización: mantiene una carpeta local
// igual que el servidor, sin pisar nunca trabajo local.
package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	_ "modernc.org/sqlite" // Controlador SQLite sin cgo.
)

const stateSchema = `
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS projects (
    id       INTEGER PRIMARY KEY,
    name     TEXT    NOT NULL,
    selected INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE IF NOT EXISTS files (
    asset_id   INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL,
    path       TEXT    NOT NULL,
    version    INTEGER NOT NULL,
    version_id INTEGER NOT NULL,
    sha256     TEXT    NOT NULL,
    size       INTEGER NOT NULL,
    seen_size  INTEGER NOT NULL,
    seen_mtime INTEGER NOT NULL,
    local_sha  TEXT    NOT NULL,
    status     TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS files_project ON files(project_id);

CREATE TABLE IF NOT EXISTS conflicts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL,
    path       TEXT    NOT NULL,
    copy_path  TEXT    NOT NULL,
    reason     TEXT    NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;
`

// Claves de la configuración.
const (
	keyServer      = "server"
	keyToken       = "token"
	keyClientID    = "client_id"
	keyClientName  = "client_name"
	keyUserID      = "user_id"
	keyUsername    = "username"
	keyDisplayName = "display_name"
	keyRoot        = "root"
	keyPaused      = "paused"
)

// Estado de un archivo respecto a su versión de partida.
const (
	StatusSynced  = "synced"
	StatusChanged = "changed"
	StatusMissing = "missing"
)

// Config es la configuración del agente. No incluye la contraseña: el agente la
// cambia por un token al conectar y no la guarda.
type Config struct {
	Server      string
	Token       string
	ClientID    string
	ClientName  string
	UserID      int64
	Username    string
	DisplayName string
	Root        string
	Paused      bool
}

// Connected indica si hay una cuenta conectada.
func (c Config) Connected() bool {
	return c.Server != "" && c.Token != ""
}

// Ready indica si ya se puede sincronizar.
func (c Config) Ready() bool {
	return c.Connected() && c.Root != ""
}

// Project es un proyecto conocido por esta PC.
type Project struct {
	ID       int64
	Name     string
	Selected bool
}

// FileRecord es lo que esta PC sabe de un archivo.
type FileRecord struct {
	AssetID   int64
	ProjectID int64
	Path      string
	// Version, VersionID, SHA256 y Size describen la versión de partida: la última
	// que esta PC descargó o subió.
	Version   int64
	VersionID int64
	SHA256    string
	Size      int64
	// SeenSize y SeenMTime son el tamaño y la fecha del archivo cuando se calculó
	// LocalSHA. Si no cambian, no hace falta volver a leerlo entero.
	SeenSize  int64
	SeenMTime int64
	LocalSHA  string
	Status    string
	UpdatedAt time.Time
}

// Conflict es una copia que se apartó para no perder trabajo local.
type Conflict struct {
	ID        int64
	ProjectID int64
	Path      string
	CopyPath  string
	Reason    string
	CreatedAt time.Time
}

// Store guarda el estado local. Usa una sola conexión: el agente hace pocas
// consultas y así no hay esperas por bloqueos entre conexiones.
type Store struct {
	db *sql.DB
}

// OpenStore abre o crea la base de estado.
func OpenStore(ctx context.Context, path string) (*Store, error) {
	q := url.Values{"_pragma": {"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(FULL)"}}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("abrir el estado local: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, stateSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("preparar el estado local: %w", err)
	}
	return &Store{db: db}, nil
}

// Close cierra la base.
func (s *Store) Close() error {
	return s.db.Close()
}

// LoadConfig lee la configuración.
func (s *Store) LoadConfig(ctx context.Context) (Config, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return Config{}, fmt.Errorf("leer la configuración: %w", err)
	}
	defer rows.Close()

	var cfg Config
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return Config{}, fmt.Errorf("leer la configuración: %w", err)
		}
		switch key {
		case keyServer:
			cfg.Server = value
		case keyToken:
			cfg.Token = value
		case keyClientID:
			cfg.ClientID = value
		case keyClientName:
			cfg.ClientName = value
		case keyUserID:
			cfg.UserID, _ = strconv.ParseInt(value, 10, 64)
		case keyUsername:
			cfg.Username = value
		case keyDisplayName:
			cfg.DisplayName = value
		case keyRoot:
			cfg.Root = value
		case keyPaused:
			cfg.Paused = value == "1"
		}
	}
	return cfg, rows.Err()
}

// SaveConfig guarda la configuración en una sola transacción.
func (s *Store) SaveConfig(ctx context.Context, cfg Config) error {
	paused := "0"
	if cfg.Paused {
		paused = "1"
	}
	values := map[string]string{
		keyServer:      cfg.Server,
		keyToken:       cfg.Token,
		keyClientID:    cfg.ClientID,
		keyClientName:  cfg.ClientName,
		keyUserID:      strconv.FormatInt(cfg.UserID, 10),
		keyUsername:    cfg.Username,
		keyDisplayName: cfg.DisplayName,
		keyRoot:        cfg.Root,
		keyPaused:      paused,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("guardar la configuración: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return fmt.Errorf("guardar la configuración: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("guardar la configuración: %w", err)
	}
	return nil
}

// Projects devuelve los proyectos conocidos, por nombre.
func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, selected FROM projects ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("leer proyectos: %w", err)
	}
	defer rows.Close()
	projects := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Selected); err != nil {
			return nil, fmt.Errorf("leer proyectos: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// UpsertProject registra un proyecto del servidor sin cambiar si está elegido.
func (s *Store) UpsertProject(ctx context.Context, id int64, name string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO projects(id, name) VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name`, id, name)
	if err != nil {
		return fmt.Errorf("guardar proyecto: %w", err)
	}
	return nil
}

// SetSelected elige o deja de elegir un proyecto.
func (s *Store) SetSelected(ctx context.Context, id int64, selected bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET selected = ? WHERE id = ?`, selected, id)
	if err != nil {
		return fmt.Errorf("guardar proyecto: %w", err)
	}
	return nil
}

const fileColumns = `asset_id, project_id, path, version, version_id, sha256, size, seen_size, seen_mtime, local_sha, status, updated_at`

func scanRecord(row interface{ Scan(...any) error }) (FileRecord, error) {
	var (
		r       FileRecord
		updated int64
	)
	err := row.Scan(&r.AssetID, &r.ProjectID, &r.Path, &r.Version, &r.VersionID, &r.SHA256, &r.Size,
		&r.SeenSize, &r.SeenMTime, &r.LocalSHA, &r.Status, &updated)
	if err != nil {
		return FileRecord{}, err
	}
	r.UpdatedAt = time.UnixMilli(updated)
	return r, nil
}

// File devuelve un registro. found es false si esta PC no conoce el archivo.
func (s *Store) File(ctx context.Context, assetID int64) (rec FileRecord, found bool, err error) {
	rec, err = scanRecord(s.db.QueryRowContext(ctx, `SELECT `+fileColumns+` FROM files WHERE asset_id = ?`, assetID))
	if errors.Is(err, sql.ErrNoRows) {
		return FileRecord{}, false, nil
	}
	if err != nil {
		return FileRecord{}, false, fmt.Errorf("leer archivo: %w", err)
	}
	return rec, true, nil
}

// Files devuelve los registros de un proyecto.
func (s *Store) Files(ctx context.Context, projectID int64) ([]FileRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+fileColumns+` FROM files WHERE project_id = ? ORDER BY path COLLATE NOCASE`, projectID)
	if err != nil {
		return nil, fmt.Errorf("leer archivos: %w", err)
	}
	defer rows.Close()
	records := []FileRecord{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("leer archivos: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// PutFile crea o actualiza un registro.
func (s *Store) PutFile(ctx context.Context, r FileRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO files(`+fileColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(asset_id) DO UPDATE SET
			project_id = excluded.project_id, path = excluded.path, version = excluded.version,
			version_id = excluded.version_id, sha256 = excluded.sha256, size = excluded.size,
			seen_size = excluded.seen_size, seen_mtime = excluded.seen_mtime, local_sha = excluded.local_sha,
			status = excluded.status, updated_at = excluded.updated_at`,
		r.AssetID, r.ProjectID, r.Path, r.Version, r.VersionID, r.SHA256, r.Size,
		r.SeenSize, r.SeenMTime, r.LocalSHA, r.Status, r.UpdatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("guardar archivo: %w", err)
	}
	return nil
}

// DeleteFile borra un registro. No toca el archivo.
func (s *Store) DeleteFile(ctx context.Context, assetID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM files WHERE asset_id = ?`, assetID); err != nil {
		return fmt.Errorf("borrar registro: %w", err)
	}
	return nil
}

// AddConflict registra una copia apartada.
func (s *Store) AddConflict(ctx context.Context, c Conflict) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO conflicts(project_id, path, copy_path, reason, created_at) VALUES (?, ?, ?, ?, ?)`,
		c.ProjectID, c.Path, c.CopyPath, c.Reason, c.CreatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("registrar conflicto: %w", err)
	}
	return nil
}

// Conflicts devuelve los conflictos sin descartar, del más nuevo al más viejo.
func (s *Store) Conflicts(ctx context.Context) ([]Conflict, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, project_id, path, copy_path, reason, created_at FROM conflicts ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("leer conflictos: %w", err)
	}
	defer rows.Close()
	conflicts := []Conflict{}
	for rows.Next() {
		var (
			c       Conflict
			created int64
		)
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Path, &c.CopyPath, &c.Reason, &created); err != nil {
			return nil, fmt.Errorf("leer conflictos: %w", err)
		}
		c.CreatedAt = time.UnixMilli(created)
		conflicts = append(conflicts, c)
	}
	return conflicts, rows.Err()
}

// DismissConflict quita un conflicto de la lista. La copia sigue en el disco.
func (s *Store) DismissConflict(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM conflicts WHERE id = ?`, id); err != nil {
		return fmt.Errorf("descartar conflicto: %w", err)
	}
	return nil
}
