// Comando 3dvault-sync. En la Fase 1 es la herramienta de línea de comandos para
// validar el protocolo; en la Fase 2 será el agente con ícono en la bandeja.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode"

	"golang.org/x/term"

	"3d-vault/internal/client"
	"3d-vault/internal/rules"
	"3d-vault/internal/vault"
)

const usage = `Uso: 3dvault-sync <comando>

Comandos:
  login --server URL --user USUARIO     conecta esta PC (no guarda la contraseña)
  projects                              lista los proyectos
  create-project NOMBRE                 crea un proyecto
  push --project P --path RUTA --comment C [--release] ARCHIVO
                                        sube un archivo enviando solo lo que cambió
  pull --project P --path RUTA [--overwrite] DESTINO
                                        descarga un archivo usando lo que ya hay en DESTINO
  lock --project P --path RUTA [--takeover]
  unlock --project P --path RUTA
  history --project P --path RUTA

P es el nombre o el número del proyecto.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return nil
	}
	command, args := args[0], args[1:]
	switch command {
	case "login":
		return login(ctx, args, stdin, out)
	case "projects":
		return projects(ctx, out)
	case "create-project":
		return createProject(ctx, args, out)
	case "push":
		return push(ctx, args, out)
	case "pull":
		return pull(ctx, args, out)
	case "lock":
		return lock(ctx, args, out)
	case "unlock":
		return unlock(ctx, args, out)
	case "history":
		return history(ctx, args, out)
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return nil
	default:
		return fmt.Errorf("comando desconocido %q\n\n%s", command, usage)
	}
}

// settings es lo que se guarda tras conectar. No incluye la contraseña.
type settings struct {
	Server     string `json:"server"`
	Token      string `json:"token"`
	ClientID   string `json:"client_id"`
	ClientName string `json:"client_name"`
}

// settingsPath puede cambiarse con VAULT3D_CONFIG. El nombre no empieza por dígito
// porque las variables de entorno no pueden hacerlo.
func settingsPath() (string, error) {
	if path := os.Getenv("VAULT3D_CONFIG"); path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("buscar la carpeta de configuración: %w", err)
	}
	return filepath.Join(dir, "3D-Vault", "cli.json"), nil
}

func loadSettings() (settings, error) {
	path, err := settingsPath()
	if err != nil {
		return settings{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return settings{}, errors.New("esta PC no está conectada: usa primero el comando login")
	}
	if err != nil {
		return settings{}, fmt.Errorf("leer la configuración: %w", err)
	}
	var s settings
	if err := json.Unmarshal(raw, &s); err != nil {
		return settings{}, fmt.Errorf("leer la configuración: %w", err)
	}
	return s, nil
}

func saveSettings(s settings) error {
	path, err := settingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("crear la carpeta de configuración: %w", err)
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("codificar la configuración: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("guardar la configuración: %w", err)
	}
	return nil
}

func newClient() (*client.Client, error) {
	s, err := loadSettings()
	if err != nil {
		return nil, err
	}
	return client.New(client.Config{Server: s.Server, Token: s.Token, ClientID: s.ClientID, ClientName: s.ClientName}), nil
}

func login(ctx context.Context, args []string, stdin io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	server := flags.String("server", "", "dirección del servidor, por ejemplo https://3dvault.miempresa.com")
	user := flags.String("user", "", "nombre de usuario")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *server == "" || *user == "" {
		return errors.New("faltan --server o --user")
	}
	password, err := readPassword(stdin)
	if err != nil {
		return err
	}

	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "PC"
	}
	clientID, err := newClientID()
	if err != nil {
		return err
	}
	token, account, err := client.New(client.Config{Server: *server}).Login(ctx, *user, password, hostname)
	if err != nil {
		return err
	}
	if err := saveSettings(settings{Server: *server, Token: token, ClientID: clientID, ClientName: hostname}); err != nil {
		return err
	}
	fmt.Fprintf(out, "Conectado como %s en %s.\n", account.DisplayName, *server)
	return nil
}

func newClientID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generar el identificador de la PC: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func readPassword(stdin io.Reader) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(os.Stderr, "Contraseña: ")
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("leer la contraseña: %w", err)
		}
		return string(raw), nil
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no se recibió ninguna contraseña por la entrada estándar")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func projects(ctx context.Context, out io.Writer) error {
	c, err := newClient()
	if err != nil {
		return err
	}
	list, err := c.Projects(ctx)
	if err != nil {
		return err
	}
	for _, p := range list {
		fmt.Fprintf(out, "%d\t%s\n", p.ID, p.Name)
	}
	return nil
}

func createProject(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("uso: create-project NOMBRE")
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	project, err := c.CreateProject(ctx, args[0], "")
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Proyecto %d creado: %s\n", project.ID, project.Name)
	return nil
}

// resolveProject acepta el número o el nombre de un proyecto.
func resolveProject(ctx context.Context, c *client.Client, ref string) (int64, error) {
	if ref == "" {
		return 0, errors.New("falta --project")
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil && id > 0 && strings.IndexFunc(ref, func(r rune) bool { return !unicode.IsDigit(r) }) < 0 {
		return id, nil
	}
	list, err := c.Projects(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range list {
		if strings.EqualFold(p.Name, ref) {
			return p.ID, nil
		}
	}
	return 0, fmt.Errorf("no existe el proyecto %q", ref)
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func push(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("push", flag.ContinueOnError)
	project := flags.String("project", "", "proyecto")
	path := flags.String("path", "", "ruta dentro del proyecto")
	comment := flags.String("comment", "", "comentario de la versión")
	release := flags.Bool("release", false, "libera el bloqueo al terminar")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 || *path == "" {
		return errors.New("uso: push --project P --path RUTA --comment C ARCHIVO")
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	projectID, err := resolveProject(ctx, c, *project)
	if err != nil {
		return err
	}

	result, err := c.Push(ctx, client.PushInput{
		ProjectID:   projectID,
		RemotePath:  *path,
		LocalPath:   flags.Arg(0),
		Comment:     *comment,
		ReleaseLock: *release,
	})
	if err != nil {
		// Las cifras finales todavía no significan nada: no se confirmó ninguna versión.
		if result.UploadedChunks > 0 {
			fmt.Fprintf(out, "Subida incompleta: %d de %d trozos enviados. Repite el comando para continuar.\n",
				result.UploadedChunks, result.TotalChunks)
		}
		return describeError(err)
	}
	fmt.Fprintf(out, "Trozos: %d en total, %d enviados.\n", result.TotalChunks, result.UploadedChunks)
	fmt.Fprintf(out, "Datos: %s en total, %s enviados, %.1f %% reutilizado.\n",
		formatBytes(result.TotalBytes), formatBytes(result.UploadedBytes), result.ReusedPercent())
	if result.Commit.NoChanges {
		fmt.Fprintf(out, "No hay cambios: sigue en v%d.\n", result.Commit.Asset.HeadVersion)
		return nil
	}
	fmt.Fprintf(out, "Versión v%d creada.\n", result.Commit.Version.Number)
	return nil
}

// describeError traduce una cancelación pedida por el usuario.
func describeError(err error) error {
	if errors.Is(err, context.Canceled) {
		return errors.New("operación cancelada")
	}
	return err
}

func pull(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("pull", flag.ContinueOnError)
	project := flags.String("project", "", "proyecto")
	path := flags.String("path", "", "ruta dentro del proyecto")
	overwrite := flags.Bool("overwrite", false, "reemplaza un archivo local distinto")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 || *path == "" {
		return errors.New("uso: pull --project P --path RUTA DESTINO")
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	projectID, err := resolveProject(ctx, c, *project)
	if err != nil {
		return err
	}

	result, err := c.Pull(ctx, client.PullInput{ProjectID: projectID, RemotePath: *path, LocalPath: flags.Arg(0), Overwrite: *overwrite})
	if err != nil {
		return describeError(err)
	}
	if result.AlreadyUpToDate {
		fmt.Fprintf(out, "Ya está al día en v%d.\n", result.Version.Number)
		return nil
	}
	fmt.Fprintf(out, "Trozos: %d en total, %d descargados.\n", result.TotalChunks, result.DownloadedChunks)
	fmt.Fprintf(out, "Datos: %s en total, %s descargados, %.1f %% reutilizado.\n",
		formatBytes(result.TotalBytes), formatBytes(result.DownloadedBytes), result.ReusedPercent())
	fmt.Fprintf(out, "Archivo actualizado a v%d.\n", result.Version.Number)
	return nil
}

// assetByPath busca un archivo por su ruta.
func assetByPath(ctx context.Context, c *client.Client, flags *flag.FlagSet, project, path string) (vault.Asset, error) {
	if path == "" {
		return vault.Asset{}, fmt.Errorf("falta --path en %s", flags.Name())
	}
	projectID, err := resolveProject(ctx, c, project)
	if err != nil {
		return vault.Asset{}, err
	}
	want, err := rules.NormalizePath(path)
	if err != nil {
		return vault.Asset{}, err
	}
	snap, err := c.Snapshot(ctx, projectID)
	if err != nil {
		return vault.Asset{}, err
	}
	for _, asset := range snap.Assets {
		if got, err := rules.NormalizePath(asset.Path); err == nil && got.Key == want.Key {
			return asset, nil
		}
	}
	return vault.Asset{}, fmt.Errorf("no existe el archivo %q", path)
}

func lock(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("lock", flag.ContinueOnError)
	project := flags.String("project", "", "proyecto")
	path := flags.String("path", "", "ruta dentro del proyecto")
	takeover := flags.Bool("takeover", false, "mueve a esta PC un bloqueo tuyo de otra PC")
	if err := flags.Parse(args); err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	asset, err := assetByPath(ctx, c, flags, *project, *path)
	if err != nil {
		return err
	}
	result, err := c.Lock(ctx, asset.ID, *takeover)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Bloqueado en v%d.\n", result.HeadVersion)
	return nil
}

func unlock(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("unlock", flag.ContinueOnError)
	project := flags.String("project", "", "proyecto")
	path := flags.String("path", "", "ruta dentro del proyecto")
	if err := flags.Parse(args); err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	asset, err := assetByPath(ctx, c, flags, *project, *path)
	if err != nil {
		return err
	}
	if err := c.Unlock(ctx, asset.ID); err != nil {
		return err
	}
	fmt.Fprintln(out, "Liberado.")
	return nil
}

func history(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("history", flag.ContinueOnError)
	project := flags.String("project", "", "proyecto")
	path := flags.String("path", "", "ruta dentro del proyecto")
	if err := flags.Parse(args); err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	asset, err := assetByPath(ctx, c, flags, *project, *path)
	if err != nil {
		return err
	}
	versions, err := c.Versions(ctx, asset.ID)
	if err != nil {
		return err
	}
	for _, v := range versions {
		fmt.Fprintf(out, "v%d\t%s\t%s\t%s\t%s\n", v.Number, v.CreatedAt.Format("2006-01-02 15:04"), v.UploadedBy, formatBytes(v.SizeBytes), v.Comment)
	}
	return nil
}
