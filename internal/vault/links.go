package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"3d-vault/internal/blendrefs"
	"3d-vault/internal/manifest"
	"3d-vault/internal/rules"
)

// refsTimeout acota el análisis de un .blend, que lo lee entero: un archivo de
// 20 GB a 150 MB/s tarda unos dos minutos y medio.
const refsTimeout = 30 * time.Minute

// Estados de un enlace.
const (
	// LinkOK: el archivo está en 3D-Vault y la ruta funciona en cualquier PC.
	LinkOK = "ok"
	// LinkMissing: la ruta es buena, pero nadie ha subido ese archivo.
	LinkMissing = "missing"
	// LinkAbsolute: ruta absoluta. Solo funciona en la PC de quien guardó, aunque
	// el archivo esté en 3D-Vault.
	LinkAbsolute = "absolute"
	// LinkOutside: apunta fuera de las carpetas de 3D-Vault.
	LinkOutside = "outside"
)

// Link es un archivo externo que usa un .blend, con lo que hay en 3D-Vault.
type Link struct {
	Kind    string `json:"kind"`
	Raw     string `json:"raw_path"`
	Pattern bool   `json:"pattern,omitempty"`
	Status  string `json:"status"`
	// Path es la ruta dentro del proyecto a la que apunta, si se pudo saber.
	Path string `json:"path,omitempty"`
	// Project es el proyecto al que apunta, si se pudo saber.
	Project *Project `json:"project,omitempty"`
	// Target es el archivo enlazado. En una secuencia o UDIM, el primero.
	Target *Asset `json:"target,omitempty"`
	// Matches cuenta los archivos de una secuencia o UDIM que hay en 3D-Vault.
	Matches int `json:"matches,omitempty"`
}

// Broken indica si el enlace fallará en otra PC.
func (l Link) Broken() bool { return l.Status != LinkOK }

// LinkUser es un .blend que usa un archivo.
type LinkUser struct {
	Asset   Asset   `json:"asset"`
	Project Project `json:"project"`
	Kind    string  `json:"kind"`
	Status  string  `json:"status"`
}

// AssetLinks resume los enlaces de un archivo: qué usa, si es un .blend, y qué
// .blend lo usan a él, en su última versión.
type AssetLinks struct {
	// Pending indica que el .blend aún se está analizando.
	Pending bool `json:"pending,omitempty"`
	// Error dice por qué no se pudo leer, si falló.
	Error  string     `json:"error,omitempty"`
	Uses   []Link     `json:"uses"`
	UsedBy []LinkUser `json:"used_by"`
}

// Broken cuenta los enlaces que fallarán en otra PC.
func (a AssetLinks) Broken() int {
	n := 0
	for _, l := range a.Uses {
		if l.Broken() {
			n++
		}
	}
	return n
}

// LinkSummary es lo que muestra la lista de un proyecto por archivo.
type LinkSummary struct {
	Uses   int
	Broken int
	UsedBy int
}

// ---------- análisis ----------

// refsLater analiza en segundo plano los enlaces de una versión recién creada.
func (s *Service) refsLater(asset Asset, v Version) {
	if !isBlend(asset.Path) {
		return
	}
	s.background.Go(func() {
		ctx, cancel := context.WithTimeout(s.bg, refsTimeout)
		defer cancel()
		_ = s.ensureRefs(ctx, v.FileSHA256, v.manifestID)
	})
}

// ScanMissingRefs analiza, en segundo plano y de a uno, los .blend cuya última
// versión todavía no se analizó: los subidos antes de esta función, o los que
// quedaron a medias al detener el servidor.
func (s *Service) ScanMissingRefs(ctx context.Context) {
	bg := s.bg
	type pending struct {
		sha        string
		manifestID int64
	}
	var todo []pending
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT DISTINCT v.file_sha256, v.manifest_id FROM assets a
			JOIN versions v ON v.asset_id = a.id AND v.number = a.head_version
			WHERE a.deleted_at IS NULL AND a.ext = '.blend'
			  AND NOT EXISTS (SELECT 1 FROM blend_scans b WHERE b.file_sha256 = v.file_sha256)`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p pending
			if err := rows.Scan(&p.sha, &p.manifestID); err != nil {
				return err
			}
			todo = append(todo, p)
		}
		return rows.Err()
	})
	if err != nil || len(todo) == 0 {
		return
	}
	s.background.Go(func() {
		for _, p := range todo {
			if bg.Err() != nil {
				return
			}
			c, cancel := context.WithTimeout(bg, refsTimeout)
			_ = s.ensureRefs(c, p.sha, p.manifestID)
			cancel()
		}
	})
}

// ensureRefs lee y guarda los enlaces de un contenido, si aún no se leyeron.
// Un .blend que no se puede leer se guarda con su error: volver a intentarlo
// daría lo mismo. Un fallo al leer los trozos no se guarda.
func (s *Service) ensureRefs(ctx context.Context, sha string, manifestID int64) error {
	select {
	case s.refsSem <- struct{}{}:
		defer func() { <-s.refsSem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	var m manifest.Manifest
	done := false
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM blend_scans WHERE file_sha256 = ?`, sha).Scan(new(int))
		if err == nil {
			done = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		m, err = loadManifest(ctx, tx, manifestID)
		return err
	})
	if err != nil || done {
		return err
	}

	reader := &chunkReader{ctx: ctx, store: s.store, chunks: m.Chunks}
	refs, err := blendrefs.Read(reader)
	if reader.err != nil {
		return fmt.Errorf("leer enlaces: %w", reader.err)
	}
	message := ""
	if err != nil {
		if !errors.Is(err, blendrefs.ErrInvalid) {
			return fmt.Errorf("leer enlaces: %w", err)
		}
		message = err.Error()
		refs = nil
	}
	return s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO blend_scans (file_sha256, scanned_at, error) VALUES (?, ?, ?)`,
			sha, millis(s.now()), message)
		if err != nil {
			return fmt.Errorf("guardar enlaces: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // otro análisis llegó antes
		}
		for i, r := range refs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO blend_refs (file_sha256, idx, kind, raw_path, pattern) VALUES (?, ?, ?, ?, ?)`,
				sha, i, r.Kind, r.Path, r.Pattern); err != nil {
				return fmt.Errorf("guardar enlaces: %w", err)
			}
		}
		return nil
	})
}

// ---------- grafo ----------

type storedRef struct {
	kind, raw string
	pattern   bool
}

type scanResult struct {
	refs  []storedRef
	error string
}

// linkGraph es la foto de todos los proyectos que hace falta para resolver
// enlaces, incluidos los que cruzan de un proyecto a otro.
type linkGraph struct {
	projects map[int64]Project
	folders  map[string]Project // clave de la carpeta del proyecto en la PC
	byPath   map[int64]map[string]Asset
	byDir    map[int64]map[string][]Asset
	blends   []Asset
	scans    map[string]scanResult // por file_sha256; falta si no se analizó
}

func (s *Service) loadGraph(ctx context.Context) (*linkGraph, error) {
	g := &linkGraph{
		projects: map[int64]Project{},
		folders:  map[string]Project{},
		byPath:   map[int64]map[string]Asset{},
		byDir:    map[int64]map[string][]Asset{},
		scans:    map[string]scanResult{},
	}
	err := s.db.ReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, name, description, created_by, created_at, archived FROM projects`)
		if err != nil {
			return fmt.Errorf("listar proyectos: %w", err)
		}
		for rows.Next() {
			p, err := scanProject(rows)
			if err != nil {
				rows.Close()
				return fmt.Errorf("leer proyecto: %w", err)
			}
			g.projects[p.ID] = p
			if !p.Archived {
				g.folders[rules.FoldKey(FolderName(p.Name))] = p
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		rows, err = tx.QueryContext(ctx, `SELECT `+assetColumns+assetFrom)
		if err != nil {
			return fmt.Errorf("listar archivos: %w", err)
		}
		for rows.Next() {
			a, err := scanAsset(rows)
			if err != nil {
				rows.Close()
				return fmt.Errorf("leer archivo: %w", err)
			}
			if g.byPath[a.ProjectID] == nil {
				g.byPath[a.ProjectID] = map[string]Asset{}
				g.byDir[a.ProjectID] = map[string][]Asset{}
			}
			g.byPath[a.ProjectID][a.pathKey] = a
			dir := path.Dir(a.pathKey)
			g.byDir[a.ProjectID][dir] = append(g.byDir[a.ProjectID][dir], a)
			if isBlend(a.Path) {
				g.blends = append(g.blends, a)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		rows, err = tx.QueryContext(ctx, `
			SELECT b.file_sha256, b.error, r.kind, r.raw_path, r.pattern FROM blend_scans b
			LEFT JOIN blend_refs r ON r.file_sha256 = b.file_sha256
			WHERE b.file_sha256 IN (
				SELECT v.file_sha256 FROM assets a JOIN versions v ON v.asset_id = a.id AND v.number = a.head_version
				WHERE a.deleted_at IS NULL AND a.ext = '.blend')
			ORDER BY b.file_sha256, r.idx`)
		if err != nil {
			return fmt.Errorf("leer enlaces: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				sha, message string
				kind, raw    sql.NullString
				pattern      sql.NullBool
			)
			if err := rows.Scan(&sha, &message, &kind, &raw, &pattern); err != nil {
				return fmt.Errorf("leer enlaces: %w", err)
			}
			r := g.scans[sha]
			r.error = message
			if kind.Valid {
				r.refs = append(r.refs, storedRef{kind: kind.String, raw: raw.String, pattern: pattern.Bool})
			}
			g.scans[sha] = r
		}
		return rows.Err()
	})
	return g, err
}

// FolderName es el nombre de la carpeta de un proyecto en la PC, igual que
// safeName en app.js y safe_name en el complemento de Blender.
func FolderName(name string) string {
	var b strings.Builder
	for _, c := range name {
		if strings.ContainsRune(`<>:"/\|?*`, c) || c < 32 {
			b.WriteRune('_')
		} else {
			b.WriteRune(c)
		}
	}
	out := strings.TrimRight(b.String(), ". ")
	if out == "" {
		return "proyecto"
	}
	return out
}

// resolve traduce la ruta que guardó Blender en un .blend de from a un archivo
// de 3D-Vault. En la PC, cada proyecto es una carpeta dentro de la carpeta de
// 3D-Vault, así que una ruta relativa puede saltar a otro proyecto con
// «//../../OtroProyecto/…».
func (g *linkGraph) resolve(from Asset, ref storedRef) Link {
	link := Link{Kind: ref.kind, Raw: ref.raw, Pattern: ref.pattern, Status: LinkOutside}
	var (
		project Project
		target  string
		found   bool
	)
	if rel, ok := strings.CutPrefix(ref.raw, "//"); ok {
		// Relativa a la carpeta del .blend. En Windows, con barras invertidas.
		full := path.Join(path.Dir(from.Path), strings.ReplaceAll(rel, `\`, "/"))
		project, found = g.projects[from.ProjectID], true
		target = full
		if up, ok := strings.CutPrefix(full, "../"); ok {
			found = false
			if folder, rest, ok := strings.Cut(up, "/"); ok && !strings.HasPrefix(up, "../") && rest != "" {
				project, found = g.folders[rules.FoldKey(folder)]
				target = rest
			}
		} else if full == ".." || full == "." {
			found = false
		}
		if found {
			link.Status = LinkMissing
		}
	} else {
		// Absoluta: se busca la carpeta de un proyecto dentro de la ruta, como
		// hace el complemento con el archivo abierto.
		parts := strings.FieldsFunc(ref.raw, func(c rune) bool { return c == '/' || c == '\\' })
		for i, part := range parts[:max(len(parts)-1, 0)] {
			if p, ok := g.folders[rules.FoldKey(part)]; ok {
				project, target, found = p, strings.Join(parts[i+1:], "/"), true
				break
			}
		}
	}
	if !found {
		return link
	}
	link.Project = &project
	link.Path = target

	key := rules.FoldKey(target)
	var matches []Asset
	if ref.pattern {
		re := patternRegexp(path.Base(key))
		for _, a := range g.byDir[project.ID][path.Dir(key)] {
			if re.MatchString(path.Base(a.pathKey)) {
				matches = append(matches, a)
			}
		}
	} else if a, ok := g.byPath[project.ID][key]; ok {
		matches = []Asset{a}
	}
	if len(matches) > 0 {
		link.Target = &matches[0]
		link.Matches = len(matches)
		if link.Status == LinkMissing {
			link.Status = LinkOK
		} else {
			link.Status = LinkAbsolute
		}
	} else if link.Status == LinkOutside {
		link.Status = LinkAbsolute
	}
	return link
}

var (
	udimToken  = regexp.MustCompile(`(?i)<(udim|uvtile)>`)
	lastDigits = regexp.MustCompile(`\d+`)
)

// patternRegexp convierte el nombre de una secuencia ("render.0001.png") o de
// una textura UDIM ("piel.<UDIM>.png") en una expresión que reconoce todos sus
// archivos.
func patternRegexp(name string) *regexp.Regexp {
	if loc := udimToken.FindStringIndex(name); loc != nil {
		return regexp.MustCompile(`^` + regexp.QuoteMeta(name[:loc[0]]) + `(\d+|u\d+_v\d+)` + regexp.QuoteMeta(name[loc[1]:]) + `$`)
	}
	all := lastDigits.FindAllStringIndex(name, -1)
	if len(all) == 0 {
		return regexp.MustCompile(`^` + regexp.QuoteMeta(name) + `$`)
	}
	loc := all[len(all)-1]
	return regexp.MustCompile(`^` + regexp.QuoteMeta(name[:loc[0]]) + `\d+` + regexp.QuoteMeta(name[loc[1]:]) + `$`)
}

func (g *linkGraph) linksOf(a Asset) ([]Link, bool, string) {
	scan, ok := g.scans[a.FileSHA256]
	if !ok {
		return nil, false, ""
	}
	links := make([]Link, 0, len(scan.refs))
	for _, r := range scan.refs {
		links = append(links, g.resolve(a, r))
	}
	// Primero los rotos, después las bibliotecas.
	slices.SortStableFunc(links, func(x, y Link) int {
		if x.Broken() != y.Broken() {
			if x.Broken() {
				return -1
			}
			return 1
		}
		if (x.Kind == blendrefs.KindLibrary) != (y.Kind == blendrefs.KindLibrary) {
			if x.Kind == blendrefs.KindLibrary {
				return -1
			}
			return 1
		}
		return 0
	})
	return links, true, scan.error
}

// AssetLinks devuelve los enlaces de un archivo. Si es un .blend que aún no se
// analizó, por ejemplo uno subido antes de esta función, lo analiza en segundo
// plano e indica Pending.
func (s *Service) AssetLinks(ctx context.Context, assetID int64) (AssetLinks, error) {
	g, err := s.loadGraph(ctx)
	if err != nil {
		return AssetLinks{}, err
	}
	var asset Asset
	for _, byKey := range g.byPath {
		for _, a := range byKey {
			if a.ID == assetID {
				asset = a
			}
		}
	}
	if asset.ID == 0 {
		return AssetLinks{}, notFound("El archivo")
	}
	out := AssetLinks{Uses: []Link{}, UsedBy: []LinkUser{}}
	if isBlend(asset.Path) {
		links, scanned, message := g.linksOf(asset)
		if !scanned {
			out.Pending = true
			s.background.Go(func() {
				c, cancel := context.WithTimeout(s.bg, refsTimeout)
				defer cancel()
				_ = s.ensureRefs(c, asset.FileSHA256, asset.manifestID)
			})
		}
		out.Uses, out.Error = links, message
		if out.Uses == nil {
			out.Uses = []Link{}
		}
	}
	for _, b := range g.blends {
		if b.ID == asset.ID {
			continue
		}
		links, _, _ := g.linksOf(b)
		for _, l := range links {
			if l.Target != nil && l.Target.ID == asset.ID {
				out.UsedBy = append(out.UsedBy, LinkUser{Asset: b, Project: g.projects[b.ProjectID], Kind: l.Kind, Status: l.Status})
				break
			}
		}
	}
	slices.SortFunc(out.UsedBy, func(x, y LinkUser) int { return strings.Compare(x.Asset.pathKey, y.Asset.pathKey) })
	return out, nil
}

// ProjectLinks resume los enlaces de cada archivo de un proyecto que usa o es
// usado por otro.
func (s *Service) ProjectLinks(ctx context.Context, projectID int64) (map[int64]LinkSummary, error) {
	g, err := s.loadGraph(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int64]LinkSummary{}
	for _, b := range g.blends {
		links, _, _ := g.linksOf(b)
		counted := map[int64]bool{}
		for _, l := range links {
			if b.ProjectID == projectID {
				sum := out[b.ID]
				sum.Uses++
				if l.Broken() {
					sum.Broken++
				}
				out[b.ID] = sum
			}
			if t := l.Target; t != nil && t.ProjectID == projectID && t.ID != b.ID && !counted[t.ID] {
				counted[t.ID] = true
				sum := out[t.ID]
				sum.UsedBy++
				out[t.ID] = sum
			}
		}
	}
	return out, nil
}

// LinkedFile es un archivo que hace falta para abrir un .blend completo, con su
// ruta en la carpeta de 3D-Vault de una PC: «Proyecto/carpeta/archivo».
type LinkedFile struct {
	Asset  Asset
	Folder string
}

// LinkedClosure devuelve un .blend y todo lo que usa, siguiendo también los
// enlaces de las bibliotecas que enlaza. Solo incluye lo que está en 3D-Vault.
func (s *Service) LinkedClosure(ctx context.Context, assetID int64) ([]LinkedFile, error) {
	g, err := s.loadGraph(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[int64]Asset{}
	for _, byKey := range g.byPath {
		for _, a := range byKey {
			byID[a.ID] = a
		}
	}
	root, ok := byID[assetID]
	if !ok {
		return nil, notFound("El archivo")
	}
	var out []LinkedFile
	seen := map[int64]bool{}
	queue := []Asset{root}
	for len(queue) > 0 {
		a := queue[0]
		queue = queue[1:]
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		out = append(out, LinkedFile{Asset: a, Folder: FolderName(g.projects[a.ProjectID].Name)})
		if !isBlend(a.Path) {
			continue
		}
		links, _, _ := g.linksOf(a)
		for _, l := range links {
			if l.Target == nil {
				continue
			}
			if !l.Pattern {
				queue = append(queue, *l.Target)
				continue
			}
			re := patternRegexp(path.Base(rules.FoldKey(l.Path)))
			for _, m := range g.byDir[l.Target.ProjectID][path.Dir(l.Target.pathKey)] {
				if re.MatchString(path.Base(m.pathKey)) {
					queue = append(queue, m)
				}
			}
		}
	}
	return out, nil
}
