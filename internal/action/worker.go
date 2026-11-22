package action

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ontology-engine/internal/ontology"
)

func (s *Service) workerRequest(ctx context.Context, method, endpoint string, payload any) (*http.Response, error) {
	var reader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	r, err := http.NewRequestWithContext(ctx, method, s.config.WorkerURL+endpoint, reader)
	if err != nil {
		return nil, err
	}
	if reader != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if s.config.WorkerToken != "" {
		r.Header.Set("Authorization", "Bearer "+s.config.WorkerToken)
	}
	response, err := s.client.Do(r)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, problem(http.StatusGatewayTimeout, "资料处理超时，请拆分后重试。")
		}
		return nil, problem(http.StatusServiceUnavailable, "解析 Worker 暂时不可用。")
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		var result struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result)
		return nil, workerProblem(response.StatusCode, result.Error)
	}
	return response, nil
}

func workerProblem(status int, message string) error {
	if strings.TrimSpace(message) == "" || !safeText(message, 1000) {
		message = "解析 Worker 未能完成请求。"
	}
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	return problem(status, message)
}

func (s *Service) workerJSON(ctx context.Context, timeout time.Duration, endpoint string, payload, result any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, err := s.workerRequest(ctx, http.MethodPost, endpoint, payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(response.Body, 24<<20))
	if err := decoder.Decode(result); err != nil {
		return workerProblem(http.StatusBadGateway, "")
	}
	return nil
}

func (s *Service) WorkerHealth(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r, err := s.workerRequest(ctx, http.MethodGet, "/health", nil)
	if err == nil {
		r.Body.Close()
	}
	return err
}

type uploadedFile struct {
	Name    string `json:"name"`
	Content string `json:"content_base64"`
}

type chatInput struct {
	sessionInput
	Message string         `json:"message"`
	Files   []uploadedFile `json:"uploaded_files"`
}

type workerChat struct {
	chatInput
	Skeleton  ontology.Skeleton   `json:"skeleton"`
	Draft     ontology.DraftPatch `json:"draft_patch"`
	Artifacts []ontology.Artifact `json:"artifacts"`
}

type progressEvent struct {
	Tool   string `json:"tool"`
	Status string `json:"status"`
	Text   string `json:"text"`
}

type chatResult struct {
	Message    string              `json:"message"`
	Patch      ontology.DraftPatch `json:"draft_patch"`
	Operations []operation         `json:"operations"`
	Staging    draftPayload        `json:"staging_payload"`
	Events     []progressEvent     `json:"events"`
	Focus      string              `json:"focus_domain_id"`
	HotUpdates []map[string]string `json:"hot_updates"`
}

func (s *Service) invokeChat(ctx context.Context, owner string, chat workerChat) (chatResult, error) {
	response, err := s.workerRequest(ctx, http.MethodPost, "/chat/stream", chat)
	if err != nil {
		return chatResult{}, err
	}
	defer response.Body.Close()
	lines := bufio.NewScanner(response.Body)
	lines.Buffer(make([]byte, 4096), 8<<20)
	for lines.Scan() {
		var record struct {
			Event   string          `json:"event"`
			Result  chatResult      `json:"result"`
			Message string          `json:"message"`
			Status  json.RawMessage `json:"status"`
			Tool    string          `json:"tool"`
			Text    string          `json:"text"`
		}
		if err := json.Unmarshal(lines.Bytes(), &record); err != nil {
			return chatResult{}, workerProblem(http.StatusBadGateway, "")
		}
		switch record.Event {
		case "progress":
			var status string
			_ = json.Unmarshal(record.Status, &status)
			s.events.publish("chat_progress", owner, map[string]any{
				"session_id": chat.SessionID, "tool": record.Tool, "status": status, "text": record.Text,
			})
		case "result":
			return record.Result, nil
		case "error":
			var status int
			_ = json.Unmarshal(record.Status, &status)
			return chatResult{}, workerProblem(status, record.Message)
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return chatResult{}, workerProblem(http.StatusGatewayTimeout, "资料处理超时，请拆分后重试。")
	}
	return chatResult{}, workerProblem(http.StatusBadGateway, "资料解析连接提前结束，请重试。")
}

func (s *Service) QuerySQL(ctx context.Context, statement string) (json.RawMessage, error) {
	if strings.TrimSpace(statement) == "" || !safeText(statement, 16000) {
		return nil, problem(http.StatusBadRequest, "请提供不超过 16,000 字的查询。")
	}
	var result json.RawMessage
	err := s.workerJSON(ctx, 7*time.Second, "/query_config_sql", map[string]any{
		"sql": statement, "artifacts": publishedArtifacts(s.Graph(), ontology.DuckDBTable),
	}, &result)
	return result, err
}

func (s *Service) Tracking(ctx context.Context, version string, eventNames []string) (json.RawMessage, error) {
	if strings.TrimSpace(version) == "" || !safeText(version, 64) || len(eventNames) > 100 {
		return nil, problem(http.StatusBadRequest, "请提供埋点版本和最多 100 个事件名称。")
	}
	var result json.RawMessage
	err := s.workerJSON(ctx, 20*time.Second, "/query_tracking_events", map[string]any{
		"version": version, "event_names": eventNames,
	}, &result)
	return result, err
}

func publishedArtifacts(graph *ontology.Graph, kind string) []ontology.Artifact {
	result := []ontology.Artifact{}
	for _, a := range graph.Data().Artifacts {
		node, ok := graph.Node(a.NodeID)
		if ok && node.Status != ontology.Deprecated && (kind == "" || a.ArtifactType == kind) {
			result = append(result, a)
		}
	}
	return result
}

func (s *Service) download(w http.ResponseWriter, r *http.Request) error {
	g := s.Graph()
	id := r.PathValue("node_id")
	a, found := g.Artifact(id)
	n, _ := g.Node(id)
	if !found || a.ArtifactType != ontology.DuckDBTable || n.Status == ontology.Deprecated {
		return problem(http.StatusNotFound, "找不到已发布的配置表。")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	result, err := s.workerRequest(ctx, http.MethodPost, "/download_config", map[string]any{"artifact": a})
	if err != nil {
		return err
	}
	defer result.Body.Close()
	const maxFile = 40 << 20
	data, err := io.ReadAll(io.LimitReader(result.Body, maxFile+1))
	if err != nil || len(data) > maxFile {
		return problem(http.StatusBadGateway, "下载文件不可用或超过上限。")
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", id+".xlsx"))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
	return nil
}
