package manifest

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"

	"3d-vault/internal/chunker"
)

func TestBuildDescribesTheFile(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte("camion_797F-"), 2<<20)
	m, err := Build(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if m.FileSHA256 != sha256.Sum256(data) {
		t.Error("hash del archivo incorrecto")
	}
	if m.Size() != int64(len(data)) {
		t.Errorf("Size = %d, quiero %d", m.Size(), len(data))
	}
	if err := m.Validate(int64(len(data))); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestBuildEmptyFile(t *testing.T) {
	t.Parallel()

	m, err := Build(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(m.Chunks) != 0 || m.FileSHA256 != sha256.Sum256(nil) {
		t.Fatalf("manifiesto vacío incorrecto: %+v", m)
	}
	if err := m.Validate(1); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		m       Manifest
		max     int64
		wantErr string
	}{
		{"tamaño cero", Manifest{Chunks: []Chunk{{Size: 0}}}, 100, "fuera del rango"},
		{"tamaño negativo", Manifest{Chunks: []Chunk{{Size: -1}}}, 100, "fuera del rango"},
		{"trozo enorme", Manifest{Chunks: []Chunk{{Size: chunker.MaxSize + 1}}}, 1 << 40, "fuera del rango"},
		{"archivo enorme", Manifest{Chunks: []Chunk{{Size: 60}, {Size: 60}}}, 100, "supera el máximo"},
		{"vacío con hash de otro archivo", Manifest{FileSHA256: chunker.Hash{1}}, 100, "archivo vacío"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.m.Validate(tt.max)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate = %v, quiero un error con %q", err, tt.wantErr)
			}
		})
	}
}
