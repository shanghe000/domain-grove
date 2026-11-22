package spaceapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "github.com/modelcontextprotocol/go-sdk/mcp"

	"ontology-engine/internal/index"
	"ontology-engine/internal/indexapi"
	"ontology-engine/internal/spaces"
)

type clientPeer struct {
	bearer  string
	mutex   sync.Mutex
	session string
}

func (p *clientPeer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+p.bearer)
	res, err := http.DefaultTransport.RoundTrip(req)
	if err == nil {
		if session := res.Header.Get("Mcp-Session-Id"); session != "" {
			p.mutex.Lock()
			p.session = session
			p.mutex.Unlock()
		}
	}
	return res, err
}

func (p *clientPeer) sessionID() string {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	return p.session
}

func connect(t *testing.T, ctx context.Context, endpoint, token string, sse bool) (*protocol.ClientSession, *clientPeer) {
	t.Helper()
	peer := &clientPeer{bearer: token}
	httpClient := &http.Client{Transport: peer}
	var tx protocol.Transport = &protocol.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient, MaxRetries: -1}
	if sse {
		tx = &protocol.SSEClientTransport{Endpoint: endpoint, HTTPClient: httpClient}
	}
	s, err := protocol.NewClient(&protocol.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, tx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, peer
}

func TestProjectMCPTokenAndSessionIsolation(t *testing.T) {
	store, cat := openFixture(t)
	api := New(cat, false, nil, "project-agent")
	defaultHandler, err := indexapi.NewMCPHandler(store, "fixture-default", "fixture-default-submit", "")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NamedMCP(defaultHandler)
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(handler)
	t.Cleanup(host.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	for _, id := range []string{"alpha", "beta"} {
		if _, err := cat.Create(ctx, "owner", id, id); err != nil {
			t.Fatal(err)
		}
	}
	rotate := func(project, scope string) string {
		t.Helper()
		token, err := cat.RotateToken(ctx, project, "owner", scope)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	aRead := rotate("alpha", spaces.ScopeRead)
	aSubmit := rotate("alpha", spaces.ScopeSubmit)
	bSubmit := rotate("beta", spaces.ScopeSubmit)

	response(t, handler, http.MethodGet, "/mcp/alpha", "fixture-default", "", http.StatusUnauthorized)
	response(t, handler, http.MethodGet, "/mcp/beta", aSubmit, "", http.StatusUnauthorized)
	response(t, handler, http.MethodGet, "/mcp/alpha/sse", bSubmit, "", http.StatusUnauthorized)
	response(t, handler, http.MethodGet, "/mcp", aRead, "", http.StatusUnauthorized)
	response(t, handler, http.MethodGet, "/mcp/alpha/unknown", aRead, "", http.StatusNotFound)
	connect(t, ctx, host.URL+"/mcp", "fixture-default", false)
	connect(t, ctx, host.URL+"/mcp/default", "fixture-default", false)

	reader, _ := connect(t, ctx, host.URL+"/mcp/alpha/sse", aRead, true)
	readTools, err := reader.ListTools(ctx, nil)
	if err != nil || len(readTools.Tools) != 6 {
		t.Fatalf("named SSE reader tools: %v %v", readTools, err)
	}
	submitter, peer := connect(t, ctx, host.URL+"/mcp/alpha", aSubmit, false)
	input := index.SubmitInput{
		RequestID: "space-submit", Title: "alpha 候选",
		Patch: index.Patch{UpsertNodes: []index.Node{
			{ID: "unique_alpha", Kind: index.KindDomain, Label: "Alpha"},
		}},
	}
	result, err := submitter.CallTool(ctx, &protocol.CallToolParams{Name: "submit_index_proposal", Arguments: input})
	if err != nil || result.IsError {
		t.Fatalf("named submission failed: %v %v", result, err)
	}
	alpha, _ := cat.Store(ctx, "alpha")
	beta, _ := cat.Store(ctx, "beta")
	aItems, _ := alpha.List(ctx, "")
	bItems, _ := beta.List(ctx, "")
	defaultItems, _ := store.List(ctx, "")
	if len(aItems) != 1 || aItems[0].SubmittedBy != "project-agent" || len(bItems) != 0 || len(defaultItems) != 0 {
		t.Fatal("MCP submission crossed the project boundary or used the wrong actor")
	}

	oldSession := peer.sessionID()
	if oldSession == "" {
		t.Fatal("fixture did not obtain a session")
	}
	trySession := func(path, token string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Mcp-Session-Id", oldSession)
		req.Header.Set("Accept", "text/event-stream")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != want {
			t.Fatalf("token/session replay on %s: got %d want %d", path, out.Code, want)
		}
	}
	trySession("/mcp/beta", bSubmit, http.StatusNotFound)
	trySession("/mcp/alpha", aRead, http.StatusNotFound)

	rotated := rotate("alpha", spaces.ScopeSubmit)
	trySession("/mcp/alpha", aSubmit, http.StatusUnauthorized)
	trySession("/mcp/alpha", rotated, http.StatusNotFound)
	connect(t, ctx, host.URL+"/mcp/alpha", rotated, false)
	if otherCfg := response(t, handler, http.MethodGet, "/mcp/missing", aSubmit, "", http.StatusUnauthorized); !strings.Contains(otherCfg.Body.String(), "Bearer token is required") {
		t.Fatal("unknown project gave a project-specific failure")
	}
}
