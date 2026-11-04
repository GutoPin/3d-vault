// Package maintenance implementa las copias de seguridad, su restauración y la
// verificación del almacén. La usan los comandos de administración del servidor.
package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"3d-vault/internal/chunker"
	"3d-vault/internal/db"
	"3d-vault/internal/store"
)

const (
	// DatabaseFile y ChunksDir son los nombres dentro de DATA_DIR y de un backup.
	DatabaseFile = "vault.db"
	ChunksDir    = "chunks"

	dirPerm = 0o750
)

// BackupStats resume una copia.
type BackupStats struct {
	Copied  int
	Skipped int
}

// Backup copia la base y los trozos a dir, y puede ejecutarse con el servidor en
// marcha. El orden importa: una fila de trozo solo existe cuando sus datos ya están
// en disco, así que todo lo que referencia esta copia de la base existe al copiar
// los trozos. Al revés, la copia podría referenciar trozos que no se copiaron.
// Nunca borra nada en el destino.
func Backup(ctx context.Context, database *db.DB, chunks *store.Store, dir string) (BackupStats, error) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return BackupStats{}, fmt.Errorf("crear la carpeta de copia: %w", err)
	}
	if err := database.BackupTo(ctx, filepath.Join(dir, DatabaseFile)); err != nil {
		return BackupStats{}, err
	}

	dst, err := store.New(filepath.Join(dir, ChunksDir))
	if err != nil {
		return BackupStats{}, err
	}
	defer dst.Close()

	stats, err := chunks.CopyMissingTo(ctx, dst)
	if err != nil {
		return BackupStats{}, fmt.Errorf("copiar los trozos: %w", err)
	}
	return BackupStats{Copied: stats.Copied, Skipped: stats.Skipped}, nil
}

// ErrDataExists indica que el destino de una restauración ya tiene datos.
var ErrDataExists = errors.New("ya existe una base de datos en el destino: usa --force para apartarla y restaurar")

// Restore recupera un backup en dataDir. Con force, aparta la base existente en
// lugar de borrarla. El servidor debe estar detenido.
func Restore(ctx context.Context, from, dataDir string, force bool, now time.Time) (BackupStats, error) {
	src := filepath.Join(from, DatabaseFile)
	if _, err := os.Stat(src); err != nil {
		return BackupStats{}, fmt.Errorf("no hay copia en %s: %w", from, err)
	}
	if err := os.MkdirAll(dataDir, dirPerm); err != nil {
		return BackupStats{}, fmt.Errorf("crear la carpeta de datos: %w", err)
	}

	dst := filepath.Join(dataDir, DatabaseFile)
	if _, err := os.Stat(dst); err == nil {
		if !force {
			return BackupStats{}, ErrDataExists
		}
		if err := setAside(dst, now); err != nil {
			return BackupStats{}, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return BackupStats{}, fmt.Errorf("consultar la base de datos: %w", err)
	}

	if err := copyFileAtomic(src, dst); err != nil {
		return BackupStats{}, err
	}

	backupChunks, err := store.New(filepath.Join(from, ChunksDir))
	if err != nil {
		return BackupStats{}, err
	}
	defer backupChunks.Close()
	dataChunks, err := store.New(filepath.Join(dataDir, ChunksDir))
	if err != nil {
		return BackupStats{}, err
	}
	defer dataChunks.Close()

	stats, err := backupChunks.CopyMissingTo(ctx, dataChunks)
	if err != nil {
		return BackupStats{}, fmt.Errorf("copiar los trozos: %w", err)
	}

	// Sin esto, un agente seguiría pidiendo eventos desde un número que ya no
	// significa lo mismo y se saltaría cambios.
	database, err := db.Open(ctx, dst)
	if err != nil {
		return BackupStats{}, err
	}
	defer database.Close()
	if err := database.RegenerateEpoch(ctx); err != nil {
		return BackupStats{}, err
	}
	return BackupStats{Copied: stats.Copied, Skipped: stats.Skipped}, nil
}

func setAside(path string, now time.Time) error {
	suffix := ".antes-de-restaurar-" + now.UTC().Format("20060102-150405")
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Rename(name, name+suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("apartar la base de datos: %w", err)
		}
	}
	return nil
}

func copyFileAtomic(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("abrir la copia: %w", err)
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp-*")
	if err != nil {
		return fmt.Errorf("crear temporal: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		return fmt.Errorf("copiar la base de datos: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sincronizar la base de datos: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cerrar la base de datos: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("mover la base de datos: %w", err)
	}
	return nil
}

// CheckReport es el resultado de verificar el almacén.
type CheckReport struct {
	Checked int
	// Corrupt y Missing son trozos registrados que no se pueden leer.
	Corrupt []string
	Missing []string
	// Orphans son archivos sin registrar. Son inofensivos: quedan si el servidor
	// se detiene entre guardar un trozo y registrarlo.
	Orphans int
}

// OK indica que todos los trozos registrados están bien.
func (r CheckReport) OK() bool {
	return len(r.Corrupt) == 0 && len(r.Missing) == 0
}

// Check relee todos los trozos registrados y verifica sus hashes.
func Check(ctx context.Context, database *db.DB, chunks *store.Store) (CheckReport, error) {
	var hashes []string
	err := database.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT sha256 FROM chunks`)
		if err != nil {
			return fmt.Errorf("listar trozos: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var hash string
			if err := rows.Scan(&hash); err != nil {
				return fmt.Errorf("leer trozo: %w", err)
			}
			hashes = append(hashes, hash)
		}
		return rows.Err()
	})
	if err != nil {
		return CheckReport{}, err
	}

	var report CheckReport
	registered := make(map[chunker.Hash]struct{}, len(hashes))
	for _, text := range hashes {
		if err := ctx.Err(); err != nil {
			return CheckReport{}, err
		}
		h, err := chunker.ParseHash(text)
		if err != nil {
			report.Corrupt = append(report.Corrupt, text)
			continue
		}
		registered[h] = struct{}{}
		report.Checked++
		if _, err := chunks.Get(h); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				report.Missing = append(report.Missing, text)
				continue
			}
			if errors.Is(err, store.ErrCorrupt) {
				report.Corrupt = append(report.Corrupt, text)
				continue
			}
			return CheckReport{}, err
		}
	}

	err = chunks.Walk(ctx, func(h chunker.Hash) error {
		if _, ok := registered[h]; !ok {
			report.Orphans++
		}
		return nil
	})
	if err != nil {
		return CheckReport{}, err
	}
	return report, nil
}
