package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"3d-vault/internal/vault"
)

// Puesta al día: una guía paso a paso para que cada modelador deje su PC con la
// última versión de Mi carpeta y del complemento de Blender, y entienda que
// guardar en la carpeta no es subir. Mientras le falte algo, ve un aviso arriba
// en todas las páginas. El aviso se va solo al terminar o al llegar setupUntil.

// setupUntil es el último día del aviso. Es una variable para las pruebas.
var setupUntil = time.Date(2026, 12, 3, 23, 59, 59, 0, time.FixedZone("Lima", -5*3600))

const (
	noteUploadExplained = "puesta-al-dia/guardar-no-es-subir"
	// noteSynced: Mi carpeta, con la versión actual, pidió la lista de un
	// proyecto. Es decir, ya tiene carpeta elegida y permiso: sincroniza de verdad.
	noteSynced = "puesta-al-dia/mi-carpeta-sincroniza"
)

type setupStatus struct {
	// Mi carpeta abierta en alguna PC con la versión actual.
	Web       bool
	WebDetail string
	// Complemento de Blender conectado con la versión actual. BlenderOld: se vio
	// uno, pero viejo.
	Blender       bool
	BlenderOld    bool
	BlenderDetail string
	// Confirmó que entendió que guardar no es subir.
	Ack bool

	Left  int
	Done  bool
	Until string
}

func (s *Server) setupStatus(clients []vault.SeenClient, acked, synced bool) setupStatus {
	st := setupStatus{Ack: acked, Until: untilText(setupUntil)}
	now := s.now()
	for _, c := range clients {
		switch c.App {
		case appWeb:
			if c.Version == appVersion && synced && !st.Web {
				st.Web = true
				st.WebDetail = "Al día en " + c.ClientName + " (" + c.Browser + "), " + agoLong(now.Sub(c.LastSeen)) + "."
			}
		case appBlender:
			if c.Version == blenderAddonVersion && !st.Blender {
				st.Blender = true
				st.BlenderDetail = "Conectado en " + c.ClientName + " (" + c.Browser + ", complemento " + c.Version + "), " + agoLong(now.Sub(c.LastSeen)) + "."
			} else if c.Version != blenderAddonVersion && !st.Blender && !st.BlenderOld {
				st.BlenderOld = true
				v := c.Version
				if v == "" {
					v = "1.2.0 o anterior"
				}
				st.BlenderDetail = "En " + c.ClientName + " tienes el complemento " + v + ": instala el nuevo (" + blenderAddonVersion + ")."
			}
		}
	}
	if st.Blender {
		st.BlenderOld = false
	}
	if !st.Web && !synced {
		for _, c := range clients {
			if c.App == appWeb && c.Version == appVersion {
				st.WebDetail = "Abriste Mi carpeta en " + c.ClientName + ", pero aún no sincroniza: elige tu carpeta y pulsa Permitir y sincronizar."
				break
			}
		}
	}
	for _, ok := range []bool{st.Web, st.Blender, st.Ack} {
		if !ok {
			st.Left++
		}
	}
	st.Done = st.Left == 0
	return st
}

func (s *Server) setupFor(ctx context.Context, userID int64) (setupStatus, error) {
	clients, err := s.svc.ListClients(ctx, userID, s.now().Add(-clientsShown))
	if err != nil {
		return setupStatus{}, err
	}
	acked, err := s.svc.NoteUsers(ctx, noteUploadExplained)
	if err != nil {
		return setupStatus{}, err
	}
	synced, err := s.svc.NoteUsers(ctx, noteSynced)
	if err != nil {
		return setupStatus{}, err
	}
	_, ok := acked[userID]
	_, sy := synced[userID]
	return s.setupStatus(clients, ok, sy), nil
}

// setupActive dice si el aviso sigue vigente para esta persona. Los admins no lo
// ven: la guía es para los modeladores.
func (s *Server) setupActive(user vault.User) bool {
	return !user.IsAdmin && s.now().Before(setupUntil)
}

func untilText(t time.Time) string {
	days := []string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"}
	months := []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}
	return days[t.Weekday()] + " " + strconv.Itoa(t.Day()) + " de " + months[t.Month()-1]
}

func (s *Server) webSetup(w http.ResponseWriter, r *http.Request) {
	user, _ := currentUser(r)
	st, err := s.setupFor(r.Context(), user.ID)
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "setup", pageData{Title: "Ponerme al día", Active: "setup", Data: struct {
		Status         setupStatus
		BlenderVersion string
	}{st, blenderAddonVersion}})
}

// apiSetup da el estado de la guía: la página lo consulta cada pocos segundos
// para marcar cada paso apenas se cumple.
func (s *Server) apiSetup(w http.ResponseWriter, r *http.Request) {
	user, _ := currentUser(r)
	st, err := s.setupFor(r.Context(), user.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"web": st.Web, "web_detail": st.WebDetail,
		"blender": st.Blender, "blender_old": st.BlenderOld, "blender_detail": st.BlenderDetail,
		"ack": st.Ack, "left": st.Left, "done": st.Done,
	})
}

func (s *Server) apiSetupAck(w http.ResponseWriter, r *http.Request) {
	user, _ := currentUser(r)
	if err := s.svc.MarkNote(r.Context(), user.ID, noteUploadExplained); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setupRow es una persona en la tabla de seguimiento del admin.
type setupRow struct {
	Who string
	setupStatus
}

// setupOverview da el avance de la guía de cada modelador activo.
func (s *Server) setupOverview(ctx context.Context) ([]setupRow, int, error) {
	users, err := s.svc.ListUsers(ctx)
	if err != nil {
		return nil, 0, err
	}
	clients, err := s.svc.ListClients(ctx, 0, s.now().Add(-clientsShown))
	if err != nil {
		return nil, 0, err
	}
	acked, err := s.svc.NoteUsers(ctx, noteUploadExplained)
	if err != nil {
		return nil, 0, err
	}
	synced, err := s.svc.NoteUsers(ctx, noteSynced)
	if err != nil {
		return nil, 0, err
	}
	byUser := map[int64][]vault.SeenClient{}
	for _, c := range clients {
		byUser[c.UserID] = append(byUser[c.UserID], c)
	}
	var rows []setupRow
	done := 0
	for _, u := range users {
		if u.IsAdmin || !u.IsActive {
			continue
		}
		_, ok := acked[u.ID]
		_, sy := synced[u.ID]
		st := s.setupStatus(byUser[u.ID], ok, sy)
		if st.Done {
			done++
		}
		rows = append(rows, setupRow{Who: u.DisplayName + " (" + u.Username + ")", setupStatus: st})
	}
	return rows, done, nil
}
