package thumbs

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"image/color"
	"image/png"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// fakeBlend arma un .blend mínimo: cabecera, un REND, el TEST opcional, GLOB y ENDB.
type fakeBlend struct {
	header string
	format bheadFormat
	order  binary.ByteOrder
}

func (f fakeBlend) block(buf *bytes.Buffer, code string, data []byte) {
	head := make([]byte, f.format.size())
	copy(head, code)
	switch f.format {
	case largeBHead8:
		f.order.PutUint64(head[16:24], uint64(len(data)))
		f.order.PutUint64(head[24:32], 1)
	default:
		f.order.PutUint32(head[4:8], uint32(len(data)))
		f.order.PutUint32(head[len(head)-4:], 1)
	}
	buf.Write(head)
	buf.Write(data)
}

// testPixels es una miniatura de 2 × 2. En el archivo, la primera fila es la de abajo.
var testPixels = []byte{
	255, 0, 0, 255, 0, 255, 0, 255, // abajo: rojo, verde
	0, 0, 255, 255, 255, 255, 255, 128, // arriba: azul, blanco semitransparente
}

func (f fakeBlend) build(withThumb bool) []byte {
	var buf bytes.Buffer
	buf.WriteString(f.header)
	f.block(&buf, "REND", make([]byte, 72))
	if withThumb {
		test := make([]byte, 8, 8+len(testPixels))
		f.order.PutUint32(test[0:4], 2)
		f.order.PutUint32(test[4:8], 2)
		f.block(&buf, "TEST", append(test, testPixels...))
	}
	f.block(&buf, "GLOB", make([]byte, 1024))
	f.block(&buf, "ENDB", nil)
	return buf.Bytes()
}

var variants = []struct {
	name  string
	blend fakeBlend
}{
	{"punteros de 4 bytes", fakeBlend{"BLENDER_v279", bhead4, binary.LittleEndian}},
	{"punteros de 8 bytes", fakeBlend{"BLENDER-v300", smallBHead8, binary.LittleEndian}},
	{"big endian de 4 bytes", fakeBlend{"BLENDER_V262", bhead4, binary.BigEndian}},
	{"big endian de 8 bytes", fakeBlend{"BLENDER-V279", smallBHead8, binary.BigEndian}},
	{"Blender 5.0", fakeBlend{"BLENDER17-01v0501", largeBHead8, binary.LittleEndian}},
}

func zstdBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return enc.EncodeAll(data, nil)
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBlendPNG(t *testing.T) {
	t.Parallel()

	for _, v := range variants {
		raw := v.blend.build(true)
		for _, c := range []struct {
			name string
			data []byte
		}{
			{"sin comprimir", raw},
			{"zstd", zstdBytes(t, raw)},
			{"gzip", gzipBytes(t, raw)},
		} {
			t.Run(v.name+", "+c.name, func(t *testing.T) {
				t.Parallel()
				out, err := BlendPNG(bytes.NewReader(c.data))
				if err != nil {
					t.Fatalf("BlendPNG: %v", err)
				}
				img, err := png.Decode(bytes.NewReader(out))
				if err != nil {
					t.Fatalf("el resultado no es PNG: %v", err)
				}
				if b := img.Bounds(); b.Dx() != 2 || b.Dy() != 2 {
					t.Fatalf("tamaño = %v", b)
				}
				// En la imagen, arriba a la izquierda va la última fila del archivo.
				want := map[[2]int]color.NRGBA{
					{0, 0}: {0, 0, 255, 255},
					{1, 0}: {255, 255, 255, 128},
					{0, 1}: {255, 0, 0, 255},
					{1, 1}: {0, 255, 0, 255},
				}
				for xy, c := range want {
					if got := color.NRGBAModel.Convert(img.At(xy[0], xy[1])); got != c {
						t.Errorf("píxel %v = %v, quiero %v", xy, got, c)
					}
				}
			})
		}
	}
}

func TestBlendWithoutThumbnail(t *testing.T) {
	t.Parallel()

	for _, v := range variants {
		_, err := BlendPNG(bytes.NewReader(v.blend.build(false)))
		if !errors.Is(err, ErrNoThumbnail) {
			t.Errorf("%s: error = %v, quiero ErrNoThumbnail", v.name, err)
		}
	}
}

func TestBlendInvalid(t *testing.T) {
	t.Parallel()

	full := variants[4].blend.build(true)
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"vacío", nil},
		{"no es un .blend", []byte("PNG no es un blend, aunque sea largo")},
		{"cabecera truncada", full[:10]},
		{"truncado en el TEST", full[:len(full)-1100]},
		{"zstd truncado", zstdBytes(t, full)[:20]},
		{"formato futuro", []byte("BLENDER17-02v0600" + string(make([]byte, 64)))},
	} {
		_, err := BlendPNG(bytes.NewReader(tt.data))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, quiero ErrInvalid", tt.name, err)
		}
	}
}

func TestCache(t *testing.T) {
	t.Parallel()

	c, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if c.Known(sha) {
		t.Fatal("una caché vacía no conoce nada")
	}
	if err := c.Put(sha, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(sha); !errors.Is(err, ErrNoThumbnail) || !c.Known(sha) {
		t.Fatalf("sin miniatura: Get = %v", err)
	}
	if err := c.Put(sha, []byte("png")); err != nil {
		t.Fatal(err)
	}
	if data, err := c.Get(sha); err != nil || string(data) != "png" {
		t.Fatalf("Get = %q, %v", data, err)
	}
	if err := c.Put("../../etc/passwd", []byte("x")); err == nil {
		t.Fatal("aceptó una ruta en lugar de un hash")
	}
}
