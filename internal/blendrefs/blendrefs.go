// Package blendrefs lee de un .blend qué archivos externos usa: otros .blend
// enlazados (File → Link) y texturas, sonidos, videos, fuentes, volúmenes y
// cachés que no van empaquetados dentro.
//
// Se lee sin Blender, de principio a fin y sin cargarlo en memoria: se guardan
// solo los bloques de los tipos de dato que interesan y, al llegar al bloque DNA1
// del final, que describe la forma de cada estructura, se leen sus campos.
// El diseño de la cabecera y de los BHead sigue a Blender 5.1, igual que
// internal/thumbs; el de DNA1, a source/blender/makesdna/intern/dna_genfile.cc.
package blendrefs

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// ErrInvalid indica que no es un .blend que se sepa leer, o que está truncado.
var ErrInvalid = errors.New("no es un archivo .blend válido")

// Tipos de referencia.
const (
	KindLibrary = "library" // otro .blend enlazado
	KindImage   = "image"
	KindSound   = "sound"
	KindMovie   = "movie" // clip de seguimiento de cámara
	KindFont    = "font"
	KindVolume  = "volume"
	KindCache   = "cache" // Alembic o USD
)

// Ref es un archivo externo que el .blend necesita para verse completo.
type Ref struct {
	Kind string
	// Path es la ruta tal como la guarda Blender: "//" al principio si es
	// relativa a la carpeta del .blend, y con las barras del sistema donde se
	// guardó.
	Path string
	// Pattern indica que Path representa varios archivos: una secuencia de
	// imágenes o las teselas UDIM de una textura.
	Pattern bool
}

const (
	maxZstdWindow = 64 << 20
	// maxKept acota la memoria de los bloques guardados hasta leer DNA1. Una
	// Image ocupa unos 2 KiB, así que caben decenas de miles.
	maxKept = 64 << 20
	// maxDNA acota el bloque DNA1, que en Blender 5.1 ronda los 400 KiB.
	maxDNA = 16 << 20
)

var (
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
	gzipMagic = []byte{0x1f, 0x8b}
)

// idCodes son los bloques de datos que pueden apuntar a un archivo externo.
var idCodes = map[string]string{
	"LI": KindLibrary,
	"IM": KindImage,
	"SO": KindSound,
	"MC": KindMovie,
	"VF": KindFont,
	"VO": KindVolume,
	"CF": KindCache,
}

// Valores de Image.source (DNA_image_types.h).
const (
	imaSrcFile     = 1
	imaSrcSequence = 2
	imaSrcMovie    = 3
	imaSrcTiled    = 6
)

type bheadFormat int

const (
	bhead4      bheadFormat = iota // code, len, old (4), SDNAnr, nr: 20 bytes
	smallBHead8                    // code, len, old (8), SDNAnr, nr: 24 bytes
	largeBHead8                    // code, SDNAnr, old (8), len (8), nr (8): 32 bytes
)

func (f bheadFormat) size() int    { return [...]int{20, 24, 32}[f] }
func (f bheadFormat) pointer() int { return [...]int{4, 8, 8}[f] }

type kept struct {
	code string
	sdna int
	data []byte
}

// Read devuelve las referencias externas de un .blend, comprimido o no, sin
// repetir. El orden es el del archivo.
func Read(r io.Reader) ([]Ref, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	magic, _ := br.Peek(4)
	var data io.Reader = br
	switch {
	case bytes.HasPrefix(magic, zstdMagic):
		dec, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(maxZstdWindow))
		if err != nil {
			return nil, fmt.Errorf("preparar la descompresión: %w", err)
		}
		defer dec.Close()
		data = dec
	case bytes.HasPrefix(magic, gzipMagic):
		dec, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("%w: gzip: %v", ErrInvalid, err)
		}
		data = dec
	}
	return scan(bufio.NewReaderSize(data, 1<<20))
}

func truncated(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: archivo truncado", ErrInvalid)
	}
	return err
}

func readHeader(r io.Reader) (bheadFormat, binary.ByteOrder, error) {
	head := make([]byte, 12)
	if _, err := io.ReadFull(r, head); err != nil {
		return 0, nil, truncated(err)
	}
	if string(head[:7]) != "BLENDER" {
		return 0, nil, ErrInvalid
	}
	switch head[7] {
	case '_', '-':
		format := smallBHead8
		if head[7] == '_' {
			format = bhead4
		}
		switch head[8] {
		case 'v':
			return format, binary.LittleEndian, nil
		case 'V':
			return format, binary.BigEndian, nil
		}
		return 0, nil, fmt.Errorf("%w: orden de bytes desconocido", ErrInvalid)
	}
	// Formato de Blender 5.0: "BLENDER" + "17" + "-" + "01" + "v" + versión.
	if string(head[7:12]) != "17-01" {
		return 0, nil, fmt.Errorf("%w: formato de cabecera desconocido", ErrInvalid)
	}
	rest := make([]byte, 5)
	if _, err := io.ReadFull(r, rest); err != nil {
		return 0, nil, truncated(err)
	}
	if rest[0] != 'v' {
		return 0, nil, fmt.Errorf("%w: orden de bytes desconocido", ErrInvalid)
	}
	return largeBHead8, binary.LittleEndian, nil
}

func scan(r io.Reader) ([]Ref, error) {
	format, order, err := readHeader(r)
	if err != nil {
		return nil, err
	}
	head := make([]byte, format.size())
	var blocks []kept
	keptBytes := 0
	for {
		if _, err := io.ReadFull(r, head); err != nil {
			return nil, truncated(err)
		}
		code := string(bytes.TrimRight(head[:4], "\x00"))
		var length int64
		var sdna int
		switch format {
		case largeBHead8:
			sdna = int(int32(order.Uint32(head[4:8])))
			length = int64(order.Uint64(head[16:24]))
		case smallBHead8:
			length = int64(int32(order.Uint32(head[4:8])))
			sdna = int(int32(order.Uint32(head[16:20])))
		default:
			length = int64(int32(order.Uint32(head[4:8])))
			sdna = int(int32(order.Uint32(head[12:16])))
		}
		if length < 0 {
			return nil, fmt.Errorf("%w: bloque de tamaño negativo", ErrInvalid)
		}

		switch {
		case code == "ENDB":
			return nil, fmt.Errorf("%w: falta el bloque DNA1", ErrInvalid)
		case code == "DNA1":
			if length > maxDNA {
				return nil, fmt.Errorf("%w: bloque DNA1 de %d bytes", ErrInvalid, length)
			}
			raw := make([]byte, length)
			if _, err := io.ReadFull(r, raw); err != nil {
				return nil, truncated(err)
			}
			dna, err := parseDNA(raw, order, format.pointer())
			if err != nil {
				return nil, err
			}
			return extract(blocks, dna, order, format.pointer())
		case idCodes[code] != "":
			if keptBytes+int(length) > maxKept {
				return nil, fmt.Errorf("%w: demasiados datos externos", ErrInvalid)
			}
			data := make([]byte, length)
			if _, err := io.ReadFull(r, data); err != nil {
				return nil, truncated(err)
			}
			keptBytes += len(data)
			blocks = append(blocks, kept{code: code, sdna: sdna, data: data})
		default:
			if _, err := io.CopyN(io.Discard, r, length); err != nil {
				return nil, truncated(err)
			}
		}
	}
}

// field es un miembro de una estructura de DNA, con su posición.
type field struct {
	typ    string
	name   string // sin punteros ni dimensiones: "filepath", "lib"
	ptr    bool
	offset int
	size   int
}

type dnaStruct struct {
	typ    string
	fields []field
}

type dna struct {
	structs []dnaStruct
	byType  map[string]int
}

func (d *dna) find(s int, name string) (field, bool) {
	for _, f := range d.structs[s].fields {
		if f.name == name {
			return f, true
		}
	}
	return field{}, false
}

// parseDNA lee SDNA: los nombres de los campos, los tipos con su tamaño y las
// estructuras como listas de (tipo, nombre).
func parseDNA(b []byte, order binary.ByteOrder, ptrSize int) (*dna, error) {
	bad := func(what string) error { return fmt.Errorf("%w: DNA1 %s", ErrInvalid, what) }
	pos := 0
	align := func() { pos = (pos + 3) &^ 3 }
	tag := func(t string) bool {
		if pos+4 > len(b) || string(b[pos:pos+4]) != t {
			return false
		}
		pos += 4
		return true
	}
	count := func() (int, bool) {
		if pos+4 > len(b) {
			return 0, false
		}
		n := int(int32(order.Uint32(b[pos:])))
		pos += 4
		return n, n >= 0 && n <= len(b)
	}
	strs := func(n int) ([]string, bool) {
		out := make([]string, n)
		for i := range out {
			end := bytes.IndexByte(b[pos:], 0)
			if end < 0 {
				return nil, false
			}
			out[i] = string(b[pos : pos+end])
			pos += end + 1
		}
		return out, true
	}

	if !tag("SDNA") || !tag("NAME") {
		return nil, bad("sin cabecera")
	}
	n, ok := count()
	if !ok {
		return nil, bad("NAME")
	}
	names, ok := strs(n)
	if !ok {
		return nil, bad("NAME")
	}
	align()
	if !tag("TYPE") {
		return nil, bad("sin TYPE")
	}
	if n, ok = count(); !ok {
		return nil, bad("TYPE")
	}
	types, ok := strs(n)
	if !ok {
		return nil, bad("TYPE")
	}
	align()
	if !tag("TLEN") {
		return nil, bad("sin TLEN")
	}
	// Los tamaños son de 16 bits. Por si una versión futura los ensancha, el
	// ancho se deduce de dónde empieza STRC.
	start := pos
	width := 2
	if strc := bytes.Index(b[start:], []byte("STRC")); strc >= 0 && strc >= 4*len(types) && (strc-4*len(types)) < 4 {
		width = 4
	}
	sizes := make([]int, len(types))
	for i := range sizes {
		if pos+width > len(b) {
			return nil, bad("TLEN")
		}
		if width == 4 {
			sizes[i] = int(int32(order.Uint32(b[pos:])))
		} else {
			sizes[i] = int(order.Uint16(b[pos:]))
		}
		pos += width
	}
	align()
	if !tag("STRC") {
		return nil, bad("sin STRC")
	}
	if n, ok = count(); !ok {
		return nil, bad("STRC")
	}
	short := func() (int, bool) {
		if pos+2 > len(b) {
			return 0, false
		}
		v := int(order.Uint16(b[pos:]))
		pos += 2
		return v, true
	}
	d := &dna{structs: make([]dnaStruct, n), byType: make(map[string]int, n)}
	for i := range d.structs {
		t, ok1 := short()
		nf, ok2 := short()
		if !ok1 || !ok2 || t >= len(types) {
			return nil, bad("STRC")
		}
		s := dnaStruct{typ: types[t], fields: make([]field, nf)}
		offset := 0
		for j := range s.fields {
			ft, ok1 := short()
			fn, ok2 := short()
			if !ok1 || !ok2 || ft >= len(types) || fn >= len(names) {
				return nil, bad("STRC")
			}
			f := memberOf(types[ft], names[fn], sizes[ft], ptrSize)
			f.offset = offset
			offset += f.size
			s.fields[j] = f
		}
		d.structs[i] = s
		d.byType[s.typ] = i
	}
	return d, nil
}

var dims = regexp.MustCompile(`\[(\d+)\]`)

// memberOf interpreta un nombre de DNA como "*next", "filepath[1024]" o
// "(*func)()".
func memberOf(typ, raw string, typeSize, ptrSize int) field {
	f := field{typ: typ}
	f.ptr = strings.HasPrefix(raw, "*") || strings.HasPrefix(raw, "(*")
	base := strings.TrimLeft(raw, "*(")
	if i := strings.IndexAny(base, "[)"); i >= 0 {
		base = base[:i]
	}
	f.name = base
	size := typeSize
	if f.ptr {
		size = ptrSize
	}
	for _, m := range dims.FindAllStringSubmatch(raw, -1) {
		var k int
		fmt.Sscanf(m[1], "%d", &k)
		size *= k
	}
	f.size = size
	return f
}

// extract lee de cada bloque guardado su ruta, si es un dato local, no
// empaquetado y que de verdad usa un archivo.
func extract(blocks []kept, d *dna, order binary.ByteOrder, ptrSize int) ([]Ref, error) {
	pointer := func(b []byte, off int) uint64 {
		if off+ptrSize > len(b) {
			return 0
		}
		if ptrSize == 4 {
			return uint64(order.Uint32(b[off:]))
		}
		return order.Uint64(b[off:])
	}
	var refs []Ref
	seen := map[Ref]bool{}
	for _, blk := range blocks {
		if blk.sdna < 0 || blk.sdna >= len(d.structs) {
			continue
		}
		s := blk.sdna
		path, ok := d.find(s, "filepath")
		if !ok {
			path, ok = d.find(s, "name") // antes de Blender 2.8x: char name[1024]
			if !ok || path.size < 256 {
				continue
			}
		}
		if path.offset+path.size > len(blk.data) {
			continue
		}
		// Un dato que viene de otro .blend: su archivo es asunto de ese .blend.
		if id, ok := d.find(s, "id"); ok && id.typ == "ID" {
			if idx, ok := d.byType["ID"]; ok {
				if lib, ok := d.find(idx, "lib"); ok && lib.ptr && pointer(blk.data, id.offset+lib.offset) != 0 {
					continue
				}
			}
		}
		if f, ok := d.find(s, "packedfile"); ok && f.ptr && pointer(blk.data, f.offset) != 0 {
			continue
		}
		if f, ok := d.find(s, "packedfiles"); ok && f.typ == "ListBase" && pointer(blk.data, f.offset) != 0 {
			continue
		}

		kind := idCodes[blk.code]
		ref := Ref{Kind: kind}
		if kind == KindImage {
			src, ok := d.find(s, "source")
			if !ok || src.offset+2 > len(blk.data) {
				continue
			}
			switch int16(order.Uint16(blk.data[src.offset:])) {
			case imaSrcFile, imaSrcMovie:
			case imaSrcSequence, imaSrcTiled:
				ref.Pattern = true
			default:
				continue // generada, visor del render…
			}
		}
		raw := blk.data[path.offset : path.offset+path.size]
		if end := bytes.IndexByte(raw, 0); end >= 0 {
			raw = raw[:end]
		}
		ref.Path = string(raw)
		if ref.Path == "" || (kind == KindFont && ref.Path == "<builtin>") {
			continue
		}
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	return refs, nil
}
