package indexapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"ontology-engine/internal/index"
	"ontology-engine/internal/indexai"
)

type modelingFixture struct {
	plan func(context.Context, string, index.Graph, []index.Reference) (indexai.Draft, error)
}

func (f modelingFixture) Configured() bool { return f.plan != nil }

func (f modelingFixture) Plan(ctx context.Context, instruction string, graph index.Graph, refs []index.Reference) (indexai.Draft, error) {
	return f.plan(ctx, instruction, graph, refs)
}

func TestModelingPreviewSubmitAndReview(t *testing.T) {
	const path = "/api/v1/index/modeling"
	store := indexStore(t)
	calls := 0
	fixture := modelingFixture{plan: func(_ context.Context, instruction string, graph index.Graph, refs []index.Reference) (indexai.Draft, error) {
		calls++
		if instruction != "建立留存口径索引" || graph.Revision != 0 || len(graph.Nodes) != 0 ||
			len(refs) != 1 || refs[0].Repository != "bi-kb" || refs[0].Path != "metrics/留存.md" {
			t.Fatalf("unexpected planning input: %q, %+v, %+v", instruction, graph, refs)
		}
		return indexai.Draft{Title: "留存口径", Rationale: "将口径文档挂到留存指标下。", Patch: index.Patch{UpsertNodes: []index.Node{
			{ID: "retention", Kind: index.KindMetric, Label: "留存"},
			{ID: "retention_doc", Kind: index.KindDocument, Label: "留存口径", ParentID: "retention", Ref: &refs[0]},
		}}}, nil
	}}
	handler := authenticatedReview(t, store, func(mux *http.ServeMux) { RegisterModeling(mux, store, false, fixture) })
	requireStatus(t, reviewRequest(handler, http.MethodGet, path, "", ""), http.StatusUnauthorized)
	status := reviewRequest(handler, http.MethodGet, path, reviewTestToken, "")
	requireStatus(t, status, http.StatusOK)
	if status.Body.String() != "{\"enabled\":true}\n" || status.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected modeling status: %s", status.Body.String())
	}
	request := `{"instruction":" 建立留存口径索引 ","references":[{"repository":"bi-kb","path":"metrics/留存.md","line_start":4,"line_end":8}]}`
	requireStatus(t, reviewRequest(handler, http.MethodPost, path, submitTestToken, request), http.StatusUnauthorized)
	for _, invalid := range []string{
		`{"instruction":" "}`,
		`{"instruction":"建立留存口径索引","references":[{"repository":"bi-kb","path":"../secret.md"}]}`,
		`{"instruction":"建立留存口径索引","document_body":"正文不属于此接口"}`,
		`{"instruction":"建立留存口径索引","references":[` + strings.Repeat(`{"repository":"bi-kb","path":"retention.md"},`, 12) + `{"repository":"bi-kb","path":"retention.md"}]}`,
		`{"instruction":"建立留存口径索引"} {}`,
	} {
		requireStatus(t, reviewRequest(handler, http.MethodPost, path, reviewTestToken, invalid), http.StatusBadRequest)
	}
	if calls != 0 {
		t.Fatal("planner called for an unauthorized or malformed request")
	}
	response := reviewRequest(handler, http.MethodPost, path, reviewTestToken, request)
	requireStatus(t, response, http.StatusOK)
	var draft modelingPreview
	if err := json.NewDecoder(response.Body).Decode(&draft); err != nil || draft.BaseRevision != 0 ||
		len(draft.Patch.UpsertNodes) != 2 || len(draft.Preview.Nodes) != 2 || calls != 1 {
		t.Fatalf("unexpected preview: %+v (error %v)", draft, err)
	}
	graph, err := store.Graph(context.Background())
	if err != nil || len(graph.Nodes) != 0 || graph.Revision != draft.BaseRevision {
		t.Fatalf("preview modified published graph: %+v (error %v)", graph, err)
	}
	proposals, err := store.List(context.Background(), "")
	if err != nil || len(proposals) != 0 {
		t.Fatalf("preview persisted a proposal: %+v (error %v)", proposals, err)
	}

	input := index.SubmitInput{RequestID: "ai-modeling-review", Title: "人工编辑留存口径", BaseRevision: draft.BaseRevision, Patch: draft.Patch}
	input.Patch.UpsertNodes[0].Label = "次日留存"
	submission, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, reviewRequest(handler, http.MethodPost, path+"/submit", submitTestToken, string(submission)), http.StatusUnauthorized)
	spoofed := `{"submitted_by":"spoofed",` + string(submission[1:])
	requireStatus(t, reviewRequest(handler, http.MethodPost, path+"/submit", reviewTestToken, spoofed), http.StatusBadRequest)
	missingRevision := strings.Replace(string(submission), `"base_revision":0,`, "", 1)
	requireStatus(t, reviewRequest(handler, http.MethodPost, path+"/submit", reviewTestToken, missingRevision), http.StatusBadRequest)
	submitted := reviewRequest(handler, http.MethodPost, path+"/submit", reviewTestToken, string(submission))
	requireStatus(t, submitted, http.StatusOK)
	var pending index.Proposal
	if err := json.NewDecoder(submitted.Body).Decode(&pending); err != nil || pending.Status != index.StatusPending ||
		pending.SubmittedBy != "reviewer-fixture" || pending.Title != input.Title || calls != 1 {
		t.Fatalf("unexpected proposal: %+v (error %v)", pending, err)
	}
	graph, err = store.Graph(context.Background())
	if err != nil || len(graph.Nodes) != 0 || graph.Revision != 0 {
		t.Fatalf("submitting published the proposal: %+v (error %v)", graph, err)
	}
	approve := "/api/v1/index/proposals/" + pending.ID + "/review"
	requireStatus(t, reviewRequest(handler, http.MethodPost, approve, reviewTestToken, `{"decision":"approve"}`), http.StatusOK)
	graph, err = store.Graph(context.Background())
	if err != nil || graph.Revision != 1 || len(graph.Nodes) != 2 || graph.Nodes[0].Label != "次日留存" {
		t.Fatalf("edited proposal was not published by review: %+v (error %v)", graph, err)
	}
}

func TestModelingDisabledAndBadProviderOutput(t *testing.T) {
	const path = "/api/v1/index/modeling"
	const request = `{"instruction":"建立留存口径索引"}`
	for _, scenario := range []struct {
		name string
		plan func(context.Context, string, index.Graph, []index.Reference) (indexai.Draft, error)
		want int
	}{
		{"disabled", nil, http.StatusServiceUnavailable},
		{"bad patch", func(context.Context, string, index.Graph, []index.Reference) (indexai.Draft, error) {
			return indexai.Draft{Title: "无效示例", Patch: index.Patch{UpsertNodes: []index.Node{{ID: "doc", Kind: index.KindDocument, Label: "文档"}}}}, nil
		}, http.StatusUnprocessableEntity},
		{"provider failure", func(context.Context, string, index.Graph, []index.Reference) (indexai.Draft, error) {
			return indexai.Draft{}, fmt.Errorf("%w: upstream-body-private", indexai.ErrUnavailable)
		}, http.StatusBadGateway},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store := indexStore(t)
			handler := authenticatedReview(t, store, func(mux *http.ServeMux) {
				RegisterModeling(mux, store, false, modelingFixture{plan: scenario.plan})
			})
			if scenario.plan == nil {
				status := reviewRequest(handler, http.MethodGet, path, reviewTestToken, "")
				requireStatus(t, status, http.StatusOK)
				if status.Body.String() != "{\"enabled\":false}\n" {
					t.Fatalf("unexpected disabled status: %s", status.Body.String())
				}
			}
			result := reviewRequest(handler, http.MethodPost, path, reviewTestToken, request)
			requireStatus(t, result, scenario.want)
			if strings.Contains(result.Body.String(), "upstream-body-private") {
				t.Fatal("provider error exposed upstream content")
			}
		})
	}
}
