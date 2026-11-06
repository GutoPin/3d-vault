package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"3d-vault/internal/vault"
)

const (
	activityPageSize = 50
	searchLimit      = 100
)

// queryID lee un id de la URL. Si falta o no es válido, devuelve 0: sin filtro.
func queryID(values url.Values, name string) int64 {
	id, err := strconv.ParseInt(values.Get(name), 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}

type activityData struct {
	Entries   []vault.ActivityEntry
	Users     []vault.User
	ProjectID int64
	UserID    int64
	Scope     string
	MoreHref  string
}

func (s *Server) webActivity(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	data := activityData{ProjectID: queryID(query, "project"), UserID: queryID(query, "user")}
	entries, err := s.svc.ListActivity(r.Context(), vault.ActivityFilter{
		ProjectID: data.ProjectID,
		UserID:    data.UserID,
		Before:    queryID(query, "before"),
		Limit:     activityPageSize + 1,
	})
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	if data.Users, err = s.svc.ListUsers(r.Context()); err != nil {
		s.webFail(w, r, err)
		return
	}

	data.Scope = "Todos los proyectos, lo más reciente primero."
	if data.ProjectID != 0 {
		project, err := s.svc.GetProject(r.Context(), data.ProjectID)
		if err != nil {
			s.webFail(w, r, err)
			return
		}
		data.Scope = "Proyecto " + project.Name + ", lo más reciente primero."
	}

	if len(entries) > activityPageSize {
		entries = entries[:activityPageSize]
		more := url.Values{}
		if data.ProjectID != 0 {
			more.Set("project", strconv.FormatInt(data.ProjectID, 10))
		}
		if data.UserID != 0 {
			more.Set("user", strconv.FormatInt(data.UserID, 10))
		}
		more.Set("before", strconv.FormatInt(entries[len(entries)-1].ID, 10))
		data.MoreHref = "/activity?" + more.Encode()
	}
	data.Entries = entries
	s.render(w, r, http.StatusOK, "activity", pageData{Title: "Actividad", Active: "activity", Data: data})
}

type searchData struct {
	Query   string
	Rows    []assetRow
	Limited bool
}

func (s *Server) webSearch(w http.ResponseWriter, r *http.Request) {
	data := searchData{Query: strings.TrimSpace(r.URL.Query().Get("q"))}
	if data.Query != "" {
		found, err := s.svc.SearchAssets(r.Context(), data.Query, searchLimit+1)
		if err != nil {
			s.webFail(w, r, err)
			return
		}
		if len(found) > searchLimit {
			found, data.Limited = found[:searchLimit], true
		}
		if data.Rows, err = s.assetRows(r.Context(), found); err != nil {
			s.webFail(w, r, err)
			return
		}
	}
	s.render(w, r, http.StatusOK, "search", pageData{Title: "Buscar", Active: "search", Data: data})
}
