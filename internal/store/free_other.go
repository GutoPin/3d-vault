//go:build !unix

package store

import "errors"

// FreeBytes no está disponible fuera de Unix; el servidor se despliega en Linux.
func (s *Store) FreeBytes() (int64, error) {
	return 0, errors.ErrUnsupported
}
