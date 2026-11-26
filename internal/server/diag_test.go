package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiagIsLogged(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{log: slog.New(slog.NewTextHandler(&buf, nil))}
	req := httptest.NewRequest(http.MethodPost, "/api/diag", strings.NewReader(`{"state":"sync-unsupported","browser":"Chrome 131 en Android (móvil)","detail":"showDirectoryPicker: false\nx"}`))
	rec := httptest.NewRecorder()
	s.apiDiag(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("código %d", rec.Code)
	}
	for _, want := range []string{"sync-unsupported", "Chrome 131 en Android", "showDirectoryPicker: false x"} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("el registro no contiene %q: %s", want, buf.String())
		}
	}
}
