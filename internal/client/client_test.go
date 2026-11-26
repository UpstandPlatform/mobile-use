package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestAcceptsNoContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	api, err := New(server.URL, "test-token-not-a-secret")
	if err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if err := api.Request(t.Context(), "DELETE", "/v1/sessions/s/video/v", nil, &raw); err != nil {
		t.Fatalf("raw 204 response: %v", err)
	}
	var typed map[string]any
	if err := api.Request(t.Context(), "DELETE", "/v1/sessions/s/video/v", nil, &typed); err != nil {
		t.Fatalf("typed 204 response: %v", err)
	}
}
