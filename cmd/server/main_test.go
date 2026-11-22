package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"ontology-engine/internal/auth"
)

func TestServerRoutes(t *testing.T) {
	provider, err := auth.New(t.Context(), auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	governance := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			t.Error("governance request did not pass through auth middleware")
		}
		_, _ = w.Write([]byte("operate"))
	})
	mcp := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("consume")) })
	health := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("healthy")) }
	web := staticWeb(fstest.MapFS{"index.html": {Data: []byte("canvas")}})
	routes := newRoutes(provider, governance, mcp, web, health, true)
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/", "canvas", 200},
		{"POST", "/", "仅支持获取页面\n", 405},
		{"POST", "/api/v1/chat/message", "operate", 200},
		{"POST", "/mcp", "consume", 200},
		{"GET", "/mcp/sse", "consume", 200},
		{"POST", "/mcp/reading-club", "consume", 200},
		{"GET", "/health", "healthy", 200},
		{"GET", "/healthz", "healthy", 200},
	} {
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		if response.Code != tc.status || response.Body.String() != tc.body {
			t.Errorf("%s %s: got %d %q, want %d %q", tc.method, tc.path, response.Code, response.Body.String(), tc.status, tc.body)
		}
	}
}

func TestDeveloperBindRequiresLoopback(t *testing.T) {
	t.Setenv("AUTH_ENABLED", "false")
	t.Setenv("PUBLIC_URL", "")
	t.Setenv("PYTHON_WORKER_URL", "http://127.0.0.1:8001")
	t.Setenv("GO_TLS_CERT_FILE", "")
	t.Setenv("GO_TLS_KEY_FILE", "")
	t.Setenv("GO_SERVER_ADDR", ":8080")
	t.Setenv("ALLOW_INSECURE_DEV", "true")
	t.Setenv("DEV_PUBLISH_HOST", "0.0.0.0")
	if _, err := loadSettings(); err == nil {
		t.Fatal("reject development mode when published on every interface")
	}
	t.Setenv("DEV_PUBLISH_HOST", "127.0.0.1")
	if _, err := loadSettings(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GO_SERVER_ADDR", "127.0.0.1:8080")
	t.Setenv("ALLOW_INSECURE_DEV", "false")
	if _, err := loadSettings(); err != nil {
		t.Fatal(err)
	}
}

func TestCookieOperationsRejectCrossOrigin(t *testing.T) {
	const origin = "https://engine.example.test:8443"
	mux := browserHeaders(origin, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		path, origin, fetchSite string
		status                  int
	}{
		{"/api/v1/chat/message", origin, "same-origin", http.StatusNoContent},
		{"/api/v1/chat/message", "https://other.example.test", "same-site", http.StatusForbidden},
		{"/auth/logout", "", "cross-site", http.StatusForbidden},
		{"/mcp", "", "", http.StatusNoContent},
	} {
		r := httptest.NewRequest(http.MethodPost, origin+tc.path, nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Sec-Fetch-Site", tc.fetchSite)
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, r)
		if res.Code != tc.status {
			t.Errorf("%s from %q: got %d, want %d", tc.path, tc.origin, res.Code, tc.status)
		}
	}
}
