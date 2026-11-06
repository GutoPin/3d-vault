package server

import "net/http"

func (s *Server) webStorage(w http.ResponseWriter, r *http.Request) {
	usage, err := s.svc.StorageUsage(r.Context())
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "storage", pageData{Title: "Espacio", Active: "storage", Data: usage})
}
