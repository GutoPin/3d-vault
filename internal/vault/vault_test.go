package vault

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"3d-vault/internal/auth"
	"3d-vault/internal/chunker"
	"3d-vault/internal/db"
	"3d-vault/internal/events"
	"3d-vault/internal/manifest"
	"3d-vault/internal/rules"
	"3d-vault/internal/store"
)

type env struct {
	t       *testing.T
	ctx     context.Context
	svc     *Service
	db      *db.DB
	store   *store.Store
	juan    rules.Actor
	juanPC2 rules.Actor
	ana     rules.Actor
	admin   rules.Actor
	project Project
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	database, err := db.Open(ctx, filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	chunks, err := store.New(filepath.Join(dir, "chunks"))
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = chunks.Close() })

	svc := New(Config{
		DB:          database,
		Store:       chunks,
		Hub:         events.NewHub(),
		MaxFileSize: 1 << 30,
		Argon:       auth.Params{Memory: 1024, Time: 1, Threads: 1},
	})

	e := &env{t: t, ctx: ctx, svc: svc, db: database, store: chunks}
	e.juan = e.newActor("juan", "Juan", false, "pc-1", "PC de Juan")
	e.juanPC2 = rules.Actor{UserID: e.juan.UserID, ClientID: "pc-2", ClientName: "Portátil de Juan"}
	e.ana = e.newActor("ana", "Ana", false, "pc-3", "PC de Ana")
	e.admin = e.newActor("jefa", "Jefa", true, "pc-4", "PC de la jefa")

	e.project, err = svc.CreateProject(ctx, e.juan, "Mina Norte", "Camiones")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return e
}

func (e *env) newActor(username, display string, admin bool, clientID, clientName string) rules.Actor {
	e.t.Helper()
	u, err := e.svc.CreateUser(e.ctx, 0, NewUserInput{Username: username, DisplayName: display, Password: "contraseña-larga", IsAdmin: admin})
	if err != nil {
		e.t.Fatalf("CreateUser: %v", err)
	}
	return rules.Actor{UserID: u.ID, IsAdmin: admin, ClientID: clientID, ClientName: clientName}
}

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	return data
}

// upload sube los trozos de data y devuelve su manifiesto.
func (e *env) upload(data []byte) manifest.Manifest {
	e.t.Helper()
	m, err := manifest.Build(bytes.NewReader(data))
	if err != nil {
		e.t.Fatalf("manifest.Build: %v", err)
	}
	c := chunker.New(bytes.NewReader(data))
	for {
		chunk, err := c.Next()
		if err != nil {
			break
		}
		if err := e.svc.PutChunk(e.ctx, chunk.Hash, chunk.Data); err != nil {
			e.t.Fatalf("PutChunk: %v", err)
		}
	}
	return m
}

// newAsset crea un archivo con contenido aleatorio.
func (e *env) newAsset(path string) CommitResult {
	e.t.Helper()
	m := e.upload(randomBytes(e.t, 8<<10))
	result, err := e.svc.CreateAsset(e.ctx, e.juan, CreateAssetInput{ProjectID: e.project.ID, Path: path, Manifest: m, Comment: "Primera versión"})
	if err != nil {
		e.t.Fatalf("CreateAsset: %v", err)
	}
	return result
}

// commit sube una versión nueva con contenido aleatorio.
func (e *env) commit(actor rules.Actor, assetID, base int64) (CommitResult, error) {
	e.t.Helper()
	m := e.upload(randomBytes(e.t, 8<<10))
	return e.svc.CommitVersion(e.ctx, actor, CommitInput{AssetID: assetID, BaseVersion: base, Manifest: m, SizeBytes: m.Size(), Comment: "Ajuste de ruedas"})
}

func wantRule(t *testing.T, err error, want rules.Code) *rules.Error {
	t.Helper()
	var ruleErr *rules.Error
	if !errors.As(err, &ruleErr) {
		t.Fatalf("error = %v, quiero código %q", err, want)
	}
	if ruleErr.Code != want {
		t.Fatalf("código = %q (%s), quiero %q", ruleErr.Code, ruleErr.Message, want)
	}
	return ruleErr
}

func (e *env) mustHead(assetID int64) Asset {
	e.t.Helper()
	a, err := e.svc.GetAsset(e.ctx, assetID)
	if err != nil {
		e.t.Fatalf("GetAsset: %v", err)
	}
	return a
}

func (e *env) mustLock(actor rules.Actor, assetID int64) {
	e.t.Helper()
	if _, err := e.svc.Lock(e.ctx, actor, assetID, false); err != nil {
		e.t.Fatalf("Lock: %v", err)
	}
}

func (e *env) activityCount(action string) int {
	e.t.Helper()
	var count int
	err := e.db.ReadTx(e.ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM activity WHERE action = ?`, action).Scan(&count)
	})
	if err != nil {
		e.t.Fatalf("contar actividad: %v", err)
	}
	return count
}

// Regla 1: cualquier usuario puede ver y descargar cualquier versión.
func TestRule1AnyUserCanReadAnyVersion(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	created := e.newAsset("modelos/camion.blend")
	e.mustLock(e.juan, created.Asset.ID)

	versions, err := e.svc.ListVersions(e.ctx, created.Asset.ID)
	if err != nil || len(versions) != 1 {
		t.Fatalf("ListVersions = %v, %v", versions, err)
	}
	file, err := e.svc.GetVersionFile(e.ctx, versions[0].ID)
	if err != nil {
		t.Fatalf("GetVersionFile: %v", err)
	}
	for _, c := range file.Manifest.Chunks {
		if _, err := e.svc.GetChunk(e.ctx, c.SHA256); err != nil {
			t.Fatalf("GetChunk: %v", err)
		}
	}
}

// Regla 2: quién puede subir una versión.
func TestRule2UploadDependsOnLock(t *testing.T) {
	t.Parallel()

	t.Run("sin bloqueo", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		a := e.newAsset("a.blend")
		result, err := e.commit(e.ana, a.Asset.ID, 1)
		if err != nil {
			t.Fatalf("commit: %v", err)
		}
		if result.Version.Number != 2 || result.Asset.HeadVersion != 2 {
			t.Fatalf("versión = %d, head = %d", result.Version.Number, result.Asset.HeadVersion)
		}
	})

	t.Run("bloqueado por mí", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		a := e.newAsset("a.blend")
		e.mustLock(e.juan, a.Asset.ID)
		if _, err := e.commit(e.juan, a.Asset.ID, 1); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if e.mustHead(a.Asset.ID).Lock == nil {
			t.Fatal("subir no debe liberar el bloqueo si no se pide")
		}
	})

	t.Run("bloqueado por otro", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		a := e.newAsset("a.blend")
		e.mustLock(e.juan, a.Asset.ID)
		_, err := e.commit(e.ana, a.Asset.ID, 1)
		ruleErr := wantRule(t, err, rules.CodeLocked)
		if !strings.Contains(ruleErr.Message, "Bloqueado por Juan desde") {
			t.Fatalf("mensaje = %q", ruleErr.Message)
		}
		if ruleErr.Lock == nil || ruleErr.HeadVersion != 1 {
			t.Fatalf("faltan datos en el rechazo: %+v", ruleErr)
		}
		if head := e.mustHead(a.Asset.ID).HeadVersion; head != 1 {
			t.Fatalf("no debe crear versión: head = %d", head)
		}
	})

	t.Run("versión vieja", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		a := e.newAsset("a.blend")
		if _, err := e.commit(e.juan, a.Asset.ID, 1); err != nil {
			t.Fatal(err)
		}
		_, err := e.commit(e.ana, a.Asset.ID, 1)
		ruleErr := wantRule(t, err, rules.CodeStaleVersion)
		if ruleErr.HeadVersion != 2 {
			t.Fatalf("HeadVersion = %d", ruleErr.HeadVersion)
		}
	})
}

// Regla 3: comentario obligatorio, también al crear.
func TestRule3CommentIsRequired(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("a.blend")
	m := e.upload(randomBytes(t, 1024))

	_, err := e.svc.CommitVersion(e.ctx, e.juan, CommitInput{AssetID: a.Asset.ID, BaseVersion: 1, Manifest: m, SizeBytes: m.Size(), Comment: "  ok "})
	wantRule(t, err, rules.CodeInvalidComment)

	_, err = e.svc.CreateAsset(e.ctx, e.juan, CreateAssetInput{ProjectID: e.project.ID, Path: "b.blend", Manifest: m, Comment: ""})
	wantRule(t, err, rules.CodeInvalidComment)
}

// Regla 4: solo quien bloquea libera; el admin fuerza y queda registrado.
func TestRule4Release(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("a.blend")
	id := a.Asset.ID
	e.mustLock(e.juan, id)

	wantRule(t, e.svc.Unlock(e.ctx, e.ana, id, false), rules.CodeNotLockOwner)
	wantRule(t, e.svc.Unlock(e.ctx, e.ana, id, true), rules.CodeForbidden)
	wantRule(t, e.svc.Unlock(e.ctx, e.admin, id, false), rules.CodeNotLockOwner)
	if e.mustHead(id).Lock == nil {
		t.Fatal("el bloqueo debería seguir")
	}

	if err := e.svc.Unlock(e.ctx, e.admin, id, true); err != nil {
		t.Fatalf("forzar: %v", err)
	}
	if e.mustHead(id).Lock != nil {
		t.Fatal("el bloqueo debería estar libre")
	}
	if got := e.activityCount(actionForceUnlock); got != 1 {
		t.Fatalf("desbloqueos forzados registrados = %d", got)
	}

	// Liberar lo ya libre no es un error, para que reintentar sea seguro.
	if err := e.svc.Unlock(e.ctx, e.juan, id, false); err != nil {
		t.Fatalf("liberar algo libre: %v", err)
	}
	e.mustLock(e.juan, id)
	if err := e.svc.Unlock(e.ctx, e.juan, id, false); err != nil {
		t.Fatalf("liberar el propio: %v", err)
	}
	if got := e.activityCount(actionUnlock); got != 1 {
		t.Fatalf("liberaciones registradas = %d", got)
	}
}

// Regla 5: restaurar crea vN+1 con el mismo manifiesto y sin duplicar bytes.
func TestRule5Restore(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("a.blend")
	id := a.Asset.ID
	first := a.Version
	if _, err := e.commit(e.juan, id, 1); err != nil {
		t.Fatal(err)
	}

	countChunks := func() int {
		var n int
		if err := e.db.ReadTx(e.ctx, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := countChunks()

	result, err := e.svc.Restore(e.ctx, e.ana, id, 1, 2)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	v := result.Version
	if v.Number != 3 || v.Comment != "Restaurado desde v1" || v.FileSHA256 != first.FileSHA256 {
		t.Fatalf("versión restaurada = %+v", v)
	}
	if v.manifestID != first.manifestID {
		t.Fatal("debe reutilizar el manifiesto")
	}
	if v.RestoredFrom == nil || *v.RestoredFrom != first.ID {
		t.Fatalf("RestoredFrom = %v", v.RestoredFrom)
	}
	if countChunks() != before {
		t.Fatal("restaurar no debe añadir trozos")
	}

	t.Run("bloqueado por otro", func(t *testing.T) {
		e.mustLock(e.juan, id)
		_, err := e.svc.Restore(e.ctx, e.ana, id, 2, 3)
		wantRule(t, err, rules.CodeLocked)
		if err := e.svc.Unlock(e.ctx, e.juan, id, false); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("versión vieja", func(t *testing.T) {
		_, err := e.svc.Restore(e.ctx, e.ana, id, 2, 2)
		wantRule(t, err, rules.CodeStaleVersion)
	})
	t.Run("versión inexistente", func(t *testing.T) {
		_, err := e.svc.Restore(e.ctx, e.ana, id, 99, 3)
		wantRule(t, err, rules.CodeNotFound)
	})
	t.Run("restaurar lo mismo que la última no crea versión", func(t *testing.T) {
		result, err := e.svc.Restore(e.ctx, e.ana, id, 1, 3)
		if err != nil {
			t.Fatalf("Restore: %v", err)
		}
		if !result.NoChanges || result.Asset.HeadVersion != 3 {
			t.Fatalf("resultado = %+v", result)
		}
	})
}

// Regla 6: mismo contenido, sin versión.
func TestRule6NoChanges(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	data := randomBytes(t, 8<<10)
	m := e.upload(data)
	created, err := e.svc.CreateAsset(e.ctx, e.juan, CreateAssetInput{ProjectID: e.project.ID, Path: "a.blend", Manifest: m, Comment: "Primera"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Asset.ID

	result, err := e.svc.CommitVersion(e.ctx, e.juan, CommitInput{AssetID: id, BaseVersion: 1, Manifest: m, SizeBytes: m.Size(), Comment: "Sin cambios"})
	if err != nil {
		t.Fatalf("CommitVersion: %v", err)
	}
	if !result.NoChanges || result.Version.Number != 1 {
		t.Fatalf("resultado = %+v", result)
	}
	if head := e.mustHead(id).HeadVersion; head != 1 {
		t.Fatalf("head = %d", head)
	}

	// Una subida confirmada cuya respuesta se perdió: el reintento parte de una
	// versión vieja, pero su contenido ya es la última versión.
	m2 := e.upload(randomBytes(t, 8<<10))
	in := CommitInput{AssetID: id, BaseVersion: 1, Manifest: m2, SizeBytes: m2.Size(), Comment: "Segunda"}
	if _, err := e.svc.CommitVersion(e.ctx, e.juan, in); err != nil {
		t.Fatal(err)
	}
	retry, err := e.svc.CommitVersion(e.ctx, e.juan, in)
	if err != nil {
		t.Fatalf("reintento: %v", err)
	}
	if !retry.NoChanges || retry.Version.Number != 2 {
		t.Fatalf("reintento = %+v", retry)
	}

	// Crear de nuevo el mismo archivo tampoco es un error.
	again, err := e.svc.CreateAsset(e.ctx, e.juan, CreateAssetInput{ProjectID: e.project.ID, Path: "A.BLEND", Manifest: m2, Comment: "Reintento"})
	if err != nil {
		t.Fatalf("crear de nuevo: %v", err)
	}
	if !again.NoChanges || again.Asset.ID != id {
		t.Fatalf("crear de nuevo = %+v", again)
	}
}

// Reglas 7 y 9: mover conserva el historial y la extensión.
func TestRule7And9Move(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("modelos/camion.blend")
	id := a.Asset.ID
	e.newAsset("modelos/otro.blend")
	if _, err := e.commit(e.juan, id, 1); err != nil {
		t.Fatal(err)
	}

	wantMove := func(actor rules.Actor, base int64, path string, want rules.Code) {
		t.Helper()
		_, err := e.svc.Move(e.ctx, actor, id, base, path)
		wantRule(t, err, want)
	}
	wantMove(e.ana, 2, "modelos/camion.fbx", rules.CodeExtensionChanged)
	wantMove(e.ana, 2, "modelos/camion", rules.CodeExtensionChanged)
	wantMove(e.ana, 2, "modelos/OTRO.blend", rules.CodePathExists)
	wantMove(e.ana, 1, "nuevo/camion.blend", rules.CodeStaleVersion)
	wantMove(e.ana, 2, "../camion.blend", rules.CodeInvalidPath)

	e.mustLock(e.juan, id)
	wantMove(e.ana, 2, "nuevo/camion.blend", rules.CodeLocked)

	moved, err := e.svc.Move(e.ctx, e.juan, id, 2, "nuevo/camion.blend")
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if moved.Path != "nuevo/camion.blend" || moved.HeadVersion != 2 {
		t.Fatalf("archivo movido = %+v", moved)
	}
	versions, err := e.svc.ListVersions(e.ctx, id)
	if err != nil || len(versions) != 2 {
		t.Fatalf("debe conservar el historial: %d versiones, %v", len(versions), err)
	}
	if got := e.activityCount(actionMove); got != 1 {
		t.Fatalf("movimientos registrados = %d", got)
	}

	// Cambiar solo las mayúsculas es válido: la ruta sigue siendo la suya.
	renamed, err := e.svc.Move(e.ctx, e.juan, id, 2, "nuevo/CAMION.blend")
	if err != nil {
		t.Fatalf("renombrar mayúsculas: %v", err)
	}
	if renamed.Path != "nuevo/CAMION.blend" {
		t.Fatalf("ruta = %q", renamed.Path)
	}
	// Mover a la misma ruta no hace nada.
	if _, err := e.svc.Move(e.ctx, e.juan, id, 2, "nuevo/CAMION.blend"); err != nil {
		t.Fatalf("mover a la misma ruta: %v", err)
	}
	if got := e.activityCount(actionMove); got != 2 {
		t.Fatalf("movimientos registrados = %d", got)
	}
}

// Regla 8: borrado suave, con las mismas condiciones de bloqueo.
func TestRule8SoftDelete(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("a.blend")
	id := a.Asset.ID

	e.mustLock(e.juan, id)
	wantRule(t, e.svc.Delete(e.ctx, e.ana, id, 1), rules.CodeLocked)
	if err := e.svc.Unlock(e.ctx, e.juan, id, false); err != nil {
		t.Fatal(err)
	}
	wantRule(t, e.svc.Delete(e.ctx, e.ana, id, 0), rules.CodeStaleVersion)

	if err := e.svc.Delete(e.ctx, e.ana, id, 1); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := e.svc.GetAsset(e.ctx, id)
	wantRule(t, err, rules.CodeNotFound)
	snap, err := e.svc.GetSnapshot(e.ctx, e.project.ID)
	if err != nil || len(snap.Assets) != 0 {
		t.Fatalf("no debe listar borrados: %+v, %v", snap.Assets, err)
	}

	// Los datos siguen disponibles.
	file, err := e.svc.GetVersionFile(e.ctx, a.Version.ID)
	if err != nil {
		t.Fatalf("GetVersionFile tras borrar: %v", err)
	}
	if _, err := e.svc.GetChunk(e.ctx, file.Manifest.Chunks[0].SHA256); err != nil {
		t.Fatalf("GetChunk tras borrar: %v", err)
	}

	// La ruta queda libre para un archivo nuevo, con historial propio.
	second := e.newAsset("a.blend")
	if second.Asset.ID == id || second.Version.Number != 1 {
		t.Fatalf("archivo nuevo = %+v", second)
	}
	wantRule(t, e.svc.Delete(e.ctx, e.ana, id, 1), rules.CodeNotFound)
}

// Regla 10: toda acción queda registrada.
func TestRule10ActivityIsRecorded(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("a.blend")
	id := a.Asset.ID
	e.mustLock(e.juan, id)
	if _, err := e.commit(e.juan, id, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Unlock(e.ctx, e.juan, id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Restore(e.ctx, e.juan, id, 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Move(e.ctx, e.juan, id, 3, "b.blend"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(e.ctx, e.juan, id, 3); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.CreateToken(e.ctx, e.juan.UserID, "PC"); err != nil {
		t.Fatal(err)
	}

	for _, action := range []string{
		actionCreateProject, actionCreateAsset, actionLock, actionUpload, actionUnlock,
		actionRestore, actionMove, actionDelete, actionCreateUser, actionCreateToken,
	} {
		if e.activityCount(action) == 0 {
			t.Errorf("sin registro para %s", action)
		}
	}
}

// Regla 11: rutas que chocarían en Windows.
func TestRule11PathsAreUniqueIgnoringCase(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	e.newAsset("Modelos/Camión.blend")

	m := e.upload(randomBytes(t, 1024))
	for _, path := range []string{"modelos/camión.blend", "MODELOS/CAMIÓN.BLEND", "Modelos/Camión.blend"} {
		_, err := e.svc.CreateAsset(e.ctx, e.juan, CreateAssetInput{ProjectID: e.project.ID, Path: path, Manifest: m, Comment: "Choque"})
		wantRule(t, err, rules.CodePathExists)
	}
}

func TestLocksBelongToAUserAndAClient(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("a.blend")
	id := a.Asset.ID
	e.mustLock(e.juan, id)

	// Pedirlo otra vez en la misma PC no cambia nada ni genera ruido.
	before := e.activityCount(actionLock)
	e.mustLock(e.juan, id)
	if got := e.activityCount(actionLock); got != before {
		t.Fatalf("volver a bloquear registró actividad: %d y %d", before, got)
	}

	_, err := e.commit(e.juanPC2, id, 1)
	wantRule(t, err, rules.CodeLockedOtherClient)
	_, err = e.svc.Lock(e.ctx, e.juanPC2, id, false)
	wantRule(t, err, rules.CodeLockedOtherClient)
	_, err = e.svc.Lock(e.ctx, e.ana, id, true)
	wantRule(t, err, rules.CodeLocked)

	result, err := e.svc.Lock(e.ctx, e.juanPC2, id, true)
	if err != nil {
		t.Fatalf("tomar el bloqueo: %v", err)
	}
	if result.Lock.ClientID != "pc-2" || result.HeadVersion != 1 {
		t.Fatalf("resultado = %+v", result)
	}
	if e.activityCount(actionTakeover) != 1 {
		t.Fatal("tomar el bloqueo debe quedar registrado")
	}

	_, err = e.commit(e.juan, id, 1)
	wantRule(t, err, rules.CodeLockedOtherClient)
	if _, err := e.commit(e.juanPC2, id, 1); err != nil {
		t.Fatalf("subir desde la PC con el bloqueo: %v", err)
	}
}

func TestCommitCanReleaseLockAtomically(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("a.blend")
	id := a.Asset.ID
	e.mustLock(e.juan, id)

	m := e.upload(randomBytes(t, 1024))
	result, err := e.svc.CommitVersion(e.ctx, e.juan, CommitInput{AssetID: id, BaseVersion: 1, Manifest: m, SizeBytes: m.Size(), Comment: "Terminado", ReleaseLock: true})
	if err != nil {
		t.Fatalf("CommitVersion: %v", err)
	}
	if result.Asset.Lock != nil {
		t.Fatal("debería quedar libre")
	}

	page, err := e.svc.EventsSince(e.ctx, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := page.Events[len(page.Events)-2:]
	if last[0].Kind != EventVersion || last[1].Kind != EventUnlocked {
		t.Fatalf("eventos = %s, %s", last[0].Kind, last[1].Kind)
	}
}

// TestConcurrentCommitsFromSameBase es uno de los criterios de aceptación: una
// gana y las demás reciben un rechazo, nunca dos con el mismo número.
func TestConcurrentCommitsFromSameBase(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("a.blend")
	id := a.Asset.ID

	const workers = 16
	manifests := make([]manifest.Manifest, workers)
	for i := range manifests {
		manifests[i] = e.upload(randomBytes(t, 8<<10))
	}

	var (
		wg      sync.WaitGroup
		won     atomic.Int32
		stale   atomic.Int32
		unknown atomic.Int32
	)
	for i := range workers {
		wg.Go(func() {
			m := manifests[i]
			_, err := e.svc.CommitVersion(e.ctx, e.juan, CommitInput{AssetID: id, BaseVersion: 1, Manifest: m, SizeBytes: m.Size(), Comment: "En paralelo"})
			var ruleErr *rules.Error
			switch {
			case err == nil:
				won.Add(1)
			case errors.As(err, &ruleErr) && ruleErr.Code == rules.CodeStaleVersion:
				stale.Add(1)
			default:
				unknown.Add(1)
				t.Errorf("error inesperado: %v", err)
			}
		})
	}
	wg.Wait()

	if won.Load() != 1 || stale.Load() != workers-1 {
		t.Fatalf("ganaron %d, rechazadas %d, otras %d", won.Load(), stale.Load(), unknown.Load())
	}
	versions, err := e.svc.ListVersions(e.ctx, id)
	if err != nil || len(versions) != 2 {
		t.Fatalf("versiones = %d, %v", len(versions), err)
	}
	if e.mustHead(id).HeadVersion != 2 {
		t.Fatal("head debería ser 2")
	}
}

func TestConcurrentLocksHaveOneWinner(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("a.blend")

	actors := []rules.Actor{e.juan, e.ana, e.admin}
	var (
		wg  sync.WaitGroup
		won atomic.Int32
	)
	for _, actor := range actors {
		wg.Go(func() {
			if _, err := e.svc.Lock(e.ctx, actor, a.Asset.ID, false); err == nil {
				won.Add(1)
			}
		})
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("ganaron %d", won.Load())
	}
}

// TestChangesDuringVerificationAreRespected cubre lo que pasa mientras se verifica
// un archivo grande, fuera de la transacción: alguien bloquea o sube otra versión.
func TestChangesDuringVerificationAreRespected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(e *env, assetID int64)
		want   rules.Code
	}{
		{"otro bloquea", func(e *env, assetID int64) { e.mustLock(e.ana, assetID) }, rules.CodeLocked},
		{"otro sube", func(e *env, assetID int64) {
			if _, err := e.commit(e.ana, assetID, 1); err != nil {
				e.t.Errorf("commit: %v", err)
			}
		}, rules.CodeStaleVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t)
			a := e.newAsset("a.blend")
			m := e.upload(randomBytes(t, 8<<10))

			// El cambio puede subir otra versión, que vuelve a pasar por aquí.
			var fired atomic.Bool
			e.svc.afterVerify = func() {
				if fired.CompareAndSwap(false, true) {
					tt.change(e, a.Asset.ID)
				}
			}
			_, err := e.svc.CommitVersion(e.ctx, e.juan, CommitInput{AssetID: a.Asset.ID, BaseVersion: 1, Manifest: m, SizeBytes: m.Size(), Comment: "Tarde"})
			wantRule(t, err, tt.want)
		})
	}
}

func TestManifestIsVerifiedBeforeUse(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("a.blend")
	id := a.Asset.ID

	// Dos trozos reales, de archivos distintos, para poder desordenarlos.
	first, second := randomBytes(t, 512<<10), randomBytes(t, 512<<10)
	whole := append(append([]byte{}, first...), second...)
	good := e.upload(whole)
	if len(good.Chunks) < 2 {
		t.Skip("el corte no produjo dos trozos")
	}

	t.Run("orden incorrecto", func(t *testing.T) {
		bad := manifest.Manifest{FileSHA256: good.FileSHA256, Chunks: append([]manifest.Chunk{}, good.Chunks...)}
		bad.Chunks[0], bad.Chunks[1] = bad.Chunks[1], bad.Chunks[0]
		_, err := e.svc.CommitVersion(e.ctx, e.juan, CommitInput{AssetID: id, BaseVersion: 1, Manifest: bad, SizeBytes: bad.Size(), Comment: "Mal orden"})
		wantRule(t, err, rules.CodeManifestMismatch)
	})
	t.Run("tamaño falso de un trozo", func(t *testing.T) {
		bad := manifest.Manifest{FileSHA256: good.FileSHA256, Chunks: append([]manifest.Chunk{}, good.Chunks...)}
		bad.Chunks[0].Size++
		_, err := e.svc.CommitVersion(e.ctx, e.juan, CommitInput{AssetID: id, BaseVersion: 1, Manifest: bad, SizeBytes: bad.Size(), Comment: "Mal tamaño"})
		wantRule(t, err, rules.CodeManifestMismatch)
	})
	t.Run("trozo que falta", func(t *testing.T) {
		bad := manifest.Manifest{FileSHA256: chunker.Hash{7}, Chunks: []manifest.Chunk{{SHA256: chunker.Hash{8}, Size: 10}}}
		_, err := e.svc.CommitVersion(e.ctx, e.juan, CommitInput{AssetID: id, BaseVersion: 1, Manifest: bad, SizeBytes: 10, Comment: "Sin subir"})
		ruleErr := wantRule(t, err, rules.CodeMissingChunks)
		if len(ruleErr.Missing) != 1 || ruleErr.Missing[0] != (chunker.Hash{8}).String() {
			t.Fatalf("Missing = %v", ruleErr.Missing)
		}
	})
	t.Run("tamaño total distinto", func(t *testing.T) {
		_, err := e.svc.CommitVersion(e.ctx, e.juan, CommitInput{AssetID: id, BaseVersion: 1, Manifest: good, SizeBytes: 1, Comment: "Mal total"})
		wantRule(t, err, rules.CodeInvalidManifest)
	})
	t.Run("demasiado grande", func(t *testing.T) {
		big := manifest.Manifest{}
		for range 300 {
			big.Chunks = append(big.Chunks, manifest.Chunk{SHA256: chunker.Hash{9}, Size: chunker.MaxSize})
		}
		_, err := e.svc.CommitVersion(e.ctx, e.juan, CommitInput{AssetID: id, BaseVersion: 1, Manifest: big, SizeBytes: big.Size(), Comment: "Enorme"})
		wantRule(t, err, rules.CodeInvalidManifest)
	})

	// Un manifiesto malo no debe envenenar al bueno, que tiene el mismo hash.
	result, err := e.svc.CommitVersion(e.ctx, e.juan, CommitInput{AssetID: id, BaseVersion: 1, Manifest: good, SizeBytes: good.Size(), Comment: "Correcto"})
	if err != nil {
		t.Fatalf("el manifiesto correcto debería funcionar: %v", err)
	}
	file, err := e.svc.GetVersionFile(e.ctx, result.Version.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rebuilt []byte
	for _, c := range file.Manifest.Chunks {
		data, err := e.svc.GetChunk(e.ctx, c.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		rebuilt = append(rebuilt, data...)
	}
	if !bytes.Equal(rebuilt, whole) {
		t.Fatal("el archivo reconstruido no coincide")
	}
}

func TestPutChunkRejectsWrongContent(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	h := chunker.Hash{1}
	wantRule(t, e.svc.PutChunk(e.ctx, h, []byte("otro")), rules.CodeManifestMismatch)
	missing, err := e.svc.MissingChunks(e.ctx, []chunker.Hash{h})
	if err != nil || len(missing) != 1 {
		t.Fatalf("no debe registrar el trozo: %v, %v", missing, err)
	}
}

func TestMissingChunksHandlesLargeLists(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	m := e.upload(randomBytes(t, 8<<10))
	present := m.Chunks[0].SHA256

	hashes := []chunker.Hash{present}
	for i := range scanBatch + 10 {
		hashes = append(hashes, chunker.Hash{byte(i), byte(i >> 8), 1})
	}
	missing, err := e.svc.MissingChunks(e.ctx, hashes)
	if err != nil {
		t.Fatalf("MissingChunks: %v", err)
	}
	if len(missing) != len(hashes)-1 {
		t.Fatalf("faltan %d, quiero %d", len(missing), len(hashes)-1)
	}
	for _, h := range missing {
		if h == present {
			t.Fatal("el trozo existente no debe aparecer")
		}
	}
}

func TestEmptyFile(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	m, err := manifest.Build(bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.svc.CreateAsset(e.ctx, e.juan, CreateAssetInput{ProjectID: e.project.ID, Path: "vacio.txt", Manifest: m, Comment: "Archivo vacío"})
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	if result.Asset.SizeBytes != 0 {
		t.Fatalf("SizeBytes = %d", result.Asset.SizeBytes)
	}
	file, err := e.svc.GetVersionFile(e.ctx, result.Version.ID)
	if err != nil || len(file.Manifest.Chunks) != 0 {
		t.Fatalf("GetVersionFile = %+v, %v", file, err)
	}
}

func TestArchivedProjectRejectsWrites(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("a.blend")
	if err := e.db.WriteTx(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE projects SET archived = 1 WHERE id = ?`, e.project.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	_, err := e.commit(e.juan, a.Asset.ID, 1)
	wantRule(t, err, rules.CodeProjectArchived)
	_, err = e.svc.Lock(e.ctx, e.juan, a.Asset.ID, false)
	wantRule(t, err, rules.CodeProjectArchived)
	m := e.upload(randomBytes(t, 1024))
	_, err = e.svc.CreateAsset(e.ctx, e.juan, CreateAssetInput{ProjectID: e.project.ID, Path: "b.blend", Manifest: m, Comment: "Nuevo"})
	wantRule(t, err, rules.CodeProjectArchived)
	if _, err := e.svc.GetAsset(e.ctx, a.Asset.ID); err != nil {
		t.Fatalf("leer debe seguir funcionando: %v", err)
	}
}

func TestProjects(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	_, err := e.svc.CreateProject(e.ctx, e.ana, "MINA NORTE", "")
	wantRule(t, err, rules.CodeNameExists)

	for _, name := range []string{"", "a/b", "..", "CON", "fin."} {
		_, err := e.svc.CreateProject(e.ctx, e.ana, name, "")
		wantRule(t, err, rules.CodeInvalidInput)
	}

	if _, err := e.svc.CreateProject(e.ctx, e.ana, "Ampliación Sur", "  "); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	projects, err := e.svc.ListProjects(e.ctx)
	if err != nil || len(projects) != 2 || projects[0].Name != "Ampliación Sur" {
		t.Fatalf("ListProjects = %+v, %v", projects, err)
	}
	_, err = e.svc.GetProject(e.ctx, 999)
	wantRule(t, err, rules.CodeNotFound)
}

func decodePayload(t *testing.T, e Event) eventPayload {
	t.Helper()
	var p eventPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	return p
}

func TestEventsDescribeChanges(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	other, err := e.svc.CreateProject(e.ctx, e.juan, "Otro", "")
	if err != nil {
		t.Fatal(err)
	}
	a := e.newAsset("a.blend")
	id := a.Asset.ID
	m := e.upload(randomBytes(t, 1024))
	if _, err := e.svc.CreateAsset(e.ctx, e.juan, CreateAssetInput{ProjectID: other.ID, Path: "b.blend", Manifest: m, Comment: "En otro"}); err != nil {
		t.Fatal(err)
	}

	e.mustLock(e.juan, id)
	if _, err := e.commit(e.juan, id, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Unlock(e.ctx, e.juan, id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Move(e.ctx, e.juan, id, 2, "c.blend"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(e.ctx, e.juan, id, 2); err != nil {
		t.Fatal(err)
	}

	page, err := e.svc.EventsSince(e.ctx, []int64{e.project.ID}, 0)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	wantKinds := []string{EventAssetCreated, EventLocked, EventVersion, EventUnlocked, EventMoved, EventDeleted}
	if len(page.Events) != len(wantKinds) {
		t.Fatalf("%d eventos, quiero %d", len(page.Events), len(wantKinds))
	}
	for i, want := range wantKinds {
		if page.Events[i].Kind != want {
			t.Errorf("evento %d = %s, quiero %s", i, page.Events[i].Kind, want)
		}
	}

	version := decodePayload(t, page.Events[2])
	if version.Version != 2 || version.ActorName != "Juan" || version.FileSHA256 == "" || version.VersionID == 0 {
		t.Errorf("payload de versión = %+v", version)
	}
	if locked := decodePayload(t, page.Events[1]); locked.Lock == nil || locked.Lock.Username != "juan" {
		t.Errorf("payload de bloqueo = %+v", locked)
	}
	if moved := decodePayload(t, page.Events[4]); moved.Path != "c.blend" || moved.OldPath != "a.blend" {
		t.Errorf("payload de movimiento = %+v", moved)
	}

	// Filtrar por proyecto.
	all, err := e.svc.EventsSince(e.ctx, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Events) != len(wantKinds)+1 {
		t.Fatalf("todos los eventos = %d", len(all.Events))
	}

	// El cursor avanza hasta el final aunque el último evento sea de otro proyecto.
	if _, err := e.svc.CreateAsset(e.ctx, e.juan, CreateAssetInput{ProjectID: other.ID, Path: "d.blend", Manifest: e.upload(randomBytes(t, 1024)), Comment: "En otro"}); err != nil {
		t.Fatal(err)
	}
	filtered, err := e.svc.EventsSince(e.ctx, []int64{e.project.ID}, page.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Events) != 0 || filtered.Seq <= page.Seq {
		t.Fatalf("cursor no avanzó: %+v", filtered)
	}
}

func TestEventsPagination(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("a.blend")
	total := maxEventsPerPage + 50
	err := e.db.WriteTx(e.ctx, func(tx *sql.Tx) error {
		for range total {
			if _, err := tx.Exec(`INSERT INTO events(project_id, kind, asset_id, payload_json, created_at) VALUES (?, 'locked', ?, '{}', 1)`, e.project.ID, a.Asset.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	seen, since := 0, int64(0)
	for {
		page, err := e.svc.EventsSince(e.ctx, nil, since)
		if err != nil {
			t.Fatalf("EventsSince: %v", err)
		}
		for _, ev := range page.Events {
			if ev.Seq <= since {
				t.Fatalf("evento repetido o desordenado: %d tras %d", ev.Seq, since)
			}
			since = ev.Seq
		}
		seen += len(page.Events)
		if len(page.Events) == 0 {
			break
		}
		since = page.Seq
	}
	if seen != total+1 {
		t.Fatalf("vistos %d, quiero %d", seen, total+1)
	}
}

func TestEventsPrunedAreReported(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	if err := e.db.WriteTx(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE meta SET value = '10' WHERE key = ?`, db.MetaEventsFloor)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.EventsSince(e.ctx, nil, 9); !errors.Is(err, ErrEventsPruned) {
		t.Fatalf("EventsSince = %v, quiero ErrEventsPruned", err)
	}
	if _, err := e.svc.EventsSince(e.ctx, nil, 10); err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
}

// TestSnapshotAndSeqAreConsistent comprueba, con escrituras en paralelo, que seq y
// los archivos de un snapshot corresponden al mismo instante. Si no, un agente
// perdería o repetiría cambios.
func TestSnapshotAndSeqAreConsistent(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("a.blend")
	id := a.Asset.ID

	var stop atomic.Bool
	var writer sync.WaitGroup
	writer.Go(func() {
		base := int64(1)
		for !stop.Load() {
			if _, err := e.commit(e.juan, id, base); err != nil {
				t.Errorf("commit: %v", err)
				return
			}
			base++
		}
	})

	for range 50 {
		snap, err := e.svc.GetSnapshot(e.ctx, e.project.ID)
		if err != nil {
			t.Fatalf("GetSnapshot: %v", err)
		}
		// Cuenta las versiones hasta seq en una consulta aparte: los eventos con
		// seq <= snap.Seq no cambian una vez confirmados.
		var count int64
		err = e.db.ReadTx(e.ctx, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT COUNT(*) FROM events WHERE seq <= ? AND kind IN (?, ?) AND asset_id = ?`,
				snap.Seq, EventAssetCreated, EventVersion, id).Scan(&count)
		})
		if err != nil {
			t.Fatal(err)
		}
		if head := snap.Assets[0].HeadVersion; head != count {
			t.Fatalf("snapshot incoherente: head %d, %d versiones hasta seq %d", head, count, snap.Seq)
		}
	}
	stop.Store(true)
	writer.Wait()
}

func TestCommitNotifiesHubAfterSaving(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	a := e.newAsset("a.blend")
	wait := e.svc.Hub().Wait()
	if _, err := e.commit(e.juan, a.Asset.ID, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wait:
	case <-time.After(time.Second):
		t.Fatal("debería avisar al confirmar")
	}
}
