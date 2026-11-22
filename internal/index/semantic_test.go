package index

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func fixtureV2() Graph {
	art := func(id, kind string) Node {
		return Node{ID: id, Kind: KindArtifact, ParentID: "d", Label: id, Ref: &Reference{Repository: "bi", Path: id + ".csv", Anchor: "ID"}, Properties: map[string]any{"artifact_type": kind}}
	}
	sink := art("items", "config_table")
	sink.Properties["hub_role"] = "sink"
	return Graph{SchemaVersion: 2, Revision: 7, Nodes: []Node{
		{ID: "d", Kind: KindDomain, Label: "玩法"},
		{ID: "metric", Kind: KindMetric, ParentID: "d", Label: "DAU", Axioms: []Axiom{{"pitfall", "用事件发生日期去重。"}}},
		{ID: "event", Kind: KindConcept, ParentID: "d", Label: "运营活动"},
		{ID: "other", Kind: KindConcept, ParentID: "d", Label: "其他"},
		art("events", "event_spec"), art("schedule", "config_table"), art("rewards", "config_table"), sink,
	}, Edges: []Edge{
		{ID: "measure", From: "metric", To: "events", Relation: "depends_on", Anchor: "user_auth"},
		{ID: "cause", From: "metric", To: "event", Relation: "impacted_by"},
		{ID: "config", From: "event", To: "schedule", Relation: "configured_by"},
		{ID: "reward", From: "schedule", To: "rewards", Relation: "foreign_key_to", SourceField: "reward_ids", TargetField: "id", Separator: ";"},
		{ID: "item", From: "rewards", To: "items", Relation: "foreign_key_to", SourceField: "item_id", TargetField: "id"},
		{ID: "unrelated", From: "other", To: "items", Relation: "configured_by"},
	}}
}
func TestV2TraversalBudgetAndTerminalProjection(t *testing.T) {
	g := fixtureV2()
	if err := Validate(g); err != nil {
		t.Fatal(err)
	}
	ctx, err := Traverse(g, TraverseArgs{NodeIDs: []string{"metric"}})
	if err != nil || len(ctx.Nodes) != 6 || len(ctx.Edges) != 5 || len(ctx.Boundary) != 0 {
		t.Fatalf("full: %+v %v", ctx, err)
	}
	k := 1.5
	ctx, err = Traverse(g, TraverseArgs{NodeIDs: []string{"metric"}, Budget: &k})
	if err != nil || len(ctx.Boundary) != 1 || ctx.Boundary[0] != (Boundary{"rewards", "reward", 7}) {
		t.Fatalf("boundary %+v %v", ctx.Boundary, err)
	}
	stale := int64(6)
	if _, err := Traverse(g, TraverseArgs{NodeIDs: []string{"rewards"}, Revision: &stale}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale boundary: %v", err)
	}
	ctx, err = Traverse(g, TraverseArgs{NodeIDs: []string{"items"}})
	if err != nil || len(ctx.Nodes) != 1 || len(ctx.Edges) != 0 {
		t.Fatalf("terminal source escaped %+v", ctx)
	}
	ctx, _ = Traverse(g, TraverseArgs{NodeIDs: []string{"event"}})
	if !slices.ContainsFunc(ctx.Nodes, func(n Node) bool { return n.ID == "metric" }) || slices.ContainsFunc(ctx.Nodes, func(n Node) bool { return n.ID == "events" }) {
		t.Fatal("incoming source expanded")
	}
}
func TestV2ValidationAndTemporalOverlap(t *testing.T) {
	g := fixtureV2()
	c := Change{ID: "winter", Kind: "activity", Label: "计划区间", StartsAt: "2026-01-01T00:00:00+08:00", EndsAt: "2026-01-08T00:00:00+08:00", Scope: "test", NodeIDs: []string{"event"}}
	candidate, err := Apply(g, Patch{UpsertChanges: []Change{c}})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		from, to, scope string
		count           int
	}{
		{"2026-01-07T15:59:59Z", "2026-01-09T00:00:00Z", "test", 1},
		{"2026-01-07T16:00:00Z", "2026-01-09T00:00:00Z", "test", 0},
		{"2026-01-01T00:00:00Z", "2026-01-03T00:00:00Z", "prod", 0},
	} {
		out, err := ChangesInWindow(candidate, ChangeQuery{From: v.from, To: v.to, Scope: v.scope})
		if err != nil || len(out) != v.count {
			t.Fatal(v, out, err)
		}
	}
	check := func(p Patch, why string) {
		t.Helper()
		_, err := Apply(g, p)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), why) {
			t.Fatalf("want %s, got %v", why, err)
		}
	}
	check(Patch{SchemaVersion: 1, RemoveEdgeIDs: []string{"measure"}}, "downgrade")
	dup := g.Nodes[4]
	dup.ID = "duplicate"
	check(Patch{UpsertNodes: []Node{dup}}, "same source")
	check(Patch{UpsertEdges: []Edge{{ID: "bad", From: "items", To: "event", Relation: "defined_in"}}}, "terminal")
	check(Patch{UpsertEdges: []Edge{{ID: "a", From: "event", To: "other", Relation: "collaborates_with"}, {ID: "b", From: "other", To: "event", Relation: "collaborates_with"}}}, "cycle")
	check(Patch{RemoveNodeIDs: []string{"schedule"}}, "missing")
	c.NodeIDs = []string{"missing"}
	check(Patch{UpsertChanges: []Change{c}}, "missing node")
}
