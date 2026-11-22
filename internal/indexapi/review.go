package indexapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"ontology-engine/internal/auth"
	"ontology-engine/internal/index"
)

const reviewBodyLimit = 32 << 10

type reviewInput struct {
	Version  int64  `json:"version"`
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

type proposalDetail struct {
	Proposal     index.Proposal  `json:"proposal"`
	Preview      *index.Graph    `json:"preview,omitempty"`
	Warnings     []index.Warning `json:"graph_warnings,omitempty"`
	PreviewError string          `json:"preview_error,omitempty"`
}

type indexHTTP struct {
	store          *index.Store
	allowDevReview bool
}

// RegisterReview must be mounted under the workbench identity middleware.
func RegisterReview(mux *http.ServeMux, store *index.Store, allowDevReview bool) {
	service := indexHTTP{store: store, allowDevReview: allowDevReview}
	mux.Handle("GET /api/v1/index", service.identity(false, service.graph))
	mux.Handle("GET /api/v1/index/traverse", service.identity(false, service.traverse))
	mux.Handle("GET /api/v1/index/changes", service.identity(false, service.changes))
	mux.Handle("GET /api/v1/index/proposals/{id}/traverse", service.identity(true, service.traverse))
	mux.Handle("POST /api/v1/index/preview/traverse", service.identity(true, service.traverse))
	mux.Handle("GET /api/v1/index/proposals/{id}/preview.yaml", service.identity(true, service.yaml))
	mux.Handle("GET /api/v1/index.yaml", service.identity(false, service.yaml))
	mux.Handle("GET /api/v1/index/session", service.identity(false, service.session))
	mux.Handle("GET /api/v1/index/proposals", service.identity(true, service.list))
	mux.Handle("GET /api/v1/index/proposals/{id}", service.identity(true, service.detail))
	mux.Handle("POST /api/v1/index/proposals/{id}/review", service.identity(true, service.review))
}

type identityHandler func(http.ResponseWriter, *http.Request, *auth.UserIdentity)

func (s indexHTTP) identity(review bool, next identityHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		user, found := auth.IdentityFromContext(r.Context())
		if !found || user == nil || strings.TrimSpace(user.UserID) == "" {
			writeError(w, http.StatusUnauthorized, "工作台身份不可用。")
			return
		}
		if review && user.UserID == auth.LocalUserID && !s.allowDevReview {
			writeError(w, http.StatusForbidden, "工作台尚未启用本机审核。")
			return
		}
		next(w, r, user)
	})
}

func (s indexHTTP) graph(w http.ResponseWriter, r *http.Request, _ *auth.UserIdentity) {
	graph, err := s.store.Graph(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, graph)
}

func (s indexHTTP) yaml(w http.ResponseWriter, r *http.Request, _ *auth.UserIdentity) {
	var graph index.Graph
	var err error
	name := "ontology.yaml"
	if id := r.PathValue("id"); id != "" {
		graph, err = s.store.Preview(r.Context(), id)
		name = "draft-" + id + ".yaml"
	} else {
		graph, err = s.store.Graph(r.Context())
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	snapshot, err := index.MarshalYAML(graph)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if r.Method != http.MethodHead {
		_, _ = w.Write(snapshot)
	}
}

func (s indexHTTP) session(w http.ResponseWriter, _ *http.Request, user *auth.UserIdentity) {
	writeJSON(w, http.StatusOK, map[string]*auth.UserIdentity{"user": user})
}

func (s indexHTTP) list(w http.ResponseWriter, r *http.Request, _ *auth.UserIdentity) {
	proposals, err := s.store.List(r.Context(), strings.TrimSpace(r.URL.Query().Get("status")))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if proposals == nil {
		proposals = []index.Proposal{}
	}
	writeJSON(w, http.StatusOK, map[string][]index.Proposal{"proposals": proposals})
}

func (s indexHTTP) detail(w http.ResponseWriter, r *http.Request, _ *auth.UserIdentity) {
	id := r.PathValue("id")
	proposal, err := s.store.Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	result := proposalDetail{Proposal: proposal}
	if proposal.Status == index.StatusPending {
		graph, err := s.store.Preview(r.Context(), id)
		if err != nil {
			_, result.PreviewError = publicError(err)
		} else {
			result.Preview = &graph
			result.Warnings = index.Warnings(graph)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s indexHTTP) review(w http.ResponseWriter, r *http.Request, user *auth.UserIdentity) {
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if contentType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "请使用 JSON 提交审核决定。")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, reviewBodyLimit))
	decoder.DisallowUnknownFields()
	var input reviewInput
	if err := decodeSingleJSON(decoder, &input); err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			writeError(w, http.StatusRequestEntityTooLarge, "审核请求超过允许的大小。")
			return
		}
		writeError(w, http.StatusBadRequest, "审核请求格式无效。")
		return
	}
	if input.Decision != "approve" && input.Decision != "reject" {
		writeError(w, http.StatusBadRequest, "请选择批准或驳回。")
		return
	}
	proposal, err := s.store.ReviewVersion(r.Context(), r.PathValue("id"), user.UserID, input.Decision, input.Note, input.Version)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, proposal)
}

func decodeSingleJSON(decoder *json.Decoder, input any) error {
	if err := decoder.Decode(input); err != nil {
		return err
	}
	var extra json.RawMessage
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func publicError(err error) (int, string) {
	switch {
	case errors.Is(err, index.ErrNotFound):
		return http.StatusNotFound, "找不到指定的索引节点或候选提案。"
	case errors.Is(err, index.ErrConflict):
		return http.StatusConflict, "索引版本或提案状态已变化，请刷新后再操作。"
	case errors.Is(err, index.ErrInvalid):
		return http.StatusBadRequest, err.Error()
	default:
		return http.StatusInternalServerError, "索引服务暂时无法完成请求。"
	}
}

func toolError(err error) error {
	if err == nil {
		return nil
	}
	_, message := publicError(err)
	return errors.New(message)
}

func writeStoreError(w http.ResponseWriter, err error) {
	status, message := publicError(err)
	writeError(w, status, message)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
