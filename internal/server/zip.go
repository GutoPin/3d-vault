package server

import (
	"archive/zip"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"3d-vault/internal/rules"
	"3d-vault/internal/vault"
)

// webProjectZip entrega en un .zip la última versión de cada archivo del
// proyecto, con su ruta, o solo los de una carpeta con ?prefix=carpeta/. Se arma
// en streaming, trozo a trozo, y sin comprimir: los .blend ya vienen comprimidos o
// se comprimen mal, y así no gasta CPU.
func (s *Server) webProjectZip(w http.ResponseWriter, r *http.Request) {
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
	snap, err := s.svc.GetSnapshot(r.Context(), id)
	if err != nil {
		s.webFail(w, r, err)
		return
	}

	prefix := strings.Trim(r.URL.Query().Get("prefix"), "/")
	if prefix != "" {
		prefix += "/"
	}
	var assets []vault.Asset
	for _, a := range snap.Assets {
		if strings.HasPrefix(a.Path, prefix) {
			assets = append(assets, a)
		}
	}
	if len(assets) == 0 {
		s.webFail(w, r, &rules.Error{Code: rules.CodeNotFound, Message: "No hay archivos que descargar en esa carpeta."})
		return
	}

	entries := make([]zipEntry, len(assets))
	for i, a := range assets {
		entries[i] = zipEntry{name: a.Path, versionID: a.HeadVersionID}
	}
	s.writeZip(w, r, project.Name+".zip", entries)
}

type zipEntry struct {
	name      string
	versionID int64
}

// writeZip envía un .zip con una versión por entrada.
func (s *Server) writeZip(w http.ResponseWriter, r *http.Request, filename string, entries []zipEntry) {
	// Sin Content-Length: si se corta, el navegador no puede darlo por bueno.
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))

	rc := http.NewResponseController(w)
	zw := zip.NewWriter(w)
	for _, e := range entries {
		file, err := s.svc.GetVersionFile(r.Context(), e.versionID)
		if err != nil {
			s.abortZip(r, e.versionID, err)
		}
		entry, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Store, Modified: file.Version.CreatedAt.Local()})
		if err != nil {
			return
		}
		for _, c := range file.Manifest.Chunks {
			data, err := s.svc.ReadChunkData(c.SHA256)
			if err != nil {
				s.abortZip(r, e.versionID, err)
			}
			_ = rc.SetWriteDeadline(time.Now().Add(writeStall))
			if _, err := entry.Write(data); err != nil {
				return
			}
		}
	}
	_ = zw.Close()
}

// abortZip corta la conexión, igual que una descarga suelta: con las cabeceras ya
// enviadas, es la única forma de que el navegador no guarde un .zip incompleto.
func (s *Server) abortZip(r *http.Request, versionID int64, err error) {
	if r.Context().Err() == nil {
		s.log.ErrorContext(r.Context(), "error al armar un .zip", slog.Int64("versión", versionID), slog.Any("error", err))
	}
	panic(http.ErrAbortHandler)
}
