package server

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"3d-vault/internal/rules"
)

func TestWebChangeOwnPassword(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	b := e.newBrowser()
	b.login("juan")
	elsewhere := e.newBrowser()
	elsewhere.login("juan")

	change := func(current, password, confirm string) (*http.Response, string) {
		return b.post("/account/password", url.Values{"current": {current}, "password": {password}, "confirm": {confirm}}, nil)
	}
	resp, body := change("incorrecta", "otra-contraseña-larga", "otra-contraseña-larga")
	wantPage(t, resp, body, http.StatusForbidden, "La contraseña actual no es correcta.")
	resp, body = change(testPassword, "otra-contraseña-larga", "otra-distinta-larga")
	wantPage(t, resp, body, http.StatusUnprocessableEntity, "no coinciden")
	resp, body = change(testPassword, "corta", "corta")
	wantPage(t, resp, body, http.StatusUnprocessableEntity, "al menos 10 caracteres")
	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: "/api/me", token: e.juanToken}), http.StatusOK)

	resp, body = change(testPassword, "otra-contraseña-larga", "otra-contraseña-larga")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/account?hecho=contrasena" {
		t.Fatalf("cambiar = %d %s: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	// Sigue dentro en este navegador, y la página avisa de Blender.
	resp, body = b.get("/account?hecho=contrasena")
	wantPage(t, resp, body, http.StatusOK, "Contraseña cambiada", "Mi carpeta")
	// Las demás sesiones y los tokens dejan de valer.
	if resp, _ := elsewhere.get("/account"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("la otra sesión debería terminar: %d", resp.StatusCode)
	}
	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: "/api/me", token: e.juanToken}), http.StatusUnauthorized)
	if _, ok, _ := e.svc.Authenticate(e.ctx, "juan", "otra-contraseña-larga"); !ok {
		t.Fatal("la contraseña nueva debe valer")
	}
}

func TestWebChangeOwnPasswordLimitsAttempts(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	b := e.newBrowser()
	b.login("ana")
	form := url.Values{"current": {"incorrecta"}, "password": {"otra-contraseña-larga"}, "confirm": {"otra-contraseña-larga"}}
	for range 5 {
		resp, body := b.post("/account/password", form, nil)
		wantPage(t, resp, body, http.StatusForbidden)
	}
	form.Set("current", testPassword)
	resp, body := b.post("/account/password", form, nil)
	wantPage(t, resp, body, http.StatusTooManyRequests, "Demasiados intentos")
}

func TestWebRevokeOwnTokenOnly(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	mine, plain, err := e.svc.CreateToken(e.ctx, e.juan.ID, "Blender · Taller")
	if err != nil {
		t.Fatal(err)
	}
	b := e.newBrowser()
	b.login("juan")
	resp, body := b.get("/account")
	wantPage(t, resp, body, http.StatusOK, "Blender · Taller", "Nunca", "Revocar")

	// Ni Ana ni la administradora lo revocan desde su cuenta.
	for _, username := range []string{"ana", "jefa"} {
		other := e.newBrowser()
		other.login(username)
		resp, body = other.post(fmt.Sprintf("/account/tokens/%d/revoke", mine.ID), nil, nil)
		wantPage(t, resp, body, http.StatusNotFound)
	}
	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: "/api/me", token: plain}), http.StatusOK)

	resp, _ = b.post(fmt.Sprintf("/account/tokens/%d/revoke", mine.ID), nil, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("revocar = %d", resp.StatusCode)
	}
	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: "/api/me", token: plain}), http.StatusUnauthorized)
	resp, body = b.get("/account")
	wantPage(t, resp, body, http.StatusOK)
	if strings.Contains(body, "Blender · Taller") {
		t.Fatal("un token revocado no debe aparecer")
	}
}

func TestWebMyLocks(t *testing.T) {
	t.Parallel()

	// El reloj del servidor va tres días por delante: los bloqueos de ahora son viejos.
	e := newEnv(t, func(c *Config) { c.Now = func() time.Time { return time.Now().Add(72 * time.Hour) } })
	project := e.newProject()
	mine := e.newAsset(project.ID, "modelos/camión.blend").Asset.ID
	theirs := e.newAsset(project.ID, "modelos/rueda.blend").Asset.ID
	juanPC := rules.Actor{UserID: e.juan.ID, ClientID: "pc-1", ClientName: "PC del taller"}
	if _, err := e.svc.Lock(e.ctx, juanPC, mine, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Lock(e.ctx, rules.Actor{UserID: e.ana.ID, ClientID: "pc-2", ClientName: "PC de Ana"}, theirs, false); err != nil {
		t.Fatal(err)
	}

	b := e.newBrowser()
	b.login("juan")
	resp, body := b.get("/account")
	wantPage(t, resp, body, http.StatusOK, "modelos/camión.blend", fmt.Sprintf(`href="/assets/%d"`, mine), "Mina Norte", "PC del taller", "Más de 2 días")
	if strings.Contains(body, "rueda.blend") {
		t.Fatal("solo deben aparecer los bloqueos propios")
	}

	// Se libera desde el navegador aunque el bloqueo sea de otra PC, y vuelve aquí.
	resp, _ = b.post(fmt.Sprintf("/assets/%d/unlock", mine), url.Values{"next": {"/account"}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/account" {
		t.Fatalf("liberar = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, body = b.get("/account")
	wantPage(t, resp, body, http.StatusOK, "No tienes archivos bloqueados")

	// next solo lleva a este sitio.
	if _, err := e.svc.Lock(e.ctx, juanPC, mine, false); err != nil {
		t.Fatal(err)
	}
	resp, _ = b.post(fmt.Sprintf("/assets/%d/unlock", mine), url.Values{"next": {"//malicioso.example"}}, nil)
	if resp.Header.Get("Location") != "/" {
		t.Fatalf("next ajeno = %s", resp.Header.Get("Location"))
	}
}

var moreLink = regexp.MustCompile(`href="(/activity\?[^"]*before=[^"]*)"`)

func TestWebActivity(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	project := e.newProject()
	id := e.newAsset(project.ID, "modelos/camión.blend").Asset.ID
	ana := rules.Actor{UserID: e.ana.ID, ClientID: "pc-2", ClientName: "PC de Ana"}
	for range 30 {
		if _, err := e.svc.Lock(e.ctx, ana, id, false); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.Unlock(e.ctx, ana, id, false); err != nil {
			t.Fatal(err)
		}
	}
	other, err := e.svc.CreateProject(e.ctx, rules.Actor{UserID: e.ana.ID}, "Puerto", "")
	if err != nil {
		t.Fatal(err)
	}

	b := e.newBrowser()
	b.login("juan")
	resp, body := b.get("/activity")
	wantPage(t, resp, body, http.StatusOK, "<h1>Actividad</h1>", "Ana</b> creó el proyecto", "Ana</b> liberó", "En PC de Ana", fmt.Sprintf(`href="/assets/%d"`, id))
	if strings.Contains(body, "crear_usuario") || strings.Contains(body, "crear_token") {
		t.Fatal("la actividad de cuentas no debe aparecer")
	}

	resp, body = b.get(fmt.Sprintf("/activity?project=%d", other.ID))
	wantPage(t, resp, body, http.StatusOK, "Proyecto Puerto", "creó el proyecto")
	if strings.Contains(body, "liberó") || strings.Contains(body, "Ver más") {
		t.Fatal("el filtro de proyecto debe ocultar lo demás")
	}

	resp, body = b.get(fmt.Sprintf("/activity?user=%d&project=%d", e.juan.ID, project.ID))
	wantPage(t, resp, body, http.StatusOK, "Juan</b> agregó el archivo", "Primera versión", "Juan</b> creó el proyecto")
	if strings.Contains(body, "Ana</b>") {
		t.Fatal("el filtro de persona debe ocultar lo demás")
	}

	// 62 entradas en Mina Norte: 50 en la primera página y 12 en la segunda.
	resp, body = b.get(fmt.Sprintf("/activity?project=%d", project.ID))
	wantPage(t, resp, body, http.StatusOK, "Ver más")
	if rows := strings.Count(body, "</tr>") - 1; rows != activityPageSize {
		t.Fatalf("primera página = %d filas", rows)
	}
	if strings.Contains(body, "agregó el archivo") {
		t.Fatal("lo más antiguo va en la segunda página")
	}
	link := moreLink.FindStringSubmatch(body)
	if link == nil {
		t.Fatalf("sin enlace a la página siguiente:\n%s", body)
	}
	resp, body = b.get(html.UnescapeString(link[1]))
	wantPage(t, resp, body, http.StatusOK, "agregó el archivo", "Juan</b> creó el proyecto")
	if rows := strings.Count(body, "</tr>") - 1; rows != 12 || strings.Contains(body, "Ver más") || strings.Contains(body, "Puerto</a>") {
		t.Fatalf("segunda página = %d filas:\n%s", rows, body)
	}
}

func TestWebSearch(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	project := e.newProject()
	camion := e.newAsset(project.ID, "modelos/Camión_797F.blend").Asset.ID
	e.newAsset(project.ID, "modelos/camionX797F.blend")
	e.newAsset(project.ID, "texturas/100%.png")
	gone := e.newAsset(project.ID, "modelos/viejo.blend").Asset
	if err := e.svc.Delete(e.ctx, rules.Actor{UserID: e.juan.ID}, gone.ID, gone.HeadVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Lock(e.ctx, rules.Actor{UserID: e.ana.ID, ClientID: "pc-2", ClientName: "PC de Ana"}, camion, false); err != nil {
		t.Fatal(err)
	}

	b := e.newBrowser()
	b.login("juan")
	resp, body := b.get("/")
	wantPage(t, resp, body, http.StatusOK, `action="/search"`, `href="/activity"`, `href="/account"`)

	search := func(q string) (*http.Response, string) {
		t.Helper()
		return b.get("/search?q=" + url.QueryEscape(q))
	}
	resp, body = search("CAMIÓN_7")
	wantPage(t, resp, body, http.StatusOK, "modelos/Camión_797F.blend", fmt.Sprintf(`href="/assets/%d"`, camion), "Mina Norte", "v1", "Ana", "1 archivo encontrado")
	if strings.Contains(body, "camionX797F") {
		t.Fatal("_ debe buscarse literalmente")
	}
	resp, body = search("797f")
	wantPage(t, resp, body, http.StatusOK, "Camión_797F.blend", "camionX797F.blend", "2 archivos encontrados")
	resp, body = search("%")
	wantPage(t, resp, body, http.StatusOK, "100%.png")
	if strings.Contains(body, "797F") {
		t.Fatal("% debe buscarse literalmente")
	}
	resp, body = search("viejo")
	wantPage(t, resp, body, http.StatusOK, "Ningún archivo coincide")
	resp, body = search("")
	wantPage(t, resp, body, http.StatusOK, "Busca archivos por nombre o carpeta")
}
