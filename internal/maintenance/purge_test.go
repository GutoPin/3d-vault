package maintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"

	"3d-vault/internal/chunker"
	"3d-vault/internal/vault"
)

var (
	longAgo = time.Now().AddDate(-1, 0, 0)
	cutoff  = time.Now().AddDate(0, 0, -180)
)

func (f *fixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := f.database.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// addVersions sube versiones de a.blend hasta llegar a n.
func (f *fixture) addVersions(t *testing.T, n int) {
	t.Helper()
	for i := 2; i <= n; i++ {
		m := upload(t, f.svc, fmt.Appendf(nil, "versión %d", i))
		in := vault.CommitInput{AssetID: f.result.Asset.ID, BaseVersion: int64(i - 1), Manifest: m, SizeBytes: m.Size(), Comment: "Cambio"}
		if _, err := f.svc.CommitVersion(context.Background(), f.actor, in); err != nil {
			t.Fatalf("CommitVersion %d: %v", i, err)
		}
	}
}

// age hace que las versiones hasta upTo tengan un año.
func (f *fixture) age(t *testing.T, upTo int) {
	t.Helper()
	f.exec(t, `UPDATE versions SET created_at = ? WHERE asset_id = ? AND number <= ?`, longAgo.UnixMilli(), f.result.Asset.ID, upTo)
}

func (f *fixture) numbers(t *testing.T) []int64 {
	t.Helper()
	var numbers []int64
	err := f.database.ReadTx(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT number FROM versions WHERE asset_id = ? ORDER BY number`, f.result.Asset.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n int64
			if err := rows.Scan(&n); err != nil {
				return err
			}
			numbers = append(numbers, n)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return numbers
}

func TestPurgeKeepsHeadLastAndRecent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		oldUpTo  int
		keepLast int
		want     []int64
	}{
		{name: "conserva la actual", oldUpTo: 5, keepLast: 0, want: []int64{5}},
		{name: "conserva las últimas N", oldUpTo: 5, keepLast: 2, want: []int64{4, 5}},
		{name: "conserva las recientes", oldUpTo: 2, keepLast: 1, want: []int64{3, 4, 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			f.addVersions(t, 5)
			f.age(t, tt.oldUpTo)

			report, err := Purge(context.Background(), f.database, tt.keepLast, cutoff, true)
			if err != nil {
				t.Fatalf("Purge: %v", err)
			}
			if got := f.numbers(t); !slices.Equal(got, tt.want) {
				t.Fatalf("quedan %v, quiero %v", got, tt.want)
			}
			if report.Versions != 5-len(tt.want) || report.Files != 0 {
				t.Fatalf("informe = %+v", report)
			}
		})
	}
}

func TestPurgeDryRunChangesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := newFixture(t)
	f.addVersions(t, 3)
	f.age(t, 3)

	simulated, err := Purge(ctx, f.database, 0, cutoff, false)
	if err != nil {
		t.Fatalf("Purge simulado: %v", err)
	}
	if got := f.numbers(t); len(got) != 3 {
		t.Fatalf("la simulación borró versiones: quedan %v", got)
	}
	if simulated.Versions != 2 || simulated.Chunks == 0 || simulated.Bytes == 0 {
		t.Fatalf("simulación = %+v", simulated)
	}

	applied, err := Purge(ctx, f.database, 0, cutoff, true)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if applied != simulated {
		t.Fatalf("la purga %+v no coincide con la simulación %+v", applied, simulated)
	}
}

// TestPurgeKeepsSharedManifest borra la versión que otra restauró: su manifiesto
// sigue en uso y sus trozos sobreviven a gc.
func TestPurgeKeepsSharedManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := newFixture(t)
	f.addVersions(t, 2)
	restored, err := f.svc.Restore(ctx, f.actor, f.result.Asset.ID, 1, 2)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	f.age(t, 2)

	report, err := Purge(ctx, f.database, 1, cutoff, true)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if got := f.numbers(t); !slices.Equal(got, []int64{3}) {
		t.Fatalf("quedan %v, quiero [3]", got)
	}
	if report.Manifests != 1 {
		t.Fatalf("debe borrar solo el manifiesto de v2: %+v", report)
	}
	if _, err := GC(ctx, f.database, f.chunks, true); err != nil {
		t.Fatalf("GC: %v", err)
	}
	if got := readVersion(t, f.svc, restored.Version.ID); !bytes.Equal(got, f.data) {
		t.Fatal("la versión restaurada perdió su contenido")
	}
	versions, err := f.svc.ListVersions(ctx, f.result.Asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if versions[0].RestoredFrom != nil {
		t.Fatalf("restored_from apunta a una versión purgada: %d", *versions[0].RestoredFrom)
	}
}

func TestPurgeRemovesOldDeletedFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := newFixture(t)
	if err := f.svc.Delete(ctx, f.actor, f.result.Asset.ID, 1); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Recién borrado: se conserva.
	if report, err := Purge(ctx, f.database, 0, cutoff, true); err != nil || report.Versions != 0 {
		t.Fatalf("Purge = %+v, %v", report, err)
	}

	f.exec(t, `UPDATE assets SET deleted_at = ? WHERE id = ?`, longAgo.UnixMilli(), f.result.Asset.ID)
	report, err := Purge(ctx, f.database, 10, cutoff, true)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if report.Files != 1 || report.Versions != 1 || len(f.numbers(t)) != 0 {
		t.Fatalf("informe = %+v, quedan %v", report, f.numbers(t))
	}
}

func TestGCRemovesOnlyUnusedChunks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := newFixture(t)
	before, err := Check(ctx, f.database, f.chunks)
	if err != nil {
		t.Fatal(err)
	}

	// Uno registrado sin manifiesto y otro solo en disco, como tras un corte.
	registered, unregistered := []byte("trozo sin manifiesto"), []byte("trozo sin registrar")
	if err := f.svc.PutChunk(ctx, hashOf(registered), registered); err != nil {
		t.Fatal(err)
	}
	if _, err := f.chunks.Put(hashOf(unregistered), unregistered); err != nil {
		t.Fatal(err)
	}

	simulated, err := GC(ctx, f.database, f.chunks, false)
	if err != nil {
		t.Fatalf("GC simulado: %v", err)
	}
	if simulated.Chunks != 2 || simulated.Bytes == 0 {
		t.Fatalf("simulación = %+v", simulated)
	}
	if ok, _ := f.chunks.Has(hashOf(registered)); !ok {
		t.Fatal("la simulación borró un trozo")
	}

	applied, err := GC(ctx, f.database, f.chunks, true)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if applied != simulated {
		t.Fatalf("gc %+v no coincide con la simulación %+v", applied, simulated)
	}
	after, err := Check(ctx, f.database, f.chunks)
	if err != nil {
		t.Fatal(err)
	}
	if !after.OK() || after.Orphans != 0 || after.Checked != before.Checked {
		t.Fatalf("Check tras gc = %+v, antes %+v", after, before)
	}
	if got := readVersion(t, f.svc, f.result.Version.ID); !bytes.Equal(got, f.data) {
		t.Fatal("gc dañó una versión")
	}
}

func hashOf(data []byte) chunker.Hash {
	return chunker.Hash(sha256.Sum256(data))
}
