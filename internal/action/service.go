package action

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ontology-engine/internal/auth"
	"ontology-engine/internal/ontology"
	"ontology-engine/internal/storage"
)

type Config struct {
	DataDir     string
	WorkerURL   string
	WorkerToken string
	Mock        bool
}

type Service struct {
	store     *storage.Store
	config    Config
	client    *http.Client
	graphMu   sync.RWMutex
	graph     *ontology.Graph
	version   string
	writes    sync.Mutex
	ingestion chan struct{}
	events    eventHub
}

func New(ctx context.Context, store *storage.Store, config Config) (*Service, error) {
	worker, err := url.Parse(config.WorkerURL)
	if err != nil || (worker.Scheme != "http" && worker.Scheme != "https") || worker.Host == "" ||
		worker.User != nil || worker.RawQuery != "" || worker.Fragment != "" {
		return nil, errors.New("invalid PYTHON_WORKER_URL")
	}
	config.WorkerURL = strings.TrimRight(config.WorkerURL, "/")
	config.DataDir, err = filepath.Abs(config.DataDir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(config.DataDir, 0o750); err != nil {
		return nil, err
	}
	graph, err := store.LoadGraph(ctx)
	if err != nil {
		return nil, err
	}
	var version string
	err = store.DB.QueryRowContext(ctx, `SELECT value FROM engine_metadata WHERE key='graph_revision'`).Scan(&version)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if version == "" {
		version = "seed"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	service := &Service{
		store: store, config: config, graph: graph, version: version,
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		ingestion: make(chan struct{}, 1),
	}
	if err := service.refreshExports(graph); err != nil {
		return nil, fmt.Errorf("refresh published exports: %w", err)
	}
	return service, nil
}

func (s *Service) Graph() *ontology.Graph {
	graph, _ := s.snapshot()
	return graph
}

func (s *Service) snapshot() (*ontology.Graph, string) {
	s.graphMu.RLock()
	defer s.graphMu.RUnlock()
	return s.graph, s.version
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/state", wrap(s.state))
	mux.HandleFunc("GET /api/v1/graph", wrap(s.readGraph))
	mux.HandleFunc("GET /api/v1/graph/context/{node_id}", wrap(s.nodeContext))
	mux.HandleFunc("GET /api/v1/artifacts/{node_id}/download", wrap(s.download))
	mux.HandleFunc("GET /api/v1/artifacts/{node_id}/document", wrap(s.readDocument))
	mux.HandleFunc("POST /api/v1/chat/message", wrap(s.chat))
	mux.HandleFunc("POST /api/v1/actions/patch_draft_graph", wrap(s.patch))
	mux.HandleFunc("POST /api/v1/actions/undo_draft", wrap(s.undo))
	mux.HandleFunc("POST /api/v1/actions/commit_draft", wrap(s.commit))
	mux.HandleFunc("POST /api/v1/actions/discard_draft", wrap(s.discard))
	mux.HandleFunc("GET /api/v1/events/stream", wrap(s.stream))
}

type apiError struct {
	status int
	text   string
}

func (e *apiError) Error() string { return e.text }

func problem(status int, text string) error { return &apiError{status, text} }

func wrap(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "请登录后继续。"})
			return
		}
		if err := fn(w, r); err != nil {
			var api *apiError
			if errors.As(err, &api) {
				writeJSON(w, api.status, map[string]string{"error": api.text})
				return
			}
			slog.Error("Ontology request failed", "route", r.Pattern, "type", fmt.Sprintf("%T", err))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "本体引擎暂时无法完成操作，请稍后重试。"})
		}
	}
}

func user(r *http.Request) *auth.UserIdentity {
	identity, _ := auth.IdentityFromContext(r.Context())
	return identity
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decode(w http.ResponseWriter, r *http.Request, max int64, target any) error {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		return problem(http.StatusUnsupportedMediaType, "请使用 JSON 发送请求。")
	}
	r.Body = http.MaxBytesReader(w, r.Body, max)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			return problem(http.StatusRequestEntityTooLarge, "投喂资料超过单次上传上限。")
		}
		return problem(http.StatusBadRequest, "请求 JSON 无法读取。")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return problem(http.StatusBadRequest, "每次只接受一个 JSON 对象。")
	}
	return nil
}

func newID() string {
	var value [16]byte
	_, _ = rand.Read(value[:])
	return hex.EncodeToString(value[:])
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *Service) state(w http.ResponseWriter, r *http.Request) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	graph, version := s.snapshot()
	item, err := s.findDraft(r.Context(), user(r), r.URL.Query().Get("session_id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"graph": graph.Data(), "version": version, "draft": item,
		"user": user(r), "mock": s.config.Mock,
	})
	return nil
}

func (s *Service) readGraph(w http.ResponseWriter, _ *http.Request) error {
	graph, version := s.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{"graph": graph.Data(), "version": version})
	return nil
}

func (s *Service) nodeContext(w http.ResponseWriter, r *http.Request) error {
	result, err := s.Graph().GetNodeContext(r.PathValue("node_id"), ontology.DefaultBudget, false)
	if errors.Is(err, ontology.ErrUnknownNode) {
		return problem(http.StatusNotFound, "没有找到已发布的节点。")
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}

func (s *Service) lockIngestion(ctx context.Context) (func(), error) {
	select {
	case s.ingestion <- struct{}{}:
		return func() { <-s.ingestion }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
