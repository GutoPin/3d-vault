package server

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"net/http"
	"strings"

	"3d-vault/web"
)

// blenderAddon entrega el complemento de Blender como .zip para "Instalar desde
// el disco". Se arma con los archivos incluidos en el binario, así que siempre
// corresponde a esta versión del servidor.
func (s *Server) blenderAddon(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := fs.WalkDir(web.Blender, web.BlenderDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				return fs.SkipDir
			}
			return nil
		}
		data, err := fs.ReadFile(web.Blender, path)
		if err != nil {
			return err
		}
		f, err := zw.Create(strings.TrimPrefix(path, web.BlenderDir+"/"))
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	})
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="3dvault-blender.zip"`)
	_, _ = buf.WriteTo(w)
}
