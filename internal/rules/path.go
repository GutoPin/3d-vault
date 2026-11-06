package rules

import (
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	maxSegmentBytes = 255
	maxPathBytes    = 1024

	// reservedDir es la carpeta de temporales del agente.
	reservedDir = ".3dvault-tmp"

	forbiddenChars = `<>:"\|?*`
)

// Path es una ruta validada dentro de un proyecto.
type Path struct {
	// Clean es la ruta para mostrar, normalizada a NFC.
	Clean string
	// Key identifica la ruta sin distinguir mayúsculas. Dos rutas con la misma
	// clave chocarían en el disco de un modelador con Windows o macOS.
	Key string
	// Ext es la extensión en minúsculas, con el punto.
	Ext string
}

// NormalizePath valida una ruta relativa con separador "/". Rechaza todo lo que
// no se pueda representar como archivo en Windows y en macOS.
func NormalizePath(raw string) (Path, error) {
	if !utf8.ValidString(raw) {
		return Path{}, invalidPath("La ruta contiene caracteres no válidos.")
	}
	clean := norm.NFC.String(raw)
	if clean == "" {
		return Path{}, invalidPath("La ruta está vacía.")
	}
	if len(clean) > maxPathBytes {
		return Path{}, invalidPath(fmt.Sprintf("La ruta supera los %d bytes.", maxPathBytes))
	}
	for segment := range strings.SplitSeq(clean, "/") {
		if err := checkSegment(segment); err != nil {
			return Path{}, err
		}
	}
	return Path{
		Clean: clean,
		Key:   FoldKey(clean),
		Ext:   strings.ToLower(path.Ext(clean)),
	}, nil
}

// FoldKey quita las mayúsculas y unifica la forma de los acentos, como Key. Sirve
// para comparar un texto con las claves guardadas.
func FoldKey(s string) string {
	return norm.NFC.String(cases.Fold().String(norm.NFC.String(s)))
}

func checkSegment(segment string) error {
	switch {
	case segment == "":
		return invalidPath("La ruta no puede ser absoluta ni tener carpetas vacías.")
	case segment == "." || segment == "..":
		return invalidPath("La ruta no puede contener «.» ni «..».")
	case len(segment) > maxSegmentBytes:
		return invalidPath(fmt.Sprintf("Cada carpeta o archivo debe tener como máximo %d bytes.", maxSegmentBytes))
	case strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " "):
		return invalidPath(fmt.Sprintf("«%s» no puede terminar en punto ni en espacio.", segment))
	case strings.EqualFold(segment, reservedDir):
		return invalidPath(fmt.Sprintf("«%s» es un nombre reservado.", segment))
	case isReservedName(segment):
		return invalidPath(fmt.Sprintf("«%s» es un nombre reservado de Windows.", segment))
	}
	for _, r := range segment {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune(forbiddenChars, r) {
			return invalidPath(fmt.Sprintf("«%s» contiene caracteres no permitidos.", segment))
		}
	}
	return nil
}

// isReservedName detecta nombres que Windows reserva, con o sin extensión.
func isReservedName(segment string) bool {
	stem, _, _ := strings.Cut(segment, ".")
	stem = strings.ToUpper(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) {
		return stem[3] >= '0' && stem[3] <= '9'
	}
	return false
}

func invalidPath(message string) error {
	return &Error{Code: CodeInvalidPath, Message: message}
}
