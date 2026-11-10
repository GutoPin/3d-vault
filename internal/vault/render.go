package vault

import (
	"context"
	"path"
	"strings"

	"3d-vault/internal/blendrefs"
	"3d-vault/internal/rules"
)

// RenderFile es un archivo del paquete que se manda a renderizar.
type RenderFile struct {
	// Name es su ruta en el .zip, «Proyecto/carpeta/archivo».
	Name      string `json:"name"`
	VersionID int64  `json:"-"`
	Size      int64  `json:"size"`
}

// RenderIssue es un archivo que la escena usa y no va en el paquete.
type RenderIssue struct {
	// From es el .blend que lo usa: la escena o uno de sus maestros.
	From   string `json:"from"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

// RenderPlan dice qué se mandaría a renderizar y qué falta.
type RenderPlan struct {
	// Scene es la ruta en el .zip de la escena a renderizar.
	Scene string       `json:"scene"`
	Files []RenderFile `json:"files"`
	Bytes int64        `json:"bytes"`
	// Blocking son los .blend enlazados que faltan: sin ellos, los objetos no salen.
	Blocking []RenderIssue `json:"blocking"`
	// Warnings son texturas, sonidos y demás: se puede renderizar igual.
	Warnings []RenderIssue `json:"warnings"`
	// Pending indica que algún .blend aún se está analizando.
	Pending bool `json:"pending"`
}

// RenderPlan arma el paquete de una escena: ella y todo lo que usa, siguiendo
// los enlaces de sus maestros.
//
// Cada archivo va con la ruta exacta que escribe el .blend que lo usa, no con la
// de 3D-Vault. Se renderiza en Linux, que distingue mayúsculas: una escena de
// Windows que apunta a «vehiculos/» no encontraría «VEHICULOS/». Si dos .blend lo
// escriben distinto, el archivo va dos veces.
func (s *Service) RenderPlan(ctx context.Context, assetID int64) (RenderPlan, error) {
	g, err := s.loadGraph(ctx)
	if err != nil {
		return RenderPlan{}, err
	}
	var root Asset
	for _, byKey := range g.byPath {
		for _, a := range byKey {
			if a.ID == assetID {
				root = a
			}
		}
	}
	if root.ID == 0 {
		return RenderPlan{}, notFound("El archivo")
	}
	if !isBlend(root.Path) {
		return RenderPlan{}, invalidInput("Solo se pueden mandar a renderizar archivos .blend.")
	}

	type node struct {
		asset Asset
		name  string
	}
	plan := RenderPlan{
		Scene:    FolderName(g.projects[root.ProjectID].Name) + "/" + root.Path,
		Files:    []RenderFile{},
		Blocking: []RenderIssue{},
		Warnings: []RenderIssue{},
	}
	seen := map[string]bool{}
	issues := map[string]bool{} // el mismo archivo puede faltarle a varios .blend
	report := func(i RenderIssue) {
		if issues[i.Kind+"|"+i.Path] {
			return
		}
		issues[i.Kind+"|"+i.Path] = true
		if i.Kind == blendrefs.KindLibrary {
			plan.Blocking = append(plan.Blocking, i)
		} else {
			plan.Warnings = append(plan.Warnings, i)
		}
	}

	queue := []node{{root, plan.Scene}}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if seen[n.name] {
			continue
		}
		seen[n.name] = true
		plan.Files = append(plan.Files, RenderFile{Name: n.name, VersionID: n.asset.HeadVersionID, Size: n.asset.SizeBytes})
		plan.Bytes += n.asset.SizeBytes
		if !isBlend(n.asset.Path) {
			continue
		}
		links, scanned, _ := g.linksOf(n.asset)
		if !scanned {
			plan.Pending = true
			continue
		}
		for _, l := range links {
			shown := l.Path
			if shown == "" {
				shown = l.Raw
			}
			if l.Broken() {
				report(RenderIssue{From: n.asset.Path, Kind: l.Kind, Path: shown, Status: l.Status})
				continue
			}
			rel := strings.ReplaceAll(strings.TrimPrefix(l.Raw, "//"), `\`, "/")
			literal := path.Join(path.Dir(n.name), rel)
			if strings.HasPrefix(literal, "../") {
				report(RenderIssue{From: n.asset.Path, Kind: l.Kind, Path: shown, Status: LinkOutside})
				continue
			}
			if !l.Pattern {
				queue = append(queue, node{*l.Target, literal})
				continue
			}
			re := patternRegexp(path.Base(rules.FoldKey(l.Path)))
			for _, m := range g.byDir[l.Target.ProjectID][path.Dir(l.Target.pathKey)] {
				if re.MatchString(path.Base(m.pathKey)) {
					queue = append(queue, node{m, path.Dir(literal) + "/" + path.Base(m.Path)})
				}
			}
		}
	}
	return plan, nil
}
