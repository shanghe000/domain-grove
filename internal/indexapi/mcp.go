package indexapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"

	"ontology-engine/internal/index"
)

const defaultSubmitterID = "bi-agent"

type graphArgs struct {
	Format string `json:"format,omitempty" jsonschema:"json (default) or yaml"`
}

type nodeArgs struct {
	NodeID string `json:"node_id" jsonschema:"Stable ID of a published index node"`
}

type nodeResult struct {
	Node     index.Node   `json:"node"`
	Parent   *index.Node  `json:"parent,omitempty"`
	Children []index.Node `json:"children"`
	Outgoing []index.Edge `json:"outgoing"`
	Incoming []index.Edge `json:"incoming"`
}

type schemaResult struct {
	SchemaVersion int             `json:"schema_version"`
	NodeKinds     []string        `json:"node_kinds"`
	Relation      string          `json:"relation_pattern"`
	Graph         json.RawMessage `json:"graph_schema"`
	Submission    json.RawMessage `json:"submission_schema"`
	Rules         []string        `json:"rules"`
	Publication   string          `json:"publication"`
}

type protocolHandlers struct {
	streamable http.Handler
	sse        http.Handler
}

func NewMCPHandler(store *index.Store, readToken, submitToken, submitterID string) (http.Handler, error) {
	if readToken != "" && readToken == submitToken {
		return nil, errors.New("index read and submission tokens must differ")
	}
	for _, token := range []string{readToken, submitToken} {
		if token != strings.TrimSpace(token) || strings.IndexFunc(token, func(r rune) bool {
			return r < 33 || r > 126
		}) >= 0 {
			return nil, errors.New("index MCP tokens must be non-whitespace printable ASCII")
		}
	}
	schema, err := publishedSchema()
	if err != nil {
		return nil, err
	}

	readHash := sha256.Sum256([]byte(readToken))
	submitHash := sha256.Sum256([]byte(submitToken))
	readConfigured, submitConfigured := readToken != "", submitToken != ""
	submitterID = strings.TrimSpace(submitterID)
	if submitterID == "" {
		submitterID = defaultSubmitterID
	}
	reader := newProtocolHandlers(store, schema, "")
	submitter := newProtocolHandlers(store, schema, submitterID)
	return secureMCP(readConfigured || submitConfigured, func(_ *http.Request, bearer string) (protocolHandlers, error) {
		hash := sha256.Sum256([]byte(bearer))
		switch {
		case submitConfigured && subtle.ConstantTimeCompare(hash[:], submitHash[:]) == 1:
			return submitter, nil
		case readConfigured && subtle.ConstantTimeCompare(hash[:], readHash[:]) == 1:
			return reader, nil
		default:
			return protocolHandlers{}, nil
		}
	}), nil
}

// MCPResolver authenticates a credential before returning a project store.
// A missing store denies access; an empty submitter ID grants read-only access.
type MCPResolver func(r *http.Request, bearer string) (store *index.Store, submitterID string, err error)

func NewResolvedMCPHandler(resolve MCPResolver) (http.Handler, error) {
	schema, err := publishedSchema()
	if err != nil {
		return nil, err
	}
	type sessionKey struct {
		store     *index.Store
		canSubmit bool
	}
	type peer struct {
		credential  [sha256.Size]byte
		submitterID string
		handlers    protocolHandlers
	}
	var mu sync.Mutex
	peers := make(map[sessionKey]peer)
	return secureMCP(true, func(r *http.Request, bearer string) (protocolHandlers, error) {
		mu.Lock()
		defer mu.Unlock()
		// Authorize in the same order as cache updates. In-flight requests
		// authenticated before rotation cannot replace a newer peer afterward.
		store, submitterID, err := resolve(r, bearer)
		if err != nil || store == nil {
			return protocolHandlers{}, err
		}
		key := sessionKey{store, submitterID != ""}
		digest := sha256.Sum256([]byte(bearer))
		if current, ok := peers[key]; ok && current.credential == digest && current.submitterID == submitterID {
			return current.handlers, nil
		}
		selected := newProtocolHandlers(store, schema, submitterID)
		peers[key] = peer{digest, submitterID, selected}
		return selected, nil
	}), nil
}

func secureMCP(configured bool, selectPeer func(*http.Request, string) (protocolHandlers, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		if !configured {
			http.Error(w, "Index MCP credentials are not configured", http.StatusServiceUnavailable)
			return
		}
		if !validMCPOrigin(r) {
			http.Error(w, "Invalid Origin", http.StatusForbidden)
			return
		}
		bearer, found := bearerToken(r.Header.Values("Authorization"))
		var selected protocolHandlers
		if found {
			var err error
			selected, err = selectPeer(r, bearer)
			if err != nil {
				http.Error(w, "Index MCP is unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		if selected.streamable == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ontology-index"`)
			http.Error(w, "Index MCP Bearer token is required", http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/sse") {
			selected.sse.ServeHTTP(w, r)
		} else {
			selected.streamable.ServeHTTP(w, r)
		}
	})
}

func publishedSchema() (schemaResult, error) {
	graph, err := jsonschema.For[index.Graph](nil)
	if err != nil {
		return schemaResult{}, err
	}
	submission, err := jsonschema.For[index.SubmitInput](nil)
	if err != nil {
		return schemaResult{}, err
	}
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		return schemaResult{}, err
	}
	submissionJSON, err := json.Marshal(submission)
	if err != nil {
		return schemaResult{}, err
	}
	return schemaResult{
		SchemaVersion: index.SemanticVersion,
		NodeKinds:     []string{index.KindDomain, index.KindConcept, index.KindMetric, index.KindArtifact},
		Relation:      "^(depends_on|impacted_by|defined_in|configured_by|foreign_key_to|collaborates_with)$",
		Graph:         graphJSON,
		Submission:    submissionJSON,
		Rules: []string{
			governanceGuide,
			consumptionGuide,
			"artifact requires properties.artifact_type = document, config_table, event_spec, skill or tool. properties.cognitive_layer: S0 artifact, S1 stable concept, S2 intervention, S3 metric. properties.hub_role: sink or source. properties.lifecycle: active or deprecated.",
			"An axiom has kind co_check/pitfall/cross_check/prune and text. Edges to the same leaf may use distinct anchor values; FK edges require source_field and target_field, with optional separator.",
			"Reference paths are canonical relative POSIX paths within a logical repository.",
			"Containment has no cycles. Every relationship endpoint must exist in the target graph.",
			"Submissions name the published base revision. Reuse request_id only to retry the same proposal.",
		},
		Publication: "Submitted proposals enter a pending queue. Only an authenticated reviewer can publish them. Graph tools return published data.",
	}, nil
}

func newProtocolHandlers(store *index.Store, schema schemaResult, submitterID string) protocolHandlers {
	server := protocol.NewServer(&protocol.Implementation{
		Name: "ontology-index", Title: "知域 · Domain Grove", Version: "2.0.0",
	}, nil)
	registerSemanticTools(server, store, submitterID != "")
	protocol.AddTool(server, readTool("get_index_schema", "Read the published index and proposal JSON contracts."),
		func(_ context.Context, _ *protocol.CallToolRequest, _ struct{}) (*protocol.CallToolResult, any, error) {
			return nil, schema, nil
		})
	protocol.AddTool(server, readTool("get_index_graph", "Read the current published index graph as JSON or YAML."),
		func(ctx context.Context, _ *protocol.CallToolRequest, args graphArgs) (*protocol.CallToolResult, any, error) {
			format := strings.ToLower(strings.TrimSpace(args.Format))
			if format != "" && format != "json" && format != "yaml" {
				return nil, nil, errors.New("format must be json or yaml")
			}
			graph, err := store.Graph(ctx)
			if err != nil {
				return nil, nil, toolError(err)
			}
			if format != "yaml" {
				return nil, graph, nil
			}
			yaml, err := index.MarshalYAML(graph)
			if err != nil {
				return nil, nil, toolError(err)
			}
			value := string(yaml)
			result := &protocol.CallToolResult{Content: []protocol.Content{&protocol.TextContent{Text: value}}}
			return result, map[string]string{"format": "yaml", "yaml": value}, nil
		})
	protocol.AddTool(server, readTool("get_index_node", "Read a published node with its parent, children and incident edges."),
		func(ctx context.Context, _ *protocol.CallToolRequest, args nodeArgs) (*protocol.CallToolResult, nodeResult, error) {
			graph, err := store.Graph(ctx)
			if err != nil {
				return nil, nodeResult{}, toolError(err)
			}
			result, err := lookupNode(graph, args.NodeID)
			return nil, result, toolError(err)
		})
	if submitterID != "" {
		nondestructive := false
		protocol.AddTool(server, &protocol.Tool{
			Name:        "submit_index_proposal",
			Description: "Submit a proposed patch against the current published revision. For new or changed business knowledge, source structure or axioms, use schema_version 2 and attach source locators. Changes stay pending for human review.",
			Annotations: &protocol.ToolAnnotations{DestructiveHint: &nondestructive, IdempotentHint: true, OpenWorldHint: &nondestructive},
		}, func(ctx context.Context, _ *protocol.CallToolRequest, args index.SubmitInput) (*protocol.CallToolResult, any, error) {
			proposal, err := store.Submit(ctx, submitterID, args)
			if err != nil {
				return nil, nil, toolError(err)
			}
			return nil, proposal, nil
		})
	}

	getServer := func(*http.Request) *protocol.Server { return server }
	return protocolHandlers{
		streamable: protocol.NewStreamableHTTPHandler(getServer, &protocol.StreamableHTTPOptions{
			JSONResponse: true, SessionTimeout: 10 * time.Minute, MaxRequestBodyBytes: 1 << 20,
		}),
		sse: protocol.NewSSEHandler(getServer, &protocol.SSEOptions{MaxRequestBodyBytes: 1 << 20}),
	}
}

func readTool(name, description string) *protocol.Tool {
	local := false
	return &protocol.Tool{
		Name: name, Description: description,
		Annotations: &protocol.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &local},
	}
}

func lookupNode(graph index.Graph, id string) (nodeResult, error) {
	result := nodeResult{Children: []index.Node{}, Outgoing: []index.Edge{}, Incoming: []index.Edge{}}
	found := false
	for _, node := range graph.Nodes {
		if node.ID == id {
			result.Node = node
			found = true
			break
		}
	}
	if !found {
		return nodeResult{}, index.ErrNotFound
	}
	for _, node := range graph.Nodes {
		if node.ID == result.Node.ParentID {
			result.Parent = &node
		}
		if node.ParentID == id {
			result.Children = append(result.Children, node)
		}
	}
	for _, edge := range graph.Edges {
		if edge.From == id {
			result.Outgoing = append(result.Outgoing, edge)
		}
		if edge.To == id {
			result.Incoming = append(result.Incoming, edge)
		}
	}
	return result, nil
}

func bearerToken(headers []string) (string, bool) {
	if len(headers) != 1 {
		return "", false
	}
	fields := strings.Fields(headers[0])
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return "", false
	}
	return fields[1], true
}

func validMCPOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin, err := url.Parse(origins[0])
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return err == nil && origin.Scheme == scheme && strings.EqualFold(origin.Host, r.Host) &&
		origin.User == nil && origin.Path == "" && !origin.ForceQuery && origin.RawQuery == "" && origin.Fragment == ""
}
