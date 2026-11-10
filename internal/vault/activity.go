package vault

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"3d-vault/internal/rules"
)

// ActivityEntry es una fila del registro de actividad, descrita en palabras.
type ActivityEntry struct {
	ID          int64
	CreatedAt   time.Time
	UserID      int64
	UserName    string
	ProjectID   int64
	ProjectName string
	// AssetID es 0 si la acción no es sobre un archivo o si el archivo se borró.
	AssetID   int64
	AssetPath string
	// Text es la acción ("subió la v3") y Note, el comentario u otros datos.
	Text string
	Note string
}

// ActivityFilter elige qué actividad listar. Los campos a cero no filtran.
type ActivityFilter struct {
	ProjectID int64
	UserID    int64
	// Before pide las entradas anteriores a ese id, para paginar.
	Before int64
	Limit  int
}

// ListActivity devuelve la actividad de los proyectos, la más reciente primero.
// Deja fuera la de cuentas y tokens, que no es de ningún proyecto.
func (s *Service) ListActivity(ctx context.Context, f ActivityFilter) ([]ActivityEntry, error) {
	// ponytail: sin índice por proyecto ni usuario; recorre activity hacia atrás
	// hasta llenar la página. Añadir índices si un filtro raro se vuelve lento.
	query := `
		SELECT ac.id, ac.created_at, ac.user_id, u.display_name, ac.action, p.id, p.name,
			COALESCE(a.id, 0), COALESCE(a.path, ''), a.deleted_at IS NOT NULL, ac.detail, COALESCE(rv.number, 0)
		FROM activity ac
		JOIN users u ON u.id = ac.user_id
		JOIN projects p ON p.id = ac.project_id
		LEFT JOIN assets a ON a.id = ac.asset_id
		LEFT JOIN versions v ON v.id = ac.version_id
		LEFT JOIN versions rv ON rv.id = v.restored_from
		WHERE 1 = 1`
	var args []any
	if f.ProjectID != 0 {
		query += ` AND ac.project_id = ?`
		args = append(args, f.ProjectID)
	}
	if f.UserID != 0 {
		query += ` AND ac.user_id = ?`
		args = append(args, f.UserID)
	}
	if f.Before != 0 {
		query += ` AND ac.id < ?`
		args = append(args, f.Before)
	}
	query += ` ORDER BY ac.id DESC LIMIT ?`
	args = append(args, f.Limit)

	entries := []ActivityEntry{}
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("listar actividad: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				e            ActivityEntry
				createdAt    int64
				action       string
				deleted      bool
				detail       string
				restoredFrom int64
			)
			if err := rows.Scan(&e.ID, &createdAt, &e.UserID, &e.UserName, &action, &e.ProjectID, &e.ProjectName,
				&e.AssetID, &e.AssetPath, &deleted, &detail, &restoredFrom); err != nil {
				return fmt.Errorf("leer actividad: %w", err)
			}
			e.CreatedAt = fromMillis(createdAt)
			if deleted {
				e.AssetID = 0
			}
			e.Text, e.Note = describeActivity(action, detail, restoredFrom)
			entries = append(entries, e)
		}
		return rows.Err()
	})
	return entries, err
}

// describeActivity pone en palabras una acción, según el detail que guarda cada
// operación de este paquete.
func describeActivity(action, detail string, restoredFrom int64) (text, note string) {
	// Las acciones con versión guardan "vN: comentario".
	number, comment, _ := strings.Cut(detail, ": ")
	switch action {
	case actionCreateProject:
		return "creó el proyecto", ""
	case actionCreateAsset:
		return "agregó el archivo", comment
	case actionUpload:
		return "subió la " + number, comment
	case actionRestore:
		if restoredFrom == 0 {
			return "restauró una versión anterior", comment
		}
		return fmt.Sprintf("restauró la v%d como %s", restoredFrom, number), ""
	case actionMove:
		from, to, _ := strings.Cut(detail, " → ")
		return "movió de " + from + " a " + to, ""
	case actionDelete:
		return "borró el archivo", ""
	case actionCreateFolder:
		return "creó la carpeta " + detail, ""
	case actionDeleteFolder:
		return "borró la carpeta " + detail, ""
	case actionRenameFolder:
		from, to, _ := strings.Cut(detail, " → ")
		return "renombró la carpeta " + from + " a " + to, ""
	case actionUndelete:
		return "recuperó de la papelera", ""
	case actionLock:
		return "bloqueó", "En " + detail
	case actionTakeover:
		return "pasó su bloqueo a otra PC", "Ahora en " + detail
	case actionUnlock:
		return "liberó", ""
	case actionForceUnlock:
		return "forzó el desbloqueo", detail
	case actionRequestUnlock:
		return "pidió a " + detail + " que lo liberara", ""
	}
	return action, detail
}

// LockedBy devuelve los archivos que bloquea un usuario, en cualquier proyecto,
// el bloqueo más antiguo primero.
func (s *Service) LockedBy(ctx context.Context, userID int64) ([]Asset, error) {
	return s.listAssets(ctx, ` AND l.user_id = ? ORDER BY l.locked_at`, userID)
}

// SearchAssets busca archivos por parte de su ruta en los proyectos no
// archivados, sin distinguir mayúsculas ni acentos: «camion» encuentra «Camión».
func (s *Service) SearchAssets(ctx context.Context, text string, limit int) ([]Asset, error) {
	// ponytail: filtra en Go todos los archivos activos. Un LIKE '%x%' también
	// recorre la tabla entera; con decenas de miles de archivos, un índice FTS.
	all, err := s.listAssets(ctx, `
		AND a.project_id IN (SELECT id FROM projects WHERE archived = 0)
		ORDER BY (SELECT name_key FROM projects WHERE id = a.project_id), a.path_key`)
	if err != nil {
		return nil, err
	}
	want := searchKey(text)
	found := []Asset{}
	for _, a := range all {
		if strings.Contains(searchKey(a.Path), want) {
			found = append(found, a)
			if len(found) == limit {
				break
			}
		}
	}
	return found, nil
}

// searchKey pliega mayúsculas y quita los acentos (las marcas que deja NFD).
func searchKey(s string) string {
	out, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), rules.FoldKey(s))
	if err != nil {
		return rules.FoldKey(s)
	}
	return out
}

func (s *Service) listAssets(ctx context.Context, where string, args ...any) ([]Asset, error) {
	assets := []Asset{}
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+assetColumns+assetFrom+where, args...)
		if err != nil {
			return fmt.Errorf("listar archivos: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAsset(rows)
			if err != nil {
				return fmt.Errorf("leer archivo: %w", err)
			}
			assets = append(assets, a)
		}
		return rows.Err()
	})
	return assets, err
}
