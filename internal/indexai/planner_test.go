package indexai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ontology-engine/internal/index"
)

func fixtureGraph() index.Graph {
	return index.Graph{
		SchemaVersion: index.SchemaVersion, Revision: 7,
		Nodes: []index.Node{
			{ID: "analytics", Kind: index.KindDomain, Label: "经营分析", Description: strings.Repeat("摘要", 280),
				Properties: map[string]any{"local": "private-undispatched-metadata"}},
			{ID: "existing_doc", Kind: index.KindDocument, ParentID: "analytics", Label: "历史口径",
				Ref: &index.Reference{Repository: "biz-docs", Path: "metrics/historical.md"}},
		},
		Edges: []index.Edge{},
	}
}

func TestPlanOpenAIProtocolAndMetadataOnly(t *testing.T) {
	var calls atomic.Int32
	selected := index.Reference{Repository: "bi-spec", Path: "metrics/retention.md",
		LineStart: 13, LineEnd: 22, Anchor: "留存口径"}
	graph := fixtureGraph()
	proposal := Draft{
		Title: "建立留存索引", Rationale: "用已有口径引用定位留存定义。",
		Patch: index.Patch{UpsertNodes: []index.Node{
			{ID: "analytics", Kind: index.KindDomain, Label: "BI 经营分析",
				Description: shortDescription(graph.Nodes[0].Description)},
			{ID: "retention_doc", Kind: index.KindDocument, Label: "留存口径", ParentID: "analytics", Ref: &selected},
		}},
	}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/fixture/v1/chat/completions" ||
			r.Header.Get("Authorization") != "Bearer fixture-credential" {
			t.Error("unexpected provider route or authorization")
		}
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error("malformed completion request")
		}
		if request.Model != "deepseek-flash" || request.MaxTokens != 3600 ||
			request.Thinking["type"] != "disabled" || request.ResponseFormat["type"] != "json_object" {
			t.Error("incorrect OpenAI-compatible request")
		}
		if len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" {
			t.Error("unexpected request roles")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var input modelingInput
		raw := request.Messages[1].Content
		if strings.Contains(raw, "private-undispatched-metadata") || json.Unmarshal([]byte(raw), &input) != nil {
			t.Error("request included private properties or malformed metadata")
		}
		if input.Instruction != "补充留存版本引用" || len(input.References) != 1 || input.References[0] != selected ||
			input.Graph.Revision != 7 || len(input.Graph.Nodes) != 2 || input.Graph.Nodes[0].Properties != nil {
			t.Error("incomplete modeling metadata")
		}
		if count == 1 {
			writeCompletion(w, "   ", "stop")
			return
		}
		writeCompletion(w, string(encoded), "stop")
	}))
	defer provider.Close()

	planner, err := New(Config{BaseURL: provider.URL + "/fixture/v1/", APIKey: "fixture-credential"})
	if err != nil || !planner.Configured() {
		t.Fatalf("configured planner: %v", err)
	}
	draft, err := planner.Plan(context.Background(), "补充留存版本引用", graph, []index.Reference{selected})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || draft.Title != proposal.Title || draft.Patch.UpsertNodes[1].Ref == nil ||
		*draft.Patch.UpsertNodes[1].Ref != selected {
		t.Fatalf("unexpected draft or retry count: %+v (%d calls)", draft, calls.Load())
	}
	edited := draft.Patch.UpsertNodes[0]
	if edited.Properties["local"] != graph.Nodes[0].Properties["local"] || edited.Description != graph.Nodes[0].Description ||
		graph.Nodes[0].Label != "经营分析" || len(graph.Nodes) != 2 {
		t.Fatal("draft changed published state or dropped existing metadata")
	}
}

func TestDraftProvenanceAndStructure(t *testing.T) {
	graph := fixtureGraph()
	selected := index.Reference{Repository: "bi-spec", Path: "metrics/retention.md", LineStart: 13, LineEnd: 22, Anchor: "留存口径"}
	for _, tc := range []struct {
		name    string
		path    string
		lineEnd int
		anchor  string
	}{
		{"invented path", "other.md", 22, "留存口径"},
		{"changed line range", selected.Path, 23, "留存口径"},
		{"changed anchor", selected.Path, 22, "留存定义"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invented := selected
			invented.Path, invented.LineEnd, invented.Anchor = tc.path, tc.lineEnd, tc.anchor
			_, err := validateDraft(draftWithNode(t, index.Node{
				ID: "candidate", Kind: index.KindDocument, ParentID: "analytics", Label: "留存", Ref: &invented,
			}), graph, []index.Reference{selected})
			if !errors.Is(err, ErrInvalidDraft) {
				t.Fatalf("invented reference classified as %v", err)
			}
		})
	}
	reused, err := validateDraft(draftWithNode(t, index.Node{
		ID: "related", Kind: index.KindDocument, ParentID: "analytics", Label: "关联口径", Ref: graph.Nodes[1].Ref,
	}), graph, nil)
	if err != nil || *reused.Patch.UpsertNodes[0].Ref != *graph.Nodes[1].Ref {
		t.Fatalf("published ref could not be reused: %v", err)
	}
	for name, raw := range map[string]string{
		"body outside patch": `{"title":"调整","rationale":"示例","patch":{"remove_node_ids":["existing_doc"]},"body":"unsupported"}`,
		"multiple objects":   `{"title":"调整","rationale":"示例","patch":{"remove_node_ids":["existing_doc"]}} {}`,
		"empty patch":        `{"title":"调整","rationale":"示例","patch":{}}`,
		"invalid parent": draftWithNode(t, index.Node{
			ID: "bad_parent", Kind: index.KindConcept, ParentID: "not_found", Label: "无效父级",
		}),
		"arbitrary properties": draftWithNode(t, index.Node{
			ID: "unsafe_properties", Kind: index.KindConcept, ParentID: "analytics", Label: "无效属性",
			Properties: map[string]any{"body": "unsupported"},
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateDraft(raw, graph, nil); !errors.Is(err, ErrInvalidDraft) {
				t.Fatalf("classified as %v", err)
			}
		})
	}
}

func TestPlannerConfigurationAndFailureBoundaries(t *testing.T) {
	unconfigured, err := New(Config{})
	if err != nil || unconfigured.Configured() {
		t.Fatalf("unconfigured result: %v", err)
	}
	if _, err := unconfigured.Plan(context.Background(), "建模", fixtureGraph(), nil); !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("missing model secret classified as %v", err)
	}
	for _, invalid := range []string{
		"http://example.invalid/v1", "https://u:p@example.invalid/v1", "https://example.invalid/v1?token=hidden",
	} {
		if _, err := New(Config{APIKey: "fixture", BaseURL: invalid}); err == nil {
			t.Fatal("accepted unsafe provider address")
		}
	}

	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer provider.Close()
	planner, err := New(Config{APIKey: "fixture", BaseURL: provider.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(context.Background(), "建模", fixtureGraph(), []index.Reference{{Path: "/local"}}); !errors.Is(err, ErrInvalid) || calls.Load() != 0 {
		t.Fatal("invalid input reached provider")
	}
	if _, err := planner.Plan(context.Background(), "建模", fixtureGraph(), nil); !errors.Is(err, ErrUnavailable) || redirected.Load() {
		t.Fatal("credential was forwarded or redirect was treated as a completion")
	}

	providerFailures := []struct {
		name string
		make func(http.ResponseWriter, *http.Request)
		want error
	}{
		{"unavailable with private error body", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "private-provider-error")
		}, ErrUnavailable},
		{"oversized response", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
		}, ErrUnavailable},
		{"provider deadline", func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		}, ErrTimeout},
	}
	for _, tc := range providerFailures {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tc.make))
			defer server.Close()
			planner, err := New(Config{APIKey: "fixture", BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if tc.want == ErrTimeout {
				deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
				ctx = deadline
			}
			_, err = planner.Plan(ctx, "建模", fixtureGraph(), nil)
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "private-provider-error") {
				t.Fatalf("failure classified as %v", err)
			}
		})
	}
}

func draftWithNode(t *testing.T, node index.Node) string {
	t.Helper()
	raw, err := json.Marshal(Draft{Title: "测试", Rationale: "核对结构", Patch: index.Patch{UpsertNodes: []index.Node{node}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func writeCompletion(w http.ResponseWriter, content, reason string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{
		{"finish_reason": reason, "message": map[string]string{"role": "assistant", "content": content}},
	}})
}
