package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// La puesta al día: el aviso sale en todas las páginas del modelador hasta que
// Mi carpeta y Blender están al día y confirmó que guardar no es subir.
func TestSetupGuideUntilDone(t *testing.T) {
	old := setupUntil
	setupUntil = time.Now().Add(7 * 24 * time.Hour)
	t.Cleanup(func() { setupUntil = old })

	e := newEnv(t)
	b := e.newBrowser()
	b.login("ana")
	r, body := b.get("/")
	wantPage(t, r, body, http.StatusOK, "Pon tu PC al día con 3D-Vault: te faltan 3 pasos", `href="/puesta-al-dia"`)
	r, body = b.get("/puesta-al-dia")
	wantPage(t, r, body, http.StatusOK, "Ponerme al día", "0 de 3 listos", "Guardar no es subir", "/blender/3dvault.zip")
	if strings.Contains(body, "setup-banner") {
		t.Fatal("la guía no debe mostrar su propio aviso")
	}

	// Mi carpeta al día y un complemento viejo.
	chrome := "Mozilla/5.0 (Windows NT 10.0) Chrome/131.0 Safari/537.36"
	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: "/api/me", token: e.anaToken, clientID: "pc", headers: map[string]string{"User-Agent": chrome, "X-App-Version": appVersion, "X-Client-Name": "Taller"}}), http.StatusOK)
	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: "/api/me", token: e.anaToken, clientID: "pc", headers: map[string]string{"User-Agent": "3D-Vault Blender", "X-Client-Name": "Taller"}}), http.StatusOK)
	// Abrir Mi carpeta no basta: tiene que sincronizar (pedir la lista de un proyecto).
	r, body = b.get("/")
	wantPage(t, r, body, http.StatusOK, "te faltan 3 pasos")
	project := e.newProject()
	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: fmt.Sprintf("/api/projects/%d/snapshot", project.ID), token: e.anaToken, clientID: "pc", headers: map[string]string{"User-Agent": chrome, "X-App-Version": appVersion}}), http.StatusOK)
	r, body = b.get("/")
	wantPage(t, r, body, http.StatusOK, "te faltan 2 pasos")

	// El admin ve el avance de cada uno.
	admin := e.newBrowser()
	admin.login("jefa")
	r, body = admin.get("/admin/versiones")
	wantPage(t, r, body, http.StatusOK, "Puesta al día", "Versión vieja", "2 pasos pendientes")
	if strings.Contains(body, "setup-banner") {
		t.Fatal("los admins no deben ver el aviso")
	}

	// Complemento nuevo y «Entendido»: el aviso desaparece.
	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: "/api/me", token: e.anaToken, clientID: "pc", headers: map[string]string{"User-Agent": "3D-Vault Blender/" + blenderAddonVersion + " (Blender 4.2.3)"}}), http.StatusOK)
	wantStatus(t, e.do(apiCall{method: http.MethodPost, path: "/api/setup/entendido", token: e.anaToken}), http.StatusNoContent)
	resp := e.do(apiCall{method: http.MethodGet, path: "/api/setup", token: e.anaToken})
	wantStatus(t, resp, http.StatusOK)
	r, body = b.get("/")
	wantPage(t, r, body, http.StatusOK)
	if strings.Contains(body, "setup-banner") {
		t.Fatalf("el aviso sigue después de terminar:\n%s", body)
	}
	r, body = admin.get("/admin/versiones")
	wantPage(t, r, body, http.StatusOK, "1 de ")

	// Pasada la fecha, el aviso ya no sale aunque falte algo.
	setupUntil = time.Now().Add(-time.Hour)
	b2 := e.newBrowser()
	b2.login("juan")
	r, body = b2.get("/")
	wantPage(t, r, body, http.StatusOK)
	if strings.Contains(body, "setup-banner") {
		t.Fatal("el aviso sale después de la fecha")
	}
}
