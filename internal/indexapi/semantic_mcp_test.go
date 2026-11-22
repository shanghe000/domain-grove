package indexapi

import (
	"encoding/json"
	"net/http/httptest"
	"ontology-engine/internal/index"
	"reflect"
	"testing"
)

func TestPreviewIsPrivateAndPublishedTraversalMatchesMCP(t *testing.T) {
	store := indexStore(t)
	ctx := t.Context()
	handler, err := NewMCPHandler(store, readTestToken, submitTestToken, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	reader := clientSession(t, ctx, httpServer.URL+"/mcp", readTestToken, false)
	web := authenticatedReview(t, store)
	input := index.SubmitInput{RequestID: "v2-example", Title: "DAU 口径", Patch: index.Patch{SchemaVersion: 2, UpsertNodes: []index.Node{
		{ID: "d", Kind: "domain", Label: "分析域"},
		{ID: "dau", Kind: "metric", ParentID: "d", Label: "登录 DAU", Axioms: []index.Axiom{{Kind: "pitfall", Text: "核对日界线。"}}},
		{ID: "events", Kind: "artifact", ParentID: "d", Label: "事件", Properties: map[string]any{"artifact_type": "event_spec"}, Ref: &index.Reference{Repository: "bi", Path: "events.csv"}},
	}, UpsertEdges: []index.Edge{{ID: "uses", From: "dau", To: "events", Relation: "depends_on", Anchor: "user_auth"}}}}
	p, err := store.Submit(ctx, "agent", input)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) index.Subgraph {
		t.Helper()
		r := reviewRequest(web, "GET", path, reviewTestToken, "")
		if r.Code != 200 {
			t.Fatal(path, r.Code, r.Body.String())
		}
		var v index.Subgraph
		if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	draft := get("/api/v1/index/proposals/" + p.ID + "/traverse?node_id=dau")
	args := index.TraverseArgs{NodeIDs: []string{"dau"}}
	if len(draft.Nodes) != 2 || draft.Edges[0].Anchor != "user_auth" {
		t.Fatalf("preview %+v", draft)
	}
	if result := callIndex(t, ctx, reader, "traverse_index", args); !result.IsError {
		t.Fatal("MCP read unapproved node")
	}
	if _, err := store.Review(ctx, p.ID, "reviewer", "approve", "验收"); err != nil {
		t.Fatal(err)
	}
	published := get("/api/v1/index/traverse?node_id=dau")
	var viaMCP index.Subgraph
	decodeResult(t, callIndex(t, ctx, reader, "traverse_index", args), &viaMCP)
	if !reflect.DeepEqual(published, viaMCP) || published.Revision != 1 {
		t.Fatalf("mismatched contexts %+v %+v", published, viaMCP)
	}
}
