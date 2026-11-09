// Package vault implementa las operaciones de 3D-Vault sobre la base de datos y
// el almacén de trozos, aplicando las reglas de negocio de internal/rules. No
// sabe nada de HTTP.
package vault

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"3d-vault/internal/auth"
	"3d-vault/internal/chunker"
	"3d-vault/internal/db"
	"3d-vault/internal/events"
	"3d-vault/internal/rules"
	"3d-vault/internal/store"
	"3d-vault/internal/thumbs"
)

// Config configura un Service.
type Config struct {
	DB          *db.DB
	Store       *store.Store
	Hub         *events.Hub
	MaxFileSize int64
	Argon       auth.Params
	// Thumbs guarda las miniaturas de los .blend. Sin él, no se generan.
	Thumbs *thumbs.Cache
	// Now permite fijar el reloj en tests. Por defecto, time.Now.
	Now func() time.Time
}

// Service es seguro para uso concurrente.
type Service struct {
	db          *db.DB
	store       *store.Store
	hub         *events.Hub
	maxFileSize int64
	argon       auth.Params
	now         func() time.Time
	thumbs      *thumbs.Cache
	// thumbSem limita a una las miniaturas que se generan a la vez.
	thumbSem chan struct{}
	// refsSem limita a uno los .blend que se analizan a la vez: se leen enteros.
	refsSem chan struct{}
	// bg se cancela al detener el servidor: corta los análisis largos, que se
	// repiten al volver a arrancar.
	bg         context.Context
	stopBg     context.CancelFunc
	background sync.WaitGroup
	// afterVerify se llama tras verificar un manifiesto, antes de la transacción
	// final. Solo lo usan los tests, para simular cambios durante la verificación.
	afterVerify func()
}

// New devuelve un Service.
func New(cfg Config) *Service {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	bg, stopBg := context.WithCancel(context.Background())
	return &Service{
		db:          cfg.DB,
		store:       cfg.Store,
		hub:         cfg.Hub,
		maxFileSize: cfg.MaxFileSize,
		argon:       cfg.Argon,
		now:         now,
		thumbs:      cfg.Thumbs,
		thumbSem:    make(chan struct{}, 1),
		refsSem:     make(chan struct{}, 1),
		bg:          bg,
		stopBg:      stopBg,
	}
}

// Hub devuelve el avisador de eventos.
func (s *Service) Hub() *events.Hub {
	return s.hub
}

// Project agrupa los archivos de un trabajo.
type Project struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedBy   int64     `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	Archived    bool      `json:"archived"`
}

// Asset es un archivo con su última versión y su bloqueo.
type Asset struct {
	ID            int64       `json:"id"`
	ProjectID     int64       `json:"project_id"`
	Path          string      `json:"path"`
	Ext           string      `json:"ext"`
	HeadVersion   int64       `json:"head_version"`
	HeadVersionID int64       `json:"head_version_id"`
	FileSHA256    string      `json:"file_sha256"`
	SizeBytes     int64       `json:"size_bytes"`
	UploadedBy    string      `json:"uploaded_by"`
	UpdatedAt     time.Time   `json:"updated_at"`
	Lock          *rules.Lock `json:"lock,omitempty"`

	pathKey    string
	manifestID int64
}

// Version es una copia inmutable de un archivo.
type Version struct {
	ID           int64     `json:"id"`
	AssetID      int64     `json:"asset_id"`
	Number       int64     `json:"number"`
	FileSHA256   string    `json:"file_sha256"`
	SizeBytes    int64     `json:"size_bytes"`
	Comment      string    `json:"comment"`
	UploadedByID int64     `json:"uploaded_by_id"`
	UploadedBy   string    `json:"uploaded_by"`
	CreatedAt    time.Time `json:"created_at"`
	RestoredFrom *int64    `json:"restored_from,omitempty"`

	manifestID int64
}

// Event es un cambio en un proyecto, usado para sincronizar.
type Event struct {
	Seq       int64           `json:"seq"`
	ProjectID int64           `json:"project_id"`
	Kind      string          `json:"kind"`
	AssetID   int64           `json:"asset_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// Tipos de evento.
const (
	EventAssetCreated = "asset_created"
	EventVersion      = "version_created"
	EventLocked       = "locked"
	EventUnlocked     = "unlocked"
	EventMoved        = "moved"
	EventDeleted      = "deleted"
)

// eventPayload describe un cambio. Lleva lo que un agente necesita sin consultar
// nada más, salvo el manifiesto.
type eventPayload struct {
	Path       string      `json:"path,omitempty"`
	OldPath    string      `json:"old_path,omitempty"`
	Version    int64       `json:"version,omitempty"`
	VersionID  int64       `json:"version_id,omitempty"`
	FileSHA256 string      `json:"file_sha256,omitempty"`
	SizeBytes  int64       `json:"size_bytes,omitempty"`
	Comment    string      `json:"comment,omitempty"`
	ActorName  string      `json:"actor_name,omitempty"`
	Lock       *rules.Lock `json:"lock,omitempty"`
	Forced     bool        `json:"forced,omitempty"`
}

// Acciones del registro de actividad (regla 10).
const (
	actionCreateProject = "crear_proyecto"
	actionCreateAsset   = "crear_archivo"
	actionUpload        = "subir_version"
	actionRestore       = "restaurar_version"
	actionMove          = "mover_archivo"
	actionDelete        = "borrar_archivo"
	actionLock          = "bloquear"
	actionTakeover      = "tomar_bloqueo"
	actionUnlock        = "liberar"
	actionForceUnlock   = "forzar_desbloqueo"
	actionCreateUser    = "crear_usuario"
	actionUpdateUser    = "modificar_usuario"
	actionSetPassword   = "cambiar_contrasena"
	actionCreateToken   = "crear_token"
	actionRevokeToken   = "revocar_token"
)

func notFound(what string) error {
	return &rules.Error{Code: rules.CodeNotFound, Message: what + " no existe."}
}

func invalidInput(message string) error {
	return &rules.Error{Code: rules.CodeInvalidInput, Message: message}
}

func millis(t time.Time) int64 {
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	return time.UnixMilli(ms)
}

// activityEntry es una fila del registro de actividad.
type activityEntry struct {
	userID    int64
	action    string
	projectID int64
	assetID   int64
	versionID int64
	detail    string
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func (s *Service) addActivity(ctx context.Context, tx *sql.Tx, e activityEntry) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO activity(user_id, action, project_id, asset_id, version_id, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.userID, e.action, nullableID(e.projectID), nullableID(e.assetID), nullableID(e.versionID), e.detail, millis(s.now()))
	if err != nil {
		return fmt.Errorf("registrar actividad: %w", err)
	}
	return nil
}

func (s *Service) addEvent(ctx context.Context, tx *sql.Tx, projectID int64, kind string, assetID int64, payload eventPayload) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("codificar evento: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO events(project_id, kind, asset_id, payload_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		projectID, kind, assetID, string(raw), millis(s.now()))
	if err != nil {
		return fmt.Errorf("registrar evento: %w", err)
	}
	return nil
}

// projectName valida el nombre de un proyecto, que será una carpeta local, y
// devuelve también su clave sin distinguir mayúsculas.
func projectName(name string) (clean, key string, err error) {
	parsed, err := rules.NormalizePath(strings.TrimSpace(name))
	if err != nil {
		var ruleErr *rules.Error
		if errors.As(err, &ruleErr) {
			return "", "", invalidInput("Nombre de proyecto no válido: " + ruleErr.Message)
		}
		return "", "", err
	}
	if strings.Contains(parsed.Clean, "/") {
		return "", "", invalidInput("El nombre del proyecto no puede contener «/».")
	}
	return parsed.Clean, parsed.Key, nil
}

// CreateProject crea un proyecto.
func (s *Service) CreateProject(ctx context.Context, actor rules.Actor, name, description string) (Project, error) {
	clean, key, err := projectName(name)
	if err != nil {
		return Project{}, err
	}
	now := s.now()
	project := Project{Name: clean, Description: strings.TrimSpace(description), CreatedBy: actor.UserID, CreatedAt: fromMillis(millis(now))}

	err = s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE name_key = ?`, key).Scan(&exists)
		if err == nil {
			return &rules.Error{Code: rules.CodeNameExists, Message: fmt.Sprintf("Ya existe un proyecto llamado «%s».", clean)}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("consultar proyecto: %w", err)
		}

		res, err := tx.ExecContext(ctx, `
			INSERT INTO projects(name, name_key, description, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
			clean, key, project.Description, actor.UserID, millis(now))
		if err != nil {
			return fmt.Errorf("crear proyecto: %w", err)
		}
		project.ID, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("leer id del proyecto: %w", err)
		}
		return s.addActivity(ctx, tx, activityEntry{userID: actor.UserID, action: actionCreateProject, projectID: project.ID, detail: clean})
	})
	if err != nil {
		return Project{}, err
	}
	return project, nil
}

// ListProjects devuelve los proyectos por nombre.
func (s *Service) ListProjects(ctx context.Context) ([]Project, error) {
	projects := []Project{}
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, name, description, created_by, created_at, archived FROM projects ORDER BY name_key`)
		if err != nil {
			return fmt.Errorf("listar proyectos: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProject(rows)
			if err != nil {
				return err
			}
			projects = append(projects, p)
		}
		return rows.Err()
	})
	return projects, err
}

// GetProject devuelve un proyecto.
func (s *Service) GetProject(ctx context.Context, id int64) (Project, error) {
	var p Project
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		p, err = loadProject(ctx, tx, id)
		return err
	})
	return p, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProject(row scanner) (Project, error) {
	var (
		p         Project
		createdAt int64
	)
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedBy, &createdAt, &p.Archived); err != nil {
		return Project{}, err
	}
	p.CreatedAt = fromMillis(createdAt)
	return p, nil
}

func loadProject(ctx context.Context, tx *sql.Tx, id int64) (Project, error) {
	p, err := scanProject(tx.QueryRowContext(ctx, `
		SELECT id, name, description, created_by, created_at, archived FROM projects WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, notFound("El proyecto")
	}
	if err != nil {
		return Project{}, fmt.Errorf("leer proyecto: %w", err)
	}
	return p, nil
}

const assetColumns = `
	a.id, a.project_id, a.path, a.path_key, a.ext, a.head_version,
	v.id, v.file_sha256, v.size_bytes, v.manifest_id, v.created_at, vu.display_name,
	l.user_id, lu.username, lu.display_name, l.client_id, l.client_name, l.locked_at`

// assetFrom une cada archivo con su última versión y su bloqueo. Omite los
// borrados, de modo que para el resto del paquete un archivo borrado no existe.
const assetFrom = `
	FROM assets a
	JOIN versions v ON v.asset_id = a.id AND v.number = a.head_version
	JOIN users vu ON vu.id = v.uploaded_by
	LEFT JOIN locks l ON l.asset_id = a.id
	LEFT JOIN users lu ON lu.id = l.user_id
	WHERE a.deleted_at IS NULL`

func scanAsset(row scanner) (Asset, error) {
	var (
		a         Asset
		updatedAt int64
		lockUser  sql.NullInt64
		username  sql.NullString
		lockName  sql.NullString
		clientID  sql.NullString
		client    sql.NullString
		lockedAt  sql.NullInt64
	)
	err := row.Scan(
		&a.ID, &a.ProjectID, &a.Path, &a.pathKey, &a.Ext, &a.HeadVersion,
		&a.HeadVersionID, &a.FileSHA256, &a.SizeBytes, &a.manifestID, &updatedAt, &a.UploadedBy,
		&lockUser, &username, &lockName, &clientID, &client, &lockedAt,
	)
	if err != nil {
		return Asset{}, err
	}
	a.UpdatedAt = fromMillis(updatedAt)
	if lockUser.Valid {
		a.Lock = &rules.Lock{
			UserID:      lockUser.Int64,
			Username:    username.String,
			DisplayName: lockName.String,
			ClientID:    clientID.String,
			ClientName:  client.String,
			LockedAt:    fromMillis(lockedAt.Int64),
		}
	}
	return a, nil
}

func loadAsset(ctx context.Context, tx *sql.Tx, id int64) (Asset, error) {
	a, err := scanAsset(tx.QueryRowContext(ctx, `SELECT `+assetColumns+assetFrom+` AND a.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Asset{}, notFound("El archivo")
	}
	if err != nil {
		return Asset{}, fmt.Errorf("leer archivo: %w", err)
	}
	return a, nil
}

// GetAsset devuelve un archivo.
func (s *Service) GetAsset(ctx context.Context, id int64) (Asset, error) {
	var a Asset
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		a, err = loadAsset(ctx, tx, id)
		return err
	})
	return a, err
}

// Snapshot es el estado completo de un proyecto en un instante.
type Snapshot struct {
	ProjectID   int64   `json:"project_id"`
	Seq         int64   `json:"seq"`
	ServerEpoch string  `json:"server_epoch"`
	Assets      []Asset `json:"assets"`
}

// GetSnapshot devuelve el estado de un proyecto. seq y los archivos salen de la
// misma transacción: un agente que luego pida eventos desde seq no pierde ni
// repite ningún cambio.
func (s *Service) GetSnapshot(ctx context.Context, projectID int64) (Snapshot, error) {
	snap := Snapshot{ProjectID: projectID, Assets: []Asset{}}
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		if _, err := loadProject(ctx, tx, projectID); err != nil {
			return err
		}
		var err error
		if snap.Seq, err = latestSeq(ctx, tx); err != nil {
			return err
		}
		if snap.ServerEpoch, err = readMeta(ctx, tx, db.MetaServerEpoch); err != nil {
			return err
		}

		rows, err := tx.QueryContext(ctx, `SELECT `+assetColumns+assetFrom+` AND a.project_id = ? ORDER BY a.path_key`, projectID)
		if err != nil {
			return fmt.Errorf("listar archivos: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAsset(rows)
			if err != nil {
				return fmt.Errorf("leer archivo: %w", err)
			}
			snap.Assets = append(snap.Assets, a)
		}
		return rows.Err()
	})
	return snap, err
}

// CurrentSeq devuelve el número del último evento.
func (s *Service) CurrentSeq(ctx context.Context) (int64, error) {
	var seq int64
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		seq, err = latestSeq(ctx, tx)
		return err
	})
	return seq, err
}

func latestSeq(ctx context.Context, tx *sql.Tx) (int64, error) {
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("leer secuencia: %w", err)
	}
	return seq, nil
}

func readMeta(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var value string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value); err != nil {
		return "", fmt.Errorf("leer meta %s: %w", key, err)
	}
	return value, nil
}

// ErrEventsPruned indica que los eventos pedidos ya se eliminaron: hay que pedir
// un snapshot nuevo.
var ErrEventsPruned = errors.New("los eventos pedidos ya no están disponibles")

// EventPage es el resultado de consultar eventos.
type EventPage struct {
	Events      []Event `json:"events"`
	Seq         int64   `json:"seq"`
	ServerEpoch string  `json:"server_epoch"`
}

const maxEventsPerPage = 500

// EventsSince devuelve hasta una página de eventos posteriores a since, solo de
// los proyectos indicados, o de todos si no se indica ninguno. Seq es el valor con
// el que continuar: el último evento devuelto o, si no hay más, el último del
// servidor, aunque sea de un proyecto que no interesa.
func (s *Service) EventsSince(ctx context.Context, projectIDs []int64, since int64) (EventPage, error) {
	page := EventPage{Events: []Event{}, Seq: since}
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		floorText, err := readMeta(ctx, tx, db.MetaEventsFloor)
		if err != nil {
			return err
		}
		var floor int64
		if _, err := fmt.Sscan(floorText, &floor); err != nil {
			return fmt.Errorf("interpretar %s: %w", db.MetaEventsFloor, err)
		}
		if since < floor {
			return ErrEventsPruned
		}
		if page.ServerEpoch, err = readMeta(ctx, tx, db.MetaServerEpoch); err != nil {
			return err
		}

		query := `SELECT seq, project_id, kind, asset_id, payload_json, created_at FROM events WHERE seq > ?`
		args := []any{since}
		if len(projectIDs) > 0 {
			query += ` AND project_id IN (` + placeholders(len(projectIDs)) + `)`
			for _, id := range projectIDs {
				args = append(args, id)
			}
		}
		query += fmt.Sprintf(` ORDER BY seq LIMIT %d`, maxEventsPerPage)

		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("consultar eventos: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				e         Event
				payload   string
				createdAt int64
			)
			if err := rows.Scan(&e.Seq, &e.ProjectID, &e.Kind, &e.AssetID, &payload, &createdAt); err != nil {
				return fmt.Errorf("leer evento: %w", err)
			}
			e.Payload = json.RawMessage(payload)
			e.CreatedAt = fromMillis(createdAt)
			page.Events = append(page.Events, e)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		if len(page.Events) == maxEventsPerPage {
			page.Seq = page.Events[len(page.Events)-1].Seq
			return nil
		}
		page.Seq, err = latestSeq(ctx, tx)
		return err
	})
	return page, err
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

const scanBatch = 500

// MissingChunks devuelve los hashes que el servidor no tiene.
func (s *Service) MissingChunks(ctx context.Context, hashes []chunker.Hash) ([]chunker.Hash, error) {
	missing := []chunker.Hash{}
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		missing, err = missingChunks(ctx, tx, hashes)
		return err
	})
	return missing, err
}

func missingChunks(ctx context.Context, tx *sql.Tx, hashes []chunker.Hash) ([]chunker.Hash, error) {
	missing := []chunker.Hash{}
	for start := 0; start < len(hashes); start += scanBatch {
		batch := hashes[start:min(start+scanBatch, len(hashes))]
		args := make([]any, len(batch))
		for i, h := range batch {
			args[i] = h.String()
		}
		rows, err := tx.QueryContext(ctx, `SELECT sha256 FROM chunks WHERE sha256 IN (`+placeholders(len(batch))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("consultar trozos: %w", err)
		}
		found := make(map[string]struct{}, len(batch))
		for rows.Next() {
			var hash string
			if err := rows.Scan(&hash); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("leer trozo: %w", err)
			}
			found[hash] = struct{}{}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, fmt.Errorf("leer trozos: %w", err)
		}
		for _, h := range batch {
			if _, ok := found[h.String()]; !ok {
				missing = append(missing, h)
			}
		}
	}
	return missing, nil
}

// PutChunk guarda un trozo y solo después lo registra: una fila en la tabla
// garantiza que sus datos ya están a salvo en disco.
func (s *Service) PutChunk(ctx context.Context, h chunker.Hash, data []byte) error {
	stored, err := s.store.Put(h, data)
	if err != nil {
		if errors.Is(err, store.ErrHashMismatch) {
			return &rules.Error{Code: rules.CodeManifestMismatch, Message: "El contenido del trozo no coincide con su hash."}
		}
		return err
	}
	return s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO chunks(sha256, size, stored_size, created_at) VALUES (?, ?, ?, ?)`,
			h.String(), len(data), stored, millis(s.now()))
		if err != nil {
			return fmt.Errorf("registrar trozo: %w", err)
		}
		return nil
	})
}

// GetChunk devuelve el contenido de un trozo registrado.
func (s *Service) GetChunk(ctx context.Context, h chunker.Hash) ([]byte, error) {
	var exists int
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT 1 FROM chunks WHERE sha256 = ?`, h.String()).Scan(&exists)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("El trozo")
	}
	if err != nil {
		return nil, fmt.Errorf("consultar trozo: %w", err)
	}
	return s.store.Get(h)
}

// ProjectSummary es un proyecto con sus totales, para la lista de proyectos.
type ProjectSummary struct {
	Project
	Assets     int
	Locked     int
	TotalBytes int64
	LastUpload *time.Time
}

// ProjectSummaries devuelve los proyectos con sus totales actuales, sin contar
// archivos borrados ni versiones anteriores.
func (s *Service) ProjectSummaries(ctx context.Context) ([]ProjectSummary, error) {
	summaries := []ProjectSummary{}
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT p.id, p.name, p.description, p.created_by, p.created_at, p.archived,
				COUNT(a.id), COUNT(l.asset_id), COALESCE(SUM(v.size_bytes), 0), MAX(v.created_at)
			FROM projects p
			LEFT JOIN assets a ON a.project_id = p.id AND a.deleted_at IS NULL
			LEFT JOIN versions v ON v.asset_id = a.id AND v.number = a.head_version
			LEFT JOIN locks l ON l.asset_id = a.id
			GROUP BY p.id
			ORDER BY p.name_key`)
		if err != nil {
			return fmt.Errorf("resumir proyectos: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				sum       ProjectSummary
				createdAt int64
				last      sql.NullInt64
			)
			if err := rows.Scan(&sum.ID, &sum.Name, &sum.Description, &sum.CreatedBy, &createdAt, &sum.Archived,
				&sum.Assets, &sum.Locked, &sum.TotalBytes, &last); err != nil {
				return fmt.Errorf("leer resumen: %w", err)
			}
			sum.CreatedAt = fromMillis(createdAt)
			if last.Valid {
				t := fromMillis(last.Int64)
				sum.LastUpload = &t
			}
			summaries = append(summaries, sum)
		}
		return rows.Err()
	})
	return summaries, err
}

// ProjectStorage es el uso de disco de un proyecto.
type ProjectStorage struct {
	ID   int64
	Name string
	// Assets cuenta los archivos actuales; Versions, todas las guardadas, también
	// las de archivos borrados que aún no se han purgado.
	Assets       int
	Versions     int
	CurrentBytes int64
	// StoredBytes es lo que ocupan en disco los trozos distintos que usa. Un trozo
	// compartido con otro proyecto cuenta en los dos.
	StoredBytes int64
}

// StorageUsage es el uso de disco del almacén.
type StorageUsage struct {
	StoredBytes int64
	FreeBytes   int64
	DiskBytes   int64
	FreeKnown   bool
	Assets      int
	Versions    int
	Projects    []ProjectStorage
}

// StorageUsage calcula el uso de disco total y por proyecto.
func (s *Service) StorageUsage(ctx context.Context) (StorageUsage, error) {
	var usage StorageUsage
	free, total, err := s.DiskSpace()
	usage.FreeBytes, usage.DiskBytes, usage.FreeKnown = free, total, err == nil

	err = s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(stored_size), 0) FROM chunks`).Scan(&usage.StoredBytes); err != nil {
			return fmt.Errorf("sumar trozos: %w", err)
		}
		// ponytail: recorre todos los manifiestos en cada visita. Es una página de
		// administración; si se vuelve lenta, guardar los totales al subir.
		rows, err := tx.QueryContext(ctx, `
			SELECT p.id, p.name,
				(SELECT COUNT(*) FROM assets a WHERE a.project_id = p.id AND a.deleted_at IS NULL),
				(SELECT COUNT(*) FROM versions v JOIN assets a ON a.id = v.asset_id WHERE a.project_id = p.id),
				(SELECT COALESCE(SUM(v.size_bytes), 0) FROM assets a
					JOIN versions v ON v.asset_id = a.id AND v.number = a.head_version
					WHERE a.project_id = p.id AND a.deleted_at IS NULL),
				(SELECT COALESCE(SUM(c.stored_size), 0) FROM chunks c WHERE c.sha256 IN (
					SELECT mc.chunk_sha256 FROM assets a
					JOIN versions v ON v.asset_id = a.id
					JOIN manifest_chunks mc ON mc.manifest_id = v.manifest_id
					WHERE a.project_id = p.id))
			FROM projects p
			ORDER BY p.name_key`)
		if err != nil {
			return fmt.Errorf("calcular el uso por proyecto: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var p ProjectStorage
			if err := rows.Scan(&p.ID, &p.Name, &p.Assets, &p.Versions, &p.CurrentBytes, &p.StoredBytes); err != nil {
				return fmt.Errorf("leer el uso de un proyecto: %w", err)
			}
			usage.Assets += p.Assets
			usage.Versions += p.Versions
			usage.Projects = append(usage.Projects, p)
		}
		return rows.Err()
	})
	return usage, err
}
