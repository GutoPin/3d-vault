package vault

import (
	"testing"

	"3d-vault/internal/rules"
)

func TestFolders(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	f, created, err := e.svc.CreateFolder(e.ctx, e.juan, e.project.ID, `ASSETS\Vehículos/`)
	if err != nil || !created || f.Path != "ASSETS/Vehículos" {
		t.Fatalf("CreateFolder = %+v, %v, %v", f, created, err)
	}
	if _, created, err = e.svc.CreateFolder(e.ctx, e.ana, e.project.ID, "assets/vehículos"); err != nil || created {
		t.Errorf("crear otra vez = %v, %v; quiero sin cambios", created, err)
	}
	snap, err := e.svc.GetSnapshot(e.ctx, e.project.ID)
	if err != nil || len(snap.Folders) != 1 || snap.Folders[0].Path != "ASSETS/Vehículos" {
		t.Fatalf("snapshot = %+v, %v", snap.Folders, err)
	}

	// Una carpeta que ya existe por sus archivos no se guarda aparte.
	e.newAsset("ESCENAS/a.blend")
	if _, created, err = e.svc.CreateFolder(e.ctx, e.juan, e.project.ID, "escenas"); err != nil || created {
		t.Errorf("carpeta con archivos = %v, %v", created, err)
	}
	// Archivo y carpeta no pueden llamarse igual.
	if _, _, err = e.svc.CreateFolder(e.ctx, e.juan, e.project.ID, "ESCENAS/a.blend/x"); err == nil {
		t.Error("se creó una carpeta dentro de un archivo")
	}
	wantRule(t, func() error {
		_, err := e.svc.CreateAsset(e.ctx, e.juan, CreateAssetInput{ProjectID: e.project.ID, Path: "ASSETS/vehículos", Manifest: e.upload(randomBytes(t, 100)), Comment: "Choca"})
		return err
	}(), rules.CodePathExists)

	// Borrar: solo vacía.
	e.newAsset("ASSETS/Vehículos/camion.blend")
	wantRule(t, e.svc.DeleteFolder(e.ctx, e.juan, e.project.ID, "ASSETS"), rules.CodePathExists)
	if _, _, err := e.svc.CreateFolder(e.ctx, e.juan, e.project.ID, "Texturas/HDRI"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteFolder(e.ctx, e.ana, e.project.ID, "texturas"); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	wantRule(t, e.svc.DeleteFolder(e.ctx, e.ana, e.project.ID, "texturas"), rules.CodeNotFound)

	page, err := e.svc.EventsSince(e.ctx, []int64{e.project.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, ev := range page.Events {
		kinds[ev.Kind]++
		if ev.Kind == EventFolderCreated && ev.AssetID != 0 {
			t.Errorf("evento de carpeta con archivo %d", ev.AssetID)
		}
	}
	if kinds[EventFolderCreated] != 2 || kinds[EventFolderDeleted] != 1 {
		t.Errorf("eventos = %v", kinds)
	}
	if e.activityCount(actionCreateFolder) != 2 || e.activityCount(actionDeleteFolder) != 1 {
		t.Error("faltan acciones de carpeta en la actividad")
	}
}
