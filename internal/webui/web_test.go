package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConsoleRoutesServeApplicationShell(t *testing.T) {
	handler := Handler()
	for _, path := range []string{"/", "/devices", "/devices/abc123", "/sessions", "/agent-setup", "/downloads", "/audit", "/auth/callback"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "text/html") {
				t.Fatalf("GET %s: status=%d content-type=%q", path, response.Code, response.Header().Get("Content-Type"))
			}
		})
	}
}

func TestUnknownConsoleRouteDoesNotServeApplicationShell(t *testing.T) {
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("GET /unknown: status=%d, want 404", response.Code)
	}
}
