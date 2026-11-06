package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"3d-vault/internal/maintenance"
	"3d-vault/internal/vault"
)

// BackupConfig son las copias automáticas que hace serve.
type BackupConfig struct {
	// Dir es la carpeta de destino. Vacío: sin copias automáticas.
	Dir  string
	Hour int
	// StatusFile guarda el resultado de las copias, también las del comando backup.
	StatusFile string
}

type storageData struct {
	Usage vault.StorageUsage
	// FreePercent es el espacio libre como porcentaje del disco.
	FreePercent float64
	Backup      backupView
}

type backupView struct {
	Enabled bool
	Dir     string
	Hour    int
	Next    time.Time
	Status  maintenance.BackupStatus
	Took    string
	// Unreadable indica que no se pudo leer el estado guardado.
	Unreadable bool
}

func (s *Server) webStorage(w http.ResponseWriter, r *http.Request) {
	usage, err := s.svc.StorageUsage(r.Context())
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	data := storageData{Usage: usage, Backup: backupView{Enabled: s.backups.Dir != "", Dir: s.backups.Dir, Hour: s.backups.Hour}}
	if usage.DiskBytes > 0 {
		data.FreePercent = float64(usage.FreeBytes) / float64(usage.DiskBytes) * 100
	}
	if data.Backup.Enabled {
		data.Backup.Next = maintenance.NextBackup(s.now(), s.backups.Hour)
	}
	if s.backups.StatusFile != "" {
		status, err := maintenance.ReadBackupStatus(s.backups.StatusFile)
		if err != nil {
			s.log.ErrorContext(r.Context(), "error al leer el estado de las copias", slog.Any("error", err))
			data.Backup.Unreadable = true
		}
		data.Backup.Status = status
		data.Backup.Took = formatDuration(status.Duration)
	}
	s.render(w, r, http.StatusOK, "storage", pageData{Title: "Espacio", Active: "storage", Data: data})
}

// formatDuration muestra una duración como "2 min 5 s".
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, sec := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%d h %d min", h, m)
	case m > 0:
		return fmt.Sprintf("%d min %d s", m, sec)
	}
	return fmt.Sprintf("%d s", sec)
}

// Umbrales del aviso de disco: lo que haga falta antes, el porcentaje o los bytes.
const (
	lowDiskPercent = 15
	lowDiskBytes   = 20 << 30
	diskCheckEvery = time.Minute
)

func defaultLowDisk(free, total int64) bool {
	return free < lowDiskBytes || free*100 < total*lowDiskPercent
}

// diskAlert es el aviso de poco disco que ven los admins en todas las páginas.
type diskAlert struct {
	Free    int64
	Percent float64
}

// diskCache evita consultar el disco en cada página.
type diskCache struct {
	mu      sync.Mutex
	checked time.Time
	alert   *diskAlert
}

// diskAlert devuelve el aviso si queda poco disco, consultándolo como mucho una
// vez por minuto. nil si hay espacio o no se puede saber.
func (s *Server) diskAlert() *diskAlert {
	s.disk.mu.Lock()
	defer s.disk.mu.Unlock()
	now := s.now()
	if !s.disk.checked.IsZero() && now.Sub(s.disk.checked) < diskCheckEvery {
		return s.disk.alert
	}
	s.disk.checked, s.disk.alert = now, nil
	free, total, err := s.svc.DiskSpace()
	if err == nil && total > 0 && s.lowDisk(free, total) {
		s.disk.alert = &diskAlert{Free: free, Percent: float64(free) / float64(total) * 100}
	}
	return s.disk.alert
}
