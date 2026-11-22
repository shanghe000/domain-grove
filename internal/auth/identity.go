package auth

import (
	"context"
	"net/http"
)

const LocalUserID = "dev_user"

type UserIdentity struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
}

type identityKey struct{}

func IdentityFromContext(ctx context.Context) (*UserIdentity, bool) {
	user, ok := ctx.Value(identityKey{}).(*UserIdentity)
	return user, ok
}

func WithIdentity(ctx context.Context, user *UserIdentity) context.Context {
	return context.WithValue(ctx, identityKey{}, user)
}

// LocalIdentity is used by the workbench on a loopback listener.
func LocalIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		user := &UserIdentity{UserID: LocalUserID, Username: LocalUserID}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), user)))
	})
}
