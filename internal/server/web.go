package server

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"3d-vault/internal/rules"
	"3d-vault/internal/vault"
	"3d-vault/web"
)

const (
	webClientID   = "web"
	webClientName = "Navegador"

	maxFormBody = 1 << 20

	maxTreeDepth = 6

	stateLocked = "bloqueados"
	stateMine   = "mios"
)

type pageSet struct {
	templates map[string]*template.Template
}

func loadPages() (*pageSet, error) {
	funcs := template.FuncMap{
		"formatBytes":   formatBytes,
		"formatPercent": formatPercent,
		"when":          func(t time.Time) string { return formatWhen(t, time.Now()) },
	}
	set := &pageSet{templates: make(map[string]*template.Template)}
	for _, name := range []string{"login", "projects", "project", "asset", "users", "storage", "message", "sync"} {
		tmpl, err := template.New("layout.html").Funcs(funcs).ParseFS(web.Files,
			"server/templates/layout.html", "server/templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("cargar la plantilla %s: %w", name, err)
		}
		set.templates[name] = tmpl
	}
	return set, nil
}

// formatBytes usa unidades binarias con el nombre que muestra Windows.
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
	value := strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64)
	return strings.Replace(value, ".", ",", 1) + " " + string("KMGT"[exp]) + "B"
}

func formatPercent(p float64) string {
	return strings.Replace(strconv.FormatFloat(p, 'f', 1, 64), ".", ",", 1) + " %"
}

// formatWhen muestra la fecha en palabras cuando es reciente.
func formatWhen(t, now time.Time) string {
	t = t.Local()
	now = now.Local()
	day := func(x time.Time) time.Time { return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, x.Location()) }
	switch day(now).Sub(day(t)) {
	case 0:
		return "hoy, " + t.Format("15:04")
	case 24 * time.Hour:
		return "ayer, " + t.Format("15:04")
	}
	return t.Format("02/01/2006 15:04")
}

func plural(n int, singular, many string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + many
}

type pageData struct {
	Title         string
	User          *vault.User
	Projects      []vault.Project
	Active        string
	ActiveProject int64
	Error         string
	Data          any
}

// render ejecuta la plantilla en memoria para no enviar una página a medias.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, page pageData) {
	if user, ok := currentUser(r); ok {
		page.User = &user
		projects, err := s.svc.ListProjects(r.Context())
		if err != nil {
			s.webFail(w, r, err)
			return
		}
		page.Projects = projects
	}

	var buf bytes.Buffer
	if err := s.pages.templates[name].Execute(&buf, page); err != nil {
		s.log.ErrorContext(r.Context(), "error al generar una página", slog.String("plantilla", name), slog.Any("error", err))
		http.Error(w, "Error interno.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

type messageData struct {
	Message string
}

func (s *Server) renderMessage(w http.ResponseWriter, r *http.Request, status int, title, message string) {
	s.render(w, r, status, "message", pageData{Title: title, Data: messageData{Message: message}})
}

// webFail muestra un error que no se puede corregir desde la página en curso.
func (s *Server) webFail(w http.ResponseWriter, r *http.Request, err error) {
	var ruleErr *rules.Error
	if errors.As(err, &ruleErr) {
		title := "No se pudo completar"
		if ruleErr.Code == rules.CodeNotFound {
			title = "No encontrado"
		}
		// Sin usuario no se puede pedir el menú: evita volver a fallar aquí.
		s.renderBare(w, statusForCode(ruleErr.Code), title, ruleErr.Message)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	s.log.ErrorContext(r.Context(), "error en una página", slog.String("ruta", r.URL.Path), slog.Any("error", err))
	s.renderBare(w, http.StatusInternalServerError, "Error interno", "Inténtalo de nuevo. Si continúa, avisa al administrador.")
}

func (s *Server) renderBare(w http.ResponseWriter, status int, title, message string) {
	var buf bytes.Buffer
	page := pageData{Title: title, Data: messageData{Message: message}}
	if err := s.pages.templates["message"].Execute(&buf, page); err != nil {
		http.Error(w, message, status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) webRoutes(r chi.Router) {
	static, err := fs.Sub(web.Files, "server/static")
	if err != nil {
		panic(err) // Los archivos van incluidos en el binario: no puede fallar.
	}
	files := http.StripPrefix("/static/", http.FileServerFS(static))
	r.Get("/static/*", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		files.ServeHTTP(w, r)
	})

	r.Get("/login", s.webLoginForm)
	r.Post("/login", s.webLogin)

	r.Group(func(r chi.Router) {
		r.Use(s.requireWebUser)

		r.Post("/logout", s.webLogout)
		r.Get("/", s.webProjects)
		r.Get("/sync", s.webSync)
		r.Get("/blender/3dvault.zip", s.blenderAddon)
		r.Post("/projects", s.webCreateProject)
		r.Get("/projects/{id}", s.webProject)
		r.Get("/assets/{id}", s.webAsset)
		r.Post("/assets/{id}/lock", s.webLock)
		r.Post("/assets/{id}/unlock", s.webUnlock)
		r.Post("/assets/{id}/restore/{n}", s.webRestore)
		r.Get("/versions/{id}/download", s.streamVersion)
		r.Get("/versions/{id}/thumb", s.webThumb)
		r.Get("/versions/{id}/image", s.webImage)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAdmin)
			r.Get("/admin/users", s.webUsers)
			r.Post("/admin/users", s.webCreateUser)
			r.Post("/admin/users/{id}/active", s.webSetActive)
			r.Post("/admin/users/password", s.webSetPassword)
			r.Get("/admin/storage", s.webStorage)
		})
	})
}

func (s *Server) requireWebUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := currentUser(r); !ok {
			target := "/login"
			if r.Method == http.MethodGet && r.URL.Path != "/" {
				target += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, _ := currentUser(r); !user.IsAdmin {
			s.renderMessage(w, r, http.StatusForbidden, "Sin permiso", "Esta página es solo para administradores.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		return &rules.Error{Code: rules.CodeInvalidInput, Message: "El formulario no es válido."}
	}
	return nil
}

func webActor(r *http.Request) rules.Actor {
	user, _ := currentUser(r)
	return rules.Actor{UserID: user.ID, IsAdmin: user.IsAdmin, ClientID: webClientID, ClientName: webClientName}
}

// webSync es la página "Mi carpeta": la sincronización la hace el navegador
// con la API, así que la plantilla solo trae el esqueleto.
func (s *Server) webSync(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "sync", pageData{Title: "Mi carpeta", Active: "sync"})
}

type loginData struct {
	Username string
}

func (s *Server) webLoginForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := currentUser(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login", pageData{Title: "Entrar", Data: loginData{}})
}

func (s *Server) webLogin(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		s.webFail(w, r, err)
		return
	}
	username := r.PostForm.Get("username")
	user, err := s.login(r, username, r.PostForm.Get("password"))
	if err != nil {
		status := http.StatusUnauthorized
		switch {
		case errors.Is(err, errTooManyAttempts):
			status = http.StatusTooManyRequests
		case !errors.Is(err, errBadCredentials):
			s.webFail(w, r, err)
			return
		}
		s.render(w, r, status, "login", pageData{Title: "Entrar", Error: err.Error(), Data: loginData{Username: username}})
		return
	}
	s.setSessionCookie(w, user)
	http.Redirect(w, r, localPath(r.URL.Query().Get("next")), http.StatusSeeOther)
}

// localPath acepta solo rutas de este sitio, para que next no lleve a otro.
func localPath(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return "/"
	}
	return next
}

func (s *Server) webLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

type projectsData struct {
	Summary         string
	Projects        []vault.ProjectSummary
	FormName        string
	FormDescription string
}

func (s *Server) webProjects(w http.ResponseWriter, r *http.Request) {
	s.renderProjects(w, r, http.StatusOK, "", projectsData{})
}

func (s *Server) renderProjects(w http.ResponseWriter, r *http.Request, status int, errorMessage string, data projectsData) {
	summaries, err := s.svc.ProjectSummaries(r.Context())
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	data.Projects = summaries

	var files, locked int
	var total int64
	for _, p := range summaries {
		files += p.Assets
		locked += p.Locked
		total += p.TotalBytes
	}
	data.Summary = fmt.Sprintf("%s con %s, %s en sus últimas versiones. %s.",
		plural(len(summaries), "proyecto", "proyectos"),
		plural(files, "archivo", "archivos"),
		formatBytes(total),
		lockSentence(locked))

	s.render(w, r, status, "projects", pageData{Title: "Proyectos", Active: "projects", Error: errorMessage, Data: data})
}

func (s *Server) webCreateProject(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		s.webFail(w, r, err)
		return
	}
	name, description := r.PostForm.Get("name"), r.PostForm.Get("description")
	project, err := s.svc.CreateProject(r.Context(), webActor(r), name, description)
	if err != nil {
		var ruleErr *rules.Error
		if errors.As(err, &ruleErr) {
			s.renderProjects(w, r, statusForCode(ruleErr.Code), ruleErr.Message, projectsData{FormName: name, FormDescription: description})
			return
		}
		s.webFail(w, r, err)
		return
	}
	http.Redirect(w, r, "/projects/"+strconv.FormatInt(project.ID, 10), http.StatusSeeOther)
}

type tabData struct {
	Label    string
	Href     string
	Count    int
	Selected bool
}

type treeRow struct {
	Folder bool
	Name   string
	Depth  int
	Asset  vault.Asset
}

type projectData struct {
	Project      vault.Project
	Summary      string
	Tabs         []tabData
	State        string
	Query        string
	Rows         []treeRow
	EmptyMessage string
}

func (s *Server) webProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	project, err := s.svc.GetProject(r.Context(), id)
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	snap, err := s.svc.GetSnapshot(r.Context(), id)
	if err != nil {
		s.webFail(w, r, err)
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	state := r.URL.Query().Get("estado")
	if state != stateLocked && state != stateMine {
		state = ""
	}
	user, _ := currentUser(r)

	// Los contadores siguen a la búsqueda: dicen cuántos verás al cambiar de pestaña.
	var matching, locked, mine []vault.Asset
	var totalBytes int64
	totalLocked := 0
	needle := strings.ToLower(query)
	for _, asset := range snap.Assets {
		totalBytes += asset.SizeBytes
		if asset.Lock != nil {
			totalLocked++
		}
		if needle != "" && !strings.Contains(strings.ToLower(asset.Path), needle) {
			continue
		}
		matching = append(matching, asset)
		if asset.Lock != nil {
			locked = append(locked, asset)
			if asset.Lock.UserID == user.ID {
				mine = append(mine, asset)
			}
		}
	}

	shown := matching
	switch state {
	case stateLocked:
		shown = locked
	case stateMine:
		shown = mine
	}

	tabHref := func(tabState string) string {
		values := url.Values{}
		if tabState != "" {
			values.Set("estado", tabState)
		}
		if query != "" {
			values.Set("q", query)
		}
		href := "/projects/" + strconv.FormatInt(id, 10)
		if encoded := values.Encode(); encoded != "" {
			href += "?" + encoded
		}
		return href
	}

	data := projectData{
		Project: project,
		Summary: fmt.Sprintf("%s, %s. %s.", plural(len(snap.Assets), "archivo", "archivos"), formatBytes(totalBytes),
			lockSentence(totalLocked)),
		Tabs: []tabData{
			{Label: "Todos", Href: tabHref(""), Count: len(matching), Selected: state == ""},
			{Label: "Bloqueados", Href: tabHref(stateLocked), Count: len(locked), Selected: state == stateLocked},
			{Label: "Bloqueados por mí", Href: tabHref(stateMine), Count: len(mine), Selected: state == stateMine},
		},
		State: state,
		Query: query,
		Rows:  buildTree(shown),
	}
	switch {
	case len(snap.Assets) == 0:
		data.EmptyMessage = "Este proyecto aún no tiene archivos. Los modeladores los agregan desde su carpeta sincronizada."
	case query != "":
		data.EmptyMessage = "Ningún archivo coincide con esa búsqueda."
	case state == stateMine:
		data.EmptyMessage = "No tienes archivos bloqueados en este proyecto."
	default:
		data.EmptyMessage = "No hay archivos bloqueados en este proyecto."
	}

	s.render(w, r, http.StatusOK, "project", pageData{Title: project.Name, ActiveProject: id, Data: data})
}

func lockSentence(locked int) string {
	switch locked {
	case 0:
		return "Ninguno bloqueado"
	case 1:
		return "1 bloqueado"
	}
	return strconv.Itoa(locked) + " bloqueados"
}

// buildTree convierte rutas ordenadas en filas con sus carpetas. Una carpeta solo
// aparece cuando cambia respecto al archivo anterior.
func buildTree(assets []vault.Asset) []treeRow {
	rows := make([]treeRow, 0, len(assets))
	var current []string
	for _, asset := range assets {
		parts := strings.Split(asset.Path, "/")
		dirs, name := parts[:len(parts)-1], parts[len(parts)-1]

		common := 0
		for common < len(dirs) && common < len(current) && dirs[common] == current[common] {
			common++
		}
		for i := common; i < len(dirs); i++ {
			rows = append(rows, treeRow{Folder: true, Name: dirs[i], Depth: min(i, maxTreeDepth)})
		}
		current = dirs
		rows = append(rows, treeRow{Name: name, Depth: min(len(dirs), maxTreeDepth), Asset: asset})
	}
	return rows
}

type assetData struct {
	Project     vault.Project
	Asset       vault.Asset
	FileName    string
	Versions    []vault.VersionDetail
	HistoryNote string
	CanRelease  bool
	CanForce    bool
	Preview     *preview
}

func (s *Server) webAsset(w http.ResponseWriter, r *http.Request) {
	s.renderAsset(w, r, http.StatusOK, "")
}

func (s *Server) renderAsset(w http.ResponseWriter, r *http.Request, status int, errorMessage string) {
	id, err := pathID(r, "id")
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	asset, err := s.svc.GetAsset(r.Context(), id)
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	project, err := s.svc.GetProject(r.Context(), asset.ProjectID)
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	versions, err := s.svc.ListVersionDetails(r.Context(), id)
	if err != nil {
		s.webFail(w, r, err)
		return
	}

	user, _ := currentUser(r)
	data := assetData{
		Project:     project,
		Asset:       asset,
		FileName:    path.Base(asset.Path),
		Versions:    versions,
		HistoryNote: historyNote(versions, asset.HeadVersion),
		CanRelease:  asset.Lock != nil && asset.Lock.UserID == user.ID,
		CanForce:    asset.Lock != nil && asset.Lock.UserID != user.ID && user.IsAdmin,
		Preview:     s.assetPreview(r.Context(), asset),
	}
	s.render(w, r, status, "asset", pageData{Title: data.FileName, ActiveProject: asset.ProjectID, Error: errorMessage, Data: data})
}

// historyNote resume lo que costó en transferencia la última versión.
func historyNote(versions []vault.VersionDetail, head int64) string {
	count := plural(int(head), "versión", "versiones")
	if len(versions) == 0 || head < 2 {
		return count + "."
	}
	latest := versions[0]
	return fmt.Sprintf("%s. Para la v%d solo viajaron %s de %s.", count, latest.Number, formatBytes(latest.ChangedBytes), formatBytes(latest.SizeBytes))
}

// assetAction ejecuta una acción de la ficha y vuelve a mostrarla. Si una regla la
// rechaza, muestra el motivo en la misma ficha.
func (s *Server) assetAction(w http.ResponseWriter, r *http.Request, action func(id int64) error) {
	if err := parseForm(w, r); err != nil {
		s.webFail(w, r, err)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	if err := action(id); err != nil {
		var ruleErr *rules.Error
		if errors.As(err, &ruleErr) && ruleErr.Code != rules.CodeNotFound {
			s.renderAsset(w, r, statusForCode(ruleErr.Code), ruleErr.Message)
			return
		}
		s.webFail(w, r, err)
		return
	}
	http.Redirect(w, r, "/assets/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) webLock(w http.ResponseWriter, r *http.Request) {
	s.assetAction(w, r, func(id int64) error {
		_, err := s.svc.Lock(r.Context(), webActor(r), id, false)
		return err
	})
}

func (s *Server) webUnlock(w http.ResponseWriter, r *http.Request) {
	s.assetAction(w, r, func(id int64) error {
		return s.svc.Unlock(r.Context(), webActor(r), id, r.PostForm.Get("force") == "1")
	})
}

func (s *Server) webRestore(w http.ResponseWriter, r *http.Request) {
	s.assetAction(w, r, func(id int64) error {
		number, err := pathID(r, "n")
		if err != nil {
			return err
		}
		base, err := parseSince(r.PostForm.Get("base_version"))
		if err != nil {
			return err
		}
		_, err = s.svc.Restore(r.Context(), webActor(r), id, number, base)
		return err
	})
}

type usersData struct {
	Summary         string
	Users           []vault.User
	FormUsername    string
	FormDisplayName string
	FormAdmin       bool
}

func (s *Server) webUsers(w http.ResponseWriter, r *http.Request) {
	s.renderUsers(w, r, http.StatusOK, "", usersData{})
}

func (s *Server) renderUsers(w http.ResponseWriter, r *http.Request, status int, errorMessage string, data usersData) {
	users, err := s.svc.ListUsers(r.Context())
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	data.Users = users
	active, admins := 0, 0
	for _, u := range users {
		if u.IsActive {
			active++
			if u.IsAdmin {
				admins++
			}
		}
	}
	data.Summary = fmt.Sprintf("%s, %s.", plural(active, "cuenta activa", "cuentas activas"), plural(admins, "administrador", "administradores"))
	s.render(w, r, status, "users", pageData{Title: "Usuarios", Active: "users", Error: errorMessage, Data: data})
}

// userAction ejecuta una acción de administración y vuelve a la lista.
func (s *Server) userAction(w http.ResponseWriter, r *http.Request, keep usersData, action func() error) {
	if err := action(); err != nil {
		var ruleErr *rules.Error
		if errors.As(err, &ruleErr) {
			s.renderUsers(w, r, statusForCode(ruleErr.Code), ruleErr.Message, keep)
			return
		}
		s.webFail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) webCreateUser(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		s.webFail(w, r, err)
		return
	}
	user, _ := currentUser(r)
	in := vault.NewUserInput{
		Username:    r.PostForm.Get("username"),
		DisplayName: r.PostForm.Get("display_name"),
		Password:    r.PostForm.Get("password"),
		IsAdmin:     r.PostForm.Get("is_admin") == "1",
	}
	// La contraseña no se devuelve al formulario.
	keep := usersData{FormUsername: in.Username, FormDisplayName: in.DisplayName, FormAdmin: in.IsAdmin}
	s.userAction(w, r, keep, func() error {
		_, err := s.svc.CreateUser(r.Context(), user.ID, in)
		return err
	})
}

func (s *Server) webSetActive(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		s.webFail(w, r, err)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	user, _ := currentUser(r)
	s.userAction(w, r, usersData{}, func() error {
		return s.svc.SetActive(r.Context(), user.ID, id, r.PostForm.Get("active") == "1")
	})
}

func (s *Server) webSetPassword(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		s.webFail(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PostForm.Get("user_id"), 10, 64)
	if err != nil {
		s.webFail(w, r, &rules.Error{Code: rules.CodeNotFound, Message: "El usuario no existe."})
		return
	}
	user, _ := currentUser(r)
	s.userAction(w, r, usersData{}, func() error {
		return s.svc.SetPassword(r.Context(), user.ID, id, r.PostForm.Get("password"))
	})
}
