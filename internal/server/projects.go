package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"3d-vault/internal/rules"
)

// webDeleteProject borra un proyecto y vuelve a la lista, donde aparece en
// Proyectos borrados. Si no se puede, lo explica en la página del proyecto.
func (s *Server) webDeleteProject(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		s.webFail(w, r, err)
		return
	}
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
	if err := s.svc.DeleteProject(r.Context(), webActor(r), id); err != nil {
		var re *rules.Error
		if errors.As(err, &re) {
			s.renderMessage(w, r, statusForCode(re.Code), "No se pudo borrar "+project.Name, re.Message)
			return
		}
		s.webFail(w, r, err)
		return
	}
	http.Redirect(w, r, "/?borrado="+url.QueryEscape(project.Name), http.StatusSeeOther)
}

// webRestoreProject recupera un proyecto borrado y abre su página.
func (s *Server) webRestoreProject(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		s.webFail(w, r, err)
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	if err := s.svc.RestoreProject(r.Context(), webActor(r), id); err != nil {
		s.webFail(w, r, err)
		return
	}
	http.Redirect(w, r, "/projects/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}
