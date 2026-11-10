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
	wantRule(t, func() error { _, err := e.svc.DeleteFolder(e.ctx, e.juan, e.project.ID, "ASSETS", false); return err }(), rules.CodePathExists)
	if _, _, err := e.svc.CreateFolder(e.ctx, e.juan, e.project.ID, "Texturas/HDRI"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DeleteFolder(e.ctx, e.ana, e.project.ID, "texturas", false); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	wantRule(t, func() error { _, err := e.svc.DeleteFolder(e.ctx, e.ana, e.project.ID, "texturas", false); return err }(), rules.CodeNotFound)

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

func TestRenameFolder(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	scene := e.addFile("ESCENAS/escena_01.blend", fixture(t, "escena_01.blend"))
	master := e.addFile("maestros/camion_maestro.blend", fixture(t, "camion_maestro.blend"))
	e.newAsset("maestros/viejos/grua.blend")
	if _, _, err := e.svc.CreateFolder(e.ctx, e.juan, e.project.ID, "maestros/vacia"); err != nil {
		t.Fatal(err)
	}

	// Antes de renombrar se sabe qué escenas de fuera perderán sus enlaces.
	info, err := e.svc.FolderInfo(e.ctx, e.ana, e.project.ID, "maestros")
	if err != nil {
		t.Fatal(err)
	}
	if info.Files != 2 || len(info.LinkedFrom) != 1 || info.LinkedFrom[0].ID != scene.Asset.ID || len(info.Locked) != 0 {
		t.Fatalf("info = %+v", info)
	}

	// Bloqueado por otra persona: no se mueve nada.
	e.mustLock(e.ana, master.Asset.ID)
	if _, _, err := e.svc.RenameFolder(e.ctx, e.juan, e.project.ID, "maestros", "MAESTROS"); err == nil {
		t.Fatal("se renombró con un archivo bloqueado por Ana")
	} else {
		wantRule(t, err, rules.CodeLocked)
	}
	// Bloqueado por quien renombra, desde otra PC: se puede, y el bloqueo sigue.
	f, moved, err := e.svc.RenameFolder(e.ctx, rules.Actor{UserID: e.ana.UserID, ClientID: "web"}, e.project.ID, "maestros", "Biblioteca/MAESTROS")
	if err != nil || moved != 2 || f.Path != "Biblioteca/MAESTROS" {
		t.Fatalf("RenameFolder = %+v, %d, %v", f, moved, err)
	}
	m := e.mustHead(master.Asset.ID)
	if m.Path != "Biblioteca/MAESTROS/camion_maestro.blend" || m.Lock == nil || m.HeadVersion != 1 {
		t.Errorf("maestro = %+v", m)
	}
	snap, _ := e.svc.GetSnapshot(e.ctx, e.project.ID)
	paths := map[string]bool{}
	for _, a := range snap.Assets {
		paths[a.Path] = true
	}
	for _, f := range snap.Folders {
		paths[f.Path+"/"] = true
	}
	for _, p := range []string{"Biblioteca/MAESTROS/viejos/grua.blend", "Biblioteca/MAESTROS/vacia/"} {
		if !paths[p] {
			t.Errorf("falta %s en %v", p, paths)
		}
	}

	// Destino ocupado, dentro de sí misma, y solo cambiar mayúsculas.
	e.newAsset("ESCENAS/otra.png")
	if _, _, err := e.svc.RenameFolder(e.ctx, e.ana, e.project.ID, "Biblioteca", "escenas"); err == nil {
		t.Error("se renombró sobre una carpeta que existe")
	}
	if _, _, err := e.svc.RenameFolder(e.ctx, e.ana, e.project.ID, "Biblioteca", "Biblioteca/dentro"); err == nil {
		t.Error("una carpeta quedó dentro de sí misma")
	}
	if _, _, err := e.svc.RenameFolder(e.ctx, e.ana, e.project.ID, "biblioteca", "BIBLIOTECA"); err != nil {
		t.Errorf("cambiar solo mayúsculas: %v", err)
	}
	if got := e.mustHead(master.Asset.ID).Path; got != "BIBLIOTECA/MAESTROS/camion_maestro.blend" {
		t.Errorf("tras cambiar mayúsculas = %s", got)
	}
	if e.activityCount(actionRenameFolder) != 2 {
		t.Error("falta la acción de renombrar en la actividad")
	}
}

func TestDeleteFolderWithFilesAndUndelete(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.newAsset("RENDERS/toma1.png")
	b := e.newAsset("RENDERS/v2/toma2.png")
	e.mustLock(e.ana, b.Asset.ID)

	wantRule(t, func() error { _, err := e.svc.DeleteFolder(e.ctx, e.juan, e.project.ID, "RENDERS", false); return err }(), rules.CodePathExists)
	wantRule(t, func() error { _, err := e.svc.DeleteFolder(e.ctx, e.juan, e.project.ID, "RENDERS", true); return err }(), rules.CodeLocked)
	if err := e.svc.Unlock(e.ctx, e.ana, b.Asset.ID, false); err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.DeleteFolder(e.ctx, e.juan, e.project.ID, "renders", true)
	if err != nil || n != 2 {
		t.Fatalf("DeleteFolder = %d, %v", n, err)
	}
	trash, err := e.svc.ListDeleted(e.ctx, e.project.ID)
	if err != nil || len(trash) != 2 || !trash[0].Free {
		t.Fatalf("papelera = %+v, %v", trash, err)
	}

	// Recuperar vuelve a su ruta, con su historial. Si la ruta está ocupada, no.
	other := e.newAsset("RENDERS/v2/toma2.png")
	if _, err := e.svc.Undelete(e.ctx, e.ana, b.Asset.ID); err == nil {
		t.Error("se recuperó encima de otro archivo")
	}
	got, err := e.svc.Undelete(e.ctx, e.ana, a.Asset.ID)
	if err != nil || got.Path != "RENDERS/toma1.png" || got.HeadVersion != 1 {
		t.Fatalf("Undelete = %+v, %v", got, err)
	}
	trash, _ = e.svc.ListDeleted(e.ctx, e.project.ID)
	if len(trash) != 1 || trash[0].Free {
		t.Errorf("papelera tras recuperar = %+v", trash)
	}
	_ = other
}

func TestDeleteAndRestoreProject(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.newAsset("ESCENAS/a.blend")
	e.mustLock(e.ana, a.Asset.ID)

	wantRule(t, e.svc.DeleteProject(e.ctx, e.juan, e.project.ID), rules.CodeLocked)
	// Quien tiene el bloqueo sí puede.
	if err := e.svc.DeleteProject(e.ctx, e.ana, e.project.ID); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.GetProject(e.ctx, e.project.ID)
	if err != nil || !p.Archived || p.DeletedAt == nil || p.DeletedBy != "Ana" {
		t.Fatalf("proyecto borrado = %+v, %v", p, err)
	}
	// Sin cambios mientras está borrado, y su nombre sigue ocupado.
	_, err = e.commit(e.ana, a.Asset.ID, 1)
	wantRule(t, err, rules.CodeProjectArchived)
	if _, err := e.svc.CreateProject(e.ctx, e.juan, "mina norte", ""); err == nil {
		t.Error("se creó otro proyecto con el nombre de uno borrado")
	}

	if err := e.svc.RestoreProject(e.ctx, e.juan, e.project.ID); err != nil {
		t.Fatal(err)
	}
	p, _ = e.svc.GetProject(e.ctx, e.project.ID)
	if p.Archived || p.DeletedAt != nil {
		t.Errorf("proyecto recuperado = %+v", p)
	}
	if got := e.mustHead(a.Asset.ID); got.Lock == nil || got.HeadVersion != 1 {
		t.Errorf("el archivo no volvió igual: %+v", got)
	}
	if e.activityCount(actionDeleteProject) != 1 || e.activityCount(actionRestoreProject) != 1 {
		t.Error("faltan acciones en la actividad")
	}
}
