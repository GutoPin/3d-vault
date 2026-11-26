package vault

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SeenClient es un programa (Mi carpeta o el complemento de Blender) visto en una
// PC, con la versión con la que habló con el servidor la última vez.
type SeenClient struct {
	UserID      int64
	DisplayName string
	ClientID    string
	App         string // "web" o "blender"
	ClientName  string
	// Version vacía: un programa de antes de que se avisara la versión.
	Version  string
	Browser  string
	LastSeen time.Time
}

// RecordClient anota que un programa habló con el servidor.
func (s *Service) RecordClient(ctx context.Context, c SeenClient) error {
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO clients (user_id, client_id, app, client_name, version, browser, last_seen)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (user_id, client_id, app) DO UPDATE SET
				client_name = CASE WHEN excluded.client_name = '' THEN clients.client_name ELSE excluded.client_name END,
				version = excluded.version,
				browser = excluded.browser, last_seen = excluded.last_seen`,
			c.UserID, c.ClientID, c.App, c.ClientName, c.Version, c.Browser, millis(c.LastSeen))
		return err
	})
	if err != nil {
		return fmt.Errorf("anotar programa: %w", err)
	}
	return nil
}

// ListClients devuelve los programas vistos desde since, del más reciente al más
// viejo. Con userID 0, los de todos los usuarios activos.
func (s *Service) ListClients(ctx context.Context, userID int64, since time.Time) ([]SeenClient, error) {
	out := []SeenClient{}
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT c.user_id, u.display_name, c.client_id, c.app, c.client_name, c.version, c.browser, c.last_seen
			FROM clients c JOIN users u ON u.id = c.user_id
			WHERE (? = 0 OR c.user_id = ?) AND u.is_active = 1 AND c.last_seen >= ?
			ORDER BY c.last_seen DESC`, userID, userID, millis(since))
		if err != nil {
			return fmt.Errorf("listar programas: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var c SeenClient
			var seen int64
			if err := rows.Scan(&c.UserID, &c.DisplayName, &c.ClientID, &c.App, &c.ClientName, &c.Version, &c.Browser, &seen); err != nil {
				return fmt.Errorf("leer programa: %w", err)
			}
			c.LastSeen = fromMillis(seen)
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}
