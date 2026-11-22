package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"ontology-engine/internal/auth"
)

func TestServerRoutes(t *testing.T) {
	governance := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, ok := auth.IdentityFromContext(r.Context()); !ok || user.UserID != auth.LocalUserID {
			t.Error("governance request did not receive the local identity")
		}
		_, _ = w.Write([]byte("operate"))
	})
	mcp := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("consume")) })
	health := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("healthy")) }
	web := staticWeb(fstest.MapFS{"index.html": {Data: []byte("canvas")}})
	routes := newRoutes(governance, mcp, web, health, true)
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

func TestWorkbenchBindRequiresLoopback(t *testing.T) {
	t.Setenv("AUTH_ENABLED", "")
	t.Setenv("PUBLIC_URL", "")
	t.Setenv("PYTHON_WORKER_URL", "http://127.0.0.1:8001")
	t.Setenv("GO_TLS_CERT_FILE", "")
	t.Setenv("GO_TLS_KEY_FILE", "")
	for _, address := range []string{":8080", "0.0.0.0:8080", "workbench.example.test:8080"} {
		t.Setenv("GO_SERVER_ADDR", address)
		if _, err := loadSettings(); err == nil {
			t.Errorf("accepted non-loopback address %q", address)
		}
	}
	t.Setenv("GO_SERVER_ADDR", "localhost:8080")
	settings, err := loadSettings()
	if err != nil || settings.addr != "127.0.0.1:8080" {
		t.Fatalf("unexpected local address %q: %v", settings.addr, err)
	}
	t.Setenv("PUBLIC_URL", "https://workbench.example.test")
	if _, err := loadSettings(); err == nil {
		t.Fatal("accepted a non-loopback workbench origin")
	}
	t.Setenv("PUBLIC_URL", "http://127.0.0.1:8080")
	t.Setenv("AUTH_ENABLED", "true")
	if _, err := loadSettings(); err == nil {
		t.Fatal("silently started the local workbench for an incompatible identity configuration")
	}
}

func TestWorkbenchRejectsNonLoopbackHost(t *testing.T) {
	handler := browserHeaders("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tt := range []struct {
		host string
		want int
	}{
		{"127.0.0.1:8080", http.StatusNoContent},
		{"localhost", http.StatusNoContent},
		{"[::1]:8080", http.StatusNoContent},
		{"workbench.example.test", http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/index", nil)
		req.Host = tt.host
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != tt.want {
			t.Errorf("host %q: got %d, want %d", tt.host, res.Code, tt.want)
		}
	}
}

func TestBrowserWritesRejectCrossOrigin(t *testing.T) {
	const origin = "http://127.0.0.1:8080"
	mux := browserHeaders(origin, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		path, origin, fetchSite string
		status                  int
	}{
		{"/api/v1/chat/message", origin, "same-origin", http.StatusNoContent},
		{"/api/v1/chat/message", "https://other.example.test", "same-site", http.StatusForbidden},
		{"/api/v1/chat/message", "", "cross-site", http.StatusForbidden},
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
