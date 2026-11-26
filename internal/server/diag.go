package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"
)

// apiDiag recibe de Mi carpeta qué pantalla mostró, en qué navegador y, si hubo,
// qué error. Solo se anota en el registro: sirve para saber por qué a alguien «no
// le funciona» en un navegador y en otro sí, sin tener que ver su pantalla.
func (s *Server) apiDiag(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State   string `json:"state"`
		Browser string `json:"browser"`
		Detail  string `json:"detail"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	user, _ := currentUser(r)
	s.log.Info("mi carpeta", slog.String("usuario", user.Username),
		slog.String("pantalla", clip(body.State, 40)), slog.String("navegador", clip(body.Browser, 200)),
		slog.String("detalle", clip(body.Detail, 500)), slog.String("pc", clip(r.Header.Get("X-Client-Id"), 64)))
	w.WriteHeader(http.StatusNoContent)
}

// clip recorta un texto del navegador para el registro: sin saltos de línea y
// con un largo máximo.
func clip(s string, n int) string {
	if !utf8.ValidString(s) {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n]) + "…"
	}
	return s
}
