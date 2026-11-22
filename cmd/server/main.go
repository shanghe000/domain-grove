package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ontology-engine/internal/action"
	"ontology-engine/internal/auth"
	"ontology-engine/internal/index"
	"ontology-engine/internal/indexai"
	"ontology-engine/internal/indexapi"
	"ontology-engine/internal/mcp"
	"ontology-engine/internal/spaceapi"
	"ontology-engine/internal/spaces"
	"ontology-engine/internal/storage"
)

type settings struct {
	addr, dataDir, sqlitePath, distDir string
	certFile, keyFile, origin          string
	workerURL, workerToken, mcpToken   string
	mode, indexPath, submitToken       string
	submitterID                        string
	mock, allowDevReview               bool
	identity                           auth.Config
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func defaultEnv(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func loadSettings() (settings, error) {
	c := settings{
		addr: defaultEnv("GO_SERVER_ADDR", "127.0.0.1:8080"), dataDir: defaultEnv("DATA_DIR", "./data"),
		distDir:  defaultEnv("WEB_DIST_DIR", "./web/dist"),
		certFile: os.Getenv("GO_TLS_CERT_FILE"), keyFile: os.Getenv("GO_TLS_KEY_FILE"),
		workerURL:   defaultEnv("PYTHON_WORKER_URL", "http://127.0.0.1:8001"),
		workerToken: os.Getenv("WORKER_API_TOKEN"), mcpToken: os.Getenv("MCP_API_TOKEN"),
		mode: defaultEnv("ONTOLOGY_MODE", "index"), submitToken: os.Getenv("MCP_SUBMIT_TOKEN"),
		submitterID:    defaultEnv("MCP_SUBMITTER_ID", "bi-agent"),
		allowDevReview: strings.EqualFold(os.Getenv("INDEX_ALLOW_DEV_REVIEW"), "true"),
		mock:           strings.EqualFold(strings.TrimSpace(os.Getenv("MOCK_EXTERNAL_APIS")), "true"), identity: auth.ConfigFromEnv(),
	}
	c.sqlitePath = defaultEnv("SQLITE_PATH", filepath.Join(c.dataDir, "ontology.db"))
	c.indexPath = defaultEnv("INDEX_DB_PATH", filepath.Join(c.dataDir, "index.db"))
	if c.mode != "index" && c.mode != "legacy" {
		return c, errors.New("ONTOLOGY_MODE must be index or legacy")
	}
	host, _, err := net.SplitHostPort(c.addr)
	if err != nil {
		return c, errors.New("GO_SERVER_ADDR must be a host:port address")
	}
	localPublish := strings.EqualFold(os.Getenv("ALLOW_INSECURE_DEV"), "true") && loopback(os.Getenv("DEV_PUBLISH_HOST"))
	if !c.identity.Enabled && !loopback(host) && !localPublish {
		return c, errors.New("without configured authentication, GO_SERVER_ADDR must bind to loopback")
	}
	if c.allowDevReview && (c.identity.Enabled || !loopback(host)) {
		return c, errors.New("INDEX_ALLOW_DEV_REVIEW requires a loopback listener without external authentication")
	}
	if (c.certFile == "") != (c.keyFile == "") {
		return c, errors.New("configure GO_TLS_CERT_FILE and GO_TLS_KEY_FILE together")
	}
	public := strings.TrimSpace(os.Getenv("PUBLIC_URL"))
	publicExplicit := public != ""
	if public != "" {
		u, err := url.Parse(public)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" || (publicExplicit && u.Path != "" && u.Path != "/") {
			return c, errors.New("PUBLIC_URL must contain only the HTTP(S) origin")
		}
		c.origin = u.Scheme + "://" + u.Host
	}
	if c.identity.Enabled && !strings.HasPrefix(c.origin, "https://") {
		return c, errors.New("external authentication requires an HTTPS PUBLIC_URL")
	}
	if c.mode == "index" {
		return c, nil
	}
	worker, err := url.Parse(c.workerURL)
	if err != nil || (worker.Scheme != "http" && worker.Scheme != "https") || worker.Hostname() == "" ||
		worker.User != nil || worker.RawQuery != "" || worker.Fragment != "" {
		return c, errors.New("PYTHON_WORKER_URL must be an HTTP(S) address without credentials, query or fragment")
	}
	if !c.mock && !loopback(worker.Hostname()) && c.workerToken == "" {
		return c, errors.New("WORKER_API_TOKEN must be configured for a non-loopback Worker")
	}
	return c, nil
}

func loopback(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func run(ctx context.Context) error {
	c, err := loadSettings()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.dataDir, 0750); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	provider, err := auth.New(startup, c.identity)
	if err != nil {
		return err
	}
	operate, consume, health, closePlane, err := configurePlane(startup, c)
	if err != nil {
		return err
	}
	defer closePlane()
	routes := newRoutes(provider, operate, consume, staticWeb(os.DirFS(c.distDir)), health, c.mode == "index")

	server := &http.Server{
		Addr: c.addr, Handler: browserHeaders(c.origin, routes),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 90 * time.Second,
		IdleTimeout: 75 * time.Second, MaxHeaderBytes: 64 << 10,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	served := make(chan error, 1)
	go func() {
		if c.certFile != "" {
			served <- server.ListenAndServeTLS(c.certFile, c.keyFile)
			return
		}
		served <- server.ListenAndServe()
	}()
	log.Printf("ontology engine listening on %s", c.addr)
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
		shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}

func configurePlane(ctx context.Context, c settings) (*http.ServeMux, http.Handler, http.HandlerFunc, func(), error) {
	operate := http.NewServeMux()
	if c.mode == "index" {
		store, err := index.Open(c.indexPath)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("open ontology index: %w", err)
		}
		consume, err := indexapi.NewMCPHandler(store, c.mcpToken, c.submitToken, c.submitterID)
		if err != nil {
			store.Close()
			return nil, nil, nil, nil, fmt.Errorf("configure ontology MCP: %w", err)
		}
		indexapi.RegisterReview(operate, store, c.allowDevReview)
		var modeling indexapi.ModelingPlanner
		if planner, err := indexai.New(indexai.ConfigFromEnv()); err == nil {
			modeling = planner
		} else {
			log.Print("AI modeling provider configuration is invalid; index and review remain available")
		}
		indexapi.RegisterModeling(operate, store, c.allowDevReview, modeling)
		catalog, err := spaces.Open(filepath.Join(c.dataDir, "projects"), store)
		if err != nil {
			store.Close()
			return nil, nil, nil, nil, fmt.Errorf("open project spaces: %w", err)
		}
		projects := spaceapi.New(catalog, c.allowDevReview, modeling, c.submitterID, nil)
		projects.Register(operate)
		consume, err = projects.NamedMCP(consume)
		if err != nil {
			catalog.Close()
			store.Close()
			return nil, nil, nil, nil, fmt.Errorf("configure project MCP: %w", err)
		}
		health := func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			if err := store.Health(r.Context()); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"ok":false,"mode":"index"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"mode":"index"}`))
		}
		return operate, consume, health, func() { catalog.Close(); store.Close() }, nil
	}
	store, err := storage.Open(c.sqlitePath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("open legacy ontology database: %w", err)
	}
	if err := store.Seed(ctx, c.dataDir, c.mock); err != nil {
		store.Close()
		return nil, nil, nil, nil, fmt.Errorf("initialize legacy ontology: %w", err)
	}
	service, err := action.New(ctx, store, action.Config{
		DataDir: c.dataDir, WorkerURL: c.workerURL, WorkerToken: c.workerToken, Mock: c.mock,
	})
	if err != nil {
		store.Close()
		return nil, nil, nil, nil, fmt.Errorf("initialize legacy governance: %w", err)
	}
	service.Register(operate)
	consume := mcp.NewHandler(c.mcpToken, service.Graph, service.ReadDoc, service.QuerySQL, service.Tracking)
	health := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := service.WorkerHealth(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"ok":false,"worker":"unavailable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
	return operate, consume, health, func() { store.Close() }, nil
}

func newRoutes(provider *auth.Provider, operate, consume, web http.Handler, health http.HandlerFunc, namedMCP bool) *http.ServeMux {
	mux := http.NewServeMux()
	provider.Register(mux)
	mux.Handle("/api/v1/", provider.Middleware(operate))
	mux.Handle("/mcp", consume)
	mux.Handle("/mcp/sse", consume)
	if namedMCP {
		mux.Handle("/mcp/", consume)
	}
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /health", health)
	mux.Handle("/", web)
	return mux
}

func staticWeb(dist fs.FS) http.Handler {
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "仅支持获取页面", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/" {
			if _, err := fs.Stat(dist, "index.html"); err != nil {
				http.Error(w, "尚未构建工作台前端", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		info, err := fs.Stat(dist, name)
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

func browserHeaders(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if isBrowserMutation(r) {
			allowed := origin
			if allowed == "" {
				scheme := "http"
				if r.TLS != nil {
					scheme = "https"
				}
				allowed = scheme + "://" + r.Host
			}
			source := r.Header.Get("Origin")
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" || (source != "" && !strings.EqualFold(source, allowed)) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"请从工作台发起操作"}`))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isBrowserMutation(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") && r.URL.Path != "/auth/logout" {
		return false
	}
	return r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
}
