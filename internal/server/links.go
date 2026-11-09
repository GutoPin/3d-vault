package server

import (
	"net/http"
	"path"
	"strings"

	"3d-vault/internal/blendrefs"
)

// linkKind es el nombre de un tipo de enlace para la web.
func linkKind(kind string) string {
	switch kind {
	case blendrefs.KindLibrary:
		return "Archivo enlazado"
	case blendrefs.KindImage:
		return "Textura"
	case blendrefs.KindSound:
		return "Sonido"
	case blendrefs.KindMovie:
		return "Video"
	case blendrefs.KindFont:
		return "Fuente"
	case blendrefs.KindVolume:
		return "Volumen"
	case blendrefs.KindCache:
		return "Caché"
	}
	return kind
}

// webLinkedZip entrega un .blend con todo lo que usa y está en 3D-Vault, con
// las carpetas de la carpeta de 3D-Vault de una PC («Proyecto/carpeta/…»): así
// las rutas relativas funcionan al descomprimirlo, también entre proyectos.
func (s *Server) webLinkedZip(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	files, err := s.svc.LinkedClosure(r.Context(), id)
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	entries := make([]zipEntry, len(files))
	for i, f := range files {
		entries[i] = zipEntry{name: f.Folder + "/" + f.Asset.Path, versionID: f.Asset.HeadVersionID}
	}
	name := strings.TrimSuffix(path.Base(files[0].Asset.Path), path.Ext(files[0].Asset.Path))
	s.writeZip(w, r, name+" con enlaces.zip", entries)
}

// apiLinks devuelve lo que usa un archivo y quién lo usa.
func (s *Server) apiLinks(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	links, err := s.svc.AssetLinks(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, links)
}
