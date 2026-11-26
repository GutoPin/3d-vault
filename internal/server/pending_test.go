package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestPendingChangesShowOnProjectPage(t *testing.T) {
	e := newEnv(t)
	project := e.newProject()
	e.newAsset(project.ID, "escenas/camion.blend")
	e.newAsset(project.ID, "escenas/viejo.blend")
	path := fmt.Sprintf("/api/projects/%d/pending", project.ID)

	// Ana cuenta lo que tiene sin subir en su PC.
	resp := e.do(apiCall{method: http.MethodPut, path: path, token: e.anaToken, clientID: "pc-ana", body: map[string]any{
		"items": []map[string]string{
			{"path": "escenas/camion.blend", "status": "changed"},
			{"path": "texturas/roca.png", "status": "new"},
			{"path": "escenas/viejo.blend", "status": "missing"},
			{"path": "escenas/ya-subido.blend", "status": "ok"}, // no es un cambio: se ignora
			// Un «nuevo» que ya está en el servidor es un aviso viejo: no se muestra.
			{"path": "Escenas/Camion.blend", "status": "new"},
		},
	}})
	wantStatus(t, resp, http.StatusNoContent)

	b := e.newBrowser()
	b.login("juan")
	page := fmt.Sprintf("/projects/%d", project.ID)
	r, body := b.get(page)
	wantPage(t, r, body, http.StatusOK,
		"Cambios en las PCs, aún sin subir",
		"<b>Ana</b>", "en pc-ana",
		"texturas/roca.png", "1 nuevo", "1 modificado", "1 borrado",
		`pill git link-mark mod`, "Modificado por Ana",
		`pill git link-mark del`, "Borrado por Ana",
	)
	if strings.Contains(body, "ya-subido") || strings.Count(body, "texturas/roca.png") != 1 {
		t.Fatalf("muestra avisos que no son cambios:\n%s", body)
	}

	// Una lista vacía quita el aviso.
	resp = e.do(apiCall{method: http.MethodPut, path: path, token: e.anaToken, clientID: "pc-ana", body: map[string]any{"items": []any{}}})
	wantStatus(t, resp, http.StatusNoContent)
	r, body = b.get(page)
	wantPage(t, r, body, http.StatusOK)
	if strings.Contains(body, "aún sin subir") {
		t.Fatal("el aviso sigue después de vaciarlo")
	}
}

func TestPendingRejectsBadInput(t *testing.T) {
	e := newEnv(t)
	project := e.newProject()
	resp := e.do(apiCall{method: http.MethodPut, path: fmt.Sprintf("/api/projects/%d/pending", project.ID), token: e.anaToken, clientID: "pc", raw: []byte("{no")})
	wantStatus(t, resp, http.StatusUnprocessableEntity)
	resp = e.do(apiCall{method: http.MethodPut, path: "/api/projects/9999/pending", token: e.anaToken, clientID: "pc", body: map[string]any{"items": []any{}}})
	wantStatus(t, resp, http.StatusNotFound)
	resp = e.do(apiCall{method: http.MethodPut, path: fmt.Sprintf("/api/projects/%d/pending", project.ID), token: e.anaToken, body: map[string]any{"items": []any{}}})
	wantStatus(t, resp, http.StatusUnprocessableEntity) // con token hace falta X-Client-Id
}
