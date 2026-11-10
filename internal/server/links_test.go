package server

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"3d-vault/internal/vault"
)

func (e *env) addFile(projectID int64, path string, data []byte) vault.CommitResult {
	e.t.Helper()
	m := e.uploadChunks(e.juanToken, data)
	resp := e.do(apiCall{method: http.MethodPost, path: "/api/assets", token: e.juanToken, clientID: "pc-1", body: map[string]any{
		"project_id": projectID, "path": path, "manifest": m, "comment": "Primera versión",
	}})
	wantStatus(e.t, resp, http.StatusCreated)
	return decode[vault.CommitResult](e.t, resp)
}

// waitLinks espera a que termine el análisis en segundo plano de un .blend.
func (e *env) waitLinks(assetID int64) vault.AssetLinks {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		links, err := e.svc.AssetLinks(e.ctx, assetID)
		if err != nil {
			e.t.Fatal(err)
		}
		if !links.Pending {
			return links
		}
		if time.Now().After(deadline) {
			e.t.Fatal("el análisis de enlaces no terminó")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func blendFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "blendrefs", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestWebLinks(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	project := e.newProject()
	scene := e.addFile(project.ID, "escenas/escena_01.blend", blendFixture(t, "escena_01.blend"))
	e.waitLinks(scene.Asset.ID)

	b := e.newBrowser()
	b.login("ana")
	sceneURL := "/assets/" + strconv.FormatInt(scene.Asset.ID, 10)
	resp, body := b.get(sceneURL)
	wantPage(t, resp, body, http.StatusOK, "Archivos enlazados", "3 archivos enlazados no se verán", "Falta subirlo", "Fuera de 3D-Vault", "maestros/camion_maestro.blend")

	base := "/projects/" + strconv.FormatInt(project.ID, 10)
	resp, body = b.get(base)
	wantPage(t, resp, body, http.StatusOK, "3 enlaces rotos", "Con enlaces rotos", "Agregar aquí", "webkitdirectory")

	master := e.addFile(project.ID, "maestros/camion_maestro.blend", blendFixture(t, "camion_maestro.blend"))
	e.addFile(project.ID, "texturas/madera.png", randomBytes(t, 200))
	e.waitLinks(master.Asset.ID)

	resp, body = b.get(sceneURL)
	wantPage(t, resp, body, http.StatusOK, "1 archivo enlazado no se verá", "En 3D-Vault · v1", "with-links.zip")
	resp, body = b.get("/assets/" + strconv.FormatInt(master.Asset.ID, 10))
	wantPage(t, resp, body, http.StatusOK, "Lo usan", "escenas/escena_01.blend")
	resp, body = b.get(base)
	wantPage(t, resp, body, http.StatusOK, "1 enlace roto", "Lo usan 1")
	resp, body = b.get(base + "?estado=enlaces-rotos")
	wantPage(t, resp, body, http.StatusOK, "1 enlace roto")
	if strings.Contains(body, `href="/assets/`+strconv.FormatInt(master.Asset.ID, 10)+`"`) {
		t.Error("la pestaña de enlaces rotos muestra el maestro, que no tiene ninguno")
	}

	resp, body = b.get(sceneURL + "/with-links.zip")
	wantPage(t, resp, body, http.StatusOK)
	zr, err := zip.NewReader(strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("no es un zip: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	want := []string{"Mina Norte/escenas/escena_01.blend", "Mina Norte/maestros/camion_maestro.blend", "Mina Norte/texturas/madera.png"}
	if !slices.Equal(names, want) {
		t.Errorf("zip = %v, want %v", names, want)
	}

	resp = e.do(apiCall{method: http.MethodGet, path: "/api/assets/" + strconv.FormatInt(master.Asset.ID, 10) + "/links", token: e.anaToken})
	wantStatus(t, resp, http.StatusOK)
	links := decode[vault.AssetLinks](t, resp)
	if len(links.UsedBy) != 1 || links.UsedBy[0].Asset.ID != scene.Asset.ID || len(links.Uses) != 1 || links.Uses[0].Status != vault.LinkOK {
		t.Errorf("links del maestro = %+v", links)
	}
}

func TestWebGuide(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.newBrowser()
	b.login("ana")
	resp, body := b.get("/guia")
	wantPage(t, resp, body, http.StatusOK, "Qué es un archivo maestro", "Enlazar desde 3D-Vault…", `href="/guia"`, `aria-selected="true">Archivos enlazados`)
	resp, body = b.get("/guia?tema=carpetas")
	wantPage(t, resp, body, http.StatusOK, "Subir una carpeta entera", "Qué hacer en cada caso", `aria-selected="true">Carpetas`)
	resp, body = b.get("/guia?tema=problemas")
	wantPage(t, resp, body, http.StatusOK, "Texturas rosas")
	resp, body = b.get("/guia?tema=otro")
	wantPage(t, resp, body, http.StatusOK, "Qué es un archivo maestro")
}

func TestWebFolders(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	project := e.newProject()
	e.newAsset(project.ID, "ESCENAS/a.blend")
	resp := e.do(apiCall{method: http.MethodPost, path: "/api/projects/" + strconv.FormatInt(project.ID, 10) + "/folders", token: e.juanToken, body: map[string]string{"path": "ESCENAS/Nuevas"}})
	wantStatus(t, resp, http.StatusCreated)
	resp = e.do(apiCall{method: http.MethodPost, path: "/api/projects/" + strconv.FormatInt(project.ID, 10) + "/folders", token: e.juanToken, body: map[string]string{"path": "escenas/nuevas"}})
	wantStatus(t, resp, http.StatusOK)

	b := e.newBrowser()
	b.login("ana")
	base := "/projects/" + strconv.FormatInt(project.ID, 10)
	resp2, body := b.get(base)
	wantPage(t, resp2, body, http.StatusOK, `data-delete-folder="ESCENAS/Nuevas/"`, `data-rename-folder="ESCENAS/"`, `data-page-new-folder="ESCENAS/"`, "Vacía", `class="tree-twisty"`, "/papelera")

	resp = e.do(apiCall{method: http.MethodDelete, path: "/api" + base + "/folders?path=ESCENAS", token: e.juanToken, clientID: "pc-1"})
	wantStatus(t, resp, http.StatusConflict)
	resp = e.do(apiCall{method: http.MethodDelete, path: "/api" + base + "/folders?path=ESCENAS%2FNuevas", token: e.juanToken, clientID: "pc-1"})
	wantStatus(t, resp, http.StatusOK)

	// Renombrar mueve los archivos; info avisa antes.
	resp = e.do(apiCall{method: http.MethodGet, path: "/api" + base + "/folders/info?path=ESCENAS", token: e.juanToken, clientID: "pc-1"})
	wantStatus(t, resp, http.StatusOK)
	if info := decode[vault.FolderInfo](t, resp); info.Files != 1 {
		t.Errorf("info = %+v", info)
	}
	resp = e.do(apiCall{method: http.MethodPost, path: "/api" + base + "/folders/rename", token: e.juanToken, clientID: "pc-1", body: map[string]string{"from": "ESCENAS", "to": "Tomas"}})
	wantStatus(t, resp, http.StatusOK)

	// Borrar con archivos manda a la papelera, y desde ahí se recupera.
	resp = e.do(apiCall{method: http.MethodDelete, path: "/api" + base + "/folders?path=Tomas&files=1", token: e.juanToken, clientID: "pc-1"})
	wantStatus(t, resp, http.StatusOK)
	resp2, body = b.get(base + "/papelera")
	wantPage(t, resp2, body, http.StatusOK, "Tomas/a.blend", "Recuperar")
	snap, _ := e.svc.ListDeleted(e.ctx, project.ID)
	resp2, body = b.post(base+"/papelera/"+strconv.FormatInt(snap[0].ID, 10)+"/recuperar", nil, nil)
	if resp2.StatusCode != http.StatusSeeOther {
		t.Fatalf("recuperar = %d: %s", resp2.StatusCode, body)
	}
	resp2, body = b.get(resp2.Header.Get("Location"))
	wantPage(t, resp2, body, http.StatusOK, "Recuperado", "La papelera está vacía")
}

func TestWebDeleteProject(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	project := e.newProject()
	e.newAsset(project.ID, "ESCENAS/a.blend")
	b := e.newBrowser()
	b.login("ana")
	base := "/projects/" + strconv.FormatInt(project.ID, 10)

	resp, body := b.get(base)
	wantPage(t, resp, body, http.StatusOK, `popovertarget="borrar-proyecto"`, base+"/borrar")
	resp, body = b.post(base+"/borrar", nil, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("borrar = %d: %s", resp.StatusCode, body)
	}
	resp, body = b.get("/")
	wantPage(t, resp, body, http.StatusOK, "Proyectos borrados", base+"/recuperar", "Ana")
	if strings.Contains(body, `href="`+base+`"`) {
		t.Error("el proyecto borrado sigue en el menú o en la lista")
	}
	resp, body = b.get(base)
	wantPage(t, resp, body, http.StatusOK, "Este proyecto está borrado")

	resp, body = b.post(base+"/recuperar", nil, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("recuperar = %d: %s", resp.StatusCode, body)
	}
	resp, body = b.get("/")
	wantPage(t, resp, body, http.StatusOK, `href="`+base+`"`)
	if strings.Contains(body, "Proyectos borrados") {
		t.Error("sigue en Proyectos borrados")
	}
}

func TestRenderSend(t *testing.T) {
	t.Parallel()
	type received struct {
		key, scene, name string
		files            []string
	}
	got := make(chan received, 1)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			http.Error(w, `{"error":"zip malo"}`, http.StatusBadRequest)
			return
		}
		rec := received{key: r.Header.Get("X-Puente-Clave"), scene: r.URL.Query().Get("escena"), name: r.URL.Query().Get("nombre")}
		for _, f := range zr.File {
			rec.files = append(rec.files, f.Name)
		}
		got <- rec
		_, _ = w.Write([]byte(`{"token":"abc123"}`))
	}))
	t.Cleanup(fake.Close)

	e := newEnv(t, func(c *Config) { c.Render = RenderConfig{URL: fake.URL, Key: "secreto"} })
	project := e.newProject()
	scene := e.addFile(project.ID, "escenas/escena_01.blend", blendFixture(t, "escena_01.blend"))
	e.waitLinks(scene.Asset.ID)
	path := "/api/assets/" + strconv.FormatInt(scene.Asset.ID, 10) + "/render"

	// Sin el maestro no se envía nada.
	resp := e.do(apiCall{method: http.MethodPost, path: path, token: e.juanToken, clientID: "pc-1"})
	wantStatus(t, resp, http.StatusUnprocessableEntity)

	master := e.addFile(project.ID, "maestros/camion_maestro.blend", blendFixture(t, "camion_maestro.blend"))
	e.waitLinks(master.Asset.ID)
	resp = e.do(apiCall{method: http.MethodPost, path: path, token: e.juanToken, clientID: "pc-1"})
	wantStatus(t, resp, http.StatusAccepted)
	id := decode[map[string]string](t, resp)["id"]

	rec := <-got
	if rec.key != "secreto" || rec.scene != "Mina Norte/escenas/escena_01.blend" {
		t.Errorf("recibido = %+v", rec)
	}
	slices.Sort(rec.files)
	if !slices.Equal(rec.files, []string{"Mina Norte/escenas/escena_01.blend", "Mina Norte/maestros/camion_maestro.blend"}) {
		t.Errorf("zip = %v", rec.files)
	}

	var status map[string]any
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		resp = e.do(apiCall{method: http.MethodGet, path: "/api/render/" + id, token: e.juanToken})
		wantStatus(t, resp, http.StatusOK)
		if status = decode[map[string]any](t, resp); status["state"] != "enviando" {
			break
		}
	}
	if status["state"] != "listo" || status["url"] != fake.URL+"/#renderizar/abc123" {
		t.Errorf("estado = %v", status)
	}
	// Otra persona no ve el envío.
	resp = e.do(apiCall{method: http.MethodGet, path: "/api/render/" + id, token: e.anaToken})
	wantStatus(t, resp, http.StatusNotFound)

	b := e.newBrowser()
	b.login("ana")
	r2, body := b.get("/assets/" + strconv.FormatInt(scene.Asset.ID, 10))
	wantPage(t, r2, body, http.StatusOK, "data-render-send", "render-send.js")
}
