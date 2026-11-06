package vault

import (
	"context"
	"database/sql"
	"fmt"
)

// DiskSpace devuelve el espacio libre y el tamaño del disco del almacén.
func (s *Service) DiskSpace() (free, total int64, err error) {
	return s.store.DiskSpace()
}

// ListLockedAssets devuelve los archivos bloqueados de todos los proyectos, del
// bloqueo más viejo al más nuevo.
func (s *Service) ListLockedAssets(ctx context.Context) ([]Asset, error) {
	var assets []Asset
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+assetColumns+assetFrom+` AND l.asset_id IS NOT NULL ORDER BY l.locked_at, a.id`)
		if err != nil {
			return fmt.Errorf("listar bloqueos: %w", err)
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
