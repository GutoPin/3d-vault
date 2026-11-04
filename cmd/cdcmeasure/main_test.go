package main

import (
	"bytes"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"3d-vault/internal/chunker"
)

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func randomBytes(seed uint64, size int) []byte {
	data := make([]byte, size)
	rng := rand.NewChaCha8([32]byte{byte(seed)})
	if _, err := rng.Read(data); err != nil {
		panic(err)
	}
	return data
}

func TestRunReportsReuse(t *testing.T) {
	t.Parallel()

	base := randomBytes(1, 16<<20)
	unrelated := randomBytes(2, 8<<20)

	tests := []struct {
		name string
		next []byte
		want string
	}{
		{"idéntico", base, "En la base   16.00 MiB (100.0 %)"},
		{"sin nada en común", unrelated, "A transferir 8.00 MiB (100.0 %)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			args := []string{writeFile(t, "base", base), writeFile(t, "nuevo", tt.next)}
			if err := run(args, &out); err != nil {
				t.Fatalf("run: %v", err)
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Fatalf("salida sin %q:\n%s", tt.want, out.String())
			}
		})
	}
}

func TestRunRejectsBadArguments(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "no-existe")
	tests := []struct {
		name string
		args []string
	}{
		{"sin argumentos", nil},
		{"un argumento", []string{"a"}},
		{"archivo inexistente", []string{missing, missing}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := run(tt.args, &bytes.Buffer{}); err == nil {
				t.Fatal("quería un error")
			}
		})
	}
}

// TestScanClassifiesChunks comprueba que cada byte cuenta en una sola categoría
// y que lo repetido dentro de un archivo no se atribuye a la base.
func TestScanClassifiesChunks(t *testing.T) {
	t.Parallel()

	seen := make(map[chunker.Hash]int)
	baseData := randomBytes(3, chunker.MaxSize)
	if _, err := scan(bytes.NewReader(baseData), seen, 0); err != nil {
		t.Fatalf("scan base: %v", err)
	}

	// Sin datos de la base: todo lo que no sea nuevo tiene que ser repetición.
	block := randomBytes(4, chunker.MaxSize)
	s, err := scan(bytes.NewReader(bytes.Repeat(block, 4)), seen, 1)
	if err != nil {
		t.Fatalf("scan nuevo: %v", err)
	}
	if s.size != 4*chunker.MaxSize {
		t.Fatalf("size = %d", s.size)
	}
	if s.earlierBytes != 0 {
		t.Fatalf("earlierBytes = %d, quiero 0", s.earlierBytes)
	}
	if s.repeatBytes == 0 {
		t.Fatal("repeatBytes = 0, quería repeticiones")
	}
	if got := s.newBytes + s.repeatBytes + s.earlierBytes; got != s.size {
		t.Fatalf("las categorías suman %d, quiero %d", got, s.size)
	}
}

func TestDetectFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		head []byte
		want string
	}{
		{"blend clásico", []byte("BLENDER-v405"), "blend sin comprimir"},
		{"blend 5.0", []byte("BLENDER17-01v0500"), "blend sin comprimir"},
		{"zstd", []byte{0x28, 0xb5, 0x2f, 0xfd, 0x40}, "comprimido con zstd"},
		{"gzip", []byte{0x1f, 0x8b, 0x08}, "comprimido con gzip"},
		{"otro", []byte("hola"), "otro"},
		{"vacío", nil, "otro"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := detectFormat(tt.head); got != tt.want {
				t.Fatalf("detectFormat = %q, quiero %q", got, tt.want)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.00 KiB"},
		{3 << 19, "1.50 MiB"},
		{2 << 30, "2.00 GiB"},
	}
	for _, tt := range tests {
		if got := formatBytes(tt.n); got != tt.want {
			t.Errorf("formatBytes(%d) = %q, quiero %q", tt.n, got, tt.want)
		}
	}
}
