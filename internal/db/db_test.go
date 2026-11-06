package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func meta(t *testing.T, d *DB, key string) string {
	t.Helper()
	var value string
	err := d.ReadTx(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	})
	if err != nil {
		t.Fatalf("leer meta: %v", err)
	}
	return value
}

func TestOpenAppliesPragmas(t *testing.T) {
	t.Parallel()

	d := openDB(t)
	tests := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"synchronous", "2"},
		{"busy_timeout", "10000"},
	}
	for _, pool := range []*sql.DB{d.write, d.read} {
		for _, tt := range tests {
			var got string
			if err := pool.QueryRow("PRAGMA " + tt.pragma).Scan(&got); err != nil {
				t.Fatalf("PRAGMA %s: %v", tt.pragma, err)
			}
			if got != tt.want {
				t.Errorf("PRAGMA %s = %q, quiero %q", tt.pragma, got, tt.want)
			}
		}
	}
}

func TestReopenKeepsDataAndEpoch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.db")

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	epoch := meta(t, first, MetaServerEpoch)
	if epoch == "" {
		t.Fatal("epoch vacío")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("segundo Open: %v", err)
	}
	defer second.Close()
	if got := meta(t, second, MetaServerEpoch); got != epoch {
		t.Fatalf("epoch cambió al reabrir: %q y %q", epoch, got)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.db")
	d, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := d.write.ExecContext(ctx, "PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("quería un error con un esquema más nuevo")
	}
}

func TestReadPoolCannotWrite(t *testing.T) {
	t.Parallel()

	d := openDB(t)
	err := d.ReadTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('x', 'y')`)
		return err
	})
	if err == nil {
		t.Fatal("una transacción de lectura no debería poder escribir")
	}
}

func TestWriteTxRollsBackOnError(t *testing.T) {
	t.Parallel()

	d := openDB(t)
	errBoom := errors.New("fallo")
	err := d.WriteTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('x', 'y')`); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("WriteTx = %v, quiero %v", err, errBoom)
	}
	var count int
	if err := d.read.QueryRow(`SELECT COUNT(*) FROM meta WHERE key = 'x'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("el cambio debería haberse deshecho")
	}
}

func TestWriteTxRollsBackOnPanic(t *testing.T) {
	t.Parallel()

	d := openDB(t)
	func() {
		defer func() { _ = recover() }()
		_ = d.WriteTx(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('x', 'y')`); err != nil {
				return err
			}
			panic("fallo")
		})
	}()

	// La conexión de escritura debe quedar disponible.
	err := d.WriteTx(context.Background(), func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM meta WHERE key = 'x'`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("el cambio debería haberse deshecho")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WriteTx tras un pánico: %v", err)
	}
}

// TestWriteTxSerializes demuestra que leer y escribir en una transacción no
// pierde incrementos, que es lo que evita números de versión repetidos.
func TestWriteTxSerializes(t *testing.T) {
	t.Parallel()

	d := openDB(t)
	ctx := context.Background()
	if err := d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('n', '0')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	const workers = 32
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			err := d.WriteTx(ctx, func(tx *sql.Tx) error {
				var n int
				if err := tx.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key = 'n'`).Scan(&n); err != nil {
					return err
				}
				_, err := tx.Exec(`UPDATE meta SET value = ? WHERE key = 'n'`, n+1)
				return err
			})
			if err != nil {
				t.Errorf("WriteTx: %v", err)
			}
		})
	}
	wg.Wait()

	if got := meta(t, d, "n"); got != "32" {
		t.Fatalf("n = %s, quiero 32", got)
	}
}

func TestRegenerateEpochChangesIt(t *testing.T) {
	t.Parallel()

	d := openDB(t)
	before := meta(t, d, MetaServerEpoch)
	if err := d.RegenerateEpoch(context.Background()); err != nil {
		t.Fatalf("RegenerateEpoch: %v", err)
	}
	if meta(t, d, MetaServerEpoch) == before {
		t.Fatal("el epoch debería haber cambiado")
	}
}

func TestBackupToIsConsistentAndHandlesQuotes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	d := openDB(t)
	if err := d.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('x', 'guardado')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "copia d'emergencia.db")
	if err := d.BackupTo(ctx, dst); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	// Una segunda copia sobre la anterior también debe funcionar.
	if err := d.BackupTo(ctx, dst); err != nil {
		t.Fatalf("segundo BackupTo: %v", err)
	}

	copied, err := Open(ctx, dst)
	if err != nil {
		t.Fatalf("Open de la copia: %v", err)
	}
	defer copied.Close()
	if got := meta(t, copied, "x"); got != "guardado" {
		t.Fatalf("x = %q", got)
	}
}

// Una copia en marcha no debe esperar a las escrituras ni hacerlas esperar: el
// servidor la hace mientras otros suben archivos.
func TestBackupToDoesNotUseWriteConnection(t *testing.T) {
	t.Parallel()

	// Si la usara, se quedaría esperando: el plazo lo convierte en un fallo.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := openDB(t)
	dst := filepath.Join(t.TempDir(), "copia.db")
	err := d.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('x', 'sin confirmar')`); err != nil {
			return err
		}
		return d.BackupTo(ctx, dst)
	})
	if err != nil {
		t.Fatalf("BackupTo dentro de una escritura: %v", err)
	}
}
