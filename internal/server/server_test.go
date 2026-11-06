package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"3d-vault/internal/auth"
	"3d-vault/internal/chunker"
	"3d-vault/internal/db"
	"3d-vault/internal/events"
	"3d-vault/internal/manifest"
	"3d-vault/internal/rules"
	"3d-vault/internal/store"
	"3d-vault/internal/thumbs"
	"3d-vault/internal/vault"
)

const testPassword = "contraseña-larga"

type env struct {
	t      *testing.T
	ctx    context.Context
	svc    *vault.Service
	server *httptest.Server
	juan   vault.User
	ana    vault.User
	admin  vault.User
	// Tokens de API de cada usuario.
	juanToken string
	anaToken  string
}

type envOption func(*Config)

func newEnv(t *testing.T, options ...envOption) *env {
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

	thumbCache, err := thumbs.NewCache(filepath.Join(dir, "thumbs"))
	if err != nil {
		t.Fatalf("thumbs.NewCache: %v", err)
	}

	svc := vault.New(vault.Config{
		DB: database, Store: chunks, Hub: events.NewHub(),
		MaxFileSize: 1 << 30, Argon: auth.Params{Memory: 1024, Time: 1, Threads: 1},
		Thumbs: thumbCache,
	})
	t.Cleanup(svc.WaitBackground)

	cfg := Config{
		Service:         svc,
		Sessions:        auth.NewSessions([]byte("secreto-de-prueba-suficientemente-largo"), time.Hour),
		Limiter:         auth.NewLimiter(),
		Logger:          slog.New(slog.DiscardHandler),
		InsecureCookies: true,
		Heartbeat:       50 * time.Millisecond,
	}
	for _, option := range options {
		option(&cfg)
	}
	handler, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	e := &env{t: t, ctx: ctx, svc: svc, server: server}
	e.juan = e.newUser("juan", "Juan", false)
	e.ana = e.newUser("ana", "Ana", false)
	e.admin = e.newUser("jefa", "Jefa", true)
	e.juanToken = e.newToken(e.juan.ID)
	e.anaToken = e.newToken(e.ana.ID)
	return e
}

func (e *env) newUser(username, display string, admin bool) vault.User {
	e.t.Helper()
	u, err := e.svc.CreateUser(e.ctx, 0, vault.NewUserInput{Username: username, DisplayName: display, Password: testPassword, IsAdmin: admin})
	if err != nil {
		e.t.Fatalf("CreateUser: %v", err)
	}
	return u
}

func (e *env) newToken(userID int64) string {
	e.t.Helper()
	_, plain, err := e.svc.CreateToken(e.ctx, userID, "PC")
	if err != nil {
		e.t.Fatalf("CreateToken: %v", err)
	}
	return plain
}

// apiCall es una petición a la API.
type apiCall struct {
	method   string
	path     string
	token    string
	clientID string
	body     any
	raw      []byte
}

func (e *env) do(call apiCall) *http.Response {
	e.t.Helper()
	var body io.Reader
	switch {
	case call.raw != nil:
		body = bytes.NewReader(call.raw)
	case call.body != nil:
		encoded, err := json.Marshal(call.body)
		if err != nil {
			e.t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(e.ctx, call.method, e.server.URL+call.path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if call.token != "" {
		req.Header.Set("Authorization", "Bearer "+call.token)
	}
	if call.clientID != "" {
		req.Header.Set("X-Client-Id", call.clientID)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", call.method, call.path, err)
	}
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decodificar respuesta: %v", err)
	}
	return out
}

func wantStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s = %d, quiero %d: %s", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, want, body)
	}
}

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	return data
}

// uploadChunks sube por HTTP los trozos de data.
func (e *env) uploadChunks(token string, data []byte) manifest.Manifest {
	e.t.Helper()
	m, err := manifest.Build(bytes.NewReader(data))
	if err != nil {
		e.t.Fatal(err)
	}
	c := chunker.New(bytes.NewReader(data))
	for {
		chunk, err := c.Next()
		if err != nil {
			break
		}
		resp := e.do(apiCall{method: http.MethodPut, path: "/api/chunks/" + chunk.Hash.String(), token: token, raw: append([]byte{}, chunk.Data...)})
		wantStatus(e.t, resp, http.StatusNoContent)
	}
	return m
}

func (e *env) newProject() vault.Project {
	e.t.Helper()
	resp := e.do(apiCall{method: http.MethodPost, path: "/api/projects", token: e.juanToken, body: map[string]string{"name": "Mina Norte"}})
	wantStatus(e.t, resp, http.StatusCreated)
	return decode[vault.Project](e.t, resp)
}

// newAsset crea un archivo por HTTP.
func (e *env) newAsset(projectID int64, path string) vault.CommitResult {
	e.t.Helper()
	m := e.uploadChunks(e.juanToken, randomBytes(e.t, 16<<10))
	resp := e.do(apiCall{method: http.MethodPost, path: "/api/assets", token: e.juanToken, clientID: "pc-1", body: map[string]any{
		"project_id": projectID, "path": path, "manifest": m, "comment": "Primera versión",
	}})
	wantStatus(e.t, resp, http.StatusCreated)
	return decode[vault.CommitResult](e.t, resp)
}

func TestAPIRequiresAuthentication(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	for _, call := range []apiCall{
		{method: http.MethodGet, path: "/api/projects"},
		{method: http.MethodGet, path: "/api/projects", token: "3dv_inventado"},
		{method: http.MethodPost, path: "/api/assets"},
		{method: http.MethodGet, path: "/api/chunks/" + strings.Repeat("a", 64)},
		{method: http.MethodGet, path: "/api/stream"},
		{method: http.MethodGet, path: "/api/versions/1/download"},
	} {
		resp := e.do(call)
		wantStatus(t, resp, http.StatusUnauthorized)
	}
}

func TestLoginGivesTokenAndNeverLeaksHash(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	resp := e.do(apiCall{method: http.MethodPost, path: "/api/login", body: map[string]string{
		"username": "juan", "password": testPassword, "token_name": "PC de Juan",
	}})
	wantStatus(t, resp, http.StatusOK)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "argon2") || strings.Contains(string(raw), "password_hash") {
		t.Fatalf("la respuesta filtra la contraseña: %s", raw)
	}
	var login loginResponse
	if err := json.Unmarshal(raw, &login); err != nil {
		t.Fatal(err)
	}
	if login.Token == "" {
		t.Fatal("falta el token")
	}
	if len(resp.Cookies()) != 0 {
		t.Fatal("pedir un token no debe crear una sesión")
	}

	me := e.do(apiCall{method: http.MethodGet, path: "/api/me", token: login.Token})
	wantStatus(t, me, http.StatusOK)
	if got := decode[vault.User](t, me); got.Username != "juan" {
		t.Fatalf("usuario = %q", got.Username)
	}
}

func TestLoginRejectsAndLimitsAttempts(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	bad := map[string]string{"username": "juan", "password": "incorrecta"}
	var last *http.Response
	for range 5 {
		last = e.do(apiCall{method: http.MethodPost, path: "/api/login", body: bad})
		wantStatus(t, last, http.StatusUnauthorized)
	}
	// El mismo mensaje con un usuario inexistente: no revela quién existe.
	unknown := e.do(apiCall{method: http.MethodPost, path: "/api/login", body: map[string]string{"username": "nadie", "password": "x"}})
	wantStatus(t, unknown, http.StatusUnauthorized)
	if got := decode[map[string]string](t, unknown); got["message"] != "Usuario o contraseña incorrectos." {
		t.Fatalf("mensaje = %q", got["message"])
	}

	blocked := e.do(apiCall{method: http.MethodPost, path: "/api/login", body: map[string]string{"username": "juan", "password": testPassword}})
	wantStatus(t, blocked, http.StatusTooManyRequests)
}

func TestSessionCookieFlags(t *testing.T) {
	t.Parallel()

	e := newEnv(t, func(c *Config) { c.InsecureCookies = false })
	resp := e.do(apiCall{method: http.MethodPost, path: "/api/login", body: map[string]string{"username": "juan", "password": testPassword}})
	wantStatus(t, resp, http.StatusOK)
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d", len(cookies))
	}
	c := cookies[0]
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie insegura: %+v", c)
	}
}

func TestTokensCanBeRevoked(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	created := e.do(apiCall{method: http.MethodPost, path: "/api/tokens", token: e.juanToken, body: map[string]string{"name": "Otra PC"}})
	wantStatus(t, created, http.StatusCreated)
	token := decode[struct {
		ID    int64  `json:"id"`
		Value string `json:"token"`
	}](t, created)

	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: "/api/me", token: token.Value}), http.StatusOK)
	path := "/api/tokens/" + strconv.FormatInt(token.ID, 10)
	wantStatus(t, e.do(apiCall{method: http.MethodDelete, path: path, token: e.anaToken}), http.StatusNotFound)
	wantStatus(t, e.do(apiCall{method: http.MethodDelete, path: path, token: e.juanToken}), http.StatusNoContent)
	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: "/api/me", token: token.Value}), http.StatusUnauthorized)
}

func TestUploadFlowOverHTTP(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	project := e.newProject()

	data := randomBytes(t, 4<<20)
	m, err := manifest.Build(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	hashes := make([]chunker.Hash, len(m.Chunks))
	for i, c := range m.Chunks {
		hashes[i] = c.SHA256
	}

	// Antes de subir, faltan todos y confirmar lo rechaza indicando cuáles.
	missing := e.do(apiCall{method: http.MethodPost, path: "/api/chunks/missing", token: e.juanToken, body: map[string]any{"hashes": hashes}})
	wantStatus(t, missing, http.StatusOK)
	if got := decode[map[string][]string](t, missing); len(got["missing"]) != len(hashes) {
		t.Fatalf("faltan %d, quiero %d", len(got["missing"]), len(hashes))
	}
	early := e.do(apiCall{method: http.MethodPost, path: "/api/assets", token: e.juanToken, clientID: "pc-1", body: map[string]any{
		"project_id": project.ID, "path": "a.blend", "manifest": m, "comment": "Primera",
	}})
	wantStatus(t, early, http.StatusUnprocessableEntity)
	if got := decode[rules.Error](t, early); got.Code != rules.CodeMissingChunks || len(got.Missing) != len(hashes) {
		t.Fatalf("rechazo = %+v", got)
	}

	e.uploadChunks(e.juanToken, data)
	missing = e.do(apiCall{method: http.MethodPost, path: "/api/chunks/missing", token: e.juanToken, body: map[string]any{"hashes": hashes}})
	if got := decode[map[string][]string](t, missing); len(got["missing"]) != 0 {
		t.Fatalf("siguen faltando %d", len(got["missing"]))
	}

	created := e.do(apiCall{method: http.MethodPost, path: "/api/assets", token: e.juanToken, clientID: "pc-1", body: map[string]any{
		"project_id": project.ID, "path": "modelos/camión.blend", "manifest": m, "comment": "Primera",
	}})
	wantStatus(t, created, http.StatusCreated)
	result := decode[vault.CommitResult](t, created)
	if result.Version.Number != 1 || result.Asset.Path != "modelos/camión.blend" {
		t.Fatalf("resultado = %+v", result)
	}

	// Rehacer la misma creación es seguro.
	again := e.do(apiCall{method: http.MethodPost, path: "/api/assets", token: e.juanToken, clientID: "pc-1", body: map[string]any{
		"project_id": project.ID, "path": "modelos/camión.blend", "manifest": m, "comment": "Primera",
	}})
	wantStatus(t, again, http.StatusOK)
	if got := decode[vault.CommitResult](t, again); !got.NoChanges {
		t.Fatal("debería indicar que no hay cambios")
	}

	// El manifiesto y la descarga reconstruyen el archivo.
	versionPath := fmt.Sprintf("/api/versions/%d", result.Version.ID)
	manifestResp := e.do(apiCall{method: http.MethodGet, path: versionPath + "/manifest", token: e.anaToken})
	wantStatus(t, manifestResp, http.StatusOK)
	if got := decode[manifest.Manifest](t, manifestResp); got.FileSHA256 != m.FileSHA256 || len(got.Chunks) != len(m.Chunks) {
		t.Fatalf("manifiesto = %+v", got)
	}

	download := e.do(apiCall{method: http.MethodGet, path: versionPath + "/download", token: e.anaToken})
	wantStatus(t, download, http.StatusOK)
	body, err := io.ReadAll(download.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, data) {
		t.Fatal("la descarga no coincide")
	}
	if got := download.Header.Get("Content-Length"); got != strconv.Itoa(len(data)) {
		t.Errorf("Content-Length = %s", got)
	}
	if got := download.Header.Get("Content-Disposition"); !strings.Contains(got, "attachment") || strings.Contains(got, "modelos") {
		t.Errorf("Content-Disposition = %s", got)
	}

	// Un trozo por su hash.
	chunkResp := e.do(apiCall{method: http.MethodGet, path: "/api/chunks/" + hashes[0].String(), token: e.anaToken})
	wantStatus(t, chunkResp, http.StatusOK)
	chunkData, err := io.ReadAll(chunkResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if chunker.Hash(sha256.Sum256(chunkData)) != hashes[0] {
		t.Fatal("el trozo no coincide con su hash")
	}
}

func TestChunkEndpointsValidate(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	valid := strings.Repeat("a", 64)
	tests := []struct {
		name string
		call apiCall
		want int
	}{
		{"hash mal formado", apiCall{method: http.MethodPut, path: "/api/chunks/xyz", token: e.juanToken, raw: []byte("x")}, http.StatusUnprocessableEntity},
		{"contenido distinto del hash", apiCall{method: http.MethodPut, path: "/api/chunks/" + valid, token: e.juanToken, raw: []byte("x")}, http.StatusUnprocessableEntity},
		{"trozo demasiado grande", apiCall{method: http.MethodPut, path: "/api/chunks/" + valid, token: e.juanToken, raw: make([]byte, chunker.MaxSize+1)}, http.StatusRequestEntityTooLarge},
		{"trozo que no existe", apiCall{method: http.MethodGet, path: "/api/chunks/" + valid, token: e.juanToken}, http.StatusNotFound},
		{"lista de hashes rota", apiCall{method: http.MethodPost, path: "/api/chunks/missing", token: e.juanToken, body: map[string]any{"hashes": []string{"xyz"}}}, http.StatusUnprocessableEntity},
		{"campo desconocido", apiCall{method: http.MethodPost, path: "/api/projects", token: e.juanToken, body: map[string]string{"nombre": "x"}}, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantStatus(t, e.do(tt.call), tt.want)
		})
	}
}

func TestWritesRequireClientIDWithToken(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	project := e.newProject()
	a := e.newAsset(project.ID, "a.blend")
	resp := e.do(apiCall{method: http.MethodPost, path: fmt.Sprintf("/api/assets/%d/lock", a.Asset.ID), token: e.juanToken})
	wantStatus(t, resp, http.StatusUnprocessableEntity)
}

func TestLockConflictResponseHasDetails(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	project := e.newProject()
	a := e.newAsset(project.ID, "a.blend")
	lockPath := fmt.Sprintf("/api/assets/%d/lock", a.Asset.ID)

	locked := e.do(apiCall{method: http.MethodPost, path: lockPath, token: e.juanToken, clientID: "pc-1"})
	wantStatus(t, locked, http.StatusOK)
	if got := decode[vault.LockResult](t, locked); got.HeadVersion != 1 || got.Lock == nil {
		t.Fatalf("bloqueo = %+v", got)
	}

	m := e.uploadChunks(e.anaToken, randomBytes(t, 4096))
	commit := map[string]any{"base_version": 1, "manifest": m, "size_bytes": m.Size(), "comment": "Ajuste"}
	versions := fmt.Sprintf("/api/assets/%d/versions", a.Asset.ID)

	resp := e.do(apiCall{method: http.MethodPost, path: versions, token: e.anaToken, clientID: "pc-2", body: commit})
	wantStatus(t, resp, http.StatusConflict)
	got := decode[rules.Error](t, resp)
	if got.Code != rules.CodeLocked || got.HeadVersion != 1 || got.Lock == nil || got.Lock.DisplayName != "Juan" {
		t.Fatalf("rechazo = %+v", got)
	}
	if !strings.HasPrefix(got.Message, "Bloqueado por Juan desde ") {
		t.Fatalf("mensaje = %q", got.Message)
	}

	otherPC := e.do(apiCall{method: http.MethodPost, path: lockPath, token: e.juanToken, clientID: "pc-9"})
	wantStatus(t, otherPC, http.StatusConflict)
	if got := decode[rules.Error](t, otherPC); got.Code != rules.CodeLockedOtherClient {
		t.Fatalf("código = %s", got.Code)
	}

	takeover := e.do(apiCall{method: http.MethodPost, path: lockPath, token: e.juanToken, clientID: "pc-9", body: map[string]bool{"takeover": true}})
	wantStatus(t, takeover, http.StatusOK)

	wantStatus(t, e.do(apiCall{method: http.MethodDelete, path: lockPath, token: e.anaToken, clientID: "pc-2"}), http.StatusForbidden)
	wantStatus(t, e.do(apiCall{method: http.MethodDelete, path: lockPath + "?force=true", token: e.anaToken, clientID: "pc-2"}), http.StatusForbidden)
	wantStatus(t, e.do(apiCall{method: http.MethodDelete, path: lockPath, token: e.juanToken, clientID: "pc-9"}), http.StatusNoContent)

	// Ya libre, se sube y vale; una segunda desde la misma base se rechaza.
	wantStatus(t, e.do(apiCall{method: http.MethodPost, path: versions, token: e.anaToken, clientID: "pc-2", body: commit}), http.StatusCreated)
	m2 := e.uploadChunks(e.juanToken, randomBytes(t, 4096))
	stale := e.do(apiCall{method: http.MethodPost, path: versions, token: e.juanToken, clientID: "pc-1", body: map[string]any{
		"base_version": 1, "manifest": m2, "size_bytes": m2.Size(), "comment": "Tarde",
	}})
	wantStatus(t, stale, http.StatusConflict)
	if got := decode[rules.Error](t, stale); got.Code != rules.CodeStaleVersion || got.HeadVersion != 2 {
		t.Fatalf("rechazo = %+v", got)
	}
}

func TestRestoreMoveDeleteOverHTTP(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	project := e.newProject()
	a := e.newAsset(project.ID, "a.blend")
	id := a.Asset.ID

	m := e.uploadChunks(e.juanToken, randomBytes(t, 4096))
	wantStatus(t, e.do(apiCall{method: http.MethodPost, path: fmt.Sprintf("/api/assets/%d/versions", id), token: e.juanToken, clientID: "pc-1", body: map[string]any{
		"base_version": 1, "manifest": m, "size_bytes": m.Size(), "comment": "Segunda",
	}}), http.StatusCreated)

	restored := e.do(apiCall{method: http.MethodPost, path: fmt.Sprintf("/api/assets/%d/restore/1", id), token: e.anaToken, clientID: "pc-2", body: map[string]int{"base_version": 2}})
	wantStatus(t, restored, http.StatusCreated)
	if got := decode[vault.CommitResult](t, restored); got.Version.Number != 3 || got.Version.Comment != "Restaurado desde v1" {
		t.Fatalf("restaurada = %+v", got.Version)
	}

	listed := e.do(apiCall{method: http.MethodGet, path: fmt.Sprintf("/api/assets/%d/versions", id), token: e.anaToken})
	wantStatus(t, listed, http.StatusOK)
	if got := decode[map[string][]vault.Version](t, listed); len(got["versions"]) != 3 || got["versions"][0].Number != 3 {
		t.Fatalf("versiones = %+v", got)
	}

	moved := e.do(apiCall{method: http.MethodPost, path: fmt.Sprintf("/api/assets/%d/move", id), token: e.anaToken, clientID: "pc-2", body: map[string]any{"base_version": 3, "path": "nuevo/b.blend"}})
	wantStatus(t, moved, http.StatusOK)
	if got := decode[vault.Asset](t, moved); got.Path != "nuevo/b.blend" {
		t.Fatalf("ruta = %q", got.Path)
	}
	wantStatus(t, e.do(apiCall{method: http.MethodPost, path: fmt.Sprintf("/api/assets/%d/move", id), token: e.anaToken, clientID: "pc-2", body: map[string]any{"base_version": 3, "path": "../x.blend"}}), http.StatusUnprocessableEntity)

	wantStatus(t, e.do(apiCall{method: http.MethodDelete, path: fmt.Sprintf("/api/assets/%d?base_version=1", id), token: e.anaToken, clientID: "pc-2"}), http.StatusConflict)
	wantStatus(t, e.do(apiCall{method: http.MethodDelete, path: fmt.Sprintf("/api/assets/%d?base_version=3", id), token: e.anaToken, clientID: "pc-2"}), http.StatusNoContent)
	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: fmt.Sprintf("/api/assets/%d/versions", id), token: e.anaToken}), http.StatusNotFound)
}

func TestSnapshotAndEventsOverHTTP(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	project := e.newProject()
	e.newAsset(project.ID, "a.blend")

	snapResp := e.do(apiCall{method: http.MethodGet, path: fmt.Sprintf("/api/projects/%d/snapshot", project.ID), token: e.anaToken})
	wantStatus(t, snapResp, http.StatusOK)
	snap := decode[vault.Snapshot](t, snapResp)
	if len(snap.Assets) != 1 || snap.ServerEpoch == "" || snap.Seq == 0 {
		t.Fatalf("snapshot = %+v", snap)
	}

	second := e.newAsset(project.ID, "b.blend")
	eventsResp := e.do(apiCall{method: http.MethodGet, path: fmt.Sprintf("/api/projects/%d/events?since=%d", project.ID, snap.Seq), token: e.anaToken})
	wantStatus(t, eventsResp, http.StatusOK)
	page := decode[vault.EventPage](t, eventsResp)
	if len(page.Events) != 1 || page.Events[0].AssetID != second.Asset.ID || page.Seq <= snap.Seq {
		t.Fatalf("eventos = %+v", page)
	}

	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: "/api/projects/999/snapshot", token: e.anaToken}), http.StatusNotFound)
	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: "/api/projects/999/events", token: e.anaToken}), http.StatusNotFound)
	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: fmt.Sprintf("/api/projects/%d/events?since=abc", project.ID), token: e.anaToken}), http.StatusUnprocessableEntity)
}

// sseEvent es un mensaje leído del flujo.
type sseEvent struct {
	name string
	id   string
	data string
}

// readSSE lee mensajes del flujo hasta que se corta y los envía por un canal.
func readSSE(body io.Reader) <-chan sseEvent {
	out := make(chan sseEvent, 16)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		var current sseEvent
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				if current.name != "" {
					out <- current
				}
				current = sseEvent{}
			case strings.HasPrefix(line, "event: "):
				current.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				current.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				current.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return out
}

func nextEvent(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("el flujo se cerró")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no llegó ningún evento")
	}
	return sseEvent{}
}

func (e *env) openStream(query string, header http.Header) (<-chan sseEvent, func()) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(e.ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.server.URL+"/api/stream"+query, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.anaToken)
	for key, values := range header {
		req.Header[key] = values
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		e.t.Fatalf("abrir flujo: %v", err)
	}
	wantStatus(e.t, resp, http.StatusOK)
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		e.t.Fatalf("Content-Type = %s", got)
	}
	return readSSE(resp.Body), func() {
		cancel()
		_ = resp.Body.Close()
	}
}

func TestStreamDeliversEventsLive(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	project := e.newProject()
	other, err := e.svc.CreateProject(e.ctx, rules.Actor{UserID: e.juan.ID}, "Otro", "")
	if err != nil {
		t.Fatal(err)
	}

	stream, stop := e.openStream("?projects="+strconv.FormatInt(project.ID, 10), nil)
	defer stop()

	hello := nextEvent(t, stream)
	if hello.name != "hello" || !strings.Contains(hello.data, "server_epoch") {
		t.Fatalf("saludo = %+v", hello)
	}

	// Un evento de otro proyecto no debe llegar: el siguiente será el de este.
	e.newAsset(other.ID, "x.blend")
	created := e.newAsset(project.ID, "a.blend")

	ev := nextEvent(t, stream)
	if ev.name != vault.EventAssetCreated || ev.id == "" {
		t.Fatalf("evento = %+v", ev)
	}
	var event vault.Event
	if err := json.Unmarshal([]byte(ev.data), &event); err != nil {
		t.Fatal(err)
	}
	if event.AssetID != created.Asset.ID || event.ProjectID != project.ID {
		t.Fatalf("evento = %+v", event)
	}

	lock := e.do(apiCall{method: http.MethodPost, path: fmt.Sprintf("/api/assets/%d/lock", created.Asset.ID), token: e.juanToken, clientID: "pc-1"})
	wantStatus(t, lock, http.StatusOK)
	if got := nextEvent(t, stream); got.name != vault.EventLocked {
		t.Fatalf("evento = %+v", got)
	}
}

// TestStreamResumesWithoutLosingEvents comprueba que reconectar con Last-Event-ID
// entrega lo ocurrido mientras la conexión estuvo cortada.
func TestStreamResumesWithoutLosingEvents(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	project := e.newProject()

	first := e.newAsset(project.ID, "a.blend")
	snapResp := e.do(apiCall{method: http.MethodGet, path: fmt.Sprintf("/api/projects/%d/snapshot", project.ID), token: e.anaToken})
	seq := decode[vault.Snapshot](t, snapResp).Seq
	second := e.newAsset(project.ID, "b.blend")

	for _, tt := range []struct {
		name   string
		query  string
		header http.Header
	}{
		{"con since", "?since=" + strconv.FormatInt(seq, 10), nil},
		{"con Last-Event-ID", "?since=0", http.Header{"Last-Event-Id": {strconv.FormatInt(seq, 10)}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stream, stop := e.openStream(tt.query, tt.header)
			defer stop()
			if got := nextEvent(t, stream); got.name != "hello" {
				t.Fatalf("saludo = %+v", got)
			}
			ev := nextEvent(t, stream)
			var event vault.Event
			if err := json.Unmarshal([]byte(ev.data), &event); err != nil {
				t.Fatal(err)
			}
			if event.AssetID != second.Asset.ID || event.AssetID == first.Asset.ID {
				t.Fatalf("evento = %+v", event)
			}
		})
	}
}

func TestStreamEndsAtMaxAge(t *testing.T) {
	t.Parallel()

	e := newEnv(t, func(c *Config) { c.MaxStreamAge = 100 * time.Millisecond })
	stream, stop := e.openStream("", nil)
	defer stop()
	if got := nextEvent(t, stream); got.name != "hello" {
		t.Fatalf("saludo = %+v", got)
	}
	select {
	case _, ok := <-stream:
		if ok {
			t.Fatal("no debería llegar otro evento")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("el flujo debería cerrarse al llegar a su duración máxima")
	}
}

func TestRealIPAndAllowedIPs(t *testing.T) {
	t.Parallel()

	local := netip.MustParsePrefix("127.0.0.0/8")
	allowed := netip.MustParsePrefix("203.0.113.0/24")

	tests := []struct {
		name    string
		trusted []netip.Prefix
		allowed []netip.Prefix
		xff     string
		want    int
	}{
		{"sin restricción", nil, nil, "", http.StatusOK},
		{"IP directa no permitida", nil, []netip.Prefix{allowed}, "", http.StatusForbidden},
		{"XFF ignorado si el proxy no es de confianza", nil, []netip.Prefix{allowed}, "203.0.113.9", http.StatusForbidden},
		{"XFF válido de un proxy de confianza", []netip.Prefix{local}, []netip.Prefix{allowed}, "203.0.113.9", http.StatusOK},
		{"se cree solo la última IP de XFF", []netip.Prefix{local}, []netip.Prefix{allowed}, "203.0.113.9, 198.51.100.1", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, func(c *Config) {
				c.TrustedProxies = tt.trusted
				c.AllowedIPs = tt.allowed
			})
			req, err := http.NewRequestWithContext(e.ctx, http.MethodGet, e.server.URL+"/api/me", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+e.juanToken)
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			resp, err := e.server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Fatalf("estado = %d, quiero %d", resp.StatusCode, tt.want)
			}
		})
	}
}

func TestHealthzIsPublic(t *testing.T) {
	t.Parallel()

	e := newEnv(t, func(c *Config) { c.AllowedIPs = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")} })
	resp, err := e.server.Client().Get(e.server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("estado = %d", resp.StatusCode)
	}
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	resp := e.do(apiCall{method: http.MethodGet, path: "/login"})
	wantStatus(t, resp, http.StatusOK)
	h := resp.Header
	if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("CSP = %q", h.Get("Content-Security-Policy"))
	}
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" {
		t.Errorf("cabeceras = %v", h)
	}
}

// browser es un cliente con cookies que no sigue redirecciones.
type browser struct {
	t      *testing.T
	e      *env
	client *http.Client
}

func (e *env) newBrowser() *browser {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return &browser{t: e.t, e: e, client: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (b *browser) get(path string) (*http.Response, string) {
	b.t.Helper()
	req, err := http.NewRequestWithContext(b.e.ctx, http.MethodGet, b.e.server.URL+path, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	return b.send(req)
}

func (b *browser) post(path string, form url.Values, headers map[string]string) (*http.Response, string) {
	b.t.Helper()
	req, err := http.NewRequestWithContext(b.e.ctx, http.MethodPost, b.e.server.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return b.send(req)
}

func (b *browser) send(req *http.Request) (*http.Response, string) {
	b.t.Helper()
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return resp, string(body)
}

func (b *browser) login(username string) {
	b.t.Helper()
	resp, body := b.post("/login", url.Values{"username": {username}, "password": {testPassword}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		b.t.Fatalf("login = %d: %s", resp.StatusCode, body)
	}
}

func wantPage(t *testing.T, resp *http.Response, body string, status int, contains ...string) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("%s = %d, quiero %d:\n%s", resp.Request.URL.Path, resp.StatusCode, status, body)
	}
	for _, text := range contains {
		if !strings.Contains(body, text) {
			t.Fatalf("%s no contiene %q:\n%s", resp.Request.URL.Path, text, body)
		}
	}
}

func TestWebRequiresLogin(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	b := e.newBrowser()
	for _, path := range []string{"/", "/sync", "/projects/1", "/assets/1", "/admin/users", "/versions/1/download"} {
		resp, _ := b.get(path)
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
			t.Errorf("%s = %d, %s", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}

	resp, body := b.get("/login")
	wantPage(t, resp, body, http.StatusOK, "<h1>Entrar</h1>", `lang="es"`)

	resp, body = b.post("/login", url.Values{"username": {"juan"}, "password": {"incorrecta"}}, nil)
	wantPage(t, resp, body, http.StatusUnauthorized, "Usuario o contraseña incorrectos.")
	if strings.Contains(body, "incorrecta\"") {
		t.Fatal("la página no debe devolver la contraseña")
	}
}

func TestWebSessionEndsWithPasswordChange(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	b := e.newBrowser()
	b.login("juan")
	resp, body := b.get("/")
	wantPage(t, resp, body, http.StatusOK, "Juan")

	if err := e.svc.SetPassword(e.ctx, e.admin.ID, e.juan.ID, "otra-contraseña-larga"); err != nil {
		t.Fatal(err)
	}
	resp, _ = b.get("/")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("la sesión debería terminar: %d", resp.StatusCode)
	}
}

func TestWebLogout(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	b := e.newBrowser()
	b.login("juan")
	resp, _ := b.post("/logout", nil, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	resp, _ = b.get("/")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("debería pedir entrar de nuevo: %d", resp.StatusCode)
	}
}

func TestWebRejectsCrossSiteForms(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	b := e.newBrowser()
	b.login("juan")
	form := url.Values{"name": {"Desde otro sitio"}}

	resp, _ := b.post("/projects", form, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Sec-Fetch-Site cross-site = %d, quiero 403", resp.StatusCode)
	}
	resp, _ = b.post("/projects", form, map[string]string{"Origin": "https://malicioso.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Origin ajeno = %d, quiero 403", resp.StatusCode)
	}
	resp, _ = b.post("/projects", form, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("mismo origen = %d, quiero 303", resp.StatusCode)
	}
}

func TestWebProjectsAndTree(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	b := e.newBrowser()
	b.login("juan")

	resp, body := b.get("/")
	wantPage(t, resp, body, http.StatusOK, "Todavía no hay proyectos", "0 proyectos")

	resp, body = b.get("/sync")
	wantPage(t, resp, body, http.StatusOK, "<h1>Mi carpeta</h1>", `src="/static/sync/app.js"`)

	resp, body = b.post("/projects", url.Values{"name": {"Mina Norte"}, "description": {"Camiones"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("crear = %d: %s", resp.StatusCode, body)
	}
	location := resp.Header.Get("Location")

	resp, body = b.post("/projects", url.Values{"name": {"mina norte"}}, nil)
	wantPage(t, resp, body, http.StatusConflict, "Ya existe un proyecto", `value="mina norte"`)

	resp, body = b.get(location)
	wantPage(t, resp, body, http.StatusOK, "aún no tiene archivos", "Mina Norte")

	id, err := strconv.ParseInt(strings.TrimPrefix(location, "/projects/"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	e.newAsset(id, "modelos/camiones/797F.blend")
	e.newAsset(id, "modelos/camiones/793D.blend")
	locked := e.newAsset(id, "texturas/rueda.png")
	wantStatus(t, e.do(apiCall{method: http.MethodPost, path: fmt.Sprintf("/api/assets/%d/lock", locked.Asset.ID), token: e.anaToken, clientID: "pc-2"}), http.StatusOK)

	resp, body = b.get(location)
	wantPage(t, resp, body, http.StatusOK, "3 archivos", "1 bloqueado", "modelos/", "camiones/", "797F.blend", "rueda.png", "Ana")
	if strings.Count(body, ">camiones/<") != 1 {
		t.Fatal("la carpeta debe aparecer una sola vez")
	}

	resp, body = b.get(location + "?estado=bloqueados")
	wantPage(t, resp, body, http.StatusOK, "rueda.png")
	if strings.Contains(body, "797F.blend") {
		t.Fatal("el filtro debe ocultar los archivos libres")
	}

	resp, body = b.get(location + "?q=797")
	wantPage(t, resp, body, http.StatusOK, "797F.blend")
	if strings.Contains(body, "793D.blend") {
		t.Fatal("la búsqueda debe ocultar lo demás")
	}

	resp, body = b.get(location + "?estado=mios")
	wantPage(t, resp, body, http.StatusOK, "No tienes archivos bloqueados")

	resp, body = b.get("/")
	wantPage(t, resp, body, http.StatusOK, "1 proyecto con 3 archivos", "Mina Norte")

	resp, body = b.get("/projects/999")
	wantPage(t, resp, body, http.StatusNotFound, "No encontrado")
}

func TestWebAssetActions(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	project := e.newProject()

	// Un comentario con HTML debe mostrarse como texto.
	m := e.uploadChunks(e.juanToken, randomBytes(t, 16<<10))
	created := e.do(apiCall{method: http.MethodPost, path: "/api/assets", token: e.juanToken, clientID: "pc-1", body: map[string]any{
		"project_id": project.ID, "path": "modelos/camión.blend", "manifest": m, "comment": `<script>alert("x")</script>`,
	}})
	wantStatus(t, created, http.StatusCreated)
	asset := decode[vault.CommitResult](t, created).Asset
	path := "/assets/" + strconv.FormatInt(asset.ID, 10)

	b := e.newBrowser()
	b.login("juan")

	resp, body := b.get(path)
	wantPage(t, resp, body, http.StatusOK, "camión.blend", "modelos/camión.blend", "Descargar v1", "Bloquear", "&lt;script&gt;")
	if strings.Contains(body, `<script>alert`) {
		t.Fatal("el comentario no se escapó")
	}
	if strings.Contains(body, "Restaurar</button>") {
		t.Fatal("no se puede restaurar la versión actual")
	}

	// Bloquear y liberar.
	resp, _ = b.post(path+"/lock", nil, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("bloquear = %d", resp.StatusCode)
	}
	resp, body = b.get(path)
	wantPage(t, resp, body, http.StatusOK, "Liberar", "Navegador")

	// Otro usuario lo ve bloqueado y no tiene botón; un POST directo se rechaza.
	other := e.newBrowser()
	other.login("ana")
	resp, body = other.get(path)
	wantPage(t, resp, body, http.StatusOK, "Juan")
	if strings.Contains(body, "Liberar") || strings.Contains(body, "Forzar") || strings.Contains(body, ">Bloquear<") {
		t.Fatal("Ana no debería tener acciones de bloqueo")
	}
	resp, body = other.post(path+"/unlock", nil, nil)
	wantPage(t, resp, body, http.StatusForbidden, "Solo Juan puede liberar")
	resp, body = other.post(path+"/unlock", url.Values{"force": {"1"}}, nil)
	wantPage(t, resp, body, http.StatusForbidden, "Solo un administrador")

	// El admin puede forzarlo.
	boss := e.newBrowser()
	boss.login("jefa")
	resp, body = boss.get(path)
	wantPage(t, resp, body, http.StatusOK, "Forzar desbloqueo")
	resp, _ = boss.post(path+"/unlock", url.Values{"force": {"1"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("forzar = %d", resp.StatusCode)
	}

	// Una segunda versión muestra lo que cambió y permite restaurar la primera.
	m2 := e.uploadChunks(e.juanToken, randomBytes(t, 16<<10))
	wantStatus(t, e.do(apiCall{method: http.MethodPost, path: fmt.Sprintf("/api/assets/%d/versions", asset.ID), token: e.juanToken, clientID: "pc-1", body: map[string]any{
		"base_version": 1, "manifest": m2, "size_bytes": m2.Size(), "comment": "Segunda",
	}}), http.StatusCreated)
	resp, body = b.get(path)
	wantPage(t, resp, body, http.StatusOK, "Descargar v2", "Restaurar</button>", "100,0 %", "2 versiones", "solo viajaron")

	// Restaurar desde una versión vieja se rechaza con el motivo en la propia ficha.
	resp, body = b.post(path+"/restore/1", url.Values{"base_version": {"1"}}, nil)
	wantPage(t, resp, body, http.StatusConflict, "Hay una versión más nueva", "Descargar v2")

	resp, _ = b.post(path+"/restore/1", url.Values{"base_version": {"2"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("restaurar = %d", resp.StatusCode)
	}
	resp, body = b.get(path)
	wantPage(t, resp, body, http.StatusOK, "Descargar v3", "Restaurado desde v1")

	// Descargar desde la web.
	resp, body = b.get(fmt.Sprintf("/versions/%d/download", asset.HeadVersionID))
	if resp.StatusCode != http.StatusOK || len(body) != int(asset.SizeBytes) {
		t.Fatalf("descarga = %d, %d bytes", resp.StatusCode, len(body))
	}
	if !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("Content-Disposition = %q", resp.Header.Get("Content-Disposition"))
	}
}

func TestWebUserAdministration(t *testing.T) {
	t.Parallel()

	e := newEnv(t)

	plain := e.newBrowser()
	plain.login("juan")
	resp, body := plain.get("/admin/users")
	wantPage(t, resp, body, http.StatusForbidden, "Sin permiso")
	resp, _ = plain.post("/admin/users", url.Values{"username": {"intruso"}, "password": {testPassword}}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("crear usuario sin ser admin = %d", resp.StatusCode)
	}
	if strings.Contains(body, "Usuarios</a>") {
		t.Fatal("un modelador no debe ver el enlace de administración")
	}

	admin := e.newBrowser()
	admin.login("jefa")
	resp, body = admin.get("/admin/users")
	wantPage(t, resp, body, http.StatusOK, "3 cuentas activas", "1 administrador", "Jefa")

	resp, body = admin.post("/admin/users", url.Values{"username": {"nuevo"}, "display_name": {"Nuevo"}, "password": {"corta"}}, nil)
	wantPage(t, resp, body, http.StatusUnprocessableEntity, "al menos 10 caracteres", `value="nuevo"`)
	if strings.Contains(body, `value="corta"`) {
		t.Fatal("la contraseña no debe volver al formulario")
	}

	resp, _ = admin.post("/admin/users", url.Values{"username": {"nuevo"}, "display_name": {"Nuevo"}, "password": {testPassword}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("crear = %d", resp.StatusCode)
	}
	created := e.newBrowser()
	created.login("nuevo")

	active := fmt.Sprintf("/admin/users/%d/active", e.juan.ID)
	resp, _ = admin.post(active, url.Values{"active": {"0"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("desactivar = %d", resp.StatusCode)
	}
	resp, _ = plain.get("/")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("la sesión de una cuenta desactivada debe terminar: %d", resp.StatusCode)
	}

	resp, body = admin.post(fmt.Sprintf("/admin/users/%d/active", e.admin.ID), url.Values{"active": {"0"}}, nil)
	wantPage(t, resp, body, http.StatusConflict, "único administrador")

	resp, _ = admin.post("/admin/users/password", url.Values{"user_id": {strconv.FormatInt(e.ana.ID, 10)}, "password": {"nueva-contraseña-larga"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("cambiar contraseña = %d", resp.StatusCode)
	}
	if _, ok, _ := e.svc.Authenticate(e.ctx, "ana", "nueva-contraseña-larga"); !ok {
		t.Fatal("la contraseña nueva debería valer")
	}
}

func TestWebStorageIsAdminOnly(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	e.newAsset(e.newProject().ID, "a.blend")

	plain := e.newBrowser()
	plain.login("juan")
	resp, body := plain.get("/admin/storage")
	wantPage(t, resp, body, http.StatusForbidden, "Sin permiso")
	if strings.Contains(body, `href="/admin/storage"`) {
		t.Fatal("un modelador no debe ver el enlace a Espacio")
	}

	admin := e.newBrowser()
	admin.login("jefa")
	resp, body = admin.get("/admin/storage")
	wantPage(t, resp, body, http.StatusOK, "Guardado en disco", "Mina Norte", "16,0 KB", "purge --keep-last", "gc --apply")
	if strings.Contains(body, "No disponible") {
		t.Fatal("en Unix debe mostrar el espacio libre")
	}
}

func TestStaticFilesAreServed(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	for _, tt := range []struct {
		path string
		want string
	}{
		{"/static/app.css", "text/css"},
		{"/static/htmx.min.js", "javascript"},
		{"/static/fonts/geist.woff2", "font/woff2"},
	} {
		resp := e.do(apiCall{method: http.MethodGet, path: tt.path})
		wantStatus(t, resp, http.StatusOK)
		if got := resp.Header.Get("Content-Type"); !strings.Contains(got, tt.want) {
			t.Errorf("%s: Content-Type = %q", tt.path, got)
		}
	}
	wantStatus(t, e.do(apiCall{method: http.MethodGet, path: "/static/../templates/login.html"}), http.StatusNotFound)
}

func TestBuildTree(t *testing.T) {
	t.Parallel()

	assets := []vault.Asset{
		{Path: "a.blend"},
		{Path: "modelos/b.blend"},
		{Path: "modelos/camiones/c.blend"},
		{Path: "modelos/camiones/d.blend"},
		{Path: "modelos/e.blend"},
		{Path: "texturas/f.png"},
	}
	var got []string
	for _, row := range buildTree(assets) {
		mark := ""
		if row.Folder {
			mark = "/"
		}
		got = append(got, strings.Repeat(" ", row.Depth)+row.Name+mark)
	}
	want := []string{"a.blend", "modelos/", " b.blend", " camiones/", "  c.blend", "  d.blend", " e.blend", "texturas/", " f.png"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("árbol:\n%s\nquiero:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestFormatters(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		n    int64
		want string
	}{
		{0, "0 B"}, {1023, "1023 B"}, {1024, "1,0 KB"}, {3 << 19, "1,5 MB"}, {3 << 29, "1,5 GB"},
	} {
		if got := formatBytes(tt.n); got != tt.want {
			t.Errorf("formatBytes(%d) = %q, quiero %q", tt.n, got, tt.want)
		}
	}
	if got := formatPercent(12.34); got != "12,3 %" {
		t.Errorf("formatPercent = %q", got)
	}

	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.Local)
	for _, tt := range []struct {
		t    time.Time
		want string
	}{
		{now.Add(-time.Hour), "hoy, 11:00"},
		{now.Add(-13 * time.Hour), "ayer, 23:00"},
		{now.Add(-72 * time.Hour), "01/11/2026 12:00"},
	} {
		if got := formatWhen(tt.t, now); got != tt.want {
			t.Errorf("formatWhen = %q, quiero %q", got, tt.want)
		}
	}
}

func TestLocalPathOnlyAllowsThisSite(t *testing.T) {
	t.Parallel()

	for next, want := range map[string]string{
		"/sync":                "/sync",
		"/assets/3?x=1":        "/assets/3?x=1",
		"":                     "/",
		"https://malo.example": "/",
		"//malo.example":       "/",
		"/\\malo.example":      "/",
		"sync":                 "/",
	} {
		if got := localPath(next); got != want {
			t.Errorf("localPath(%q) = %q, quiero %q", next, got, want)
		}
	}
}
