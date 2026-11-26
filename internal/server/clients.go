package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"3d-vault/internal/vault"
	"3d-vault/web"
)

// Versiones de los programas de cada PC. Mi carpeta avisa la suya en la cabecera
// X-App-Version y el complemento de Blender en su User-Agent («3D-Vault
// Blender/1.3.0»). Los que no la avisan son de antes de esto: versiones viejas.

const (
	appWeb     = "web"
	appBlender = "blender"
	// clientTouch: cada cuánto se vuelve a anotar un programa que no cambió.
	clientTouch = 5 * time.Minute
	// clientsShown: los programas que no se ven desde hace más ya no se muestran.
	clientsShown = 60 * 24 * time.Hour
)

// blenderAddonVersion es la versión del complemento que entrega este servidor.
var blenderAddonVersion = func() string {
	raw, err := fs.ReadFile(web.Blender, web.BlenderDir+"/blender_manifest.toml")
	if err != nil {
		return ""
	}
	if m := manifestVersion.FindSubmatch(raw); m != nil {
		return string(m[1])
	}
	return ""
}()

// clientSeen recuerda la última anotación de cada programa, para no escribir en
// la base en cada petición.
type clientSeen struct {
	mu   sync.Mutex
	last map[string]seenMark
}

type seenMark struct {
	version, name string
	at            time.Time
}

// trackClient anota qué programa y versión usa la PC que hace la petición.
func (s *Server) trackClient(r *http.Request, user vault.User) {
	clientID := r.Header.Get("X-Client-Id")
	if clientID == "" || len(clientID) > maxClientID {
		return
	}
	ua := r.Header.Get("User-Agent")
	app, version := appWeb, strings.TrimSpace(r.Header.Get("X-App-Version"))
	switch {
	case strings.HasPrefix(ua, "3D-Vault Blender"):
		app, version = appBlender, ""
		if rest, ok := strings.CutPrefix(ua, "3D-Vault Blender/"); ok {
			version, _, _ = strings.Cut(rest, " ")
		}
	case !strings.Contains(ua, "Mozilla/"):
		return // la herramienta de línea de comandos u otro programa
	}
	name, err := url.QueryUnescape(r.Header.Get("X-Client-Name"))
	if err != nil || !utf8.ValidString(name) {
		name = ""
	}
	// Algunas peticiones no traen el nombre: se queda el que ya estaba anotado.
	name = clip(name, maxClientName)
	version = clip(version, 40)

	now := s.now()
	key := strconv.FormatInt(user.ID, 10) + "/" + clientID + "/" + app
	s.clients.mu.Lock()
	prev, ok := s.clients.last[key]
	if name == "" && ok {
		name = prev.name
	}
	fresh := ok && prev.version == version && prev.name == name && now.Sub(prev.at) < clientTouch
	if !fresh {
		if s.clients.last == nil {
			s.clients.last = map[string]seenMark{}
		}
		s.clients.last[key] = seenMark{version: version, name: name, at: now}
	}
	s.clients.mu.Unlock()
	if fresh {
		return
	}
	err = s.svc.RecordClient(r.Context(), vault.SeenClient{
		UserID: user.ID, ClientID: clientID, App: app, ClientName: name,
		Version: version, Browser: browserOf(ua, app), LastSeen: now,
	})
	if err != nil && r.Context().Err() == nil {
		s.log.Warn("no se pudo anotar el programa", slog.Any("error", err))
	}
}

// browserOf resume el User-Agent: «Edge en Windows», «Blender 4.2».
func browserOf(ua, app string) string {
	if app == appBlender {
		// «3D-Vault Blender/1.3.0 (Blender 4.2.3)»
		if _, rest, ok := strings.Cut(ua, "(Blender "); ok {
			if v, _, ok := strings.Cut(rest, ")"); ok {
				return "Blender " + clip(v, 20)
			}
		}
		return "Blender"
	}
	name := "Navegador"
	switch {
	case strings.Contains(ua, "Edg/"):
		name = "Edge"
	case strings.Contains(ua, "OPR/"):
		name = "Opera"
	case strings.Contains(ua, "Chrome/"):
		name = "Chrome"
	case strings.Contains(ua, "Firefox/"):
		name = "Firefox"
	case strings.Contains(ua, "Safari/"):
		name = "Safari"
	}
	switch {
	case strings.Contains(ua, "Windows"):
		name += " en Windows"
	case strings.Contains(ua, "Mac OS"):
		name += " en Mac"
	case strings.Contains(ua, "Linux"):
		name += " en Linux"
	}
	return name
}

// apiVersion dice qué versiones entrega el servidor: Mi carpeta la compara con
// la suya para ofrecer actualizar.
func (s *Server) apiVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"web": appVersion, "blender": blenderAddonVersion})
}

// clientView es un programa tal como se muestra en Mi cuenta y en Versiones.
type clientView struct {
	Who, PC, App, AppLabel, Version, Browser, Ago, Hint string
	Current                                            bool
}

func (s *Server) clientViews(list []vault.SeenClient) []clientView {
	now := s.now()
	out := make([]clientView, 0, len(list))
	for _, c := range list {
		pc := c.ClientName
		if pc == "" {
			pc = "PC sin nombre"
		}
		v := clientView{Who: c.DisplayName, PC: pc, App: c.App, Browser: c.Browser, Ago: agoLong(now.Sub(c.LastSeen))}
		switch c.App {
		case appBlender:
			v.AppLabel = "Complemento de Blender"
			v.Version = c.Version
			if v.Version == "" {
				v.Version = "1.2.0 o anterior"
			}
			v.Current = c.Version == blenderAddonVersion
			if !v.Current {
				v.Hint = "Hay que instalar el complemento nuevo (" + blenderAddonVersion + ") en Blender."
			}
		default:
			v.AppLabel = "Mi carpeta"
			v.Version = shortVersion(c.Version)
			v.Current = c.Version == appVersion
			if !v.Current {
				v.Hint = "Esa ventana tiene Mi carpeta con código viejo. Se actualiza al recargarla (F5) o al pulsar «Buscar actualización»."
			}
		}
		out = append(out, v)
	}
	// Lo desactualizado primero: es lo que hay que resolver.
	sort.SliceStable(out, func(i, j int) bool { return !out[i].Current && out[j].Current })
	return out
}

func shortVersion(v string) string {
	if v == "" {
		return "antigua"
	}
	if len(v) > 7 {
		return v[:7]
	}
	return v
}

func agoLong(d time.Duration) string {
	if d < 24*time.Hour {
		return agoText(d)
	}
	return "hace " + plural(int(d/(24*time.Hour)), "día", "días")
}

type versionsData struct {
	Clients        []clientView
	Outdated       int
	WebVersion     string
	BlenderVersion string
}

func (s *Server) versionsData(r *http.Request, userID int64) (versionsData, error) {
	list, err := s.svc.ListClients(r.Context(), userID, s.now().Add(-clientsShown))
	if err != nil {
		return versionsData{}, err
	}
	d := versionsData{Clients: s.clientViews(list), WebVersion: shortVersion(appVersion), BlenderVersion: blenderAddonVersion}
	for _, c := range d.Clients {
		if !c.Current {
			d.Outdated++
		}
	}
	return d, nil
}

// webVersions muestra al admin la versión de cada programa en cada PC.
func (s *Server) webVersions(w http.ResponseWriter, r *http.Request) {
	data, err := s.versionsData(r, 0)
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "versions", pageData{Title: "Versiones", Active: "versions", Data: data})
}
