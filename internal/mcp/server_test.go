package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"ontology-engine/internal/ontology"

	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

const testToken = "test-service-token"

func fakeHandler(token string) http.Handler {
	domain := ontology.Node{ID: "Domain_BI", NodeType: ontology.Domain, Label: "经营分析", Status: ontology.Active,
		Properties: map[string]any{}, Axioms: []ontology.Axiom{}}
	metric := ontology.Node{ID: "Metric_Retention", DomainID: domain.ID, NodeType: ontology.Metric,
		Label: "留存率", Status: ontology.Active, Properties: map[string]any{"formula": "D1 / D0"},
		Axioms: []ontology.Axiom{{Type: "Mandatory_Check", Rule: "核对版本"}}}
	active := ontology.Node{ID: "Artifact_CurrentTracking", DomainID: domain.ID, NodeType: ontology.ArtifactNode,
		Label: "本次埋点", Status: ontology.Active, Properties: map[string]any{}, Axioms: []ontology.Axiom{}}
	deprecated := ontology.Node{ID: "Artifact_OldTracking", DomainID: domain.ID, NodeType: ontology.ArtifactNode,
		Label: "历史埋点", Status: ontology.Deprecated, Properties: map[string]any{}, Axioms: []ontology.Axiom{}}
	graph := ontology.NewGraph([]ontology.Node{domain, metric, active, deprecated}, nil, []ontology.Artifact{
		{NodeID: active.ID, ArtifactType: ontology.TrackingAPI, PhysicalRef: "v1.0.0"},
		{NodeID: deprecated.ID, ArtifactType: ontology.TrackingAPI, PhysicalRef: "v0.1.0"},
	})
	return NewHandler(token,
		func() *ontology.Graph { return graph },
		func(_ context.Context, id string) (string, error) {
			return "---\nnode_id: " + id + "\n---\n# 留存口径", nil
		},
		func(_ context.Context, sql string) (json.RawMessage, error) {
			if strings.HasPrefix(sql, "DROP") {
				return json.RawMessage(`{"error":"read-only","hint_tables":[]}`), nil
			}
			return json.RawMessage(`{"columns":["verified"],"rows":[[1]],"truncated":false}`), nil
		},
		func(_ context.Context, version string, names []string) (json.RawMessage, error) {
			return json.Marshal(map[string]any{"version": version, "event_names": names})
		},
	)
}

type withToken struct{}

func (withToken) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+testToken)
	return http.DefaultTransport.RoundTrip(request)
}

func TestMCPAuthenticationAndTransport(t *testing.T) {
	serv := httptest.NewServer(fakeHandler(testToken))
	defer serv.Close()
	for _, path := range []string{"/mcp", "/mcp/sse"} {
		res, err := http.Get(serv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous %s status = %d", path, res.StatusCode)
		}
	}

	request, _ := http.NewRequest(http.MethodGet, serv.URL+"/mcp/sse", nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Origin", "https://unrelated.example")
	res, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("untrusted origin status = %d", res.StatusCode)
	}

	rec := httptest.NewRecorder()
	fakeHandler("").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured token status = %d", rec.Code)
	}

	client := &http.Client{Transport: withToken{}}
	transports := map[string]protocol.Transport{
		"streamable HTTP": &protocol.StreamableClientTransport{Endpoint: serv.URL + "/mcp", HTTPClient: client, MaxRetries: -1},
		"legacy SSE":      &protocol.SSEClientTransport{Endpoint: serv.URL + "/mcp/sse", HTTPClient: client},
	}
	for name, transport := range transports {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			conn, err := protocol.NewClient(&protocol.Implementation{Name: "acceptance", Version: "1"}, nil).Connect(ctx, transport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			list, err := conn.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			var toolNames []string
			for _, tool := range list.Tools {
				toolNames = append(toolNames, tool.Name)
				if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
					t.Fatalf("tool %s must be read-only", tool.Name)
				}
			}
			want := []string{"get_node_context", "get_ontology_skeleton", "query_config_sql", "query_tracking_events", "read_artifact_doc"}
			if !reflect.DeepEqual(toolNames, want) {
				t.Fatalf("tool list = %v", toolNames)
			}

			for _, tc := range []struct {
				name, contains string
				args           map[string]any
				isError        bool
			}{
				{name: "get_ontology_skeleton", contains: "Domain_BI", args: map[string]any{}},
				{name: "get_node_context", contains: "Mandatory_Check", args: map[string]any{"node_id": "Metric_Retention"}},
				{name: "read_artifact_doc", contains: "node_id: Artifact_RetentionSpec", args: map[string]any{"node_id": "Artifact_RetentionSpec"}},
				{name: "query_config_sql", contains: "verified", args: map[string]any{"sql": "SELECT 1 AS verified"}},
				{name: "query_config_sql", contains: "hint_tables", args: map[string]any{"sql": "DROP TABLE cfg_reward"}, isError: true},
				{name: "query_tracking_events", contains: "reading_session_start", args: map[string]any{"version": "v1.0.0", "event_names": []string{"reading_session_start"}}},
				{name: "query_tracking_events", contains: "null", args: map[string]any{"version": "staged_version", "event_names": []string{}}, isError: true},
				{name: "query_tracking_events", contains: "null", args: map[string]any{"version": "v0.1.0", "event_names": []string{}}, isError: true},
			} {
				result, err := conn.CallTool(ctx, &protocol.CallToolParams{Name: tc.name, Arguments: tc.args})
				if err != nil {
					t.Fatalf("%s: %v", tc.name, err)
				}
				content, err := json.Marshal(result.StructuredContent)
				if err != nil || result.IsError != tc.isError || !strings.Contains(string(content), tc.contains) {
					t.Fatalf("%s: unexpected result %s, tool error = %v, encoding error = %v", tc.name, content, result.IsError, err)
				}
			}
		})
	}
}
