package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalIdentity(t *testing.T) {
	handler := LocalIdentity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := IdentityFromContext(r.Context())
		if !ok || user.UserID != LocalUserID || user.Username != LocalUserID {
			t.Fatalf("unexpected workbench identity: %+v", user)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusNoContent || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected local response: %d %v", recorder.Code, recorder.Header())
	}
}
