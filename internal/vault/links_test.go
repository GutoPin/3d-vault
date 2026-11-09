package vault

import (
	"os"
	"path"
	"testing"

	"3d-vault/internal/blendrefs"
	"3d-vault/internal/rules"
)

func (e *env) addFile(p string, data []byte) CommitResult {
	e.t.Helper()
	r, err := e.svc.CreateAsset(e.ctx, e.juan, CreateAssetInput{ProjectID: e.project.ID, Path: p, Manifest: e.upload(data), Comment: "Primera versión"})
	if err != nil {
		e.t.Fatalf("CreateAsset %s: %v", p, err)
	}
	// Lo mismo que hace refsLater, pero sin esperar al segundo plano.
	if err := e.svc.ensureRefs(e.ctx, r.Version.FileSHA256, r.Version.manifestID); err != nil {
		e.t.Fatalf("ensureRefs %s: %v", p, err)
	}
	return r
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(path.Join("..", "blendrefs", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAssetLinks(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	scene := e.addFile("escenas/escena_01.blend", fixture(t, "escena_01.blend"))

	links, err := e.svc.AssetLinks(e.ctx, scene.Asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if links.Pending || links.Error != "" || len(links.Uses) != 3 {
		t.Fatalf("links = %+v", links)
	}
	// Sin el maestro ni la textura en 3D-Vault, todo está roto.
	if links.Broken() != 3 {
		t.Errorf("Broken = %d, want 3", links.Broken())
	}

	master := e.addFile("maestros/camion_maestro.blend", fixture(t, "camion_maestro.blend"))
	wood := e.addFile("texturas/madera.png", randomBytes(t, 100))

	links, err = e.svc.AssetLinks(e.ctx, scene.Asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		kind, status string
		target       int64
	}{
		{blendrefs.KindImage, LinkOutside, 0}, // ruta absoluta fuera de 3D-Vault
		{blendrefs.KindLibrary, LinkOK, master.Asset.ID},
		{blendrefs.KindImage, LinkOK, wood.Asset.ID},
	}
	if len(links.Uses) != len(want) {
		t.Fatalf("Uses = %+v", links.Uses)
	}
	for i, w := range want {
		l := links.Uses[i]
		var target int64
		if l.Target != nil {
			target = l.Target.ID
		}
		if l.Kind != w.kind || l.Status != w.status || target != w.target {
			t.Errorf("Uses[%d] = %s %s → %d, want %s %s → %d (%s)", i, l.Kind, l.Status, target, w.kind, w.status, w.target, l.Raw)
		}
	}

	// El maestro sabe quién lo usa, y su propia textura resuelve desde su carpeta.
	ml, err := e.svc.AssetLinks(e.ctx, master.Asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ml.UsedBy) != 1 || ml.UsedBy[0].Asset.ID != scene.Asset.ID {
		t.Errorf("UsedBy = %+v", ml.UsedBy)
	}
	if len(ml.Uses) != 1 || ml.Uses[0].Status != LinkOK || ml.Uses[0].Target.ID != wood.Asset.ID {
		t.Errorf("Uses del maestro = %+v", ml.Uses)
	}
	tl, err := e.svc.AssetLinks(e.ctx, wood.Asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.UsedBy) != 2 {
		t.Errorf("la textura la usan %d archivos, want 2", len(tl.UsedBy))
	}

	sums, err := e.svc.ProjectLinks(e.ctx, e.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s := sums[scene.Asset.ID]; s.Uses != 3 || s.Broken != 1 {
		t.Errorf("resumen de la escena = %+v", s)
	}
	if s := sums[master.Asset.ID]; s.UsedBy != 1 || s.Uses != 1 {
		t.Errorf("resumen del maestro = %+v", s)
	}

	files, err := e.svc.LinkedClosure(e.ctx, scene.Asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f.Folder+"/"+f.Asset.Path] = true
	}
	for _, p := range []string{"Mina Norte/escenas/escena_01.blend", "Mina Norte/maestros/camion_maestro.blend", "Mina Norte/texturas/madera.png"} {
		if !got[p] {
			t.Errorf("falta %s en %v", p, got)
		}
	}
	if len(files) != 3 {
		t.Errorf("LinkedClosure = %d archivos, want 3", len(files))
	}
}

func TestAssetLinksPendingAndInvalid(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	r, err := e.svc.CreateAsset(e.ctx, e.juan, CreateAssetInput{ProjectID: e.project.ID, Path: "roto.blend", Manifest: e.upload(randomBytes(t, 1000)), Comment: "No es un blend"})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.WaitBackground() // el análisis en segundo plano se corta: queda pendiente
	links, err := e.svc.AssetLinks(e.ctx, r.Asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !links.Pending {
		t.Errorf("Pending = false sin analizar")
	}
	if err := e.svc.ensureRefs(e.ctx, r.Version.FileSHA256, r.Version.manifestID); err != nil {
		t.Fatal(err)
	}
	links, err = e.svc.AssetLinks(e.ctx, r.Asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if links.Pending || links.Error == "" {
		t.Errorf("links = %+v, want error de lectura", links)
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	mina := Project{ID: 1, Name: "Mina Norte"}
	lib := Project{ID: 2, Name: "Biblioteca: comunes"} // carpeta «Biblioteca_ comunes»
	asset := func(p Project, id int64, path string) Asset {
		return Asset{ID: id, ProjectID: p.ID, Path: path, pathKey: rules.FoldKey(path)}
	}
	assets := []Asset{
		asset(mina, 10, "escenas/tajo/escena.blend"),
		asset(mina, 11, "Maestros/Camión.blend"),
		asset(lib, 20, "rocas/roca.blend"),
		asset(mina, 30, "texturas/piel.1001.png"),
		asset(mina, 31, "texturas/piel.1002.png"),
		asset(mina, 32, "render/frame_0001.exr"),
		asset(mina, 33, "render/frame_0002.exr"),
		asset(mina, 34, "render/otro_0001.exr"),
	}
	g := &linkGraph{
		projects: map[int64]Project{1: mina, 2: lib},
		folders: map[string]Project{
			rules.FoldKey(FolderName(mina.Name)): mina,
			rules.FoldKey(FolderName(lib.Name)):  lib,
		},
		byPath: map[int64]map[string]Asset{1: {}, 2: {}},
		byDir:  map[int64]map[string][]Asset{1: {}, 2: {}},
	}
	for _, a := range assets {
		g.byPath[a.ProjectID][a.pathKey] = a
		g.byDir[a.ProjectID][path.Dir(a.pathKey)] = append(g.byDir[a.ProjectID][path.Dir(a.pathKey)], a)
	}
	from := assets[0]

	for _, tc := range []struct {
		raw     string
		pattern bool
		status  string
		target  int64
		matches int
	}{
		{"//../../maestros/camión.blend", false, LinkOK, 11, 1},                   // sin distinguir mayúsculas
		{`//..\..\Maestros\Camión.blend`, false, LinkOK, 11, 1},                   // guardado en Windows
		{"//../../maestros/grua.blend", false, LinkMissing, 0, 0},                 // en el proyecto, sin subir
		{"//../../../Biblioteca_ comunes/rocas/roca.blend", false, LinkOK, 20, 1}, // otro proyecto
		{"//../../../Otro/roca.blend", false, LinkOutside, 0, 0},                  // carpeta que no es un proyecto
		{"//../../../../fuera.blend", false, LinkOutside, 0, 0},                   // sale de la carpeta de 3D-Vault
		{`C:\Users\juan\3D-Vault\Mina Norte\Maestros\Camión.blend`, false, LinkAbsolute, 11, 1},
		{`C:\Users\juan\Escritorio\camion.blend`, false, LinkOutside, 0, 0},
		{"//../../texturas/piel.<UDIM>.png", true, LinkOK, 30, 2},
		{"//../../render/frame_0001.exr", true, LinkOK, 32, 2},
	} {
		l := g.resolve(from, storedRef{kind: blendrefs.KindLibrary, raw: tc.raw, pattern: tc.pattern})
		var target int64
		if l.Target != nil {
			target = l.Target.ID
		}
		if l.Status != tc.status || target != tc.target || l.Matches != tc.matches {
			t.Errorf("%s: %s → %d (%d), want %s → %d (%d)", tc.raw, l.Status, target, l.Matches, tc.status, tc.target, tc.matches)
		}
	}
}
