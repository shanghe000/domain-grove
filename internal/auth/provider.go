package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

const sessionCookie = "ontology_session"

var (
	errUnauthorized = errors.New("需要通过 身份服务 登录")
	errForbidden    = errors.New("当前账号未获本体治理权限")
	errUnavailable  = errors.New("身份服务 暂时不可用，请稍后重试")
)

type UserIdentity struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Dept     string `json:"dept"`
}

type AuthProvider interface {
	VerifyRequest(*http.Request) (*UserIdentity, error)
}

type identityKey struct{}

func IdentityFromContext(ctx context.Context) (*UserIdentity, bool) {
	user, ok := ctx.Value(identityKey{}).(*UserIdentity)
	return user, ok
}

type Config struct {
	Enabled        bool
	AuthURL        string
	VerifyTokenURL string
	AppID          string
	TLSCAFile      string
}

func ConfigFromEnv() Config {
	enabled := strings.TrimSpace(os.Getenv("AUTH_ENABLED"))
	return Config{
		Enabled: enabled != "" && !strings.EqualFold(enabled, "false"),
		AuthURL: os.Getenv("AUTH_LOGIN_URL"), VerifyTokenURL: os.Getenv("AUTH_VERIFY_TOKEN_URL"),
		AppID: os.Getenv("AUTH_APP_ID"), TLSCAFile: os.Getenv("AUTH_TLS_CA_FILE"),
	}
}

type Provider struct {
	config  Config
	http    *http.Client
	origins []string
}

func New(_ context.Context, config Config) (*Provider, error) {
	p := &Provider{config: config}
	if !config.Enabled {
		return p, nil
	}
	if config.AppID == "" {
		return nil, errors.New("AUTH_APP_ID is required")
	}
	auth, e1 := httpsURL(config.AuthURL)
	verify, e2 := httpsURL(config.VerifyTokenURL)
	if e1 != nil || e2 != nil || verify.RawQuery != "" {
		return nil, errors.New("configure HTTPS AUTH_LOGIN_URL and AUTH_VERIFY_TOKEN_URL")
	}
	p.origins = []string{strings.ToLower(verify.Host)}
	p.config.AuthURL = auth.String()
	return p, p.setupTransport()
}

func httpsURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("invalid identity HTTPS URL")
	}
	return u, nil
}

type restrictedTransport struct {
	transport http.RoundTripper
	origins   []string
}

func (t restrictedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.User != nil || !slices.Contains(t.origins, strings.ToLower(r.URL.Host)) {
		return nil, errors.New("identity endpoint outside configured origins")
	}
	return t.transport.RoundTrip(r)
}

func (p *Provider) setupTransport() error {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if p.config.TLSCAFile != "" {
		certs, err := os.ReadFile(p.config.TLSCAFile)
		if err != nil {
			return errors.New("cannot read AUTH_TLS_CA_FILE")
		}
		pool, err := x509.SystemCertPool()
		if err != nil || !pool.AppendCertsFromPEM(certs) {
			return errors.New("cannot load 身份服务 trust certificates")
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	p.http = &http.Client{Timeout: 5 * time.Second, Transport: restrictedTransport{transport, p.origins},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return nil
}

func (p *Provider) Register(mux *http.ServeMux) {
	if p.config.Enabled {
		mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			http.SetCookie(w, &http.Cookie{
				Name: sessionCookie, Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
			w.WriteHeader(http.StatusNoContent)
		})
	}
}

func (p *Provider) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		user, err := p.VerifyRequest(r)
		if err != nil {
			p.failure(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, user)))
	})
}

func (p *Provider) VerifyRequest(r *http.Request) (*UserIdentity, error) {
	if !p.config.Enabled {
		return &UserIdentity{UserID: "dev_user", Username: "dev_user"}, nil
	}
	return p.verifyToken(r)
}

func (p *Provider) failure(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	message := errUnavailable.Error()
	result := map[string]string{}
	switch {
	case errors.Is(err, errUnauthorized):
		status, message = http.StatusUnauthorized, errUnauthorized.Error()
		result["login_url"] = p.config.AuthURL
	case errors.Is(err, errForbidden):
		status, message = http.StatusForbidden, errForbidden.Error()
	}
	result["error"] = message
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}

// A configured token verifier receives only the presented credential and app ID.
// Its response supplies the authenticated user fields for this application.
func (p *Provider) verifyToken(r *http.Request) (*UserIdentity, error) {
	authz := r.Header.Get("Authorization")
	if authz == "" {
		if provided, err := r.Cookie(sessionCookie); err == nil {
			authz = "Bearer " + provided.Value
		}
	}
	scheme, token, ok := strings.Cut(authz, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || len(token) > 8192 || strings.ContainsAny(token, " \t\r\n") {
		return nil, errUnauthorized
	}
	body, _ := json.Marshal(map[string]string{"app_id": p.config.AppID})
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.config.VerifyTokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, errUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	var result struct {
		UserIdentity
		Active bool `json:"active"`
	}
	if err := p.readJSON(request, &result, true); err != nil {
		return nil, err
	}
	if !result.Active || strings.TrimSpace(result.UserID) == "" || strings.TrimSpace(result.Username) == "" {
		return nil, errUnauthorized
	}
	return &result.UserIdentity, nil
}

func (p *Provider) readJSON(r *http.Request, result any, verify bool) error {
	response, err := p.http.Do(r)
	if err != nil {
		return errUnavailable
	}
	defer response.Body.Close()
	if verify && response.StatusCode == http.StatusUnauthorized {
		return errUnauthorized
	}
	if verify && response.StatusCode == http.StatusForbidden {
		return errForbidden
	}
	if response.StatusCode != http.StatusOK {
		return errUnavailable
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 32<<10))
	if decoder.Decode(result) != nil || decoder.Decode(new(any)) != io.EOF {
		return errUnavailable
	}
	return nil
}
