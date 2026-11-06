package maintenance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"3d-vault/internal/auth"
	"3d-vault/internal/chunker"
	"3d-vault/internal/db"
	"3d-vault/internal/events"
	"3d-vault/internal/manifest"
	"3d-vault/internal/rules"
	"3d-vault/internal/store"
	"3d-vault/internal/vault"
)

type fixture struct {
	dir      string
	database *db.DB
	chunks   *store.Store
	svc      *vault.Service
	actor    rules.Actor
	project  vault.Project
	data     []byte
	result   vault.CommitResult
}

func openData(t *testing.T, dir string) (*db.DB, *store.Store, *vault.Service) {
	t.Helper()
	database, err := db.Open(context.Background(), filepath.Join(dir, DatabaseFile))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	chunks, err := store.New(filepath.Join(dir, ChunksDir))
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = chunks.Close() })
	svc := vault.New(vault.Config{
		DB: database, Store: chunks, Hub: events.NewHub(),
		MaxFileSize: 1 << 30, Argon: auth.Params{Memory: 1024, Time: 1, Threads: 1},
	})
	return database, chunks, svc
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	database, chunks, svc := openData(t, dir)

	user, err := svc.CreateUser(ctx, 0, vault.NewUserInput{Username: "juan", DisplayName: "Juan", Password: "contraseña-larga"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	actor := rules.Actor{UserID: user.ID, ClientID: "pc-1", ClientName: "PC"}
	project, err := svc.CreateProject(ctx, actor, "Mina", "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	data := make([]byte, 3<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	m := upload(t, svc, data)
	result, err := svc.CreateAsset(ctx, actor, vault.CreateAssetInput{ProjectID: project.ID, Path: "a.blend", Manifest: m, Comment: "Primera"})
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	return &fixture{dir: dir, database: database, chunks: chunks, svc: svc, actor: actor, project: project, data: data, result: result}
}

func upload(t *testing.T, svc *vault.Service, data []byte) manifest.Manifest {
	t.Helper()
	m, err := manifest.Build(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	c := chunker.New(bytes.NewReader(data))
	for {
		chunk, err := c.Next()
		if err != nil {
			break
		}
		if err := svc.PutChunk(context.Background(), chunk.Hash, chunk.Data); err != nil {
			t.Fatalf("PutChunk: %v", err)
		}
	}
	return m
}

func epoch(t *testing.T, database *db.DB) string {
	t.Helper()
	var value string
	if err := database.ReadTx(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT value FROM meta WHERE key = ?`, db.MetaServerEpoch).Scan(&value)
	}); err != nil {
		t.Fatal(err)
	}
	return value
}

// readVersion reconstruye una versión a partir de sus trozos.
func readVersion(t *testing.T, svc *vault.Service, versionID int64) []byte {
	t.Helper()
	ctx := context.Background()
	file, err := svc.GetVersionFile(ctx, versionID)
	if err != nil {
		t.Fatalf("GetVersionFile: %v", err)
	}
	var out []byte
	for _, c := range file.Manifest.Chunks {
		data, err := svc.GetChunk(ctx, c.SHA256)
		if err != nil {
			t.Fatalf("GetChunk: %v", err)
		}
		out = append(out, data...)
	}
	return out
}

// TestBackupAndRestoreDrill es el simulacro de recuperación del plan: restaurar en
// un entorno limpio, validar con Check y seguir trabajando.
func TestBackupAndRestoreDrill(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := newFixture(t)
	backupDir := t.TempDir()

	stats, err := Backup(ctx, f.database, f.chunks, backupDir)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if stats.Copied == 0 {
		t.Fatal("no copió ningún trozo")
	}

	// Un cambio posterior a la copia no debe aparecer al restaurar.
	if _, err := f.svc.Lock(ctx, f.actor, f.result.Asset.ID, false); err != nil {
		t.Fatal(err)
	}

	restored := t.TempDir()
	if _, err := Restore(ctx, backupDir, restored, false, time.Now()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	database, chunks, svc := openData(t, restored)

	report, err := Check(ctx, database, chunks)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.OK() || report.Checked == 0 {
		t.Fatalf("Check tras restaurar = %+v", report)
	}
	if got := readVersion(t, svc, f.result.Version.ID); !bytes.Equal(got, f.data) {
		t.Fatal("el contenido restaurado no coincide")
	}
	asset, err := svc.GetAsset(ctx, f.result.Asset.ID)
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if asset.Lock != nil {
		t.Fatal("el bloqueo es posterior a la copia y no debería estar")
	}
	if epoch(t, database) == epoch(t, f.database) {
		t.Fatal("restaurar debe cambiar el epoch: los agentes tienen que volver a sincronizar")
	}

	// El sistema restaurado sigue funcionando.
	more := upload(t, svc, []byte("una versión nueva tras restaurar"))
	if _, err := svc.CommitVersion(ctx, f.actor, vault.CommitInput{AssetID: asset.ID, BaseVersion: 1, Manifest: more, SizeBytes: more.Size(), Comment: "Tras restaurar"}); err != nil {
		t.Fatalf("CommitVersion tras restaurar: %v", err)
	}
}

func TestBackupIsIncrementalAndKeepsDestination(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := newFixture(t)
	backupDir := t.TempDir()

	first, err := Backup(ctx, f.database, f.chunks, backupDir)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	second, err := Backup(ctx, f.database, f.chunks, backupDir)
	if err != nil {
		t.Fatalf("segundo Backup: %v", err)
	}
	if second.Copied != 0 || second.Skipped != first.Copied {
		t.Fatalf("la segunda copia debería omitir todo: %+v", second)
	}
}

func TestRestoreRefusesToOverwrite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := newFixture(t)
	backupDir := t.TempDir()
	if _, err := Backup(ctx, f.database, f.chunks, backupDir); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	marker := []byte("base anterior")
	dst := filepath.Join(target, DatabaseFile)
	if err := os.WriteFile(dst, marker, 0o640); err != nil {
		t.Fatal(err)
	}

	if _, err := Restore(ctx, backupDir, target, false, time.Now()); !errors.Is(err, ErrDataExists) {
		t.Fatalf("Restore = %v, quiero ErrDataExists", err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, marker) {
		t.Fatal("no debe tocar la base existente")
	}

	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if _, err := Restore(ctx, backupDir, target, true, now); err != nil {
		t.Fatalf("Restore con force: %v", err)
	}
	aside := dst + ".antes-de-restaurar-20261104-120000"
	if got, err := os.ReadFile(aside); err != nil || !bytes.Equal(got, marker) {
		t.Fatalf("la base anterior debe quedar apartada: %v", err)
	}
}

func TestRestoreWithoutBackupFails(t *testing.T) {
	t.Parallel()

	if _, err := Restore(context.Background(), t.TempDir(), t.TempDir(), false, time.Now()); err == nil {
		t.Fatal("quería un error")
	}
}

func TestCheckFindsDamage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := newFixture(t)

	report, err := Check(ctx, f.database, f.chunks)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.OK() || report.Orphans != 0 {
		t.Fatalf("Check inicial = %+v", report)
	}

	file, err := f.svc.GetVersionFile(ctx, f.result.Version.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Manifest.Chunks) < 2 {
		t.Skip("hacen falta dos trozos")
	}
	chunkPath := func(h chunker.Hash) string {
		name := h.String()
		return filepath.Join(f.dir, ChunksDir, name[:2], name[2:4], name)
	}
	corrupt, missing := file.Manifest.Chunks[0].SHA256, file.Manifest.Chunks[1].SHA256
	if err := os.WriteFile(chunkPath(corrupt), []byte{0, 1, 2, 3}, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(chunkPath(missing)); err != nil {
		t.Fatal(err)
	}
	orphan := []byte("trozo sin registrar")
	if _, err := f.chunks.Put(chunker.Hash(sha256.Sum256(orphan)), orphan); err != nil {
		t.Fatal(err)
	}

	report, err = Check(ctx, f.database, f.chunks)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.OK() {
		t.Fatal("debería detectar el daño")
	}
	if len(report.Corrupt) != 1 || report.Corrupt[0] != corrupt.String() {
		t.Errorf("Corrupt = %v", report.Corrupt)
	}
	if len(report.Missing) != 1 || report.Missing[0] != missing.String() {
		t.Errorf("Missing = %v", report.Missing)
	}
	if report.Orphans != 1 {
		t.Errorf("Orphans = %d", report.Orphans)
	}
}

func TestLockDataDirIsExclusive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lock, err := LockDataDir(dir)
	if err != nil {
		t.Fatalf("LockDataDir: %v", err)
	}
	if _, err := LockDataDir(dir); !errors.Is(err, ErrDataInUse) {
		t.Fatalf("segundo LockDataDir = %v, quiero ErrDataInUse", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := LockDataDir(dir)
	if err != nil {
		t.Fatalf("LockDataDir tras liberar: %v", err)
	}
	_ = again.Close()
}

// TestRecordedBackupIsRestorable es la copia automática: la función que llama el
// servidor cada día deja una copia que se restaura y verifica.
func TestRecordedBackupIsRestorable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := newFixture(t)
	backupDir := t.TempDir()
	statusPath := filepath.Join(f.dir, StatusFile)

	stats, _, err := RecordedBackup(ctx, f.database, f.chunks, backupDir, statusPath)
	if err != nil {
		t.Fatalf("RecordedBackup: %v", err)
	}
	status, err := ReadBackupStatus(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastOK.IsZero() || status.Dir != backupDir || status.Copied != stats.Copied || status.Failing() {
		t.Fatalf("estado = %+v", status)
	}

	restored := t.TempDir()
	if _, err := Restore(ctx, backupDir, restored, false, time.Now()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	database, chunks, svc := openData(t, restored)
	report, err := Check(ctx, database, chunks)
	if err != nil || !report.OK() || report.Checked == 0 {
		t.Fatalf("Check = %+v, %v", report, err)
	}
	if got := readVersion(t, svc, f.result.Version.ID); !bytes.Equal(got, f.data) {
		t.Fatal("el contenido restaurado no coincide")
	}
}

// Una copia que falla a medias no estropea la anterior y queda registrada.
func TestFailedBackupKeepsPreviousCopy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := newFixture(t)
	backupDir := t.TempDir()
	statusPath := filepath.Join(f.dir, StatusFile)
	if _, _, err := RecordedBackup(ctx, f.database, f.chunks, backupDir, statusPath); err != nil {
		t.Fatal(err)
	}

	// Un archivo nuevo con un trozo dañado en el origen: la copia falla al copiarlo,
	// después de haber copiado la base.
	m := upload(t, f.svc, bytes.Repeat([]byte("versión nueva "), 100_000))
	if _, err := f.svc.CreateAsset(ctx, f.actor, vault.CreateAssetInput{ProjectID: f.project.ID, Path: "b.blend", Manifest: m, Comment: "Nueva"}); err != nil {
		t.Fatal(err)
	}
	name := m.Chunks[0].SHA256.String()
	if err := os.WriteFile(filepath.Join(f.dir, ChunksDir, name[:2], name[2:4], name), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RecordedBackup(ctx, f.database, f.chunks, backupDir, statusPath); err == nil {
		t.Fatal("la copia debería fallar")
	}
	status, err := ReadBackupStatus(statusPath)
	if err != nil || !status.Failing() || status.LastError == "" || status.LastOK.IsZero() {
		t.Fatalf("estado = %+v, %v", status, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(backupDir, DatabaseFile+".*")); len(leftovers) != 0 {
		t.Errorf("quedaron temporales: %v", leftovers)
	}

	restored := t.TempDir()
	if _, err := Restore(ctx, backupDir, restored, false, time.Now()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	database, chunks, svc := openData(t, restored)
	if report, err := Check(ctx, database, chunks); err != nil || !report.OK() {
		t.Fatalf("la copia anterior debe seguir sana: %+v, %v", report, err)
	}
	snap, err := svc.GetSnapshot(ctx, f.project.ID)
	if err != nil || len(snap.Assets) != 1 {
		t.Fatalf("la copia debe ser la anterior: %+v, %v", snap.Assets, err)
	}
}

func TestNextBackup(t *testing.T) {
	t.Parallel()

	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Skip("sin zonas horarias:", err)
	}
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 3, day, hour, minute, 0, 0, madrid) }
	for _, tt := range []struct {
		now  time.Time
		hour int
		want time.Time
	}{
		{at(10, 2, 59), 3, at(10, 3, 0)},
		{at(10, 3, 0), 3, at(11, 3, 0)},
		{at(10, 23, 30), 3, at(11, 3, 0)},
		{at(10, 23, 30), 0, at(11, 0, 0)},
		{at(31, 12, 0), 23, at(31, 23, 0)},
		{at(28, 22, 0), 3, at(29, 3, 0)},
	} {
		if got := NextBackup(tt.now, tt.hour); !got.Equal(tt.want) {
			t.Errorf("NextBackup(%v, %d) = %v, quiero %v", tt.now, tt.hour, got, tt.want)
		}
	}
	// La noche del cambio de hora solo pasan 4 horas de 22:00 a 03:00.
	if got := NextBackup(at(28, 22, 0), 3).Sub(at(28, 22, 0)); got != 4*time.Hour {
		t.Errorf("con cambio de hora = %v, quiero 4h", got)
	}
}
