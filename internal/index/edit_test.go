package index

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestEditUndoReviewVersion(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	domain := Node{ID: "d", Kind: "domain", Label: "数据分析域"}
	p, err := store.Submit(ctx, "agent", SubmitInput{RequestID: "r", Title: "初稿", Patch: Patch{SchemaVersion: 2, UpsertNodes: []Node{domain}}})
	if err != nil {
		t.Fatal(err)
	}
	for i, label := range []string{"一", "二"} {
		domain.Label = label
		var g Graph
		p, g, err = store.Edit(ctx, p.ID, "reviewer", int64(i), &Patch{UpsertNodes: []Node{domain}})
		if err != nil || g.Nodes[0].Label != label || !p.CanUndo {
			t.Fatalf("edit failed: %v", err)
		}
	}
	if _, err := store.ReviewVersion(ctx, p.ID, "reviewer", "approve", "", 0); !errors.Is(err, ErrConflict) {
		t.Fatal("stale review accepted")
	}
	for i, label := range []string{"一", "数据分析域"} {
		var g Graph
		p, g, err = store.Edit(ctx, p.ID, "reviewer", int64(2+i), nil)
		if err != nil || g.Nodes[0].Label != label || p.CanUndo != (i == 0) {
			t.Fatalf("undo failed: %v", err)
		}
	}
	if _, err = store.ReviewVersion(ctx, p.ID, "reviewer", "approve", "", p.EditVersion); err != nil {
		t.Fatal(err)
	}
	g, err := store.Graph(ctx)
	if err != nil || g.Revision != 1 || g.Nodes[0].Label != "数据分析域" {
		t.Fatalf("bad published graph: %v", err)
	}
}

func TestReplaceDraftRollsBack(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	in := SubmitInput{RequestID: "old", Title: "原稿", Patch: Patch{UpsertNodes: []Node{{ID: "a", Kind: "domain", Label: "旧"}}}}
	p, err := store.Submit(ctx, "user", in)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID = "new"
	in.Patch.SchemaVersion = 2
	in.Patch.UpsertNodes[0].Label = "新"
	if _, err = store.Review(ctx, p.ID, "reviewer", "reject", "手动处理"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceDraft(ctx, p.ID, "operator", in); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted already reviewed base")
	}
	all, err := store.List(ctx, "")
	if err != nil || len(all) != 1 {
		t.Fatal("uncommitted replacement leaked")
	}
}
