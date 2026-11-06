package server

import (
	"errors"
	"net/http"
	"time"

	"3d-vault/internal/rules"
	"3d-vault/internal/vault"
)

// staleLock es la antigüedad a partir de la cual un bloqueo se destaca: quizá
// alguien se olvidó de liberarlo.
const staleLock = 48 * time.Hour

type lockRow struct {
	Asset   vault.Asset
	Project string
	// Days es la antigüedad en días de un bloqueo viejo; 0 si es reciente.
	Days int
}

type locksData struct {
	Rows []lockRow
}

func (s *Server) webLocks(w http.ResponseWriter, r *http.Request) {
	s.renderLocks(w, r, http.StatusOK, "")
}

func (s *Server) renderLocks(w http.ResponseWriter, r *http.Request, status int, errorMessage string) {
	assets, err := s.svc.ListLockedAssets(r.Context())
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	projects, err := s.svc.ListProjects(r.Context())
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	names := make(map[int64]string, len(projects))
	for _, p := range projects {
		names[p.ID] = p.Name
	}

	now := s.now()
	var data locksData
	for _, a := range assets {
		row := lockRow{Asset: a, Project: names[a.ProjectID]}
		if age := now.Sub(a.Lock.LockedAt); age > staleLock {
			row.Days = int(age / (24 * time.Hour))
		}
		data.Rows = append(data.Rows, row)
	}
	s.render(w, r, status, "locks", pageData{Title: "Bloqueos", Active: "locks", Error: errorMessage, Data: data})
}

// webForceUnlock es el desbloqueo forzado de la ficha del archivo, desde la lista:
// queda registrado igual.
func (s *Server) webForceUnlock(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		s.webFail(w, r, err)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	if err := s.svc.Unlock(r.Context(), webActor(r), id, true); err != nil {
		var ruleErr *rules.Error
		if errors.As(err, &ruleErr) {
			s.renderLocks(w, r, statusForCode(ruleErr.Code), ruleErr.Message)
			return
		}
		s.webFail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/locks", http.StatusSeeOther)
}
