package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

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

// apiDeleteFolder borra una carpeta: DELETE …/folders?path=carpeta. Con
// files=1 también manda sus archivos a la papelera.
func (s *Server) apiDeleteFolder(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	actor, err := clientActor(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	n, err := s.svc.DeleteFolder(r.Context(), actor, id, q.Get("path"), q.Get("files") == "1")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted_files": n})
}

// apiRenameFolder renombra o mueve una carpeta con todo lo que tiene.
func (s *Server) apiRenameFolder(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, jsonTimeout, jsonTimeout)
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := decodeJSON(w, r, maxJSONBody, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	actor, err := clientActor(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	folder, moved, err := s.svc.RenameFolder(r.Context(), actor, id, req.From, req.To)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Folder vault.Folder `json:"folder"`
		Moved  int          `json:"moved_files"`
	}{folder, moved})
}

// apiFolderInfo dice qué pasaría al renombrar o borrar una carpeta.
func (s *Server) apiFolderInfo(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	actor, err := clientActor(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	info, err := s.svc.FolderInfo(r.Context(), actor, id, r.URL.Query().Get("path"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

type trashData struct {
	Project  vault.Project
	Files    []vault.DeletedAsset
	Restored string
}

// webTrash es la papelera de un proyecto: lo borrado, para recuperarlo.
func (s *Server) webTrash(w http.ResponseWriter, r *http.Request) {
	s.renderTrash(w, r, http.StatusOK, "")
}

func (s *Server) renderTrash(w http.ResponseWriter, r *http.Request, status int, errorMessage string) {
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
	files, err := s.svc.ListDeleted(r.Context(), id)
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	data := trashData{Project: project, Files: files, Restored: r.URL.Query().Get("recuperado")}
	s.render(w, r, status, "trash", pageData{Title: "Papelera de " + project.Name, ActiveProject: id, Error: errorMessage, Data: data})
}

// webUndelete recupera un archivo de la papelera y vuelve a ella.
func (s *Server) webUndelete(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		s.webFail(w, r, err)
		return
	}
	assetID, err := pathID(r, "asset")
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	asset, err := s.svc.Undelete(r.Context(), webActor(r), assetID)
	if err != nil {
		var re *rules.Error
		if errors.As(err, &re) {
			s.renderTrash(w, r, statusForCode(re.Code), re.Message)
			return
		}
		s.webFail(w, r, err)
		return
	}
	http.Redirect(w, r, "/projects/"+strconv.FormatInt(asset.ProjectID, 10)+"/papelera?recuperado="+url.QueryEscape(asset.Path), http.StatusSeeOther)
}
