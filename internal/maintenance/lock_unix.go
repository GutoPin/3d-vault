//go:build unix

package maintenance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrDataInUse indica que otro proceso, normalmente el servidor, usa la carpeta.
var ErrDataInUse = errors.New("la carpeta de datos está en uso: detén el servidor antes de continuar")

// LockDataDir toma un bloqueo exclusivo sobre dataDir mientras el archivo devuelto
// siga abierto. El sistema lo libera aunque el proceso termine de forma abrupta.
func LockDataDir(dataDir string) (*os.File, error) {
	if err := os.MkdirAll(dataDir, dirPerm); err != nil {
		return nil, fmt.Errorf("crear la carpeta de datos: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dataDir, ".lock"), os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, fmt.Errorf("abrir el bloqueo: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrDataInUse
		}
		return nil, fmt.Errorf("tomar el bloqueo: %w", err)
	}
	return f, nil
}
