package vault

import (
	"slices"
	"testing"

	"3d-vault/internal/blendrefs"
)

func TestRenderPlan(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	scene := e.addFile("escenas/escena_01.blend", fixture(t, "escena_01.blend"))

	// Sin el maestro no se puede enviar; la textura que falta solo avisa.
	plan, err := e.svc.RenderPlan(e.ctx, scene.Asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Scene != "Mina Norte/escenas/escena_01.blend" || len(plan.Blocking) != 1 || plan.Blocking[0].Kind != blendrefs.KindLibrary {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Warnings) != 2 {
		t.Errorf("Warnings = %+v", plan.Warnings)
	}

	// Subidos con otras mayúsculas: en el .zip van como los escribe la escena,
	// porque en Linux «MAESTROS/» no es «maestros/».
	e.addFile("MAESTROS/camion_maestro.blend", fixture(t, "camion_maestro.blend"))
	e.addFile("texturas/madera.png", randomBytes(t, 100))
	plan, err = e.svc.RenderPlan(e.ctx, scene.Asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blocking) != 0 || plan.Pending {
		t.Fatalf("plan = %+v", plan)
	}
	var names []string
	for _, f := range plan.Files {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	want := []string{"Mina Norte/escenas/escena_01.blend", "Mina Norte/maestros/camion_maestro.blend", "Mina Norte/texturas/madera.png"}
	if !slices.Equal(names, want) {
		t.Errorf("archivos = %v, want %v", names, want)
	}

	png := e.newAsset("texturas/otra.png")
	if _, err := e.svc.RenderPlan(e.ctx, png.Asset.ID); err == nil {
		t.Error("se aceptó enviar a render algo que no es un .blend")
	}
}
