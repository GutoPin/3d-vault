package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Cada PC avisa la versión de su programa; Mi cuenta y Versiones dicen quién
// está desactualizado.
func TestClientVersionsShowOutdated(t *testing.T) {
	e := newEnv(t)
	chrome := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"
	edge := chrome + " Edg/131.0"

	// Mi carpeta al día en Edge, Mi carpeta vieja (sin versión) en Chrome, un
	// complemento viejo y otro al día.
	calls := []apiCall{
		{method: http.MethodGet, path: "/api/me", token: e.anaToken, clientID: "pc-edge", headers: map[string]string{"User-Agent": edge, "X-App-Version": appVersion, "X-Client-Name": "Taller"}},
		{method: http.MethodGet, path: "/api/me", token: e.anaToken, clientID: "pc-chrome", headers: map[string]string{"User-Agent": chrome, "X-Client-Name": "Casa"}},
		{method: http.MethodGet, path: "/api/me", token: e.anaToken, clientID: "pc-edge", headers: map[string]string{"User-Agent": "3D-Vault Blender", "X-Client-Name": "Taller"}},
		{method: http.MethodGet, path: "/api/me", token: e.anaToken, clientID: "pc-nueva", headers: map[string]string{"User-Agent": "3D-Vault Blender/" + blenderAddonVersion + " (Blender 4.2.3)", "X-Client-Name": "Nueva"}},
		// Una petición sin nombre de PC no borra el que ya estaba.
		{method: http.MethodGet, path: "/api/me", token: e.anaToken, clientID: "pc-edge", headers: map[string]string{"User-Agent": edge, "X-App-Version": appVersion}},
		// La herramienta de línea de comandos no cuenta.
		{method: http.MethodGet, path: "/api/me", token: e.anaToken, clientID: "cli", headers: map[string]string{"User-Agent": "Go-http-client/1.1"}},
	}
	for _, c := range calls {
		wantStatus(t, e.do(c), http.StatusOK)
	}

	resp := e.do(apiCall{method: http.MethodGet, path: "/api/version", token: e.anaToken, clientID: "pc-edge"})
	wantStatus(t, resp, http.StatusOK)
	var v map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil || v["web"] != appVersion || v["blender"] != blenderAddonVersion || v["blender"] == "" {
		t.Fatalf("versión: %v %v", v, err)
	}

	b := e.newBrowser()
	b.login("ana")
	r, body := b.get("/account")
	wantPage(t, r, body, http.StatusOK, "Mis programas", "2 programas desactualizados",
		"Edge en Windows", "Chrome en Windows", "Blender 4.2.3", "1.2.0 o anterior", "antigua", "Al día", "Desactualizado",
		`href="/blender/3dvault.zip"`)
	if strings.Contains(body, "Go-http-client") || strings.Contains(body, "pc-edge") || strings.Count(body, ">Desactualizado<") != 2 || strings.Count(body, ">Al día<") != 2 {
		t.Fatalf("estados mal contados:\n%s", body)
	}

	// Solo el admin ve la página de todos.
	r, body = b.get("/admin/versiones")
	wantPage(t, r, body, http.StatusForbidden)
	admin := e.newBrowser()
	admin.login("jefa")
	r, body = admin.get("/admin/versiones")
	wantPage(t, r, body, http.StatusOK, "Versiones", "2 programas desactualizados", "Ana", "Taller", "Casa", "Nueva")
}
