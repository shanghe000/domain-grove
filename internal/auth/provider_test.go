package auth

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeveloperIdentity(t *testing.T) {
	provider, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	provider.Middleware(currentUser).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"user_id":"dev_user"`) {
		t.Fatalf("unexpected developer response: %d %s", recorder.Code, recorder.Body)
	}
}

func TestDirectVerification(t *testing.T) {
	verifier := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body["app_id"] != "test-app" {
			t.Error("incorrect direct verifier request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer valid-test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "user_id": "known", "username": "分析师", "dept": "BI"})
	}))
	defer verifier.Close()
	config := Config{Enabled: true, AppID: "test-app", AuthURL: "https://login.example.test/", VerifyTokenURL: verifier.URL, TLSCAFile: testCertificate(t, verifier)}
	provider, err := New(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		token  string
		cookie bool
		status int
		body   string
	}{
		{name: "missing", status: 401, body: config.AuthURL},
		{name: "invalid", token: "invalid", status: 401, body: config.AuthURL},
		{name: "bearer", token: "valid-test-token", status: 200, body: `"dept":"BI"`},
		{name: "cookie", token: "valid-test-token", cookie: true, status: 200, body: `"dept":"BI"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.cookie {
				req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tc.token})
			} else if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			res := httptest.NewRecorder()
			provider.Middleware(currentUser).ServeHTTP(res, req)
			if res.Code != tc.status || !strings.Contains(res.Body.String(), tc.body) {
				t.Fatalf("got %d %s", res.Code, res.Body)
			}
		})
	}
}

func testCertificate(t *testing.T, server *httptest.Server) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "ca.pem")
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(filename, cert, 0600); err != nil {
		t.Fatal(err)
	}
	return filename
}

var currentUser = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFromContext(r.Context())
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_ = json.NewEncoder(w).Encode(identity)
})
