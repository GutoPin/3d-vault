//go:build unix

package store

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// FreeBytes devuelve el espacio libre del disco del almacén.
func (s *Store) FreeBytes() (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(s.root, &st); err != nil {
		return 0, fmt.Errorf("consultar el disco: %w", err)
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:unconvert // Los tipos cambian según el sistema.
}
