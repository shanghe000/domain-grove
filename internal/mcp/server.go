package mcp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ontology-engine/internal/ontology"

	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

type skeletonArgs struct {
	DomainID string `json:"domain_id,omitempty" jsonschema:"Optional ID of the domain to browse."`
}
type contextArgs struct {
	NodeID            string   `json:"node_id" jsonschema:"Node ID from get_ontology_skeleton or a previous frontier."`
	Budget            *float64 `json:"budget,omitempty" jsonschema:"Non-negative cumulative traversal budget. Defaults to 2.5."`
	IncludeDeprecated bool     `json:"include_deprecated,omitempty" jsonschema:"Include deprecated ontology nodes. Defaults to false."`
}
type nodeArgs struct {
	NodeID string `json:"node_id" jsonschema:"Published feishu_md artifact node ID."`
}
type docResult struct {
	NodeID   string `json:"node_id"`
	Markdown string `json:"markdown"`
}
type sqlArgs struct {
	SQL string `json:"sql" jsonschema:"Single SELECT or WITH SELECT on published logical configuration table names."`
}
type trackingArgs struct {
	Version    string   `json:"version" jsonschema:"Published tracking definition version."`
	EventNames []string `json:"event_names" jsonschema:"Filter to these names. Use an empty array for the full version."`
}

func NewHandler(
	token string,
	graph func() *ontology.Graph,
	readDoc func(context.Context, string) (string, error),
	querySQL func(context.Context, string) (json.RawMessage, error),
	tracking func(context.Context, string, []string) (json.RawMessage, error),
) http.Handler {
	server := protocol.NewServer(&protocol.Implementation{
		Name: "ontology-engine", Title: "知域 · Domain Grove", Version: "1.0.0",
	}, nil)
	protocol.AddTool(server, tool("get_ontology_skeleton", "List node IDs, aliases and relationship directions without loading business axioms.", false),
		func(_ context.Context, _ *protocol.CallToolRequest, args skeletonArgs) (*protocol.CallToolResult, ontology.Skeleton, error) {
			return nil, graph().Skeleton(args.DomainID), nil
		})
	protocol.AddTool(server, tool("get_node_context", "Get the bounded subgraph, axioms, artifact bindings and continuation frontier for a node.", false),
		func(_ context.Context, _ *protocol.CallToolRequest, args contextArgs) (*protocol.CallToolResult, ontology.NodeContext, error) {
			budget := ontology.DefaultBudget
			if args.Budget != nil {
				budget = *args.Budget
			}
			value, err := graph().GetNodeContext(args.NodeID, budget, args.IncludeDeprecated)
			return nil, value, err
		})
	protocol.AddTool(server, tool("read_artifact_doc", "Read the complete published Markdown document and its YAML frontmatter.", false),
		func(ctx context.Context, _ *protocol.CallToolRequest, args nodeArgs) (*protocol.CallToolResult, docResult, error) {
			markdown, err := readDoc(ctx, args.NodeID)
			return &protocol.CallToolResult{Content: []protocol.Content{&protocol.TextContent{Text: markdown}}},
				docResult{NodeID: args.NodeID, Markdown: markdown}, err
		})
	protocol.AddTool(server, tool("query_config_sql", "Run read-only SQL against published configuration tables. SQL errors include table metadata to guide corrections.", false),
		func(ctx context.Context, _ *protocol.CallToolRequest, args sqlArgs) (*protocol.CallToolResult, any, error) {
			result, err := querySQL(ctx, args.SQL)
			return workerResult(result, err)
		})
	protocol.AddTool(server, tool("query_tracking_events", "Read event definitions for a published tracking version from the warehouse API.", true),
		func(ctx context.Context, _ *protocol.CallToolRequest, args trackingArgs) (*protocol.CallToolResult, any, error) {
			if !publishedTrackingVersion(graph(), args.Version) {
				return nil, nil, errors.New("找不到该已发布埋点版本。")
			}
			result, err := tracking(ctx, args.Version, args.EventNames)
			return workerResult(result, err)
		})

	getServer := func(*http.Request) *protocol.Server { return server }
	streamable := protocol.NewStreamableHTTPHandler(getServer, &protocol.StreamableHTTPOptions{
		JSONResponse: true, SessionTimeout: 10 * time.Minute, MaxRequestBodyBytes: 1 << 20,
	})
	legacy := protocol.NewSSEHandler(getServer, &protocol.SSEOptions{MaxRequestBodyBytes: 1 << 20})
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.Error(w, "MCP_API_TOKEN is not configured", http.StatusServiceUnavailable)
			return
		}
		if !validOrigin(r) {
			http.Error(w, "Invalid Origin", http.StatusForbidden)
			return
		}
		if !authorized(r.Header.Values("Authorization"), expected) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ontology-engine"`)
			http.Error(w, "Bearer token is required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		switch r.URL.Path {
		case "/mcp":
			streamable.ServeHTTP(w, r)
		case "/mcp/sse", "/mcp/sse/":
			legacy.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func publishedTrackingVersion(graph *ontology.Graph, version string) bool {
	for _, artifact := range graph.Data().Artifacts {
		if artifact.ArtifactType == ontology.TrackingAPI && artifact.PhysicalRef == version {
			if node, found := graph.Node(artifact.NodeID); found && node.Status != ontology.Deprecated {
				return true
			}
		}
	}
	return false
}

func tool(name, description string, openWorld bool) *protocol.Tool {
	return &protocol.Tool{
		Name: name, Description: description,
		Annotations: &protocol.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &openWorld},
	}
}

func workerResult(value json.RawMessage, err error) (*protocol.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	var envelope struct {
		Error string `json:"error"`
	}
	if !json.Valid(value) {
		return nil, nil, errors.New("invalid extraction worker response")
	}
	isError := json.Unmarshal(value, &envelope) == nil && envelope.Error != ""
	return &protocol.CallToolResult{IsError: isError}, value, nil
}

func authorized(headers []string, expected [sha256.Size]byte) bool {
	if len(headers) != 1 {
		return false
	}
	fields := strings.Fields(headers[0])
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return false
	}
	actual := sha256.Sum256([]byte(fields[1]))
	return subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
}

func validOrigin(r *http.Request) bool {
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
	return err == nil && origin.Scheme == scheme &&
		origin.User == nil && origin.Path == "" && !origin.ForceQuery && origin.RawQuery == "" && origin.Fragment == "" &&
		strings.EqualFold(origin.Host, r.Host)
}
