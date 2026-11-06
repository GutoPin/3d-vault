package chunker

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"sync"
	"testing"
	"testing/iotest"
)

// randomData genera datos reproducibles: la misma semilla da siempre los mismos bytes.
func randomData(seed uint64, size int) []byte {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	data := make([]byte, size+8)
	for i := 0; i < size; i += 8 {
		binary.LittleEndian.PutUint64(data[i:], rng.Uint64())
	}
	return data[:size]
}

// chunkInfo es una copia de los metadatos de un trozo, válida tras llamar a Next.
type chunkInfo struct {
	offset int64
	size   int
	hash   Hash
}

func collect(r io.Reader) ([]chunkInfo, error) {
	var chunks []chunkInfo
	c := New(r)
	for {
		chunk, err := c.Next()
		if errors.Is(err, io.EOF) {
			return chunks, nil
		}
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunkInfo{chunk.Offset, len(chunk.Data), chunk.Hash})
	}
}

func readAll(t *testing.T, r io.Reader) []chunkInfo {
	t.Helper()
	chunks, err := collect(r)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return chunks
}

// fingerprint resume la lista completa de cortes en una sola cadena.
func fingerprint(chunks []chunkInfo) string {
	h := sha256.New()
	for _, c := range chunks {
		fmt.Fprintf(h, "%d:%d:%s\n", c.offset, c.size, c.hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestGoldenCuts fija los cortes. Si falla, cambió el algoritmo: los archivos
// existentes dejarán de deduplicarse con sus versiones anteriores, así que el
// cambio debe ser deliberado y constar en docs/DECISIONES.md.
func TestGoldenCuts(t *testing.T) {
	t.Parallel()

	const (
		wantChunks = 55
		wantPrint  = "ecd5f3cffe141a883539c89ccadd76f28a32f2ad8e7964354ced79cc6f13641a"
	)

	chunks := readAll(t, bytes.NewReader(randomData(1, 64<<20)))
	if got := fingerprint(chunks); got != wantPrint || len(chunks) != wantChunks {
		t.Fatalf("cortes distintos: %d trozos, huella %s", len(chunks), got)
	}
}

func TestReassemblesInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		size int
	}{
		{"vacío", 0},
		{"un byte", 1},
		{"igual al mínimo", MinSize},
		{"mínimo más uno", MinSize + 1},
		{"igual al máximo", MaxSize},
		{"igual al búfer", bufSize},
		{"grande", 32 << 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			input := randomData(2, tt.size)
			var got []byte
			c := New(bytes.NewReader(input))
			for {
				chunk, err := c.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("Next: %v", err)
				}
				if chunk.Offset != int64(len(got)) {
					t.Fatalf("Offset = %d, quiero %d", chunk.Offset, len(got))
				}
				if chunk.Hash != sha256.Sum256(chunk.Data) {
					t.Fatalf("hash incorrecto en la posición %d", chunk.Offset)
				}
				got = append(got, chunk.Data...)
			}
			if !bytes.Equal(got, input) {
				t.Fatalf("los trozos no reconstruyen la entrada: %d bytes, quiero %d", len(got), len(input))
			}
		})
	}
}

func TestChunkSizeBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data []byte
	}{
		{"aleatorio", randomData(3, 64<<20)},
		{"ceros", make([]byte, 32<<20)},
		{"repetitivo", bytes.Repeat([]byte("camion_797F"), 2<<20)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			chunks := readAll(t, bytes.NewReader(tt.data))
			for i, c := range chunks {
				if c.size > MaxSize {
					t.Errorf("trozo %d mide %d, supera el máximo", i, c.size)
				}
				if i < len(chunks)-1 && c.size < MinSize {
					t.Errorf("trozo %d mide %d, no llega al mínimo", i, c.size)
				}
			}
		})
	}
}

func TestAverageSizeIsNearTarget(t *testing.T) {
	t.Parallel()

	const size = 256 << 20
	chunks := readAll(t, bytes.NewReader(randomData(4, size)))
	if avg := size / len(chunks); avg < AvgSize/2 || avg > AvgSize*2 {
		t.Fatalf("tamaño promedio %d, muy lejos de %d", avg, AvgSize)
	}
}

func TestCutsDoNotDependOnReadSizes(t *testing.T) {
	t.Parallel()

	data := randomData(5, 32<<20)
	want := fingerprint(readAll(t, bytes.NewReader(data)))

	tests := []struct {
		name string
		r    io.Reader
	}{
		{"lecturas cortas", iotest.HalfReader(bytes.NewReader(data))},
		{"EOF junto a los datos", iotest.DataErrReader(bytes.NewReader(data))},
		{"bloques pequeños", &stepReader{data: data, step: 4099}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := fingerprint(readAll(t, tt.r)); got != want {
				t.Fatal("los cortes cambiaron según cómo llegaron los datos")
			}
		})
	}
}

// stepReader entrega como máximo step bytes en cada lectura.
type stepReader struct {
	data []byte
	step int
}

func (r *stepReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data[:min(r.step, len(r.data))])
	r.data = r.data[n:]
	return n, nil
}

// TestLocalEditsReuseMostChunks valida la propiedad que justifica usar FastCDC:
// insertar, borrar o cambiar bytes no invalida el resto del archivo.
func TestLocalEditsReuseMostChunks(t *testing.T) {
	t.Parallel()

	const (
		size    = 64 << 20
		editAt  = 20 << 20
		minKept = 0.90
	)
	base := randomData(6, size)
	known := make(map[Hash]struct{})
	for _, c := range readAll(t, bytes.NewReader(base)) {
		known[c.hash] = struct{}{}
	}

	tests := []struct {
		name string
		edit func() []byte
	}{
		{"inserción", func() []byte {
			return append(append(append([]byte{}, base[:editAt]...), randomData(7, 100)...), base[editAt:]...)
		}},
		{"borrado", func() []byte {
			return append(append([]byte{}, base[:editAt]...), base[editAt+100:]...)
		}},
		{"sobrescritura", func() []byte {
			out := append([]byte{}, base...)
			copy(out[editAt:], randomData(7, 100))
			return out
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			edited := tt.edit()
			var kept int
			for _, c := range readAll(t, bytes.NewReader(edited)) {
				if _, ok := known[c.hash]; ok {
					kept += c.size
				}
			}
			if ratio := float64(kept) / float64(len(edited)); ratio < minKept {
				t.Fatalf("reutilizado %.1f %%, quiero al menos %.0f %%", ratio*100, minKept*100)
			}
		})
	}
}

func TestEmptyInputStaysAtEOF(t *testing.T) {
	t.Parallel()

	c := New(bytes.NewReader(nil))
	for range 2 {
		if _, err := c.Next(); !errors.Is(err, io.EOF) {
			t.Fatalf("Next = %v, quiero io.EOF", err)
		}
	}
}

func TestReadErrorIsReturned(t *testing.T) {
	t.Parallel()

	errDisk := errors.New("fallo de disco")
	c := New(io.MultiReader(bytes.NewReader(randomData(8, 1024)), iotest.ErrReader(errDisk)))
	if _, err := c.Next(); !errors.Is(err, errDisk) {
		t.Fatalf("Next = %v, quiero %v", err, errDisk)
	}
}

// TestConcurrentChunkers detecta con -race el fallo del original, que modificaba
// una tabla global al crear cada instancia.
func TestConcurrentChunkers(t *testing.T) {
	t.Parallel()

	data := randomData(9, 8<<20)
	want := fingerprint(readAll(t, bytes.NewReader(data)))

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			chunks, err := collect(bytes.NewReader(data))
			if err != nil {
				t.Errorf("Next: %v", err)
				return
			}
			if fingerprint(chunks) != want {
				t.Error("los cortes cambiaron con instancias concurrentes")
			}
		})
	}
	wg.Wait()
}

// hashData genera datos que JavaScript reproduce sin generador propio: la
// concatenación de SHA-256(i) con i en little endian de 32 bits.
func hashData(size int) []byte {
	data := make([]byte, 0, size+sha256.Size)
	var n [4]byte
	for i := uint32(0); len(data) < size; i++ {
		binary.LittleEndian.PutUint32(n[:], i)
		sum := sha256.Sum256(n[:])
		data = append(data, sum[:]...)
	}
	return data[:size]
}

// TestGoldenCutsWeb fija los cortes de web/server/static/sync/core.js. Tiene la
// misma huella en web/sync_test.mjs: si una cambia, la web y el servidor dejan
// de deduplicar entre sí.
func TestGoldenCutsWeb(t *testing.T) {
	t.Parallel()

	const wantPrint = "914a77e6310914426999f20244aa1536d24dcba473db4056e9be2b321b537ff9"
	if got := fingerprint(readAll(t, bytes.NewReader(hashData(40<<20)))); got != wantPrint {
		t.Fatalf("huella %s", got)
	}
}
