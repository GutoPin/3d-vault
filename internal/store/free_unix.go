//go:build unix

package store

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// DiskSpace devuelve el espacio libre y el tamaño del disco del almacén.
func (s *Store) DiskSpace() (free, total int64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(s.root, &st); err != nil {
		return 0, 0, fmt.Errorf("consultar el disco: %w", err)
	}
	//nolint:unconvert // Los tipos cambian según el sistema.
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize), nil
}
