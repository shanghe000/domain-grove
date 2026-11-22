package indexapi

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	protocol "github.com/modelcontextprotocol/go-sdk/mcp"

	"ontology-engine/internal/auth"
	"ontology-engine/internal/index"
)

const (
	readTestToken   = "fixture-read-bearer"
	submitTestToken = "fixture-submit-bearer"
	reviewTestToken = "fixture-session"
)

type tokenTransport string

func (token tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+string(token))
	return http.DefaultTransport.RoundTrip(req)
}

func indexStore(t *testing.T) *index.Store {
	t.Helper()
	store, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func clientSession(t *testing.T, ctx context.Context, address, token string, legacy bool) *protocol.ClientSession {
	t.Helper()
	httpClient := &http.Client{Transport: tokenTransport(token)}
	var transport protocol.Transport = &protocol.StreamableClientTransport{Endpoint: address, HTTPClient: httpClient, MaxRetries: -1}
	if legacy {
		transport = &protocol.SSEClientTransport{Endpoint: address, HTTPClient: httpClient}
	}
	client := protocol.NewClient(&protocol.Implementation{Name: "index-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func callIndex(t *testing.T, ctx context.Context, session *protocol.ClientSession, name string, args any) *protocol.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &protocol.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func decodeResult(t *testing.T, result *protocol.CallToolResult, target any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool error: %+v", result.Content)
	}
	payload, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		t.Fatalf("decode %s: %s", payload, err)
	}
}

func TestIndexMCPScopesAndHumanPublication(t *testing.T) {
	store := indexStore(t)
	handler, err := NewMCPHandler(store, readTestToken, submitTestToken, "bi-metrics-agent")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	t.Cleanup(cancel)
	reader := clientSession(t, ctx, server.URL+"/mcp", readTestToken, false)
	submitter := clientSession(t, ctx, server.URL+"/mcp", submitTestToken, false)
	for _, transport := range []struct {
		name     string
		legacy   bool
		endpoint string
	}{{"streamable", false, "/mcp"}, {"sse", true, "/mcp/sse"}} {
		t.Run(transport.name, func(t *testing.T) {
			for _, role := range []struct {
				token      string
				wantSubmit bool
			}{{readTestToken, false}, {submitTestToken, true}} {
				session := clientSession(t, ctx, server.URL+transport.endpoint, role.token, transport.legacy)
				listed, err := session.ListTools(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				actual := []string{}
				for _, tool := range listed.Tools {
					actual = append(actual, tool.Name)
					if tool.Annotations == nil || tool.Annotations.ReadOnlyHint != (tool.Name != "submit_index_proposal") {
						t.Fatalf("incorrect annotations: %+v", tool)
					}
				}
				expected := []string{"find_index_nodes", "get_index_graph", "get_index_node", "get_index_schema", "query_index_changes"}
				if role.wantSubmit {
					expected = append(expected, "submit_index_proposal")
				}
				expected = append(expected, "traverse_index")
				if !reflect.DeepEqual(actual, expected) {
					t.Fatalf("tools = %v, want %v", actual, expected)
				}
			}
		})
	}

	var schema schemaResult
	decodeResult(t, callIndex(t, ctx, reader, "get_index_schema", map[string]any{}), &schema)
	if schema.SchemaVersion != index.SemanticVersion ||
		!bytes.Contains(schema.Submission, []byte(`"request_id"`)) || !bytes.Contains(schema.Graph, []byte(`"nodes"`)) {
		t.Fatalf("incomplete schema: %+v", schema)
	}

	input := index.SubmitInput{
		RequestID:    "fixture-publication-304",
		Title:        "补充留存索引",
		Rationale:    "让 BI Agent 定位留存口径文档。",
		BaseRevision: 0,
		Patch: index.Patch{
			UpsertNodes: []index.Node{
				{ID: "reporting", Kind: index.KindDomain, Label: "经营分析"},
				{ID: "retention", Kind: index.KindMetric, ParentID: "reporting", Label: "留存"},
				{ID: "retention_doc", Kind: index.KindDocument, ParentID: "retention", Label: "留存口径",
					Ref: &index.Reference{Repository: "bi-spec", Path: "metrics/retention.md", Anchor: "定义"}},
			},
			RemoveNodeIDs: []string{},
			UpsertEdges:   []index.Edge{{ID: "retention-doc", From: "retention", To: "retention_doc", Relation: "defined_in"}},
			RemoveEdgeIDs: []string{},
		},
	}
	if result, err := reader.CallTool(ctx, &protocol.CallToolParams{Name: "submit_index_proposal", Arguments: input}); err == nil && !result.IsError {
		t.Fatal("reader unexpectedly submitted a proposal")
	}
	if result, err := submitter.CallTool(ctx, &protocol.CallToolParams{Name: "approve_index_proposal", Arguments: map[string]any{"id": "arbitrary"}}); err == nil && !result.IsError {
		t.Fatal("submitter unexpectedly reviewed a proposal")
	}

	var pending index.Proposal
	decodeResult(t, callIndex(t, ctx, submitter, "submit_index_proposal", input), &pending)
	if pending.Status != index.StatusPending || pending.SubmittedBy != "bi-metrics-agent" {
		t.Fatalf("unexpected candidate: %+v", pending)
	}
	var graph index.Graph
	decodeResult(t, callIndex(t, ctx, reader, "get_index_graph", map[string]any{}), &graph)
	if graph.Revision != 0 || len(graph.Nodes) != 0 {
		t.Fatalf("unreviewed proposal altered MCP graph: %+v", graph)
	}
	if result := callIndex(t, ctx, reader, "get_index_node", map[string]string{"node_id": "retention"}); !result.IsError {
		t.Fatal("unreviewed node appeared in MCP")
	}

	review := authenticatedReview(t, store)
	url := "/api/v1/index/proposals/" + pending.ID
	requireStatus(t, reviewRequest(review, http.MethodPost, url+"/review", submitTestToken,
		`{"decision":"approve","note":"机器不能发布"}`), http.StatusUnauthorized)
	requireStatus(t, reviewRequest(review, http.MethodPost, url+"/review", reviewTestToken,
		`{"decision":"approve","reviewed_by":"spoofed"}`), http.StatusBadRequest)
	detail := reviewRequest(review, http.MethodGet, url, reviewTestToken, "")
	requireStatus(t, detail, http.StatusOK)
	var pendingDetail proposalDetail
	if err := json.NewDecoder(detail.Body).Decode(&pendingDetail); err != nil {
		t.Fatal(err)
	}
	if pendingDetail.Preview == nil || len(pendingDetail.Preview.Nodes) != 3 || pendingDetail.Proposal.Status != index.StatusPending {
		t.Fatalf("unexpected review preview: %+v", pendingDetail)
	}
	approved := reviewRequest(review, http.MethodPost, url+"/review", reviewTestToken,
		`{"decision":"approve","note":"核对文档路径。"}`)
	requireStatus(t, approved, http.StatusOK)
	var reviewed index.Proposal
	if err := json.NewDecoder(approved.Body).Decode(&reviewed); err != nil {
		t.Fatal(err)
	}
	if reviewed.Status != index.StatusApproved || reviewed.ReviewedBy != "reviewer-fixture" {
		t.Fatalf("unexpected reviewer audit: %+v", reviewed)
	}
	requireStatus(t, reviewRequest(review, http.MethodPost, url+"/review", reviewTestToken,
		`{"decision":"reject","note":"不能重复审核"}`), http.StatusConflict)

	var node nodeResult
	decodeResult(t, callIndex(t, ctx, reader, "get_index_node", map[string]string{"node_id": "retention"}), &node)
	if node.Parent == nil || node.Parent.ID != "reporting" || len(node.Children) != 1 ||
		node.Children[0].Ref == nil || node.Children[0].Ref.Path != "metrics/retention.md" ||
		len(node.Outgoing) != 1 || len(node.Incoming) != 0 {
		t.Fatalf("unexpected published context: %+v", node)
	}
	yaml := callIndex(t, ctx, submitter, "get_index_graph", map[string]string{"format": "yaml"})
	if yaml.IsError || len(yaml.Content) != 1 {
		t.Fatalf("unexpected YAML response: %+v", yaml)
	}
	content, ok := yaml.Content[0].(*protocol.TextContent)
	if !ok || !strings.Contains(content.Text, "revision: 1") || !strings.Contains(content.Text, "metrics/retention.md") {
		t.Fatalf("unexpected YAML contents: %+v", yaml.Content)
	}
	graphResponse := reviewRequest(review, http.MethodGet, "/api/v1/index", reviewTestToken, "")
	requireStatus(t, graphResponse, http.StatusOK)
	if err := json.NewDecoder(graphResponse.Body).Decode(&graph); err != nil || graph.Revision != 1 {
		t.Fatalf("published graph = %+v (error %v)", graph, err)
	}
	export := reviewRequest(review, http.MethodGet, "/api/v1/index.yaml", reviewTestToken, "")
	requireStatus(t, export, http.StatusOK)
	if export.Header().Get("Content-Disposition") == "" || export.Body.String() != content.Text {
		t.Fatal("review download and MCP YAML differ")
	}

	queue := reviewRequest(review, http.MethodGet, "/api/v1/index/proposals?status=approved", reviewTestToken, "")
	requireStatus(t, queue, http.StatusOK)
	if !strings.Contains(queue.Body.String(), pending.ID) {
		t.Fatalf("approved item missing from queue: %s", queue.Body.String())
	}
}

func TestIndexCredentialAndDevelopmentReviewGates(t *testing.T) {
	store := indexStore(t)
	if _, err := NewMCPHandler(store, readTestToken, readTestToken, ""); err == nil {
		t.Fatal("accepted identical read and submit credentials")
	}
	if handler, err := NewMCPHandler(store, "", "", ""); err != nil {
		t.Fatal(err)
	} else {
		requireStatus(t, reviewRequest(handler, http.MethodGet, "/mcp", "", ""), http.StatusServiceUnavailable)
	}
	handler, err := NewMCPHandler(store, readTestToken, "", "")
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, reviewRequest(handler, http.MethodGet, "/mcp", submitTestToken, ""), http.StatusUnauthorized)
	req := httptest.NewRequest(http.MethodGet, "http://workbench.test/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+readTestToken)
	req.Header.Set("Origin", "http://other.test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	requireStatus(t, rec, http.StatusForbidden)

	provider, err := auth.New(context.Background(), auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, allow := range []bool{false, true} {
		mux := http.NewServeMux()
		RegisterReview(mux, store, allow)
		queue := "/api/v1/index/proposals"
		requireStatus(t, reviewRequest(mux, http.MethodGet, queue, "", ""), http.StatusUnauthorized)
		secured := provider.Middleware(mux)
		want := http.StatusForbidden
		if allow {
			want = http.StatusOK
		}
		requireStatus(t, reviewRequest(secured, http.MethodGet, queue, "", ""), want)
		session := reviewRequest(secured, http.MethodGet, "/api/v1/index/session", "", "")
		requireStatus(t, session, http.StatusOK)
		if !strings.Contains(session.Body.String(), `"user_id":"dev_user"`) {
			t.Fatalf("missing development identity: %s", session.Body.String())
		}
	}
}

func authenticatedReview(t *testing.T, store *index.Store, register ...func(*http.ServeMux)) http.Handler {
	t.Helper()
	verifier := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+reviewTestToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]string
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body["app_id"] != "index-review-fixture" {
			t.Error("incorrect identity verifier request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active": true, "user_id": "reviewer-fixture", "username": "审核员", "dept": "BI",
		})
	}))
	t.Cleanup(verifier.Close)
	caPath := filepath.Join(t.TempDir(), "review-verifier.pem")
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: verifier.Certificate().Raw})
	if err := os.WriteFile(caPath, cert, 0600); err != nil {
		t.Fatal(err)
	}
	provider, err := auth.New(context.Background(), auth.Config{
		Enabled: true, AppID: "index-review-fixture", AuthURL: "https://login.example.test",
		VerifyTokenURL: verifier.URL, TLSCAFile: caPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterReview(mux, store, false)
	for _, attach := range register {
		attach(mux)
	}
	return provider.Middleware(mux)
}

func reviewRequest(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	return result
}

func requireStatus(t *testing.T, result *httptest.ResponseRecorder, status int) {
	t.Helper()
	if result.Code != status {
		t.Fatalf("status = %d, want %d, body = %s", result.Code, status, result.Body.String())
	}
}
