package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"3d-vault/internal/chunker"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPutGetRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data []byte
		// wantFormat es el primer byte esperado en disco.
		wantFormat byte
	}{
		{"comprimible", bytes.Repeat([]byte("camion_797F"), 100_000), formatZstd},
		{"aleatorio", randomBytes(t, 1<<20), formatRaw},
		{"parece zstd pero no lo es", append([]byte{0x28, 0xb5, 0x2f, 0xfd}, randomBytes(t, 4096)...), formatRaw},
		{"vacío", nil, formatRaw},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newStore(t)
			h := chunker.Hash(sha256.Sum256(tt.data))

			stored, err := s.Put(h, tt.data)
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			raw, err := os.ReadFile(s.path(h))
			if err != nil {
				t.Fatal(err)
			}
			if raw[0] != tt.wantFormat {
				t.Errorf("formato = %d, quiero %d", raw[0], tt.wantFormat)
			}
			if stored != int64(len(raw)) {
				t.Errorf("stored = %d, en disco hay %d", stored, len(raw))
			}

			got, err := s.Get(h)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !bytes.Equal(got, tt.data) {
				t.Fatal("el contenido leído no coincide")
			}
		})
	}
}

func TestPutRejectsWrongHash(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	h := chunker.Hash{1, 2, 3}
	if _, err := s.Put(h, []byte("otro contenido")); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("Put = %v, quiero ErrHashMismatch", err)
	}
	if exists, _ := s.Has(h); exists {
		t.Fatal("no debería guardar datos con un hash incorrecto")
	}
}

func TestPutIsIdempotent(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	data := randomBytes(t, 1024)
	h := chunker.Hash(sha256.Sum256(data))

	first, err := s.Put(h, data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	second, err := s.Put(h, data)
	if err != nil {
		t.Fatalf("segundo Put: %v", err)
	}
	if first != second {
		t.Fatalf("tamaños distintos: %d y %d", first, second)
	}
}

func TestConcurrentPutsOfSameChunk(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	data := bytes.Repeat([]byte("x"), 1<<20)
	h := chunker.Hash(sha256.Sum256(data))

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, err := s.Put(h, data); err != nil {
				t.Errorf("Put: %v", err)
			}
		})
	}
	wg.Wait()

	if _, err := s.Get(h); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(s.root, tmpDirName)); len(entries) != 0 {
		t.Fatalf("quedaron %d temporales", len(entries))
	}
}

func TestGetDetectsDamage(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte("camion_797F"), 10_000)
	h := chunker.Hash(sha256.Sum256(data))

	tests := []struct {
		name   string
		damage func([]byte) []byte
	}{
		{"byte alterado", func(raw []byte) []byte {
			raw[len(raw)/2] ^= 0xff
			return raw
		}},
		{"truncado", func(raw []byte) []byte { return raw[:len(raw)/2] }},
		{"vacío", func([]byte) []byte { return nil }},
		{"formato desconocido", func(raw []byte) []byte {
			raw[0] = 9
			return raw
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newStore(t)
			if _, err := s.Put(h, data); err != nil {
				t.Fatalf("Put: %v", err)
			}
			raw, err := os.ReadFile(s.path(h))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.path(h), tt.damage(raw), filePerm); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Get(h); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Get = %v, quiero ErrCorrupt", err)
			}
		})
	}
}

func TestGetMissing(t *testing.T) {
	t.Parallel()

	if _, err := newStore(t).Get(chunker.Hash{9}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, quiero ErrNotFound", err)
	}
}

func TestCopyMissingToKeepsDestination(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	src, dst := newStore(t), newStore(t)

	shared := randomBytes(t, 2048)
	onlySrc := bytes.Repeat([]byte("solo origen"), 1000)
	onlyDst := randomBytes(t, 2048)

	for _, data := range [][]byte{shared, onlySrc} {
		if _, err := src.Put(sha256.Sum256(data), data); err != nil {
			t.Fatal(err)
		}
	}
	for _, data := range [][]byte{shared, onlyDst} {
		if _, err := dst.Put(sha256.Sum256(data), data); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := src.CopyMissingTo(ctx, dst)
	if err != nil {
		t.Fatalf("CopyMissingTo: %v", err)
	}
	if stats.Copied != 1 || stats.Skipped != 1 {
		t.Fatalf("stats = %+v, quiero 1 copiado y 1 omitido", stats)
	}
	for _, data := range [][]byte{shared, onlySrc, onlyDst} {
		got, err := dst.Get(sha256.Sum256(data))
		if err != nil {
			t.Fatalf("Get en destino: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatal("contenido distinto en destino")
		}
	}
}

func TestWalkListsChunksAndSkipsTemp(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	data := randomBytes(t, 512)
	h := chunker.Hash(sha256.Sum256(data))
	if _, err := s.Put(h, data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, tmpDirName, "trozo-a"), []byte("x"), filePerm); err != nil {
		t.Fatal(err)
	}

	var found []chunker.Hash
	if err := s.Walk(context.Background(), func(got chunker.Hash) error {
		found = append(found, got)
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(found) != 1 || found[0] != h {
		t.Fatalf("Walk = %v", found)
	}

	if err := s.CleanTemp(); err != nil {
		t.Fatalf("CleanTemp: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(s.root, tmpDirName)); len(entries) != 0 {
		t.Fatalf("quedaron %d temporales", len(entries))
	}
}
