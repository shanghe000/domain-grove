package spaceapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ontology-engine/internal/auth"
	"ontology-engine/internal/index"
	"ontology-engine/internal/indexai"
	"ontology-engine/internal/spaces"
)

type scopedPlanner func(context.Context, string, index.Graph, []index.Reference) (indexai.Draft, error)

func (scopedPlanner) Configured() bool { return true }

func (p scopedPlanner) Plan(ctx context.Context, instruction string, graph index.Graph, refs []index.Reference) (indexai.Draft, error) {
	return p(ctx, instruction, graph, refs)
}

func openFixture(t *testing.T) (*index.Store, *spaces.Catalog) {
	t.Helper()
	root := t.TempDir()
	store, err := index.Open(filepath.Join(root, "default-index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	catalog, err := spaces.Open(filepath.Join(root, "spaces"), store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close() })
	return store, catalog
}

func fixtureIdentities(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subject, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer fixture-")
		if ok && (subject == "owner" || subject == "reviewer" || subject == "other") {
			user := &auth.UserIdentity{UserID: subject, Username: subject}
			r = r.WithContext(auth.WithIdentity(r.Context(), user))
		}
		next.ServeHTTP(w, r)
	})
}

func response(t *testing.T, handler http.Handler, method, path, token, payload string, want int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(payload))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if payload != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	if result.Code != want {
		t.Fatalf("method %s on %s returned %d, want %d, body: %s", method, path, result.Code, want, result.Body)
	}
	return result
}

func TestProjectHTTPMembershipAndScopedIndex(t *testing.T) {
	_, catalog := openFixture(t)
	mux := http.NewServeMux()
	planCalls := 0
	planner := scopedPlanner(func(_ context.Context, instruction string, graph index.Graph, refs []index.Reference) (indexai.Draft, error) {
		planCalls++
		if instruction != "补充项目分类" || graph.Revision != 1 || len(graph.Nodes) != 1 ||
			graph.Nodes[0].ID != "domain_alpha" || len(refs) != 0 {
			t.Fatalf("planner received another project's state: %+v", graph)
		}
		return indexai.Draft{Title: "项目分类", Rationale: "补充业务分组", Patch: index.Patch{UpsertNodes: []index.Node{
			{ID: "alpha_metric", Kind: index.KindMetric, ParentID: "domain_alpha", Label: "策划组指标"},
		}}}, nil
	})
	New(catalog, false, planner, "").Register(mux)
	app := fixtureIdentities(mux)
	const owner, reviewer, other = "fixture-owner", "fixture-reviewer", "fixture-other"

	response(t, mux, http.MethodGet, spacesPath, "", "", http.StatusUnauthorized)
	response(t, app, http.MethodPost, spacesPath, owner, `{"id":"alpha","name":"策划组"}`, http.StatusCreated)
	response(t, app, http.MethodPost, spacesPath, other, `{"id":"beta","name":"数据组"}`, http.StatusCreated)
	ownerList := response(t, app, http.MethodGet, spacesPath, owner, "", http.StatusOK)
	if !strings.Contains(ownerList.Body.String(), `"id":"alpha"`) ||
		strings.Contains(ownerList.Body.String(), `"id":"beta"`) {
		t.Fatal("owner saw spaces outside their membership")
	}
	foreign := response(t, app, http.MethodGet, spacesPath+"/alpha/index", other, "", http.StatusNotFound)
	missing := response(t, app, http.MethodGet, spacesPath+"/unknown/index", other, "", http.StatusNotFound)
	if foreign.Body.String() != missing.Body.String() {
		t.Fatal("unrelated user can distinguish existing project from missing project")
	}

	const alpha = spacesPath + "/alpha"
	response(t, app, http.MethodPost, alpha+"/members", owner, `{"subject":"reviewer"}`, http.StatusOK)
	members := response(t, app, http.MethodGet, alpha+"/members", owner, "", http.StatusOK)
	if !strings.Contains(members.Body.String(), `"role":"reviewer"`) {
		t.Fatal("new reviewer not listed")
	}
	response(t, app, http.MethodGet, alpha+"/members", reviewer, "", http.StatusForbidden)
	response(t, app, http.MethodPost, alpha+"/members", reviewer, `{"subject":"other"}`, http.StatusForbidden)
	response(t, app, http.MethodPost, alpha+"/credentials", reviewer, `{"scope":"read"}`, http.StatusForbidden)
	response(t, app, http.MethodPost, spacesPath+"/default/credentials", owner, `{"scope":"read"}`, http.StatusForbidden)
	token := response(t, app, http.MethodPost, alpha+"/credentials", owner, `{"scope":"submit"}`, http.StatusOK)
	if token.Header().Get("Cache-Control") != "no-store" || !strings.Contains(token.Body.String(), `"token":"idx_`) {
		t.Fatal("token issuance is not a secure one-time response")
	}

	ctx := context.Background()
	store, err := catalog.Store(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := store.Submit(ctx, "fixture-agent", index.SubmitInput{
		RequestID: "fixture-alpha", Title: "待审核的业务域",
		Patch: index.Patch{UpsertNodes: []index.Node{
			{ID: "domain_alpha", Kind: index.KindDomain, Label: "策划组定义"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	detailPath := alpha + "/index/proposals/" + proposal.ID
	pending := response(t, app, http.MethodGet, detailPath, reviewer, "", http.StatusOK)
	if !strings.Contains(pending.Body.String(), `"preview"`) {
		t.Fatal("reviewer did not see the scoped proposal preview")
	}
	response(t, app, http.MethodGet, spacesPath+"/beta/index/proposals/"+proposal.ID, other, "", http.StatusNotFound)
	response(t, app, http.MethodGet, spacesPath+"/default/index/proposals/"+proposal.ID, owner, "", http.StatusNotFound)
	response(t, app, http.MethodGet, alpha+"/index/modeling", reviewer, "", http.StatusOK)
	response(t, app, http.MethodPost, detailPath+"/review", reviewer, `{"decision":"approve","note":"已核对项目引用。"}`, http.StatusOK)
	betaStore, err := catalog.Store(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	private, err := betaStore.Submit(ctx, "fixture-agent", index.SubmitInput{RequestID: "beta-only", Title: "另一项目",
		Patch: index.Patch{UpsertNodes: []index.Node{{ID: "domain_beta_private", Kind: index.KindDomain, Label: "数据组定义"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := betaStore.Review(ctx, private.ID, "other", "approve", ""); err != nil {
		t.Fatal(err)
	}
	modelPath := alpha + "/index/modeling"
	instruction := `{"instruction":"补充项目分类"}`
	response(t, app, http.MethodPost, modelPath, other, instruction, http.StatusNotFound)
	model := response(t, app, http.MethodPost, modelPath, reviewer, instruction, http.StatusOK)
	var preview struct {
		BaseRevision int64       `json:"base_revision"`
		Graph        index.Graph `json:"preview"`
	}
	if err := json.NewDecoder(model.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if planCalls != 1 || preview.BaseRevision != 1 || preview.Graph.Revision != 1 ||
		len(preview.Graph.Nodes) != 2 || preview.Graph.Nodes[0].ID != "alpha_metric" {
		t.Fatalf("scoped modeling returned the wrong base or preview: %+v", preview)
	}
	graph := response(t, app, http.MethodGet, alpha+"/index", reviewer, "", http.StatusOK)
	if !strings.Contains(graph.Body.String(), "domain_alpha") || strings.Contains(graph.Body.String(), "alpha_metric") {
		t.Fatal("scoped approval did not publish to alpha or modeling preview wrote to the graph")
	}
	yaml := response(t, app, http.MethodGet, alpha+"/index.yaml", reviewer, "", http.StatusOK)
	if !strings.Contains(yaml.Body.String(), "domain_alpha") || yaml.Header().Get("Content-Disposition") == "" {
		t.Fatal("project YAML does not reflect project graph")
	}
	base := response(t, app, http.MethodGet, spacesPath+"/default/index", owner, "", http.StatusOK)
	beta := response(t, app, http.MethodGet, spacesPath+"/beta/index", other, "", http.StatusOK)
	if strings.Contains(base.Body.String(), "domain_alpha") || strings.Contains(beta.Body.String(), "domain_alpha") {
		t.Fatal("alpha publication leaked into an unrelated space")
	}

	response(t, app, http.MethodPost, alpha+"/members/remove", owner, `{"subject":"reviewer"}`, http.StatusNoContent)
	response(t, app, http.MethodGet, alpha+"/index", reviewer, "", http.StatusNotFound)
}

func TestSyntheticIdentityRequiresExplicitPermissionForProjects(t *testing.T) {
	_, catalog := openFixture(t)
	if _, err := catalog.Create(context.Background(), "dev_user", "old-test", "原测试"); err != nil {
		t.Fatal(err)
	}
	for _, allow := range []bool{false, true} {
		mux := http.NewServeMux()
		New(catalog, allow, nil, "").Register(mux)
		app := auth.LocalIdentity(mux)
		data := response(t, app, http.MethodGet, spacesPath, "", "", http.StatusOK)
		if strings.Contains(data.Body.String(), "old-test") != allow {
			t.Fatal("named project metadata not gated for dev identity")
		}
		if allow {
			response(t, app, http.MethodGet, spacesPath+"/old-test/index", "", "", http.StatusOK)
			response(t, app, http.MethodPost, spacesPath, "", `{"id":"new-test","name":"新测试"}`, http.StatusCreated)
		} else {
			response(t, app, http.MethodGet, spacesPath+"/old-test/index", "", "", http.StatusForbidden)
			response(t, app, http.MethodPost, spacesPath, "", `{"id":"new-test","name":"新测试"}`, http.StatusForbidden)
		}
	}
}
