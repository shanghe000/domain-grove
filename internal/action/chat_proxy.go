package action

import (
	"context"
	"net/http"
	"strings"
	"time"
)

const maxChatBody = 23 << 20

func (s *Service) chat(w http.ResponseWriter, r *http.Request) error {
	var input chatInput
	if err := decode(w, r, maxChatBody, &input); err != nil {
		return err
	}
	if (strings.TrimSpace(input.Message) == "" && len(input.Files) == 0) ||
		!safeText(input.Message, 12000) || len(input.Files) > 2 {
		return problem(http.StatusBadRequest, "请填写消息或添加最多两个 .xlsx 文件。")
	}
	identity := user(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 75*time.Second)
	defer cancel()
	release, err := s.lockIngestion(ctx)
	if err != nil {
		return workerProblem(http.StatusGatewayTimeout, "同一时间有其他资料正在处理，请稍后重试。")
	}
	defer release()

	s.writes.Lock()
	item, err := s.editableDraft(ctx, identity, input.SessionID, true)
	if err == nil {
		err = s.currentDraft(item)
	}
	s.writes.Unlock()
	if err != nil {
		return err
	}
	input.SessionID = item.ID
	graph := s.Graph()
	result, err := s.invokeChat(ctx, identity.UserID, workerChat{
		chatInput: input, Skeleton: graph.Skeleton(""), Draft: item.Patch, Artifacts: publishedArtifacts(graph, ""),
	})
	if err != nil {
		return err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	latest, err := s.findDraft(ctx, identity, item.ID)
	if err != nil {
		return err
	}
	if item.fresh && latest == nil {
		latest = item
	} else if item.fresh || latest == nil || latest.Status != "staging" || latest.UpdatedAt != item.UpdatedAt {
		return problem(http.StatusConflict, "处理期间草稿已被其他窗口修改。请核对草稿后重试。")
	}
	if err := s.currentDraft(latest); err != nil {
		return err
	}
	changed := hasChanges(result.Patch) || len(result.Operations) > 0
	if changed {
		latest.addHistory(identity, "governance_ai", result.Operations)
		editor := newEditor(graph, latest.Patch)
		editor.merge(result.Patch)
		if result.Focus != "" {
			latest.Payload.Focus = result.Focus
		}
		for _, op := range result.Operations {
			domain, err := editor.apply(op)
			if err != nil {
				return err
			}
			if domain != "" {
				latest.Payload.Focus = domain
			}
		}
		latest.Patch = editor.patch()
		latest.Payload.Sources = approvedSources(append(latest.Payload.Sources, result.Staging.Sources...), latest.Patch.Artifacts)
		latest.SourceType = sourceKind(latest.Payload.Sources, input)
	}
	if changed || latest.fresh {
		if err := s.persistDraft(ctx, latest, changed); err != nil {
			return err
		}
	}
	if changed {
		s.events.draft(latest)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": latest.ID, "message": result.Message, "draft": latest,
		"events": result.Events, "hot_updates": result.HotUpdates,
	})
	return nil
}

func sourceKind(sources []source, input chatInput) string {
	kind := ""
	for _, source := range sources {
		if kind != "" && kind != source.Type {
			return "mixed"
		}
		kind = source.Type
	}
	switch kind {
	case "feishu_md":
		return "feishu_doc"
	case "tracking_api":
		return "tracking_version"
	case "duckdb_table":
		if len(input.Files) > 0 {
			return "excel_upload"
		}
		return "config_url"
	default:
		return "mixed"
	}
}
