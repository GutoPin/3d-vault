package server

import (
	"net/http"

	"3d-vault/internal/rules"
	"3d-vault/internal/vault"
)

// apiCreateFolder crea una carpeta, aunque esté vacía. Responde 201 si la creó
// y 200 si ya existía.
func (s *Server) apiCreateFolder(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(w, r, maxJSONBody, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	user, _ := currentUser(r)
	folder, created, err := s.svc.CreateFolder(r.Context(), rules.Actor{UserID: user.ID, IsAdmin: user.IsAdmin}, id, req.Path)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, struct {
		Folder  vault.Folder `json:"folder"`
		Created bool         `json:"created"`
	}{folder, created})
}

// apiDeleteFolder borra una carpeta vacía: DELETE …/folders?path=carpeta.
func (s *Server) apiDeleteFolder(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	user, _ := currentUser(r)
	if err := s.svc.DeleteFolder(r.Context(), rules.Actor{UserID: user.ID, IsAdmin: user.IsAdmin}, id, r.URL.Query().Get("path")); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
