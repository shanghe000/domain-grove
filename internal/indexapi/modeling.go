package indexapi

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"

	"ontology-engine/internal/auth"
	"ontology-engine/internal/index"
	"ontology-engine/internal/indexai"
)

const (
	modelingBodyLimit       = 256 << 10
	modelingSubmitBodyLimit = 1 << 20
)

type ModelingPlanner interface {
	Configured() bool
	Plan(context.Context, string, index.Graph, []index.Reference) (indexai.Draft, error)
}

type modelingRequest struct {
	Instruction string            `json:"instruction"`
	References  []index.Reference `json:"references,omitempty"`
}

type modelingPreview struct {
	Title        string      `json:"title"`
	Rationale    string      `json:"rationale"`
	BaseRevision int64       `json:"base_revision"`
	Patch        index.Patch `json:"patch"`
	Preview      index.Graph `json:"preview"`
}

type modelingSubmission struct {
	RequestID    string       `json:"request_id"`
	Title        string       `json:"title"`
	Rationale    string       `json:"rationale,omitempty"`
	BaseRevision *int64       `json:"base_revision"`
	Patch        *index.Patch `json:"patch"`
}

type indexModeling struct {
	indexHTTP
	planner ModelingPlanner
}

// RegisterModeling must be mounted under the same authentication middleware as RegisterReview.
func RegisterModeling(mux *http.ServeMux, store *index.Store, allowDevReview bool, planner ModelingPlanner) {
	service := indexModeling{indexHTTP: indexHTTP{store: store, allowDevReview: allowDevReview}, planner: planner}
	mux.Handle("POST /api/v1/index/proposals/{id}/edit", service.identity(true, service.editProposal))
	mux.Handle("POST /api/v1/index/ingest", service.identity(true, service.ingest))
	mux.Handle("GET /api/v1/index/modeling", service.identity(true, service.modelingStatus))
	mux.Handle("POST /api/v1/index/modeling", service.identity(true, service.modelingPreview))
	mux.Handle("POST /api/v1/index/modeling/submit", service.identity(true, service.modelingSubmit))
}

func (s indexModeling) modelingStatus(w http.ResponseWriter, _ *http.Request, _ *auth.UserIdentity) {
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": s.planner != nil && s.planner.Configured()})
}

func (s indexModeling) modelingPreview(w http.ResponseWriter, r *http.Request, _ *auth.UserIdentity) {
	if s.planner == nil || !s.planner.Configured() {
		writeModelingError(w, indexai.ErrUnconfigured)
		return
	}
	var input modelingRequest
	if !readModelingJSON(w, r, modelingBodyLimit, &input) {
		return
	}
	instruction := strings.TrimSpace(input.Instruction)
	if instruction == "" {
		writeError(w, http.StatusBadRequest, "请输入建模说明。")
		return
	}
	if len(instruction) > indexai.MaxInstructionBytes {
		writeError(w, http.StatusBadRequest, "建模说明超过允许的长度，请缩短后重试。")
		return
	}
	if len(input.References) > indexai.MaxReferences {
		writeError(w, http.StatusBadRequest, "最多可提供 12 条资料定位。")
		return
	}
	for _, reference := range input.References {
		if err := index.ValidateReference(reference); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	graph, err := s.store.Graph(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	draft, err := s.planner.Plan(r.Context(), instruction, graph, input.References)
	if err != nil {
		writeModelingError(w, err)
		return
	}
	preview, err := index.Apply(graph, draft.Patch)
	if err != nil {
		writeModelingError(w, indexai.ErrInvalidDraft)
		return
	}
	writeJSON(w, http.StatusOK, modelingPreview{
		Title: draft.Title, Rationale: draft.Rationale, BaseRevision: graph.Revision, Patch: draft.Patch, Preview: preview,
	})
}

func (s indexModeling) modelingSubmit(w http.ResponseWriter, r *http.Request, user *auth.UserIdentity) {
	var input modelingSubmission
	if !readModelingJSON(w, r, modelingSubmitBodyLimit, &input) {
		return
	}
	if input.BaseRevision == nil || input.Patch == nil {
		writeError(w, http.StatusBadRequest, "请提供基础版本和提案变更。")
		return
	}
	proposal, err := s.store.Submit(r.Context(), user.UserID, index.SubmitInput{
		RequestID: input.RequestID, Title: input.Title, Rationale: input.Rationale,
		BaseRevision: *input.BaseRevision, Patch: *input.Patch,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, proposal)
}

func readModelingJSON(w http.ResponseWriter, r *http.Request, limit int64, input any) bool {
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if contentType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "请使用 JSON 提交建模请求。")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decodeSingleJSON(decoder, input); err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			writeError(w, http.StatusRequestEntityTooLarge, "建模请求超过允许的大小。")
		} else {
			writeError(w, http.StatusBadRequest, "建模请求格式无效。")
		}
		return false
	}
	return true
}

func writeModelingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, indexai.ErrUnconfigured):
		writeError(w, http.StatusServiceUnavailable, "AI 建模尚未配置。")
	case errors.Is(err, indexai.ErrInvalid):
		writeError(w, http.StatusBadRequest, "请核对建模说明和资料定位。")
	case errors.Is(err, indexai.ErrInvalidDraft):
		writeError(w, http.StatusUnprocessableEntity, "无法生成有效草稿，请调整建模说明或核对资料定位。")
	case errors.Is(err, indexai.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "AI 建模请求超时，请重试。")
	default:
		writeError(w, http.StatusBadGateway, "AI 建模暂时不可用，请稍后重试。")
	}
}

func (s indexModeling) editProposal(w http.ResponseWriter, r *http.Request, user *auth.UserIdentity) {
	var input struct {
		Version     *int64       `json:"version"`
		Patch       *index.Patch `json:"patch"`
		Undo        bool         `json:"undo"`
		Instruction string       `json:"instruction"`
	}
	if !readModelingJSON(w, r, modelingSubmitBodyLimit, &input) {
		return
	}
	count := 0
	if input.Patch != nil {
		count++
	}
	if input.Undo {
		count++
	}
	if input.Instruction != "" {
		count++
	}
	if count != 1 || input.Version == nil {
		writeError(w, 400, "请选择一项草案编辑操作并提供编辑版本。")
		return
	}
	id := r.PathValue("id")
	if input.Instruction != "" {
		if s.planner == nil || !s.planner.Configured() {
			writeModelingError(w, indexai.ErrUnconfigured)
			return
		}
		preview, err := s.store.Preview(r.Context(), id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		draft, err := s.planner.Plan(r.Context(), input.Instruction, preview, nil)
		if err != nil {
			writeModelingError(w, err)
			return
		}
		input.Patch = &draft.Patch
	}
	proposal, graph, err := s.store.Edit(r.Context(), id, user.UserID, *input.Version, input.Patch)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, proposalDetail{Proposal: proposal, Preview: &graph})
}
