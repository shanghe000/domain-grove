package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"ontology-engine/internal/ontology"
)

func TestPersistRetentionSeedAndSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "ontology.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.Seed(ctx, dir, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	graph, err := store.LoadGraph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := graph.GetNodeContext("Metric_Retention", ontology.DefaultBudget, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 1 || len(result.FrontierNodes) != 1 || result.FrontierNodes[0].NodeID != "Concept_MailSystem" {
		t.Fatalf("unexpected retention evidence and frontier: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, result.Artifacts[0].PhysicalRef)); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(dir, "ontology.yaml")
	for range 2 {
		if err := ontology.WriteSnapshot(snapshot, graph); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("snapshot should be read only, got %s", info.Mode())
	}
}

func TestLiveSeedDoesNotPublishMockEvidence(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "ontology.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Seed(t.Context(), dir, false); err != nil {
		t.Fatal(err)
	}
	graph, err := store.LoadGraph(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.GetNodeContext("Metric_Retention", ontology.DefaultBudget, false); err != nil {
		t.Fatal(err)
	}
	if len(graph.Data().Artifacts) != 0 {
		t.Fatal("live seed exposed a sample artifact")
	}
	if _, err := os.Stat(filepath.Join(dir, "docs")); !os.IsNotExist(err) {
		t.Fatalf("live seed created sample documents: %v", err)
	}
}

func TestGraphPatchRollsBackDanglingReference(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "ontology.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = ApplyPatch(ctx, tx, ontology.DraftPatch{
		Nodes: []ontology.Node{{ID: "Concept_Test", NodeType: "Concept", Label: "测试"}},
		Edges: []ontology.Edge{{SrcID: "Concept_Test", DstID: "Concept_Missing", Predicate: "depends_on"}},
	})
	if err == nil {
		t.Error("dangling edge was accepted")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	graph, err := store.LoadGraph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Data().Nodes) != 0 {
		t.Fatal("rolled back patch left partially written data")
	}
}
