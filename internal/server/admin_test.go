package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"3d-vault/internal/maintenance"
	"3d-vault/internal/vault"
)

func TestWebLocksPage(t *testing.T) {
	t.Parallel()

	// Los bloqueos se hacen ahora y la página los mira tres días después.
	later := time.Now().Add(73 * time.Hour)
	e := newEnv(t, func(c *Config) { c.Now = func() time.Time { return later } })
	project := e.newProject()
	a := e.newAsset(project.ID, "modelos/camion.blend")
	e.newAsset(project.ID, "libre.blend")
	lockPath := fmt.Sprintf("/api/assets/%d/lock", a.Asset.ID)
	wantStatus(t, e.do(apiCall{method: http.MethodPost, path: lockPath, token: e.juanToken, clientID: "pc-1"}), http.StatusOK)

	plain := e.newBrowser()
	plain.login("ana")
	resp, body := plain.get("/admin/locks")
	wantPage(t, resp, body, http.StatusForbidden, "Sin permiso")
	if strings.Contains(body, `href="/admin/locks"`) {
		t.Error("un modelador no debe ver el enlace a Bloqueos")
	}
	unlockPath := fmt.Sprintf("/admin/locks/%d/unlock", a.Asset.ID)
	resp, _ = plain.post(unlockPath, nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forzar sin ser admin = %d", resp.StatusCode)
	}

	admin := e.newBrowser()
	admin.login("jefa")
	resp, body = admin.get("/admin/locks")
	wantPage(t, resp, body, http.StatusOK, `href="/admin/locks" aria-current="page"`, "modelos/camion.blend", "Mina Norte", "Juan", "Hace 3 días", "Forzar desbloqueo", `action="`+unlockPath+`"`)
	if strings.Contains(body, "libre.blend") {
		t.Error("solo deben aparecer los archivos bloqueados")
	}

	resp, body = admin.post(unlockPath, nil, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/locks" {
		t.Fatalf("forzar = %d %s: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	asset, err := e.svc.GetAsset(e.ctx, a.Asset.ID)
	if err != nil || asset.Lock != nil {
		t.Fatalf("debería quedar libre: %+v, %v", asset.Lock, err)
	}
	resp, body = admin.get("/admin/locks")
	wantPage(t, resp, body, http.StatusOK, "No hay ningún archivo bloqueado")

	// Queda registrado como forzado, igual que desde la ficha.
	events := e.do(apiCall{method: http.MethodGet, path: fmt.Sprintf("/api/projects/%d/events?since=0", project.ID), token: e.juanToken})
	wantStatus(t, events, http.StatusOK)
	page := decode[vault.EventPage](t, events)
	last := page.Events[len(page.Events)-1]
	if last.Kind != vault.EventUnlocked || !strings.Contains(string(last.Payload), `"forced":true`) {
		t.Fatalf("último evento = %s %s", last.Kind, last.Payload)
	}
}

func TestDiskAlertForAdmins(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		now   = time.Now()
		low   = true
		calls int
	)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	e := newEnv(t, func(c *Config) {
		c.Now = clock
		c.LowDisk = func(free, total int64) bool {
			mu.Lock()
			defer mu.Unlock()
			calls++
			return low
		}
	})

	plain := e.newBrowser()
	plain.login("juan")
	resp, body := plain.get("/")
	wantPage(t, resp, body, http.StatusOK)
	if strings.Contains(body, "Queda poco espacio") {
		t.Error("el aviso es solo para admins")
	}

	admin := e.newBrowser()
	admin.login("jefa")
	for _, path := range []string{"/", "/admin/storage"} {
		resp, body = admin.get(path)
		wantPage(t, resp, body, http.StatusOK, `class="disk-alert"`, "Queda poco espacio en el disco del servidor", `<a href="/admin/storage">Ver Espacio</a>`)
	}

	// Se consulta como mucho una vez por minuto.
	mu.Lock()
	low = false
	mu.Unlock()
	resp, body = admin.get("/")
	wantPage(t, resp, body, http.StatusOK, "Queda poco espacio")
	mu.Lock()
	now = now.Add(diskCheckEvery + time.Second)
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Errorf("consultas al disco = %d, quiero 1", got)
	}
	resp, body = admin.get("/")
	wantPage(t, resp, body, http.StatusOK)
	if strings.Contains(body, "Queda poco espacio") {
		t.Error("el aviso debe desaparecer al pasar el minuto")
	}
}

func TestDefaultLowDisk(t *testing.T) {
	t.Parallel()

	const gb = 1 << 30
	for _, tt := range []struct {
		free, total int64
		want        bool
	}{
		{100 * gb, 200 * gb, false},
		{25 * gb, 200 * gb, true},  // 12,5 %
		{30 * gb, 200 * gb, false}, // 15 %
		{19 * gb, 50 * gb, true},   // menos de 20 GB
		{21 * gb, 50 * gb, false},
	} {
		if got := defaultLowDisk(tt.free, tt.total); got != tt.want {
			t.Errorf("defaultLowDisk(%d GB, %d GB) = %v", tt.free/gb, tt.total/gb, got)
		}
	}
}

func TestStorageShowsBackups(t *testing.T) {
	t.Parallel()

	statusFile := filepath.Join(t.TempDir(), maintenance.StatusFile)
	e := newEnv(t, func(c *Config) {
		c.Backups = BackupConfig{Dir: "/backup", Hour: 4, StatusFile: statusFile}
	})
	admin := e.newBrowser()
	admin.login("jefa")

	resp, body := admin.get("/admin/storage")
	wantPage(t, resp, body, http.StatusOK, "Cada día a las 4:00 en <code>/backup</code>", "Todavía no se ha hecho ninguna copia")

	writeStatus := func(status maintenance.BackupStatus) {
		raw, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(statusFile, raw, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	ok := time.Now().Add(-time.Hour)
	writeStatus(maintenance.BackupStatus{Dir: "/backup", LastOK: ok, Duration: 125 * time.Second, Copied: 12})
	resp, body = admin.get("/admin/storage")
	wantPage(t, resp, body, http.StatusOK, "Última copia correcta: "+formatWhen(ok, time.Now()), "Tardó 2 min 5 s y copió 12 trozos nuevos")
	if strings.Contains(body, "La última copia falló") {
		t.Error("no hay error que mostrar")
	}

	writeStatus(maintenance.BackupStatus{Dir: "/backup", LastOK: ok, Copied: 12, LastError: "no queda espacio", LastErrorAt: time.Now()})
	resp, body = admin.get("/admin/storage")
	wantPage(t, resp, body, http.StatusOK, "La última copia falló", "no queda espacio", "Última copia correcta")

	off := newEnv(t)
	admin = off.newBrowser()
	admin.login("jefa")
	resp, body = admin.get("/admin/storage")
	wantPage(t, resp, body, http.StatusOK, "Las copias automáticas están desactivadas", "BACKUP_DIR")
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	for d, want := range map[time.Duration]string{
		8 * time.Second:                 "8 s",
		125 * time.Second:               "2 min 5 s",
		time.Hour + 3*time.Minute + 400: "1 h 3 min",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, quiero %q", d, got, want)
		}
	}
}

// commit sube una versión nueva por HTTP.
func (e *env) commit(assetID, base int64, data []byte) vault.CommitResult {
	e.t.Helper()
	m := e.uploadChunks(e.juanToken, data)
	resp := e.do(apiCall{method: http.MethodPost, path: fmt.Sprintf("/api/assets/%d/versions", assetID), token: e.juanToken, clientID: "pc-1", body: map[string]any{
		"base_version": base, "manifest": m, "size_bytes": m.Size(), "comment": "Otra versión",
	}})
	wantStatus(e.t, resp, http.StatusCreated)
	return decode[vault.CommitResult](e.t, resp)
}

func TestProjectZip(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	project := e.newProject()
	files := map[string][]byte{
		"a.blend":          randomBytes(t, 3<<20),
		"modelos/b.blend":  randomBytes(t, 20<<10),
		"modelos/c/d.png":  randomBytes(t, 5<<10),
		"texturas/e.jpg":   randomBytes(t, 1<<10),
		"modelos-no/f.obj": randomBytes(t, 1<<10),
	}
	ids := map[string]int64{}
	for path, data := range files {
		ids[path] = e.newAssetWith(project.ID, path, data).ID
	}
	// La última versión, no la primera.
	files["a.blend"] = randomBytes(t, 2<<20)
	e.commit(ids["a.blend"], 1, files["a.blend"])
	// Un archivo borrado no se incluye.
	gone := e.newAssetWith(project.ID, "modelos/borrado.blend", randomBytes(t, 1<<10))
	wantStatus(t, e.do(apiCall{method: http.MethodDelete, path: fmt.Sprintf("/api/assets/%d?base_version=1", gone.ID), token: e.juanToken, clientID: "pc-1"}), http.StatusNoContent)

	b := e.newBrowser()
	b.login("ana")
	base := "/projects/" + strconv.FormatInt(project.ID, 10)
	resp, body := b.get(base)
	wantPage(t, resp, body, http.StatusOK, "Descargar todo (.zip)", `href="`+base+`/download.zip"`, `href="`+base+`/download.zip?prefix=modelos%2fc%2f"`)

	read := func(query string) (*http.Response, map[string][]byte) {
		t.Helper()
		resp, body := b.get(base + "/download.zip" + query)
		wantPage(t, resp, body, http.StatusOK)
		zr, err := zip.NewReader(strings.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatalf("no es un zip: %v", err)
		}
		got := map[string][]byte{}
		for _, f := range zr.File {
			if f.Method != zip.Store {
				t.Errorf("%s: método %d, quiero Store", f.Name, f.Method)
			}
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(rc)
			_ = rc.Close()
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			got[f.Name] = data
		}
		return resp, got
	}
	check := func(got map[string][]byte, want ...string) {
		t.Helper()
		names := slices.Sorted(func(yield func(string) bool) {
			for name := range got {
				if !yield(name) {
					return
				}
			}
		})
		slices.Sort(want)
		if !slices.Equal(names, want) {
			t.Fatalf("archivos = %v, quiero %v", names, want)
		}
		for _, name := range want {
			if !bytes.Equal(got[name], files[name]) {
				t.Errorf("%s no coincide con la última versión", name)
			}
		}
	}

	resp, got := read("")
	check(got, "a.blend", "modelos/b.blend", "modelos/c/d.png", "texturas/e.jpg", "modelos-no/f.obj")
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, `"Mina Norte.zip"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}

	_, got = read("?prefix=modelos/")
	check(got, "modelos/b.blend", "modelos/c/d.png")
	_, got = read("?prefix=modelos")
	check(got, "modelos/b.blend", "modelos/c/d.png")

	resp, body = b.get(base + "/download.zip?prefix=nada/")
	wantPage(t, resp, body, http.StatusNotFound, "No hay archivos")
}

func TestHistoryThumbnails(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	project := e.newProject()
	v1 := e.newAssetWith(project.ID, "camion.blend", fakeBlend(true))
	v2 := e.commit(v1.ID, 1, fakeBlend(false)).Version
	// Otra miniatura con otro contenido: lo que sigue a ENDB no lo lee nadie.
	v3 := e.commit(v1.ID, 2, append(fakeBlend(true), "otro"...)).Version

	b := e.newBrowser()
	b.login("juan")
	resp, body := b.get("/assets/" + strconv.FormatInt(v1.ID, 10))
	wantPage(t, resp, body, http.StatusOK,
		`popovertarget="vista-v1"`, fmt.Sprintf(`src="/versions/%d/thumb"`, v1.HeadVersionID),
		`popovertarget="vista-v3"`, fmt.Sprintf(`src="/versions/%d/thumb"`, v3.ID))
	if strings.Contains(body, "vista-v2") || strings.Contains(body, fmt.Sprintf("/versions/%d/thumb", v2.ID)) {
		t.Error("una versión sin miniatura no debe mostrar ninguna")
	}

	pic := e.newAssetWith(project.ID, "texturas/madera.png", randomBytes(t, 1<<10))
	resp, body = b.get("/assets/" + strconv.FormatInt(pic.ID, 10))
	wantPage(t, resp, body, http.StatusOK, `popovertarget="vista-v1"`, fmt.Sprintf(`<img src="/versions/%d/image" alt="" width="40"`, pic.HeadVersionID))

	plain := e.newAsset(project.ID, "notas.txt").Asset
	resp, body = b.get("/assets/" + strconv.FormatInt(plain.ID, 10))
	wantPage(t, resp, body, http.StatusOK)
	if strings.Contains(body, "popovertarget") {
		t.Error("un archivo sin vista previa no lleva miniatura")
	}
}
