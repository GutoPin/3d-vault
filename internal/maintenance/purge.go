package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"3d-vault/internal/chunker"
	"3d-vault/internal/db"
	"3d-vault/internal/store"
)

// errDryRun deshace la transacción de una simulación.
var errDryRun = errors.New("simulación")

// PurgeReport resume una purga.
type PurgeReport struct {
	Versions int
	// Files son los archivos borrados que pierden todas sus versiones.
	Files     int
	Manifests int
	// Chunks y Bytes son los trozos que quedan sin referencias y lo que ocupan;
	// los borra gc.
	Chunks int
	Bytes  int64
}

// Purge borra las versiones anteriores a cutoff, salvo la actual de cada archivo
// y las keepLast más nuevas. A los archivos borrados antes de cutoff les quita
// todas las versiones; su fila queda para que la actividad y los eventos sigan
// apuntando a algo. Un manifiesto solo se borra cuando ninguna versión lo usa, lo
// que protege a las restauradas, que comparten el de su origen.
//
// Sin apply hace lo mismo y deshace la transacción, así que la simulación
// coincide con la purga real. No borra trozos: eso es gc. El servidor debe estar
// detenido.
func Purge(ctx context.Context, database *db.DB, keepLast int, cutoff time.Time, apply bool) (PurgeReport, error) {
	var report PurgeReport
	before := cutoff.UnixMilli()
	err := database.WriteTx(ctx, func(tx *sql.Tx) error {
		// Se fija la lista antes de borrar: al borrar cambia el orden de las que quedan.
		if _, err := tx.ExecContext(ctx, `
			CREATE TEMP TABLE purge_versions AS
			SELECT id, asset_id FROM (
				SELECT v.id, v.asset_id, v.number, v.created_at, a.head_version, a.deleted_at,
					ROW_NUMBER() OVER (PARTITION BY v.asset_id ORDER BY v.number DESC) AS newest
				FROM versions v JOIN assets a ON a.id = v.asset_id)
			WHERE deleted_at < ? OR (number <> head_version AND newest > ? AND created_at < ?)`,
			before, keepLast, before); err != nil {
			return fmt.Errorf("elegir versiones: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM purge_versions`).Scan(&report.Versions); err != nil {
			return fmt.Errorf("contar versiones: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM assets WHERE deleted_at < ? AND id IN (SELECT asset_id FROM purge_versions)`,
			before).Scan(&report.Files); err != nil {
			return fmt.Errorf("contar archivos: %w", err)
		}

		for _, stmt := range []string{
			`UPDATE activity SET version_id = NULL WHERE version_id IN (SELECT id FROM purge_versions)`,
			// restored_from de las versiones que quedan pasa a NULL por la clave foránea.
			`DELETE FROM versions WHERE id IN (SELECT id FROM purge_versions)`,
			`DELETE FROM manifest_chunks WHERE manifest_id NOT IN (SELECT manifest_id FROM versions)`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("purgar versiones: %w", err)
			}
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM manifests WHERE id NOT IN (SELECT manifest_id FROM versions)`)
		if err != nil {
			return fmt.Errorf("purgar manifiestos: %w", err)
		}
		manifests, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("purgar manifiestos: %w", err)
		}
		report.Manifests = int(manifests)

		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*), COALESCE(SUM(stored_size), 0) FROM chunks c
			WHERE NOT EXISTS (SELECT 1 FROM manifest_chunks mc WHERE mc.chunk_sha256 = c.sha256)`,
		).Scan(&report.Chunks, &report.Bytes); err != nil {
			return fmt.Errorf("contar trozos sin referencias: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE temp.purge_versions`); err != nil {
			return fmt.Errorf("purgar versiones: %w", err)
		}
		if !apply {
			return errDryRun
		}
		return nil
	})
	if err != nil && !errors.Is(err, errDryRun) {
		return PurgeReport{}, err
	}
	return report, nil
}

// GCReport resume una recolección de trozos.
type GCReport struct {
	// Chunks son los archivos de trozo sin referencias y Bytes lo que ocupan.
	Chunks int
	Bytes  int64
}

// GC borra los trozos que ningún manifiesto usa. Borra primero las filas y después
// los archivos: si se corta a medias, quedan archivos sin registrar, inofensivos,
// que el siguiente gc recoge. Al revés quedarían filas sin datos y el servidor
// daría por guardados trozos que no existen. La clave foránea de manifest_chunks
// impide además borrar la fila de un trozo en uso. Sin apply solo cuenta. El
// servidor debe estar detenido.
func GC(ctx context.Context, database *db.DB, chunks *store.Store, apply bool) (GCReport, error) {
	// ponytail: todos los hashes vivos en memoria, unos 15 MB por cada 200 GB de
	// trozos de 1 MiB. Basta para el VPS; con muchos más, recorrer por carpetas.
	keep := make(map[chunker.Hash]struct{})
	err := database.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM chunks
			WHERE NOT EXISTS (SELECT 1 FROM manifest_chunks mc WHERE mc.chunk_sha256 = chunks.sha256)`); err != nil {
			return fmt.Errorf("borrar trozos sin referencias: %w", err)
		}
		rows, err := tx.QueryContext(ctx, `SELECT sha256 FROM chunks`)
		if err != nil {
			return fmt.Errorf("listar trozos: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var text string
			if err := rows.Scan(&text); err != nil {
				return fmt.Errorf("leer trozo: %w", err)
			}
			h, err := chunker.ParseHash(text)
			if err != nil {
				return fmt.Errorf("hash de trozo %q: %w", text, err)
			}
			keep[h] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("listar trozos: %w", err)
		}
		if !apply {
			return errDryRun
		}
		return nil
	})
	if err != nil && !errors.Is(err, errDryRun) {
		return GCReport{}, err
	}

	var report GCReport
	err = chunks.Walk(ctx, func(h chunker.Hash) error {
		if _, ok := keep[h]; ok {
			return nil
		}
		size, err := chunks.StoredSize(h)
		if err != nil {
			return err
		}
		if apply {
			if err := chunks.Remove(h); err != nil {
				return err
			}
		}
		report.Chunks++
		report.Bytes += size
		return nil
	})
	if err != nil {
		return GCReport{}, err
	}
	return report, nil
}
