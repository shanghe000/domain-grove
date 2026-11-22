package action

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ontology-engine/internal/auth"
	"ontology-engine/internal/ontology"
	"ontology-engine/internal/storage"
)

func actionFixture(t *testing.T, worker http.Handler) (*Service, http.Handler) {
	t.Helper()
	workerServer := httptest.NewServer(worker)
	t.Cleanup(workerServer.Close)
	data := t.TempDir()
	store, err := storage.Open(filepath.Join(data, "ontology.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Seed(t.Context(), data, true); err != nil {
		t.Fatal(err)
	}
	service, err := New(t.Context(), store, Config{DataDir: data, WorkerURL: workerServer.URL})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := auth.New(t.Context(), auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	service.Register(mux)
	return service, provider.Middleware(mux)
}

func postAction(handler http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	return result
}

const validPatch = `{"session_id":"draft_test","actor":"human","operations":[{"op":"add_axiom","node_id":"Metric_Retention","axiom":{"type":"Pitfall","rule":"atomic publication test"}}]}`

func TestFailedFirstRequestsLeaveNoDraft(t *testing.T) {
	service, handler := actionFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	badPatch := strings.Replace(validPatch, `}]}`, `},{"op":"remove_node","node_id":"missing"}]}`, 1)
	if got := postAction(handler, "actions/patch_draft_graph", badPatch); got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected patch validation to fail, got %d %s", got.Code, got.Body)
	}
	if got := postAction(handler, "chat/message", `{"session_id":"draft_test","message":"source"}`); got.Code != http.StatusBadGateway {
		t.Fatalf("expected Worker failure, got %d %s", got.Code, got.Body)
	}
	var rows int
	if err := service.store.DB.QueryRow(`SELECT count(*) FROM draft_sessions`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rejected requests persisted a draft: rows=%d error=%v", rows, err)
	}
	if got := postAction(handler, "actions/patch_draft_graph", validPatch); got.Code != http.StatusOK {
		t.Fatalf("retry could not create a draft: %d %s", got.Code, got.Body)
	}
	other := &auth.UserIdentity{UserID: "other", Username: "dev_user"}
	if _, err := service.findDraft(t.Context(), other, "draft_test"); err == nil {
		t.Fatal("a matching display name exposed another subject's draft")
	}
}

func TestCommitReplayRepairsSnapshot(t *testing.T) {
	service, handler := actionFixture(t, http.NotFoundHandler())
	if got := postAction(handler, "actions/patch_draft_graph", validPatch); got.Code != http.StatusOK {
		t.Fatalf("unable to prepare draft: %d %s", got.Code, got.Body)
	}
	path := filepath.Join(service.config.DataDir, "ontology.yaml")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	commit := func() (string, bool) {
		t.Helper()
		result := postAction(handler, "actions/commit_draft", `{"session_id":"draft_test"}`)
		var saved struct {
			Draft   draft  `json:"draft"`
			Version string `json:"version_timestamp"`
			Pending bool   `json:"snapshot_pending"`
		}
		if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &saved) != nil ||
			saved.Draft.Status != "committed" || saved.Version == "" {
			t.Fatalf("unexpected publication response: %d %s", result.Code, result.Body)
		}
		return saved.Version, saved.Pending
	}
	version, pending := commit()
	if !pending {
		t.Fatal("the failed export was not reported")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if repaired, pending := commit(); pending || repaired != version {
		t.Fatal("the retry did not regenerate the export with the original publication version")
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "atomic publication test") {
		t.Fatalf("repaired snapshot missing the published change: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o444 {
		t.Fatalf("repaired snapshot is not read-only: %v", err)
	}
}

func TestCanonicalMarkdownLifecycle(t *testing.T) {
	data := t.TempDir()
	s := &Service{config: Config{DataDir: data}}
	id := "Artifact_Versioned"
	revision := strings.Repeat("a", 32)
	alias := filepath.Join(data, "docs", id+".md")
	immutable := filepath.Join(data, "docs", "revisions", id, revision+".md")
	legacy := filepath.Join(data, "docs", "legacy.md")
	if err := os.MkdirAll(filepath.Dir(immutable), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{immutable, legacy} {
		if err := os.WriteFile(path, []byte("---\ntitle: test\n---\n"), 0o440); err != nil {
			t.Fatal(err)
		}
	}
	graph := func(status string) *ontology.Graph {
		return ontology.NewGraph([]ontology.Node{{ID: id, Status: status}}, nil, []ontology.Artifact{{
			NodeID: id, ArtifactType: ontology.FeishuMarkdown, PhysicalRef: "docs/" + id + ".md",
			SchemaMeta: map[string]any{"content_revision": revision},
		}})
	}
	if err := s.reconcileDocuments(graph("active")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(alias); err != nil {
		t.Fatal("canonical export is missing", err)
	}
	if err := s.reconcileDocuments(graph(ontology.Deprecated)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(alias); !os.IsNotExist(err) {
		t.Fatal("retired alias remains accessible")
	}
	for _, path := range []string{immutable, legacy} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("source bytes were lost", err)
		}
	}
	if err := s.reconcileDocuments(graph("active")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(alias); err != nil {
		t.Fatal("reactivation did not restore the canonical export", err)
	}
}

func TestPublishedExcelColumnEditKeepsPhysicalBinding(t *testing.T) {
	const tableID = "Artifact_Activity"
	const domainID = "Domain_Archived"
	graph := ontology.NewGraph([]ontology.Node{
		{ID: tableID, NodeType: ontology.ArtifactNode, Status: "active"},
		{ID: domainID, NodeType: ontology.Domain, Status: ontology.Deprecated},
	}, nil, []ontology.Artifact{{
		NodeID: tableID, ArtifactType: ontology.DuckDBTable, PhysicalRef: "cfg_rev_abc", SchemaHash: "known-schema",
		SchemaMeta: map[string]any{"columns": []any{map[string]any{"name": "activity_id", "comment": "原说明", "confidence": "low"}}},
	}})
	editor := newEditor(graph, emptyPatch)
	comment := "修订说明"
	update := operation{Op: "update_artifact_column", NodeID: tableID, ColumnName: "activity_id", Comment: &comment, Confidence: "high"}
	if _, err := editor.apply(update); err != nil {
		t.Fatal(err)
	}
	patch := editor.patch()
	if len(patch.Artifacts) != 1 || len(patch.Nodes) != 0 || len(approvedSources(nil, patch.Artifacts)) != 0 {
		t.Fatalf("annotation should stage only metadata: %+v", patch)
	}
	original, _ := graph.Artifact(tableID)
	revised := patch.Artifacts[0]
	column := func(a ontology.Artifact) map[string]any { return a.SchemaMeta["columns"].([]any)[0].(map[string]any) }
	if revised.PhysicalRef != original.PhysicalRef || revised.SchemaHash != original.SchemaHash ||
		column(original)["comment"] != "原说明" || column(revised)["comment"] != comment || column(revised)["confidence"] != "high" {
		t.Fatalf("annotation changed the original or physical schema: %+v", revised)
	}
	if _, err := editor.apply(operation{Op: "move_node_domain", NodeID: tableID, TargetDomain: domainID}); err == nil {
		t.Fatal("moved a node into a deprecated domain")
	}
	if _, err := editor.apply(operation{Op: "remove_node", NodeID: tableID}); err != nil {
		t.Fatal(err)
	}
	if _, err := editor.apply(update); err == nil {
		t.Fatal("edited the configuration of a removed artifact")
	}
}
