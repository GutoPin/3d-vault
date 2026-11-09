package blendrefs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"slices"
	"testing"
)

// Los .blend de testdata los genera testdata/generar.py con Blender 4.0.
// escena_01 enlaza una colección de maestros/camion_maestro.blend y usa una
// textura relativa, otra absoluta fuera del proyecto, una empaquetada y otra
// generada; solo las dos primeras son archivos externos. La absoluta es la
// carpeta donde se generaron: al regenerarlos, generar.py imprime la nueva.
var wantScene = []Ref{
	{Kind: KindImage, Path: "/tmp/claude-0/fx/fuera/cielo.png"},
	{Kind: KindImage, Path: "//../texturas/madera.png"},
	{Kind: KindLibrary, Path: "//../maestros/camion_maestro.blend"},
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRead(t *testing.T) {
	for _, tc := range []struct {
		file string
		want []Ref
	}{
		{"escena_01.blend", wantScene},
		{"escena_01_comprimida.blend", wantScene},
		{"camion_maestro.blend", []Ref{{Kind: KindImage, Path: "//../texturas/madera.png"}}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			got, err := Read(bytes.NewReader(read(t, tc.file)))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("refs = %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// toLargeBHead reescribe un .blend de Blender 4 con la cabecera y los BHead de
// Blender 5.0, que es lo único que cambia en lo que lee este paquete.
func toLargeBHead(t *testing.T, in []byte) []byte {
	t.Helper()
	if string(in[:9]) != "BLENDER-v" {
		t.Fatalf("cabecera inesperada %q", in[:12])
	}
	le := binary.LittleEndian
	out := []byte("BLENDER17-01v0501")
	pos := 12
	for pos+24 <= len(in) {
		code := in[pos : pos+4]
		length := le.Uint32(in[pos+4:])
		old := in[pos+8 : pos+16]
		sdna := in[pos+16 : pos+20]
		nr := le.Uint32(in[pos+20:])
		head := make([]byte, 32)
		copy(head[0:4], code)
		copy(head[4:8], sdna)
		copy(head[8:16], old)
		le.PutUint64(head[16:24], uint64(length))
		le.PutUint64(head[24:32], uint64(nr))
		out = append(out, head...)
		pos += 24
		out = append(out, in[pos:pos+int(length)]...)
		pos += int(length)
		if string(code) == "ENDB" {
			break
		}
	}
	return out
}

func TestReadBlender5Header(t *testing.T) {
	got, err := Read(bytes.NewReader(toLargeBHead(t, read(t, "escena_01.blend"))))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, wantScene) {
		t.Errorf("refs = %+v\nwant %+v", got, wantScene)
	}
}

func TestReadInvalid(t *testing.T) {
	scene := read(t, "escena_01.blend")
	for name, data := range map[string][]byte{
		"vacío":    nil,
		"no blend": []byte("PNG no es un blend, aunque tenga bytes"),
		"truncado": scene[:len(scene)/2],
		"sin DNA1": scene[:12],
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Read(bytes.NewReader(data)); !errors.Is(err, ErrInvalid) {
				t.Errorf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestMemberOf(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		size, ptr int
		name      string
		wantSize  int
		wantPtr   bool
	}{
		{"filepath[1024]", 1, 8, "filepath", 1024, false},
		{"*next", 0, 8, "next", 8, true},
		{"*lib", 0, 4, "lib", 4, true},
		{"(*func)()", 0, 8, "func", 8, true},
		{"mat[4][4]", 4, 8, "mat", 64, false},
		{"*mtex[18]", 0, 8, "mtex", 144, true},
	} {
		f := memberOf("x", tc.raw, tc.size, tc.ptr)
		if f.name != tc.name || f.size != tc.wantSize || f.ptr != tc.wantPtr {
			t.Errorf("%s: %+v", tc.raw, f)
		}
	}
}
