package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMountSirveInterfazYAssets(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux)

	tests := []struct {
		name        string
		path        string
		contentType string
		body        string
	}{
		{name: "pagina principal", path: "/", contentType: "text/html", body: "FutGo"},
		{name: "hoja de estilos", path: "/assets/app.css", contentType: "text/css"},
		{name: "javascript", path: "/assets/app.js", contentType: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, esperaba %d", rec.Code, http.StatusOK)
			}
			if tt.contentType != "" && !strings.Contains(rec.Header().Get("Content-Type"), tt.contentType) {
				t.Errorf("Content-Type = %q, esperaba %q", rec.Header().Get("Content-Type"), tt.contentType)
			}
			if tt.body != "" && !strings.Contains(rec.Body.String(), tt.body) {
				t.Errorf("body no contiene %q", tt.body)
			}
		})
	}
}
