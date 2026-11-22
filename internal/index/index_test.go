package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func testStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func scenario(requestID, nodeID string) SubmitInput {
	return SubmitInput{RequestID: requestID, Title: "业务索引", Rationale: "来源已梳理",
		Patch: Patch{UpsertNodes: []Node{{ID: nodeID, Kind: KindConcept, Label: nodeID}}}}
}

func TestReviewPublishesOnlyApprovedProposalAndRetainsHistory(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "new index.db")
	store := testStore(t, path)
	t.Cleanup(func() { _ = store.Close() })
	empty, err := store.Graph(ctx)
	if err != nil || empty.Revision != 0 || len(empty.Nodes)+len(empty.Edges) != 0 {
		t.Fatalf("initial index = %+v, %v", empty, err)
	}
	emptyYAML, err := MarshalYAML(empty)
	if err != nil || !strings.Contains(string(emptyYAML), "nodes: []\nedges: []") {
		t.Fatalf("initial export = %q, %v", emptyYAML, err)
	}

	first := scenario("client:1", "retention")
	first.Patch.UpsertNodes = append(first.Patch.UpsertNodes,
		Node{ID: "design", Kind: KindDocument, Label: "次留策划案", ParentID: "retention",
			Ref:        &Reference{Repository: "games/analytics", Path: "资料/次留.md", Anchor: "计算口径", LineStart: 4, LineEnd: 8},
			Properties: map[string]any{"precision": json.Number("9007199254740993")}},
	)
	proposal, err := store.Submit(ctx, "BI Agent", first)
	if err != nil || proposal.Status != StatusPending {
		t.Fatalf("submission = %+v, %v", proposal, err)
	}
	other, err := store.Submit(ctx, "BI Agent", scenario("client:2", "not_approved"))
	if err != nil {
		t.Fatal(err)
	}
	first.Patch.UpsertNodes[0], first.Patch.UpsertNodes[1] = first.Patch.UpsertNodes[1], first.Patch.UpsertNodes[0]
	retry, err := store.Submit(ctx, "BI Agent", first)
	if err != nil || retry.ID != proposal.ID {
		t.Fatalf("idempotent submission = %+v, %v", retry, err)
	}
	if _, err := store.Submit(ctx, "another client", first); !errors.Is(err, ErrConflict) {
		t.Fatalf("request ID reuse across submitters: %v", err)
	}
	before, _ := store.Graph(ctx)
	if len(before.Nodes) != 0 {
		t.Fatal("pending data entered the published graph")
	}
	preview, err := store.Preview(ctx, proposal.ID)
	if err != nil || preview.Revision != 0 || len(preview.Nodes) != 2 {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	approved, err := store.Review(ctx, proposal.ID, "subject_bi_admin", "approve", "核对通过")
	if err != nil || approved.Status != StatusApproved || approved.ReviewedBy != "subject_bi_admin" ||
		approved.ReviewedAt == "" || approved.ReviewNote != "核对通过" {
		t.Fatalf("approval = %+v, %v", approved, err)
	}
	approvedAgain, err := store.Review(ctx, proposal.ID, "subject_bi_admin", "approve", "核对通过")
	if err != nil || approvedAgain.ReviewedAt != approved.ReviewedAt {
		t.Fatalf("review retry = %+v, %v", approvedAgain, err)
	}
	if _, err := store.Review(ctx, proposal.ID, "subject_bi_admin", "reject", "改主意"); !errors.Is(err, ErrConflict) {
		t.Fatalf("approved status rewrote outcome: %v", err)
	}
	if _, err := store.Submit(ctx, "BI Agent", scenario("new:stale", "another")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale submission: %v", err)
	}
	if _, err := store.Review(ctx, other.ID, "subject_bi_admin", "approve", "旧版本"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale approval: %v", err)
	}
	rejected, err := store.Review(ctx, other.ID, "subject_bi_admin", "reject", "未采纳")
	if err != nil || rejected.Status != StatusRejected {
		t.Fatalf("stale proposal rejection = %+v, %v", rejected, err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = testStore(t, path)
	published, err := store.Graph(ctx)
	if err != nil || published.Revision != 1 || len(published.Nodes) != 2 || published.Nodes[0].ID != "design" {
		t.Fatalf("published snapshot = %+v, %v", published, err)
	}
	retained, err := store.Preview(ctx, other.ID)
	if err != nil || retained.Revision != 0 || len(retained.Nodes) != 1 || retained.Nodes[0].ID != "not_approved" {
		t.Fatalf("historical preview = %+v, %v", retained, err)
	}
	all, err := store.List(ctx, "")
	if err != nil || len(all) != 2 || all[0].Status != StatusRejected || all[1].Status != StatusApproved {
		t.Fatalf("proposal history = %+v, %v", all, err)
	}
	replayed, err := store.Submit(ctx, "BI Agent", first)
	if err != nil || replayed.ID != proposal.ID || replayed.Status != StatusApproved {
		t.Fatalf("replayed request after restart = %+v, %v", replayed, err)
	}

	out, err := MarshalYAML(published)
	if err != nil {
		t.Fatal(err)
	}
	var exported Graph
	if err := yaml.Unmarshal(out, &exported); err != nil {
		t.Fatal(err)
	}
	if exported.Nodes[0].Properties["precision"] != int(9007199254740993) || exported.Nodes[0].Ref.Path != "资料/次留.md" {
		t.Fatalf("export changed reference or numeric properties:\n%s", out)
	}
}

func TestContainmentAndReferenceValidation(t *testing.T) {
	node := Node{ID: "domain", Kind: KindDomain, Label: "经营分析"}
	child := Node{ID: "leaf", Kind: KindSkill, Label: "分析技能", ParentID: node.ID,
		Ref: &Reference{Repository: "catalog/skills", Path: "保留技能/SKILL.md", LineStart: 2, LineEnd: 5}}
	base := Graph{SchemaVersion: SchemaVersion, Nodes: []Node{node, child}, Edges: []Edge{
		{ID: "help", From: node.ID, To: child.ID, Relation: "described_by"},
		{ID: "describe", From: child.ID, To: node.ID, Relation: "describes"},
	}}
	if err := Validate(base); err != nil {
		t.Fatalf("valid hierarchy and cyclic relation edges: %v", err)
	}
	for _, unsafe := range []string{"/etc/example", "a/../../x", "a/../x", "../x", "/x", "a\\b", "x/%2e%2e/y", "https://host/path", "a//b"} {
		ref := *child.Ref
		ref.Path = unsafe
		bad := child
		bad.Ref = &ref
		if err := Validate(Graph{SchemaVersion: SchemaVersion, Nodes: []Node{node, bad}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("unsafe path %q accepted: %v", unsafe, err)
		}
	}
	cases := map[string]Graph{
		"missing leaf source": {SchemaVersion: SchemaVersion, Nodes: []Node{{ID: "leaf", Kind: KindDocument, Label: "文档"}}},
		"missing parent":      {SchemaVersion: SchemaVersion, Nodes: []Node{child}},
		"cyclic containment": {SchemaVersion: SchemaVersion, Nodes: []Node{
			{ID: "one", Kind: KindDomain, Label: "第一层", ParentID: "two"},
			{ID: "two", Kind: KindConcept, Label: "第二层", ParentID: "one"},
		}},
		"dangling edge": {SchemaVersion: SchemaVersion, Nodes: []Node{node}, Edges: base.Edges},
		"leaf parent": {SchemaVersion: SchemaVersion, Nodes: []Node{node, child,
			{ID: "metric", Kind: KindMetric, Label: "留存", ParentID: "leaf"},
		}},
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Validate(value); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid graph = %v", err)
			}
		})
	}
	if _, err := Apply(base, Patch{RemoveNodeIDs: []string{child.ID}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe removal returned %v", err)
	}
	candidate, err := Apply(base, Patch{RemoveNodeIDs: []string{child.ID}, RemoveEdgeIDs: []string{"help", "describe"}})
	if err != nil || len(candidate.Nodes) != 1 || len(candidate.Edges) != 0 {
		t.Fatalf("atomic removal = %+v, %v", candidate, err)
	}
}

func TestConcurrentStoresDeduplicateRequestsAndApproval(t *testing.T) {
	ctx := t.Context()
	db := filepath.Join(t.TempDir(), "index.db")
	a, b := testStore(t, db), testStore(t, db)
	defer a.Close()
	defer b.Close()
	type receipt struct {
		proposal Proposal
		err      error
	}
	result := make(chan receipt, 2)
	start := make(chan struct{})
	in := scenario("concurrent", "first")
	for _, store := range []*Store{a, b} {
		go func(s *Store) {
			<-start
			p, err := s.Submit(ctx, "BI Agent", in)
			result <- receipt{p, err}
		}(store)
	}
	close(start)
	first, second := <-result, <-result
	if first.err != nil || second.err != nil || first.proposal.ID != second.proposal.ID {
		t.Fatalf("concurrent duplicated requests: %+v %+v", first, second)
	}
	distinct, err := a.Submit(ctx, "BI Agent", scenario("another", "second"))
	if err != nil {
		t.Fatal(err)
	}
	start = make(chan struct{})
	go func() {
		<-start
		p, err := a.Review(ctx, first.proposal.ID, "human_a", "approve", "")
		result <- receipt{p, err}
	}()
	go func() {
		<-start
		p, err := b.Review(ctx, distinct.ID, "human_b", "approve", "")
		result <- receipt{p, err}
	}()
	close(start)
	first, second = <-result, <-result
	if first.err != nil {
		first, second = second, first
	}
	published, err := a.Graph(ctx)
	if err != nil || first.err != nil || !errors.Is(second.err, ErrConflict) || published.Revision != 1 || len(published.Nodes) != 1 {
		t.Fatalf("concurrent approvals: %+v %+v, snapshot %+v %v", first, second, published, err)
	}
}

func TestOpenDoesNotModifyAnExistingDatabase(t *testing.T) {
	database := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE ontology_nodes (id TEXT)"); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(database); !errors.Is(err, ErrInvalid) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatalf("legacy database was accepted: %v", err)
	}
	var tables int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type = 'table'").Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("legacy tables changed: %d %v", tables, err)
	}
}
