package vault

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"

	"3d-vault/internal/events"
	"3d-vault/internal/rules"
	"3d-vault/internal/thumbs"
)

// compressedBlend es un .blend de Blender 5.0 comprimido con zstd, con varios MiB
// de datos antes de la miniatura: así ocupa varios trozos.
func compressedBlend(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("BLENDER17-01v0501")
	block := func(code string, data []byte) {
		head := make([]byte, 32)
		copy(head, code)
		binary.LittleEndian.PutUint64(head[16:24], uint64(len(data)))
		buf.Write(head)
		buf.Write(data)
	}
	block("REND", randomBytes(t, 3<<20))
	test := make([]byte, 8+4*4*4)
	binary.LittleEndian.PutUint32(test[0:4], 4)
	binary.LittleEndian.PutUint32(test[4:8], 4)
	block("TEST", test)
	block("ENDB", nil)

	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return enc.EncodeAll(buf.Bytes(), nil)
}

func TestThumbnailOnDemand(t *testing.T) {
	t.Parallel()

	// e.svc no tiene caché de miniaturas, como un servidor anterior a esta función.
	e := newEnv(t)
	data := compressedBlend(t)
	m := e.upload(data)
	if len(m.Chunks) < 2 {
		t.Fatalf("el archivo de prueba debería ocupar varios trozos, ocupa %d", len(m.Chunks))
	}
	created, err := e.svc.CreateAsset(e.ctx, e.juan, CreateAssetInput{ProjectID: e.project.ID, Path: "vieja.blend", Manifest: m, Comment: "Versión vieja"})
	if err != nil {
		t.Fatal(err)
	}
	other := e.newAsset("modelos/caja.obj")

	cache, err := thumbs.NewCache(filepath.Join(t.TempDir(), "thumbs"))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(Config{DB: e.db, Store: e.store, Hub: events.NewHub(), MaxFileSize: 1 << 30, Thumbs: cache})

	out, err := svc.Thumbnail(e.ctx, created.Version.ID)
	if err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil || img.Bounds().Dx() != 4 {
		t.Fatalf("miniatura no válida: %v", err)
	}
	if !cache.Known(created.Version.FileSHA256) {
		t.Error("la miniatura generada no quedó guardada")
	}

	// Una versión nueva genera la suya en segundo plano.
	fresh, err := svc.CreateAsset(e.ctx, e.juan, CreateAssetInput{ProjectID: e.project.ID, Path: "nueva.blend", Manifest: e.upload(compressedBlend(t)), Comment: "Versión nueva"})
	if err != nil {
		t.Fatal(err)
	}
	svc.WaitBackground()
	if !cache.Known(fresh.Version.FileSHA256) {
		t.Error("no se generó la miniatura al confirmar la versión")
	}

	_, err = svc.Thumbnail(e.ctx, other.Version.ID)
	wantRule(t, err, rules.CodeNotFound)
	_, err = e.svc.Thumbnail(e.ctx, created.Version.ID)
	wantRule(t, err, rules.CodeNotFound)
}
