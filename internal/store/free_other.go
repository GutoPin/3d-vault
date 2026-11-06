//go:build !unix

package store

import "errors"

// DiskSpace no está disponible fuera de Unix; el servidor se despliega en Linux.
func (s *Store) DiskSpace() (free, total int64, err error) {
	return 0, 0, errors.ErrUnsupported
}
