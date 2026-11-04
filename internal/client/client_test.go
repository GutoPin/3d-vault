package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
	"3d-vault/internal/rules"
	"3d-vault/internal/server"
	"3d-vault/internal/store"
	"3d-vault/internal/vault"
)

const (
	testPassword = "contraseña-larga"
	remotePath   = "modelos/camión.blend"
)

// counters cuenta el tráfico de trozos que llega de verdad al servidor.
type counters struct {
	mu   sync.Mutex
	puts map[string]int
	gets atomic.Int64
}

func (c *counters) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/chunks/") && r.URL.Path != "/api/chunks/missing" {
			switch r.Method {
			case http.MethodPut:
				c.mu.Lock()
				c.puts[r.URL.Path]++
				c.mu.Unlock()
			case http.MethodGet:
				c.gets.Add(1)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (c *counters) putCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, n := range c.puts {
		total += n
	}
	return total
}

func (c *counters) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.puts = make(map[string]int)
	c.gets.Store(0)
}

type env struct {
	t       *testing.T
	ctx     context.Context
	url     string
	svc     *vault.Service
	traffic *counters
	token   string
	project vault.Project
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

	svc := vault.New(vault.Config{
		DB: database, Store: chunks, Hub: events.NewHub(),
		MaxFileSize: 1 << 30, Argon: auth.Params{Memory: 1024, Time: 1, Threads: 1},
	})
	handler, err := server.New(server.Config{
		Service:         svc,
		Sessions:        auth.NewSessions([]byte("secreto-de-prueba-suficientemente-largo"), time.Hour),
		Limiter:         auth.NewLimiter(),
		Logger:          slog.New(slog.DiscardHandler),
		InsecureCookies: true,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	traffic := &counters{puts: make(map[string]int)}
	srv := httptest.NewServer(traffic.wrap(handler))
	t.Cleanup(srv.Close)

	user, err := svc.CreateUser(ctx, 0, vault.NewUserInput{Username: "juan", DisplayName: "Juan", Password: testPassword})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, token, err := svc.CreateToken(ctx, user.ID, "PC")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	project, err := svc.CreateProject(ctx, rules.Actor{UserID: user.ID}, "Mina", "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return &env{t: t, ctx: ctx, url: srv.URL, svc: svc, traffic: traffic, token: token, project: project}
}

func (e *env) client(transport http.RoundTripper) *Client {
	return New(Config{
		Server:     e.url,
		Token:      e.token,
		ClientID:   "pc-1",
		ClientName: "PC de Juan",
		HTTP:       &http.Client{Transport: transport},
		RetryDelay: time.Millisecond,
	})
}

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	return data
}

func writeFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "camion.blend")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// editInMiddle simula un guardado que solo cambia una zona del archivo.
func editInMiddle(t *testing.T, data []byte) []byte {
	t.Helper()
	out := append([]byte{}, data[:len(data)/2]...)
	out = append(out, randomBytes(t, 4096)...)
	return append(out, data[len(data)/2:]...)
}

func (e *env) push(c *Client, data []byte, comment string) PushResult {
	e.t.Helper()
	result, err := c.Push(e.ctx, PushInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: writeFile(e.t, data), Comment: comment})
	if err != nil {
		e.t.Fatalf("Push: %v", err)
	}
	return result
}

func TestLogin(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	c := e.client(nil)
	token, user, err := c.Login(e.ctx, "juan", testPassword, "PC de Juan")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if token == "" || user.Username != "juan" {
		t.Fatalf("Login = %q, %+v", token, user)
	}

	_, _, err = c.Login(e.ctx, "juan", "incorrecta", "PC")
	var serverErr *ServerError
	if !errors.As(err, &serverErr) || serverErr.Status != http.StatusUnauthorized {
		t.Fatalf("Login = %v", err)
	}
}

// TestPushSendsOnlyWhatChanged es el criterio central del producto: la segunda
// versión solo transfiere los trozos nuevos.
func TestPushSendsOnlyWhatChanged(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	c := e.client(nil)
	data := randomBytes(t, 48<<20)

	first := e.push(c, data, "Primera versión")
	if first.Commit.Version.Number != 1 || first.UploadedBytes != first.TotalBytes {
		t.Fatalf("primera = %+v", first)
	}
	if e.traffic.putCount() != first.UploadedChunks {
		t.Fatalf("subidas reales = %d, informadas = %d", e.traffic.putCount(), first.UploadedChunks)
	}

	e.traffic.reset()
	second := e.push(c, editInMiddle(t, data), "Ajuste en el centro")
	if second.Commit.Version.Number != 2 {
		t.Fatalf("segunda = %+v", second.Commit.Version)
	}
	if second.ReusedPercent() < 85 {
		t.Fatalf("reutilizado %.1f %%, enviados %d de %d trozos", second.ReusedPercent(), second.UploadedChunks, second.TotalChunks)
	}
	if got := e.traffic.putCount(); got != second.UploadedChunks || got == 0 {
		t.Fatalf("subidas reales = %d, informadas = %d", got, second.UploadedChunks)
	}

	// Sin cambios: nada viaja y no se crea versión.
	e.traffic.reset()
	same := e.push(c, editInMiddle(t, data), "Otra edición")
	if same.Commit.NoChanges {
		t.Fatal("una edición distinta debería crear versión")
	}
	e.traffic.reset()
	unchanged, err := c.Push(e.ctx, PushInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: writeFile(t, data), Comment: "Vuelta atrás"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if unchanged.UploadedChunks != 0 || e.traffic.putCount() != 0 {
		t.Fatalf("no debería enviar nada: %+v", unchanged)
	}
}

func TestPushNoChanges(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	c := e.client(nil)
	data := randomBytes(t, 1<<20)
	e.push(c, data, "Primera versión")

	e.traffic.reset()
	result := e.push(c, data, "Sin cambios")
	if !result.Commit.NoChanges || result.UploadedChunks != 0 || e.traffic.putCount() != 0 {
		t.Fatalf("resultado = %+v, subidas = %d", result, e.traffic.putCount())
	}
	if result.ReusedPercent() != 100 {
		t.Fatalf("reutilizado = %.1f", result.ReusedPercent())
	}
}

func TestPushRejectsBadInput(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	c := e.client(nil)
	file := writeFile(t, []byte("datos"))

	var ruleErr *rules.Error
	_, err := c.Push(e.ctx, PushInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: file, Comment: "ok"})
	if !errors.As(err, &ruleErr) || ruleErr.Code != rules.CodeInvalidComment {
		t.Fatalf("comentario corto = %v", err)
	}
	_, err = c.Push(e.ctx, PushInput{ProjectID: e.project.ID, RemotePath: "../x.blend", LocalPath: file, Comment: "Comentario"})
	if !errors.As(err, &ruleErr) || ruleErr.Code != rules.CodeInvalidPath {
		t.Fatalf("ruta = %v", err)
	}
	if _, err = c.Push(e.ctx, PushInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: filepath.Join(t.TempDir(), "no-existe"), Comment: "Comentario"}); err == nil {
		t.Fatal("un archivo inexistente debería fallar")
	}
}

// failingTransport corta las subidas de trozos después de un número de ellas, como
// si la red se cayera a mitad de una subida.
type failingTransport struct {
	next      http.RoundTripper
	failAfter int64
	puts      atomic.Int64
	failed    atomic.Int64
}

var errNetworkDown = errors.New("red caída")

func (f *failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPut && f.puts.Add(1) > f.failAfter {
		f.failed.Add(1)
		return nil, errNetworkDown
	}
	return f.next.RoundTrip(r)
}

// TestInterruptedPushResumesWithoutRepeatingChunks es otro criterio de aceptación:
// una subida cortada a la mitad se completa al reintentar sin repetir trozos.
func TestInterruptedPushResumesWithoutRepeatingChunks(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	data := randomBytes(t, 32<<20)
	file := writeFile(t, data)
	in := PushInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: file, Comment: "Primera versión"}

	broken := &failingTransport{next: http.DefaultTransport, failAfter: 10}
	partial, err := e.client(broken).Push(e.ctx, in)
	if !errors.Is(err, errNetworkDown) {
		t.Fatalf("Push = %v, quería un corte", err)
	}
	if partial.UploadedChunks == 0 || partial.UploadedChunks >= partial.TotalChunks {
		t.Fatalf("subida parcial = %+v", partial)
	}
	snap, err := e.svc.GetSnapshot(e.ctx, e.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Assets) != 0 {
		t.Fatal("una subida incompleta no debe crear el archivo")
	}

	resumed, err := e.client(nil).Push(e.ctx, in)
	if err != nil {
		t.Fatalf("reintento: %v", err)
	}
	if resumed.Commit.Version.Number != 1 {
		t.Fatalf("reintento = %+v", resumed)
	}
	// Lo que estaba en tránsito al cortarse puede haber llegado sin que el cliente
	// lo sepa, así que el reintento envía como mucho lo que no se confirmó.
	if resumed.UploadedChunks+partial.UploadedChunks > resumed.TotalChunks {
		t.Fatalf("el reintento envió %d y antes %d, de %d", resumed.UploadedChunks, partial.UploadedChunks, resumed.TotalChunks)
	}
	if resumed.UploadedChunks == 0 {
		t.Fatal("el reintento debería enviar lo que faltaba")
	}

	// Solo pueden repetirse los trozos en tránsito en el momento del corte.
	e.traffic.mu.Lock()
	defer e.traffic.mu.Unlock()
	repeated := 0
	for path, n := range e.traffic.puts {
		if n > 2 {
			t.Errorf("%s subido %d veces", path, n)
		}
		if n == 2 {
			repeated++
		}
	}
	if repeated > maxParallel {
		t.Fatalf("%d trozos repetidos, y solo puede haber %d en tránsito", repeated, maxParallel)
	}

	// El archivo guardado es correcto.
	dst := filepath.Join(t.TempDir(), "descarga.blend")
	if _, err := e.client(nil).Pull(e.ctx, PullInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: dst}); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("el archivo descargado no coincide")
	}
}

// flakyTransport falla una vez de cada pocas peticiones, y también corta respuestas
// ya procesadas por el servidor.
type flakyTransport struct {
	next  http.RoundTripper
	count atomic.Int64
}

func (f *flakyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	n := f.count.Add(1)
	if n%4 == 0 {
		return nil, errNetworkDown
	}
	resp, err := f.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if n%7 == 0 {
		// El servidor ya lo procesó, pero la respuesta no llega al cliente.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return nil, errNetworkDown
	}
	return resp, nil
}

func TestPushAndPullSurviveAnUnreliableNetwork(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	c := e.client(&flakyTransport{next: http.DefaultTransport})
	data := randomBytes(t, 16<<20)
	file := writeFile(t, data)

	var result PushResult
	var err error
	// Solo el paso de leer el estado del proyecto no se reintenta: en un fallo ahí,
	// repetir el comando continúa la subida.
	for range 10 {
		result, err = c.Push(e.ctx, PushInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: file, Comment: "Red inestable"})
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if result.Commit.Asset.HeadVersion != 1 {
		t.Fatalf("head = %d: los reintentos no deben crear versiones de más", result.Commit.Asset.HeadVersion)
	}

	dst := filepath.Join(t.TempDir(), "descarga.blend")
	for range 10 {
		_, err = c.Pull(e.ctx, PullInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: dst})
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("el archivo descargado no coincide")
	}
}

func TestPushDetectsFileChangedDuringUpload(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	data := randomBytes(t, 8<<20)
	file := writeFile(t, data)

	// Cambia el archivo justo después de cortarlo, antes de subir sus trozos.
	transport := &beforeFirstPut{next: http.DefaultTransport, action: func() {
		other := randomBytes(t, 8<<20)
		if err := os.WriteFile(file, other, 0o600); err != nil {
			t.Error(err)
		}
	}}
	_, err := e.client(transport).Push(e.ctx, PushInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: file, Comment: "Cambia a medias"})
	if err == nil {
		t.Fatal("debería detectar el cambio")
	}
	snap, snapErr := e.svc.GetSnapshot(e.ctx, e.project.ID)
	if snapErr != nil {
		t.Fatal(snapErr)
	}
	if len(snap.Assets) != 0 {
		t.Fatal("no debe crear una versión con un archivo que cambió")
	}
}

// beforeFirstPut ejecuta action antes de la consulta de trozos que faltan.
type beforeFirstPut struct {
	next   http.RoundTripper
	once   sync.Once
	action func()
}

func (b *beforeFirstPut) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/api/chunks/missing") {
		b.once.Do(b.action)
	}
	return b.next.RoundTrip(r)
}

func TestPullDownloadsOnlyWhatIsMissing(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	c := e.client(nil)
	data := randomBytes(t, 48<<20)
	e.push(c, data, "Primera versión")

	dst := filepath.Join(t.TempDir(), "carpeta", "camion.blend")
	first, err := c.Pull(e.ctx, PullInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: dst})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if first.DownloadedBytes != first.TotalBytes || first.Version.Number != 1 {
		t.Fatalf("primera = %+v", first)
	}

	// Al día: no descarga nada.
	e.traffic.reset()
	same, err := c.Pull(e.ctx, PullInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: dst})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !same.AlreadyUpToDate || e.traffic.gets.Load() != 0 {
		t.Fatalf("resultado = %+v, descargas = %d", same, e.traffic.gets.Load())
	}

	edited := editInMiddle(t, data)
	e.push(c, edited, "Ajuste en el centro")

	// Sin permiso para sobrescribir, no se toca el archivo local.
	_, err = c.Pull(e.ctx, PullInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: dst})
	if !errors.Is(err, ErrLocalFileDiffers) {
		t.Fatalf("Pull = %v, quiero ErrLocalFileDiffers", err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, data) {
		t.Fatal("no debe tocar el archivo local")
	}

	e.traffic.reset()
	second, err := c.Pull(e.ctx, PullInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: dst, Overwrite: true})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if second.ReusedPercent() < 85 {
		t.Fatalf("reutilizado %.1f %%", second.ReusedPercent())
	}
	if got := int(e.traffic.gets.Load()); got != second.DownloadedChunks {
		t.Fatalf("descargas reales = %d, informadas = %d", got, second.DownloadedChunks)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, edited) {
		t.Fatal("el archivo descargado no coincide")
	}

	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("quedaron temporales: %v", entries)
	}
}

func TestPullFailureLeavesLocalFileAndNoTemp(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	c := e.client(nil)
	data := randomBytes(t, 8<<20)
	e.push(c, data, "Primera versión")

	dst := writeFile(t, []byte("trabajo local"))
	broken := e.client(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/api/chunks/") {
			return nil, errNetworkDown
		}
		return http.DefaultTransport.RoundTrip(r)
	}))
	_, err := broken.Pull(e.ctx, PullInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: dst, Overwrite: true})
	if !errors.Is(err, errNetworkDown) {
		t.Fatalf("Pull = %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "trabajo local" {
		t.Fatal("un fallo no debe dañar el archivo local")
	}
	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("quedaron temporales: %v", entries)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestDamagedChunkIsRejected simula un trozo alterado en tránsito o en el servidor.
func TestDamagedChunkIsRejected(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	c := e.client(nil)
	data := randomBytes(t, 8<<20)
	e.push(c, data, "Primera versión")

	corrupting := e.client(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err != nil || !strings.Contains(r.URL.Path, "/api/chunks/") {
			return resp, err
		}
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader([]byte("contenido alterado")))
		resp.ContentLength = -1
		return resp, nil
	}))
	dst := filepath.Join(t.TempDir(), "camion.blend")
	if _, err := corrupting.Pull(e.ctx, PullInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: dst}); err == nil {
		t.Fatal("debería rechazar un trozo dañado")
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("no debe dejar un archivo dañado")
	}
}

func TestPullUnknownFile(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	var ruleErr *rules.Error
	_, err := e.client(nil).Pull(e.ctx, PullInput{ProjectID: e.project.ID, RemotePath: "no-existe.blend", LocalPath: filepath.Join(t.TempDir(), "x")})
	if !errors.As(err, &ruleErr) || ruleErr.Code != rules.CodeNotFound {
		t.Fatalf("Pull = %v", err)
	}
}

func TestRuleRejectionsAreNotRetried(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	c := e.client(nil)
	result := e.push(c, randomBytes(t, 1<<20), "Primera versión")

	// Otro usuario lo bloquea: el rechazo llega como *rules.Error y sin reintentos.
	other, err := e.svc.CreateUser(e.ctx, 0, vault.NewUserInput{Username: "ana", DisplayName: "Ana", Password: testPassword})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Lock(e.ctx, rules.Actor{UserID: other.ID, ClientID: "pc-2", ClientName: "PC de Ana"}, result.Commit.Asset.ID, false); err != nil {
		t.Fatal(err)
	}

	var commits atomic.Int64
	counting := e.client(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/versions") {
			commits.Add(1)
		}
		return http.DefaultTransport.RoundTrip(r)
	}))
	_, err = counting.Push(e.ctx, PushInput{ProjectID: e.project.ID, RemotePath: remotePath, LocalPath: writeFile(t, randomBytes(t, 1<<20)), Comment: "Bloqueado"})
	var ruleErr *rules.Error
	if !errors.As(err, &ruleErr) || ruleErr.Code != rules.CodeLocked {
		t.Fatalf("Push = %v", err)
	}
	if !strings.Contains(ruleErr.Message, "Bloqueado por Ana") || ruleErr.Lock == nil {
		t.Fatalf("rechazo = %+v", ruleErr)
	}
	if commits.Load() != 1 {
		t.Fatalf("intentos = %d, quiero 1", commits.Load())
	}
}

func TestClientNameWithAccentsSurvivesHeaders(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	c := New(Config{Server: e.url, Token: e.token, ClientID: "pc-1", ClientName: "Portátil de José", RetryDelay: time.Millisecond})
	result := e.push(c, randomBytes(t, 1<<20), "Primera versión")

	lock, err := c.Lock(e.ctx, result.Commit.Asset.ID, false)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if lock.Lock.ClientName != "Portátil de José" {
		t.Fatalf("nombre = %q", lock.Lock.ClientName)
	}
	if err := c.Unlock(e.ctx, result.Commit.Asset.ID); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}

func TestProjectsSnapshotEventsVersions(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	c := e.client(nil)

	projects, err := c.Projects(e.ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("Projects = %+v, %v", projects, err)
	}
	created, err := c.CreateProject(e.ctx, "Otro", "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if created.Name != "Otro" {
		t.Fatalf("proyecto = %+v", created)
	}

	result := e.push(c, randomBytes(t, 1<<20), "Primera versión")
	snap, err := c.Snapshot(e.ctx, e.project.ID)
	if err != nil || len(snap.Assets) != 1 {
		t.Fatalf("Snapshot = %+v, %v", snap, err)
	}

	page, err := c.Events(e.ctx, e.project.ID, 0)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("Events = %+v, %v", page, err)
	}

	versions, err := c.Versions(e.ctx, result.Commit.Asset.ID)
	if err != nil || len(versions) != 1 {
		t.Fatalf("Versions = %+v, %v", versions, err)
	}
	m, err := c.Manifest(e.ctx, versions[0].ID)
	if err != nil || m.FileSHA256.String() != versions[0].FileSHA256 {
		t.Fatalf("Manifest = %+v, %v", m, err)
	}
}

func TestGetChunkTooLargeResponseIsRejected(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	c := e.client(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(make([]byte, chunker.MaxSize+100))),
			Header:     http.Header{},
		}, nil
	}))
	if _, err := c.GetChunk(e.ctx, chunker.Hash{1}); err == nil {
		t.Fatal("debería rechazar una respuesta que no coincide con el hash")
	}
}

func TestRetryStopsWhenCanceled(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	ctx, cancel := context.WithCancel(e.ctx)
	var attempts atomic.Int64
	c := New(Config{
		Server:     e.url,
		Token:      e.token,
		RetryDelay: time.Hour,
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts.Add(1)
			cancel()
			return nil, errNetworkDown
		})},
	})
	if err := c.PutChunk(ctx, chunker.Hash{1}, []byte("x")); err == nil {
		t.Fatal("debería fallar")
	}
	if attempts.Load() != 1 {
		t.Fatalf("intentos = %d", attempts.Load())
	}
}
