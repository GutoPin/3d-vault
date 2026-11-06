package server

import (
	"context"
	"errors"
	"net/http"

	"3d-vault/internal/rules"
	"3d-vault/internal/vault"
)

// assetRow es un archivo con el nombre de su proyecto, para listas que mezclan
// proyectos.
type assetRow struct {
	vault.Asset
	ProjectName string
	Stale       bool
}

func (s *Server) assetRows(ctx context.Context, assets []vault.Asset) ([]assetRow, error) {
	projects, err := s.svc.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(projects))
	for _, p := range projects {
		names[p.ID] = p.Name
	}
	now := s.now()
	rows := make([]assetRow, len(assets))
	for i, a := range assets {
		rows[i] = assetRow{Asset: a, ProjectName: names[a.ProjectID], Stale: a.Lock != nil && now.Sub(a.Lock.LockedAt) > staleLock}
	}
	return rows, nil
}

type accountData struct {
	PasswordChanged bool
	Locks           []assetRow
	Tokens          []vault.Token
}

func (s *Server) webAccount(w http.ResponseWriter, r *http.Request) {
	s.renderAccount(w, r, http.StatusOK, "")
}

func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, status int, errorMessage string) {
	user, _ := currentUser(r)
	locked, err := s.svc.LockedBy(r.Context(), user.ID)
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	rows, err := s.assetRows(r.Context(), locked)
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	tokens, err := s.svc.ListTokens(r.Context(), user.ID)
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	data := accountData{PasswordChanged: r.URL.Query().Get("hecho") == "contrasena", Locks: rows, Tokens: tokens}
	s.render(w, r, status, "account", pageData{Title: "Mi cuenta", Active: "account", Error: errorMessage, Data: data})
}

// webChangePassword cambia la contraseña propia. Comprobar la actual pasa por el
// mismo límite de intentos que el login. Cambiarla cierra las sesiones y revoca
// los tokens, así que se emite una cookie nueva para seguir dentro.
func (s *Server) webChangePassword(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		s.webFail(w, r, err)
		return
	}
	user, _ := currentUser(r)
	password := r.PostForm.Get("password")
	if password != r.PostForm.Get("confirm") {
		s.renderAccount(w, r, http.StatusUnprocessableEntity, "Las dos contraseñas nuevas no coinciden.")
		return
	}
	if _, err := s.login(r, user.Username, r.PostForm.Get("current")); err != nil {
		switch {
		case errors.Is(err, errTooManyAttempts):
			s.renderAccount(w, r, http.StatusTooManyRequests, err.Error())
		case errors.Is(err, errBadCredentials):
			s.renderAccount(w, r, http.StatusForbidden, "La contraseña actual no es correcta.")
		default:
			s.webFail(w, r, err)
		}
		return
	}
	if err := s.svc.SetPassword(r.Context(), user.ID, user.ID, password); err != nil {
		var ruleErr *rules.Error
		if errors.As(err, &ruleErr) {
			s.renderAccount(w, r, statusForCode(ruleErr.Code), ruleErr.Message)
			return
		}
		s.webFail(w, r, err)
		return
	}
	updated, err := s.svc.GetUser(r.Context(), user.ID)
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	s.setSessionCookie(w, updated)
	http.Redirect(w, r, "/account?hecho=contrasena", http.StatusSeeOther)
}

func (s *Server) webRevokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	user, _ := currentUser(r)
	// Sin IsAdmin: desde Mi cuenta solo se revocan los propios, también un admin.
	if err := s.svc.RevokeToken(r.Context(), rules.Actor{UserID: user.ID}, id); err != nil {
		s.webFail(w, r, err)
		return
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}
