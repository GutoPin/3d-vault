// Comando cdcmeasure informa de cuánto se reutilizaría entre dos guardados de un
// archivo, es decir, qué parte del segundo ya no haría falta transferir.
//
// Lee cada archivo de forma secuencial, así que también acepta tuberías:
//
//	cdcmeasure base.blend nuevo.blend
//	cdcmeasure <(zstd -dc base.blend) <(zstd -dc nuevo.blend)
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"3d-vault/internal/chunker"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) != 2 {
		return errors.New("uso: cdcmeasure <archivo-base> <archivo-nuevo>")
	}

	seen := make(map[chunker.Hash]int)

	base, err := scanFile(args[0], seen, 0)
	if err != nil {
		return err
	}
	next, err := scanFile(args[1], seen, 1)
	if err != nil {
		return err
	}

	printFile(out, "Base", args[0], base)
	printFile(out, "Nuevo", args[1], next)
	fmt.Fprintf(out, "En la base   %s (%.1f %%), %d trozos\n",
		formatBytes(next.earlierBytes), percent(next.earlierBytes, next.size), next.earlierChunks)
	fmt.Fprintf(out, "Repetido     %s (%.1f %%) dentro del propio archivo nuevo\n",
		formatBytes(next.repeatBytes), percent(next.repeatBytes, next.size))
	fmt.Fprintf(out, "A transferir %s (%.1f %%), %d trozos\n",
		formatBytes(next.newBytes), percent(next.newBytes, next.size), next.newChunks)
	return nil
}

// stats resume un archivo y clasifica cada trozo: nuevo, ya presente en un
// archivo anterior o repetido dentro de este mismo archivo.
type stats struct {
	format        string
	size          int64
	chunks        int
	newBytes      int64
	newChunks     int
	earlierBytes  int64
	earlierChunks int
	repeatBytes   int64
	elapsed       time.Duration
}

func percent(part, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

func scanFile(path string, seen map[chunker.Hash]int, file int) (stats, error) {
	f, err := os.Open(path)
	if err != nil {
		return stats{}, fmt.Errorf("abrir el archivo: %w", err)
	}
	defer f.Close()

	s, err := scan(f, seen, file)
	if err != nil {
		return stats{}, fmt.Errorf("procesar %s: %w", path, err)
	}
	return s, nil
}

// scan clasifica los trozos de r. seen guarda en qué archivo apareció cada hash
// por primera vez, y file identifica al archivo actual.
func scan(r io.Reader, seen map[chunker.Hash]int, file int) (stats, error) {
	start := time.Now()
	s := stats{format: "vacío"}
	c := chunker.New(r)
	for {
		chunk, err := c.Next()
		if errors.Is(err, io.EOF) {
			s.elapsed = time.Since(start)
			return s, nil
		}
		if err != nil {
			return stats{}, err
		}
		if s.chunks == 0 {
			s.format = detectFormat(chunk.Data)
		}
		size := int64(len(chunk.Data))
		s.size += size
		s.chunks++

		first, known := seen[chunk.Hash]
		switch {
		case !known:
			seen[chunk.Hash] = file
			s.newBytes += size
			s.newChunks++
		case first == file:
			s.repeatBytes += size
		default:
			s.earlierBytes += size
			s.earlierChunks++
		}
	}
}

// detectFormat identifica la compresión por los primeros bytes del archivo.
func detectFormat(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte("BLENDER")):
		return "blend sin comprimir"
	case bytes.HasPrefix(head, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return "comprimido con zstd"
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return "comprimido con gzip"
	default:
		return "otro"
	}
}

func printFile(out io.Writer, label, path string, s stats) {
	var speed float64
	if secs := s.elapsed.Seconds(); secs > 0 {
		speed = float64(s.size) / (1 << 20) / secs
	}
	fmt.Fprintf(out, "%-12s %s\n", label, path)
	fmt.Fprintf(out, "             %s, %s, %d trozos, %.0f MiB/s\n", formatBytes(s.size), s.format, s.chunks, speed)
}

func formatBytes(n int64) string {
	const unit = 1 << 10
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
