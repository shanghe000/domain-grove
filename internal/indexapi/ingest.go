package indexapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ontology-engine/internal/auth"
	"ontology-engine/internal/index"
)

type incomingSource struct {
	URL     string `json:"url,omitempty"`
	Source  string `json:"source,omitempty"`
	Version string `json:"version,omitempty"`
}
type sourceResponse struct {
	Status   string      `json:"status"`
	Title    string      `json:"title"`
	ID       string      `json:"id"`
	Kind     string      `json:"kind"`
	Previous string      `json:"previous"`
	Content  string      `json:"content"`
	Shape    string      `json:"shape"`
	Patch    index.Patch `json:"patch"`
	Error    string      `json:"error"`
	Code     int         `json:"code"`
}

var sourceSlots = make(chan struct{}, 2)

func (s indexModeling) ingest(w http.ResponseWriter, r *http.Request, user *auth.UserIdentity) {
	select {
	case sourceSlots <- struct{}{}:
		defer func() { <-sourceSlots }()
	default:
		writeError(w, 429, "资料解析繁忙，请稍后重试。")
		return
	}
	space, _ := r.Context().Value(sourceSpaceKey{}).(string)
	if space == "" {
		space = "default"
	}
	input := map[string]any{"space": space}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "multipart/form-data" {
		r.Body = http.MaxBytesReader(w, r.Body, 9<<20)
		if err := r.ParseMultipartForm(9 << 20); err != nil {
			writeError(w, 400, "请上传 8 MiB 以内的工作簿。")
			return
		}
		defer r.MultipartForm.RemoveAll()
		repository, path := r.FormValue("repository"), r.FormValue("path")
		if err := index.ValidateReference(index.Reference{Repository: repository, Path: path}); err != nil {
			writeStoreError(w, err)
			return
		}
		f, h, err := r.FormFile("file")
		if err != nil {
			writeError(w, 400, "请选择工作簿。")
			return
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
		if err != nil || len(b) > 8<<20 {
			writeError(w, 413, "工作簿超过大小限制。")
			return
		}
		input["repository"] = repository
		input["path"] = path
		input["file"] = map[string]string{"name": h.Filename, "content_base64": base64.StdEncoding.EncodeToString(b)}
	} else {
		var src incomingSource
		if !readModelingJSON(w, r, 32<<10, &src) {
			return
		}
		if (src.URL == "") == (src.Source != "tracking") {
			writeError(w, 400, "需要一个来源链接或埋点同步操作。")
			return
		}
		input["url"] = src.URL
		input["source"] = src.Source
		input["version"] = src.Version
	}
	graph, err := s.store.Graph(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	input["graph"] = graph
	body, err := json.Marshal(input)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	adapter := os.Getenv("INDEX_SOURCE_ADAPTER")
	if adapter == "" {
		adapter = "/app/workers/extractor/index_source.py"
	}
	python := os.Getenv("INDEX_SOURCE_PYTHON")
	if python == "" {
		python = "python3"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, filepath.Clean(adapter))
	command.Stdin = bytes.NewReader(body)
	out, err := command.Output()
	if err != nil {
		writeError(w, 503, "来源解析组件不可用或超时，请重试。")
		return
	}
	var data sourceResponse
	if len(out) > 2<<20 || json.Unmarshal(out, &data) != nil {
		writeError(w, 502, "来源结构超过限制或响应无效。")
		return
	}
	if data.Error != "" {
		code := data.Code
		if code < 400 || code > 599 {
			code = 422
		}
		writeError(w, code, data.Error)
		return
	}
	if data.Status == "matched" {
		changed, err := s.store.SyncSource(r.Context(), graph.Revision, data.ID, data.Shape, data.Previous, data.Content)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		status, msg := "unchanged", "已核对来源，索引结构与内容均无变更。"
		if changed && data.Kind == "config_table" {
			status, msg = "hot_update", "配置数值已在来源侧更新，已记录同步指纹。"
		}
		writeJSON(w, 200, map[string]string{"status": status, "message": msg})
		return
	}
	key := fmt.Sprintf("source.%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%s/%s/%s", user.UserID, graph.Revision, data.ID, data.Shape, data.Content))))
	p, err := s.store.Submit(r.Context(), user.UserID, index.SubmitInput{RequestID: key, Title: data.Title, Rationale: "来源结构或资料内容发生变化，请核对归属、逻辑锚点与关联。", BaseRevision: graph.Revision, Patch: data.Patch})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	msg := "新资料已生成待审草案。"
	if p.Status != "pending" {
		msg = "此版本的来源已处理，审核结果为 " + strings.TrimSpace(p.Status)
	}
	writeJSON(w, 200, map[string]any{"status": p.Status, "proposal": p, "message": msg})
}

type sourceSpaceKey struct{}

// WithSourceSpace pins connector access to the space authorized by the outer router.
func WithSourceSpace(ctx context.Context, space string) context.Context {
	return context.WithValue(ctx, sourceSpaceKey{}, space)
}
