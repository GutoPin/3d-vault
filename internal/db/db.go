// Package db abre la base SQLite y ofrece transacciones de lectura y escritura.
//
// Usa dos grupos de conexiones: uno de escritura con una sola conexión, porque
// SQLite solo admite un escritor, y otro de lectura. Una transacción de escritura
// toma el bloqueo al empezar (_txlock=immediate), así que lo que lee dentro sigue
// siendo cierto hasta que confirma.
package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // Controlador SQLite sin cgo.
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Claves de la tabla meta.
const (
	// MetaServerEpoch cambia al restaurar un backup. Sin él, un agente no notaría
	// que seq retrocedió y se saltaría eventos reutilizados.
	MetaServerEpoch = "server_epoch"
	// MetaEventsFloor es el último seq eliminado de la tabla de eventos.
	MetaEventsFloor = "events_floor"
)

// DB agrupa las conexiones de lectura y escritura.
type DB struct {
	path  string
	write *sql.DB
	read  *sql.DB
}

// Open abre la base en path, la crea si no existe y aplica las migraciones.
func Open(ctx context.Context, path string) (*DB, error) {
	write, err := openPool(path, url.Values{"_txlock": {"immediate"}}, 1)
	if err != nil {
		return nil, err
	}
	d := &DB{path: path, write: write}

	if err := d.migrate(ctx); err != nil {
		_ = write.Close()
		return nil, err
	}
	if err := d.ensureMeta(ctx); err != nil {
		_ = write.Close()
		return nil, err
	}

	readers := max(4, 2*runtime.GOMAXPROCS(0))
	read, err := openPool(path, url.Values{"_pragma": {"query_only(1)"}}, readers)
	if err != nil {
		_ = write.Close()
		return nil, err
	}
	d.read = read
	return d, nil
}

func openPool(path string, extra url.Values, maxConns int) (*sql.DB, error) {
	q := url.Values{
		"_pragma": {
			"busy_timeout(10000)",
			"journal_mode(WAL)",
			"synchronous(FULL)",
			"foreign_keys(1)",
		},
	}
	for key, values := range extra {
		q[key] = append(q[key], values...)
	}
	pool, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("abrir la base de datos: %w", err)
	}
	pool.SetMaxOpenConns(maxConns)
	pool.SetMaxIdleConns(maxConns)
	return pool, nil
}

// Close cierra todas las conexiones.
func (d *DB) Close() error {
	var errs []error
	if d.read != nil {
		errs = append(errs, d.read.Close())
	}
	errs = append(errs, d.write.Close())
	return errors.Join(errs...)
}

// WriteTx ejecuta fn en una transacción de escritura. fn no debe usar la red ni
// operaciones largas: mientras corre, nadie más puede escribir. Tampoco debe
// llamar a WriteTx, porque solo hay una conexión de escritura.
func (d *DB) WriteTx(ctx context.Context, fn func(*sql.Tx) error) error {
	return runTx(ctx, d.write, fn)
}

// ReadTx ejecuta fn en una transacción de solo lectura, que ve una foto
// consistente de la base aunque otros escriban a la vez.
func (d *DB) ReadTx(ctx context.Context, fn func(*sql.Tx) error) error {
	return runTx(ctx, d.read, fn)
}

func runTx(ctx context.Context, pool *sql.DB, fn func(*sql.Tx) error) (err error) {
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("iniciar transacción: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("confirmar transacción: %w", err)
	}
	return nil
}

func (d *DB) migrate(ctx context.Context) error {
	var current int
	if err := d.write.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("leer la versión del esquema: %w", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if current > len(migrations) {
		return fmt.Errorf("la base de datos es de una versión más nueva (%d) que este programa (%d)", current, len(migrations))
	}

	for i := current; i < len(migrations); i++ {
		err := d.WriteTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
				return err
			}
			// security: i es un índice interno, no una entrada del usuario.
			_, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(i+1))
			return err
		})
		if err != nil {
			return fmt.Errorf("aplicar la migración %d: %w", i+1, err)
		}
	}
	return nil
}

func loadMigrations() ([]string, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("leer migraciones: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)

	scripts := make([]string, 0, len(names))
	for _, name := range names {
		script, err := migrationFiles.ReadFile(path.Join("migrations", name))
		if err != nil {
			return nil, fmt.Errorf("leer migración %s: %w", name, err)
		}
		scripts = append(scripts, string(script))
	}
	return scripts, nil
}

func (d *DB) ensureMeta(ctx context.Context) error {
	epoch, err := newEpoch()
	if err != nil {
		return err
	}
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO meta(key, value) VALUES (?, ?), (?, '0')`,
			MetaServerEpoch, epoch, MetaEventsFloor); err != nil {
			return fmt.Errorf("inicializar meta: %w", err)
		}
		return nil
	})
}

func newEpoch() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generar epoch: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// RegenerateEpoch cambia server_epoch. Se usa al restaurar un backup.
func (d *DB) RegenerateEpoch(ctx context.Context) error {
	epoch, err := newEpoch()
	if err != nil {
		return err
	}
	return d.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE meta SET value = ? WHERE key = ?`, epoch, MetaServerEpoch); err != nil {
			return fmt.Errorf("actualizar epoch: %w", err)
		}
		return nil
	})
}

// BackupTo escribe una copia consistente de la base en dst, de forma atómica.
// Puede ejecutarse con el servidor en marcha.
func (d *DB) BackupTo(ctx context.Context, dst string) (err error) {
	tmp := dst + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("borrar temporal anterior: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	// security: VACUUM INTO no admite parámetros en todas las versiones; la ruta
	// la da el administrador por línea de comandos y se escapa como literal SQL.
	stmt := "VACUUM INTO '" + strings.ReplaceAll(tmp, "'", "''") + "'"
	if _, err := d.write.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("copiar la base de datos: %w", err)
	}

	f, err := os.Open(tmp)
	if err != nil {
		return fmt.Errorf("abrir la copia: %w", err)
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sincronizar la copia: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("mover la copia: %w", err)
	}
	return nil
}
