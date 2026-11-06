package server

import (
	"errors"
	"net/http"
	"strconv"

	"3d-vault/internal/rules"
)

// apiRequestUnlock pide a quien tiene un archivo que lo libere.
func (s *Server) apiRequestUnlock(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	user, _ := currentUser(r)
	if err := s.svc.RequestUnlock(r.Context(), rules.Actor{UserID: user.ID}, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// webRequestUnlock es lo mismo desde la ficha; vuelve a ella con la confirmación.
func (s *Server) webRequestUnlock(w http.ResponseWriter, r *http.Request) {
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
	if err := s.svc.RequestUnlock(r.Context(), rules.Actor{UserID: user.ID}, id); err != nil {
		var ruleErr *rules.Error
		if errors.As(err, &ruleErr) && ruleErr.Code != rules.CodeNotFound {
			s.renderAsset(w, r, statusForCode(ruleErr.Code), ruleErr.Message)
			return
		}
		s.webFail(w, r, err)
		return
	}
	http.Redirect(w, r, "/assets/"+strconv.FormatInt(id, 10)+"?pedido=1", http.StatusSeeOther)
}
