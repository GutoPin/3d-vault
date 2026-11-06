package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"3d-vault/internal/db"
	"3d-vault/internal/store"
)

// StatusFile guarda en DATA_DIR el resultado de las copias, para la página
// Espacio. No entra en la copia.
const StatusFile = "ultima-copia.json"

// BackupStatus es el resultado de las últimas copias.
type BackupStatus struct {
	// Dir, LastOK, Duration y Copied describen la última copia correcta. LastOK es
	// cuándo empezó: la base copiada es la de ese momento.
	Dir      string        `json:"dir,omitempty"`
	LastOK   time.Time     `json:"last_ok"`
	Duration time.Duration `json:"duration"`
	Copied   int           `json:"copied"`

	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at"`
}

// Failing indica que la última copia falló.
func (s BackupStatus) Failing() bool {
	return s.LastErrorAt.After(s.LastOK)
}

// ReadBackupStatus lee el estado guardado. Sin archivo, aún no hubo copias.
func ReadBackupStatus(path string) (BackupStatus, error) {
	var status BackupStatus
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, fmt.Errorf("leer el estado de las copias: %w", err)
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return BackupStatus{}, fmt.Errorf("leer el estado de las copias: %w", err)
	}
	return status, nil
}

// RecordedBackup hace Backup y guarda el resultado en statusPath. La usan el
// comando backup y las copias automáticas del servidor.
func RecordedBackup(ctx context.Context, database *db.DB, chunks *store.Store, dir, statusPath string) (BackupStats, time.Duration, error) {
	start := time.Now()
	stats, err := Backup(ctx, database, chunks, dir)
	took := time.Since(start)

	// Un estado ilegible se reemplaza: solo sirve para mostrarlo.
	status, _ := ReadBackupStatus(statusPath)
	if err != nil {
		status.LastError, status.LastErrorAt = errorText(err), start
	} else {
		status.Dir, status.LastOK, status.Duration, status.Copied = dir, start, took, stats.Copied
	}
	raw, jsonErr := json.Marshal(status)
	// ponytail: sin escritura atómica; un corte a medias solo pierde el estado mostrado.
	if writeErr := errors.Join(jsonErr, os.WriteFile(statusPath, raw, 0o640)); writeErr != nil && err == nil {
		err = fmt.Errorf("la copia terminó, pero no se pudo guardar su estado: %w", writeErr)
	}
	return stats, took, err
}

func errorText(err error) string {
	if errors.Is(err, context.Canceled) {
		return "Se interrumpió al detener el servidor."
	}
	return err.Error()
}

// NextBackup devuelve la próxima hora en punto hour posterior a now, en la zona
// horaria de now.
func NextBackup(now time.Time, hour int) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = time.Date(now.Year(), now.Month(), now.Day()+1, hour, 0, 0, 0, now.Location())
	}
	return next
}
