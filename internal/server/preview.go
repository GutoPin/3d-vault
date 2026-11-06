package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strings"

	"3d-vault/internal/rules"
	"3d-vault/internal/vault"
)

const (
	// largeModel es el tamaño a partir del cual el visor pide confirmación.
	largeModel = 200 << 20

	// Una versión nunca cambia: el navegador puede guardar su vista previa.
	immutableCache = "private, max-age=31536000, immutable"
)

// imageTypes son las imágenes que se muestran tal cual. Solo formatos de mapa de
// bits: un SVG o un HTML servido en línea podría ejecutar código.
var imageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
}

var modelExts = []string{".glb", ".gltf", ".fbx", ".obj", ".stl"}

// preview describe la vista previa de la ficha de un archivo.
type preview struct {
	Kind      string // "blend", "image" o "model"
	VersionID int64
	Ext       string
	Size      int64
	// Thumb indica que el .blend tiene miniatura.
	Thumb bool
	// Large pide confirmación antes de cargar el modelo.
	Large bool
}

// assetPreview elige la vista previa de la última versión. Para un .blend genera
// la miniatura si falta, para saber si mostrarla o el ícono genérico.
func (s *Server) assetPreview(ctx context.Context, asset vault.Asset) *preview {
	p := &preview{VersionID: asset.HeadVersionID, Ext: asset.Ext, Size: asset.SizeBytes}
	switch {
	case asset.Ext == ".blend":
		p.Kind = "blend"
		_, err := s.svc.Thumbnail(ctx, asset.HeadVersionID)
		p.Thumb = err == nil
		var ruleErr *rules.Error
		if err != nil && !errors.As(err, &ruleErr) && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "error al generar una miniatura", slog.Int64("versión", asset.HeadVersionID), slog.Any("error", err))
		}
	case imageTypes[asset.Ext] != "":
		p.Kind = "image"
	case slices.Contains(modelExts, asset.Ext):
		p.Kind = "model"
		p.Large = asset.SizeBytes > largeModel
	default:
		return nil
	}
	return p
}

// webThumb sirve la miniatura de una versión de un .blend, por su id: nunca por
// una ruta que construya el usuario.
func (s *Server) webThumb(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	data, err := s.svc.Thumbnail(r.Context(), id)
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", immutableCache)
	_, _ = w.Write(data)
}

// webImage sirve una versión de una imagen para mostrarla en la ficha.
func (s *Server) webImage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	file, err := s.svc.GetVersionFile(r.Context(), id)
	if err != nil {
		s.webFail(w, r, err)
		return
	}
	contentType := imageTypes[strings.ToLower(path.Ext(file.Path))]
	if contentType == "" {
		s.webFail(w, r, &rules.Error{Code: rules.CodeNotFound, Message: "Este archivo no es una imagen."})
		return
	}
	w.Header().Set("Cache-Control", immutableCache)
	s.sendVersion(w, r, file, contentType, "inline")
}
