package server

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"3d-vault/internal/vault"
)

// fakeBlend arma un .blend de Blender 5.0 sin comprimir, con miniatura de 2 × 2
// si withThumb.
func fakeBlend(withThumb bool) []byte {
	var buf bytes.Buffer
	buf.WriteString("BLENDER17-01v0501")
	block := func(code string, data []byte) {
		head := make([]byte, 32)
		copy(head, code)
		binary.LittleEndian.PutUint64(head[16:24], uint64(len(data)))
		binary.LittleEndian.PutUint64(head[24:32], 1)
		buf.Write(head)
		buf.Write(data)
	}
	block("REND", make([]byte, 72))
	if withThumb {
		test := make([]byte, 8+2*2*4)
		binary.LittleEndian.PutUint32(test[0:4], 2)
		binary.LittleEndian.PutUint32(test[4:8], 2)
		for i := 8; i < len(test); i++ {
			test[i] = 0xff
		}
		block("TEST", test)
	}
	block("GLOB", make([]byte, 256))
	block("ENDB", nil)
	return buf.Bytes()
}

func (e *env) newAssetWith(projectID int64, path string, data []byte) vault.Asset {
	e.t.Helper()
	m := e.uploadChunks(e.juanToken, data)
	resp := e.do(apiCall{method: http.MethodPost, path: "/api/assets", token: e.juanToken, clientID: "pc-1", body: map[string]any{
		"project_id": projectID, "path": path, "manifest": m, "comment": "Primera versión",
	}})
	wantStatus(e.t, resp, http.StatusCreated)
	return decode[vault.CommitResult](e.t, resp).Asset
}

func versionPath(a vault.Asset, what string) string {
	return "/versions/" + strconv.FormatInt(a.HeadVersionID, 10) + "/" + what
}

func TestBlendThumbnail(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	project := e.newProject()
	blend := e.newAssetWith(project.ID, "escenas/camion.blend", fakeBlend(true))

	b := e.newBrowser()
	b.login("juan")
	resp, body := b.get(versionPath(blend, "thumb"))
	wantPage(t, resp, body, http.StatusOK)
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q", got)
	}
	img, err := png.Decode(strings.NewReader(body))
	if err != nil {
		t.Fatalf("no es PNG: %v", err)
	}
	if img.Bounds() != image.Rect(0, 0, 2, 2) {
		t.Errorf("tamaño = %v", img.Bounds())
	}

	resp, body = b.get("/assets/" + strconv.FormatInt(blend.ID, 10))
	wantPage(t, resp, body, http.StatusOK, `src="`+versionPath(blend, "thumb")+`"`)

	// Sin miniatura: 404 y el ícono genérico en la ficha.
	plain := e.newAssetWith(project.ID, "escenas/sin.blend", fakeBlend(false))
	resp, body = b.get(versionPath(plain, "thumb"))
	wantPage(t, resp, body, http.StatusNotFound)
	resp, body = b.get("/assets/" + strconv.FormatInt(plain.ID, 10))
	wantPage(t, resp, body, http.StatusOK, "no tiene miniatura")
	if strings.Contains(body, versionPath(plain, "thumb")) {
		t.Error("la ficha enlaza una miniatura que no existe")
	}

	// Un archivo que no es .blend no tiene miniatura.
	obj := e.newAsset(project.ID, "modelos/caja.obj").Asset
	resp, body = b.get(versionPath(obj, "thumb"))
	wantPage(t, resp, body, http.StatusNotFound)

	resp, _ = e.newBrowser().get(versionPath(blend, "thumb"))
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("sin sesión = %d", resp.StatusCode)
	}
}

func TestImagesAndModelsInAssetPage(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	project := e.newProject()
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewNRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	pic := e.newAssetWith(project.ID, "texturas/Madera.PNG", pngData.Bytes())

	b := e.newBrowser()
	b.login("ana")
	resp, body := b.get(versionPath(pic, "image"))
	wantPage(t, resp, body, http.StatusOK)
	h := resp.Header
	if h.Get("Content-Type") != "image/png" || h.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(h.Get("Content-Disposition"), "inline") {
		t.Errorf("cabeceras = %v", h)
	}
	if body != pngData.String() {
		t.Error("la imagen servida no coincide")
	}
	resp, body = b.get("/assets/" + strconv.FormatInt(pic.ID, 10))
	wantPage(t, resp, body, http.StatusOK, `src="`+versionPath(pic, "image")+`"`)

	// Solo se sirven en línea las imágenes de mapa de bits.
	svg := e.newAssetWith(project.ID, "texturas/logo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	resp, body = b.get(versionPath(svg, "image"))
	wantPage(t, resp, body, http.StatusNotFound)

	model := e.newAsset(project.ID, "modelos/camion.glb").Asset
	resp, body = b.get("/assets/" + strconv.FormatInt(model.ID, 10))
	wantPage(t, resp, body, http.StatusOK, `/static/viewer.js`, `data-src="`+versionPath(model, "download")+`"`, `data-ext=".glb"`, "Encuadrar")
	if strings.Contains(body, "Cargar de todos modos") {
		t.Error("un modelo pequeño no debe pedir confirmación")
	}

	for _, path := range []string{"/static/viewer.js", "/static/three/three.module.min.js", "/static/three/addons/loaders/GLTFLoader.js"} {
		resp := e.do(apiCall{method: http.MethodGet, path: path})
		wantStatus(t, resp, http.StatusOK)
		if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "javascript") {
			t.Errorf("%s: Content-Type = %q", path, got)
		}
	}
}

func TestLargeModelAsksFirst(t *testing.T) {
	t.Parallel()

	pages, err := loadPages()
	if err != nil {
		t.Fatal(err)
	}
	asset := vault.Asset{Ext: ".fbx", SizeBytes: 300 << 20}
	var buf bytes.Buffer
	err = pages.templates["asset"].Execute(&buf, pageData{Data: assetData{Asset: asset, Preview: &preview{
		Kind: "model", Ext: asset.Ext, Size: asset.SizeBytes, Large: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Cargar de todos modos", "300,0 MB", "data-large"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("falta %q", want)
		}
	}
}
