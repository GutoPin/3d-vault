package vault

import (
	"slices"
	"testing"
)

func TestListActivityDescribesAndFilters(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	id := e.newAsset("a.blend").Asset.ID
	e.mustLock(e.juan, id)
	if _, err := e.commit(e.juan, id, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Unlock(e.ctx, e.juan, id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Restore(e.ctx, e.ana, id, 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Move(e.ctx, e.ana, id, 3, "b.blend"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.CreateToken(e.ctx, e.juan.UserID, "PC"); err != nil {
		t.Fatal(err)
	}

	all, err := e.svc.ListActivity(e.ctx, ActivityFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, entry := range all {
		texts = append(texts, entry.Text)
	}
	want := []string{"movió de a.blend a b.blend", "restauró la v1 como v3", "liberó", "subió la v2", "bloqueó", "agregó el archivo", "creó el proyecto"}
	if !slices.Equal(texts, want) {
		t.Fatalf("actividad = %q, quiero %q (sin cuentas ni tokens)", texts, want)
	}
	if all[3].Note != "Ajuste de ruedas" || all[4].Note != "En PC de Juan" || all[0].AssetPath != "b.blend" || all[0].ProjectName != "Mina Norte" {
		t.Fatalf("detalles = %+v", all)
	}

	byAna, err := e.svc.ListActivity(e.ctx, ActivityFilter{UserID: e.ana.UserID, Limit: 50})
	if err != nil || len(byAna) != 2 {
		t.Fatalf("de Ana = %d, %v", len(byAna), err)
	}
	page, err := e.svc.ListActivity(e.ctx, ActivityFilter{ProjectID: e.project.ID, Before: all[1].ID, Limit: 2})
	if err != nil || len(page) != 2 || page[0].ID != all[2].ID {
		t.Fatalf("página = %+v, %v", page, err)
	}
	other, err := e.svc.ListActivity(e.ctx, ActivityFilter{ProjectID: e.project.ID + 1, Limit: 50})
	if err != nil || len(other) != 0 {
		t.Fatalf("otro proyecto = %d, %v", len(other), err)
	}

	// Un archivo borrado sigue en la actividad, sin enlace.
	if err := e.svc.Delete(e.ctx, e.ana, id, 3); err != nil {
		t.Fatal(err)
	}
	latest, err := e.svc.ListActivity(e.ctx, ActivityFilter{Limit: 1})
	if err != nil || latest[0].Text != "borró el archivo" || latest[0].AssetID != 0 || latest[0].AssetPath != "b.blend" {
		t.Fatalf("borrado = %+v, %v", latest, err)
	}
}

func TestSearchAssetsAndLockedBy(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	camion := e.newAsset("Modelos/Camión_797F.blend").Asset.ID
	e.newAsset("modelos/camionX797F.blend")
	e.newAsset("texturas/100%.png")
	gone := e.newAsset("modelos/camión viejo.blend").Asset.ID
	if err := e.svc.Delete(e.ctx, e.juan, gone, 1); err != nil {
		t.Fatal(err)
	}

	paths := func(text string) []string {
		t.Helper()
		found, err := e.svc.SearchAssets(e.ctx, text, 100)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, a := range found {
			out = append(out, a.Path)
		}
		return out
	}
	// Sin mayúsculas ni acentos, en los dos sentidos.
	both := []string{"modelos/camionX797F.blend", "Modelos/Camión_797F.blend"}
	if got := paths("CAMIÓN"); !slices.Equal(got, both) {
		t.Fatalf("CAMIÓN = %q", got)
	}
	if got := paths("camion_"); !slices.Equal(got, []string{"Modelos/Camión_797F.blend"}) {
		t.Fatalf("camion_ = %q", got)
	}
	if got := paths("n_7"); !slices.Equal(got, []string{"Modelos/Camión_797F.blend"}) {
		t.Fatalf("_ debe ser literal: %q", got)
	}
	if got := paths("%"); !slices.Equal(got, []string{"texturas/100%.png"}) {
		t.Fatalf("%% debe ser literal: %q", got)
	}
	if got := paths("797f"); len(got) != 2 {
		t.Fatalf("797f = %q", got)
	}

	e.mustLock(e.juan, camion)
	locked, err := e.svc.LockedBy(e.ctx, e.juan.UserID)
	if err != nil || len(locked) != 1 || locked[0].ID != camion || locked[0].Lock.ClientName != "PC de Juan" {
		t.Fatalf("LockedBy = %+v, %v", locked, err)
	}
	if locked, _ := e.svc.LockedBy(e.ctx, e.ana.UserID); len(locked) != 0 {
		t.Fatalf("Ana no bloquea nada: %+v", locked)
	}
}
