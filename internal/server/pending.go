package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"3d-vault/internal/rules"
)

// Cambios sin subir: cada PC con Mi carpeta abierta cuenta aquí qué archivos
// tiene nuevos, modificados, borrados o movidos y aún no subió. Así los demás lo
// ven en la web, como el «git status» de cada compañero, antes de pisarse.
//
// Se guarda en memoria a propósito: es un aviso, no un registro. Si el servidor
// se reinicia, cada PC lo vuelve a contar en su siguiente pasada (como mucho un
// minuto con la página abierta).

const (
	maxPendingItems = 2000
	maxPendingPath  = 1000
	// pendingTTL: si una PC deja de avisar (se apagó, cerró la página), su aviso
	// se quita tras este tiempo para no mostrar cambios que quizá ya no existen.
	pendingTTL = 12 * time.Hour
)

// Estados de un archivo en una PC, los mismos que usa Mi carpeta.
var pendingKinds = map[string]struct {
	Label, Class, Mark, Hint string
	Order                    int
}{
	"new":     {"Nuevo", "add", "+", "Está en su PC y aún no lo agregó a 3D-Vault.", 0},
	"changed": {"Modificado", "mod", "~", "Lo cambió en su PC y aún no subió la versión.", 1},
	"missing": {"Borrado", "del", "−", "Lo borró en su PC y aún no confirmó el borrado.", 2},
	"moved":   {"Movido", "mov", "→", "Lo renombró o movió en su PC y aún no lo hizo en el servidor.", 3},
	// Trabajo que no llega a 3D-Vault: lo que más confunde a quien «ya lo subió».
	"conflict": {"Conflicto", "mod", "!", "Su copia chocó con una versión más nueva y quedó aparte en su PC. No se sube sola.", 4},
	"error":    {"No se pudo subir", "del", "×", "Falló al sincronizar en su PC.", 5},
}

type pendingItem struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	// Detail explica un error o de qué archivo es una copia de conflicto.
	Detail string `json:"detail,omitempty"`
}

type pendingReport struct {
	UserID      int64
	DisplayName string
	ClientName  string
	At          time.Time
	Items       []pendingItem
}

type pendingStore struct {
	mu sync.Mutex
	// proyecto → "usuario/PC" → aviso
	byProject map[int64]map[string]pendingReport
}

func pendingKey(userID int64, clientID string) string {
	return strconv.FormatInt(userID, 10) + "/" + clientID
}

func (p *pendingStore) set(projectID int64, key string, rep pendingReport) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byProject == nil {
		p.byProject = map[int64]map[string]pendingReport{}
	}
	if len(rep.Items) == 0 {
		delete(p.byProject[projectID], key)
		return
	}
	if p.byProject[projectID] == nil {
		p.byProject[projectID] = map[string]pendingReport{}
	}
	p.byProject[projectID][key] = rep
}

// list devuelve los avisos vigentes de un proyecto, del más reciente al más viejo.
func (p *pendingStore) list(projectID int64, now time.Time) []pendingReport {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []pendingReport
	for key, rep := range p.byProject[projectID] {
		if now.Sub(rep.At) > pendingTTL {
			delete(p.byProject[projectID], key)
			continue
		}
		out = append(out, rep)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// apiReportPending recibe la lista completa de cambios sin subir de esta PC en
// un proyecto. Una lista vacía quita el aviso.
func (s *Server) apiReportPending(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if _, err := s.svc.GetProject(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}
	actor, err := clientActor(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	user, _ := currentUser(r)

	var body struct {
		Items []pendingItem `json:"items"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, r, &rules.Error{Code: rules.CodeInvalidInput, Message: "La lista de cambios no es válida."})
		return
	}
	if len(body.Items) > maxPendingItems {
		body.Items = body.Items[:maxPendingItems]
	}
	items := make([]pendingItem, 0, len(body.Items))
	for _, it := range body.Items {
		if _, ok := pendingKinds[it.Status]; !ok {
			continue
		}
		if it.Path == "" || len(it.Path) > maxPendingPath || !utf8.ValidString(it.Path) {
			continue
		}
		if len(it.Detail) > 300 || !utf8.ValidString(it.Detail) {
			it.Detail = ""
		}
		items = append(items, it)
	}
	s.pending.set(id, pendingKey(actor.UserID, actor.ClientID), pendingReport{
		UserID:      actor.UserID,
		DisplayName: user.DisplayName,
		ClientName:  actor.ClientName,
		At:          s.now(),
		Items:       items,
	})
	w.WriteHeader(http.StatusNoContent)
}

// pendingMark es un cambio sin subir tal como se muestra en la web.
type pendingMark struct {
	Path, Who, Label, Class, Mark, Hint string
}

// pendingPC agrupa los cambios sin subir de una PC.
type pendingPC struct {
	Who, ClientName, Ago string
	Mine                 bool
	Marks                []pendingMark
	Counts               []pendingMark // un resumen por estado: «3 nuevos»
}

// pendingView prepara los avisos de un proyecto para la página. Descarta lo que
// el servidor ya tiene al día (un «nuevo» que ya se subió, un «borrado» que ya
// no está) para no mostrar avisos viejos entre una pasada y la siguiente.
func (s *Server) pendingView(projectID int64, assetPaths map[string]bool, me int64) ([]pendingPC, map[string][]pendingMark) {
	now := s.now()
	byPath := map[string][]pendingMark{}
	var pcs []pendingPC
	for _, rep := range s.pending.list(projectID, now) {
		pc := pendingPC{Who: rep.DisplayName, ClientName: rep.ClientName, Ago: agoText(now.Sub(rep.At)), Mine: rep.UserID == me}
		counts := map[string]int{}
		for _, it := range rep.Items {
			key := strings.ToLower(it.Path)
			if it.Status == "new" && assetPaths[key] {
				continue
			}
			if it.Status == "missing" && !assetPaths[key] {
				continue
			}
			k := pendingKinds[it.Status]
			who := rep.DisplayName
			if pc.Mine {
				who = "Tú"
			}
			hint := k.Hint
			if it.Detail != "" {
				hint += " " + it.Detail
			}
			mark := pendingMark{Path: it.Path, Who: who, Label: k.Label, Class: k.Class, Mark: k.Mark, Hint: hint}
			pc.Marks = append(pc.Marks, mark)
			counts[it.Status]++
			byPath[key] = append(byPath[key], mark)
		}
		if len(pc.Marks) == 0 {
			continue
		}
		sort.SliceStable(pc.Marks, func(i, j int) bool { return pc.Marks[i].Path < pc.Marks[j].Path })
		for _, status := range []string{"new", "changed", "missing", "moved", "conflict", "error"} {
			if n := counts[status]; n > 0 {
				k := pendingKinds[status]
				pc.Counts = append(pc.Counts, pendingMark{Label: pluralKind(n, status), Class: k.Class, Mark: k.Mark})
			}
		}
		pcs = append(pcs, pc)
	}
	return pcs, byPath
}

func pluralKind(n int, status string) string {
	switch status {
	case "new":
		return plural(n, "nuevo", "nuevos")
	case "changed":
		return plural(n, "modificado", "modificados")
	case "missing":
		return plural(n, "borrado", "borrados")
	case "conflict":
		return plural(n, "conflicto", "conflictos")
	case "error":
		return plural(n, "con error", "con error")
	default:
		return plural(n, "movido", "movidos")
	}
}

func agoText(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "hace un momento"
	case d < time.Hour:
		return "hace " + plural(int(d/time.Minute), "minuto", "minutos")
	default:
		return "hace " + plural(int(d/time.Hour), "hora", "horas")
	}
}
