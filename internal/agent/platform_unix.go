//go:build !windows

package agent

import (
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// diskFree devuelve el espacio libre del disco que contiene path.
func diskFree(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("consultar el disco: %w", err)
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

// markHidden no hace nada: en estos sistemas basta con que el nombre empiece por punto.
func markHidden(string) {}

// OpenURL abre una dirección en el navegador.
func OpenURL(url string) error {
	return startDetached("xdg-open", url)
}

// OpenFolder abre una carpeta en el explorador de archivos.
func OpenFolder(path string) error {
	return startDetached("xdg-open", path)
}

func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("abrir: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// AutostartSupported indica si esta plataforma permite iniciar con el sistema.
func AutostartSupported() bool { return false }

// AutostartEnabled indica si el agente arranca con el sistema.
func AutostartEnabled() (bool, error) { return false, nil }

// SetAutostart no está disponible en esta plataforma.
func SetAutostart(bool) error {
	return fmt.Errorf("iniciar con el sistema no está disponible en esta plataforma")
}

// SystemNotifier muestra avisos con notify-send, si está instalado.
type SystemNotifier struct{}

// Notify muestra un aviso. Si no se puede, no pasa nada.
func (SystemNotifier) Notify(title, message string) {
	path, err := exec.LookPath("notify-send")
	if err != nil {
		return
	}
	_ = startDetached(path, "--app-name=3D-Vault", "--", title, message)
}

// InstanceLock impide ejecutar dos agentes a la vez.
type InstanceLock struct {
	file *os.File
}

// AcquireInstance toma el bloqueo. ok es false si ya hay otro agente en marcha.
func AcquireInstance(path string) (lock *InstanceLock, ok bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("abrir el bloqueo de instancia: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false, nil
	}
	return &InstanceLock{file: f}, true, nil
}

// Close libera el bloqueo.
func (l *InstanceLock) Close() error {
	return l.file.Close()
}
