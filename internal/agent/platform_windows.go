//go:build windows

package agent

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	runKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue = "3D-Vault"
	// createNoWindow evita que parpadee una consola al lanzar procesos auxiliares.
	createNoWindow = 0x08000000
)

// diskFree devuelve el espacio libre del disco que contiene path.
func diskFree(path string) (uint64, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("consultar el disco: %w", err)
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &free, nil, nil); err != nil {
		return 0, fmt.Errorf("consultar el disco: %w", err)
	}
	return free, nil
}

// markHidden oculta la carpeta de temporales en el Explorador.
func markHidden(path string) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	attrs, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return
	}
	_ = windows.SetFileAttributes(ptr, attrs|windows.FILE_ATTRIBUTE_HIDDEN)
}

// OpenURL abre una dirección en el navegador predeterminado.
func OpenURL(url string) error {
	return startHidden("rundll32", "url.dll,FileProtocolHandler", url)
}

// OpenFolder abre una carpeta en el Explorador.
func OpenFolder(path string) error {
	return startHidden("explorer", path)
}

func startHidden(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("abrir: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// AutostartSupported indica si esta plataforma permite iniciar con el sistema.
func AutostartSupported() bool { return true }

// AutostartEnabled indica si el agente arranca con Windows.
func AutostartEnabled() (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false, fmt.Errorf("leer el registro: %w", err)
	}
	defer key.Close()
	_, _, err = key.GetStringValue(runValue)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("leer el registro: %w", err)
	}
	return true, nil
}

// SetAutostart activa o desactiva el inicio con Windows para este usuario.
func SetAutostart(enabled bool) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("abrir el registro: %w", err)
	}
	defer key.Close()
	if !enabled {
		if err := key.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("modificar el registro: %w", err)
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("buscar el programa: %w", err)
	}
	if err := key.SetStringValue(runValue, `"`+exe+`"`); err != nil {
		return fmt.Errorf("modificar el registro: %w", err)
	}
	return nil
}

// toastScript es fijo. El texto llega por variables de entorno, ya escapado como
// XML, y nunca como parte del script: un nombre de archivo no puede inyectar órdenes.
const toastScript = `
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom, ContentType = WindowsRuntime] > $null
$doc = New-Object Windows.Data.Xml.Dom.XmlDocument
$doc.LoadXml("<toast><visual><binding template='ToastGeneric'><text>$env:VAULT3D_TITLE</text><text>$env:VAULT3D_MESSAGE</text></binding></visual></toast>")
$id = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($id).Show([Windows.UI.Notifications.ToastNotification]::new($doc))
`

// SystemNotifier muestra avisos de Windows. No hay una librería sin cgo que encaje,
// así que usa PowerShell, disponible en todo Windows 10 y 11.
type SystemNotifier struct{}

// Notify muestra un aviso. Si no se puede, no pasa nada: la misma información está
// en el panel.
func (SystemNotifier) Notify(title, message string) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", toastScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	cmd.Env = append(os.Environ(), "VAULT3D_TITLE="+escapeXML(title), "VAULT3D_MESSAGE="+escapeXML(message))
	if err := cmd.Start(); err != nil {
		return
	}
	go func() { _ = cmd.Wait() }()
}

func escapeXML(text string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(text)); err != nil {
		return ""
	}
	return buf.String()
}

// InstanceLock impide ejecutar dos agentes a la vez.
type InstanceLock struct {
	handle windows.Handle
}

// AcquireInstance toma un mutex con nombre. ok es false si ya hay otro agente en
// marcha en esta sesión de usuario. path no se usa en Windows.
func AcquireInstance(string) (lock *InstanceLock, ok bool, err error) {
	name, err := windows.UTF16PtrFromString(`Local\3D-Vault-Agente`)
	if err != nil {
		return nil, false, fmt.Errorf("crear el mutex: %w", err)
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		_ = windows.CloseHandle(handle)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("crear el mutex: %w", err)
	}
	return &InstanceLock{handle: handle}, true, nil
}

// Close libera el mutex.
func (l *InstanceLock) Close() error {
	return windows.CloseHandle(l.handle)
}
