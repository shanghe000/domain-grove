package action

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"unicode/utf8"

	"ontology-engine/internal/auth"
	"ontology-engine/internal/ontology"
	"ontology-engine/internal/storage"
)

type source struct {
	NodeID  string `json:"node_id"`
	Type    string `json:"type"`
	StageID string `json:"stage_id"`
}

type draftPayload struct {
	Owner            string   `json:"owner"`
	Base             string   `json:"base_version"`
	Focus            string   `json:"focus_domain_id"`
	Sources          []source `json:"sources"`
	CommittedVersion string   `json:"committed_version,omitempty"`
}

type historyEntry struct {
	Actor      string              `json:"actor"`
	UserID     string              `json:"user_id"`
	Username   string              `json:"username"`
	Dept       string              `json:"dept"`
	At         string              `json:"at"`
	Operations []operation         `json:"operations,omitempty"`
	Before     ontology.DraftPatch `json:"before"`
	Payload    draftPayload        `json:"payload"`
	SourceType string              `json:"source_type"`
}

type draft struct {
	ID          string              `json:"session_id"`
	CreatedBy   string              `json:"created_by"`
	Status      string              `json:"status"`
	SourceType  string              `json:"source_type"`
	Patch       ontology.DraftPatch `json:"draft_patch"`
	Focus       string              `json:"focus_domain_id"`
	CreatedAt   string              `json:"created_at"`
	UpdatedAt   string              `json:"updated_at"`
	UndoCount   int                 `json:"undo_count"`
	BaseVersion string              `json:"base_version"`
	Payload     draftPayload        `json:"-"`
	History     []historyEntry      `json:"-"`
	fresh       bool
}

type sessionInput struct {
	SessionID string `json:"session_id"`
}

type patchRequest struct {
	sessionInput
	Actor      string      `json:"actor"`
	Operations []operation `json:"operations"`
}

var emptyPatch = ontology.DraftPatch{Nodes: []ontology.Node{}, Edges: []ontology.Edge{}, Artifacts: []ontology.Artifact{}}

const selectDraft = `SELECT id, created_by, status, source_type, staging_payload, draft_patch_json,
	ops_history_json, cast(created_at AS TEXT), cast(updated_at AS TEXT) FROM draft_sessions`

func (s *Service) findDraft(ctx context.Context, identity *auth.UserIdentity, id string) (*draft, error) {
	var row *sql.Row
	if id == "" {
		row = s.store.DB.QueryRowContext(ctx, selectDraft+` WHERE json_extract(staging_payload,'$.owner')=?
			AND status='staging' ORDER BY updated_at DESC LIMIT 1`, identity.UserID)
	} else {
		if !ontology.ValidNodeID(id) {
			return nil, problem(http.StatusBadRequest, "草稿会话标识无效。")
		}
		row = s.store.DB.QueryRowContext(ctx, selectDraft+" WHERE id=?", id)
	}
	var result draft
	var payload, patch, history []byte
	err := row.Scan(&result.ID, &result.CreatedBy, &result.Status, &result.SourceType, &payload, &patch, &history,
		&result.CreatedAt, &result.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload, &result.Payload); err != nil {
		return nil, err
	}
	if result.Payload.Owner != identity.UserID {
		return nil, problem(http.StatusNotFound, "没有找到你的草稿会话。")
	}
	if err := json.Unmarshal(patch, &result.Patch); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(history, &result.History); err != nil {
		return nil, err
	}
	result.refresh()
	return &result, nil
}

func (d *draft) refresh() {
	d.Focus = d.Payload.Focus
	d.UndoCount = len(d.History)
	d.BaseVersion = d.Payload.Base
}

func (s *Service) editableDraft(ctx context.Context, identity *auth.UserIdentity, id string, create bool) (*draft, error) {
	item, err := s.findDraft(ctx, identity, id)
	if err != nil {
		return nil, err
	}
	_, version := s.snapshot()
	if item == nil {
		if !create {
			return nil, problem(http.StatusNotFound, "没有可操作的草稿。")
		}
		if id == "" {
			id = "draft_" + newID()
		}
		at := timestamp()
		item = &draft{ID: id, CreatedBy: identity.Username, Status: "staging", SourceType: "mixed",
			Patch: emptyPatch, CreatedAt: at, UpdatedAt: at, History: []historyEntry{},
			Payload: draftPayload{Owner: identity.UserID, Base: version, Sources: []source{}},
			fresh:   true,
		}
		item.refresh()
	}
	if item.Status != "staging" {
		return nil, problem(http.StatusConflict, "草稿已经结束，请发起新的会话。")
	}
	return item, nil
}

func (s *Service) currentDraft(item *draft) error {
	_, version := s.snapshot()
	if item.Payload.Base != version {
		return problem(http.StatusConflict, "发布图谱已更新。请核对新版本并重新创建草稿。")
	}
	return nil
}

func saveDraft(ctx context.Context, tx *sql.Tx, d *draft) error {
	d.UpdatedAt = timestamp()
	d.refresh()
	payload, pErr := json.Marshal(d.Payload)
	patch, gErr := json.Marshal(d.Patch)
	history, hErr := json.Marshal(d.History)
	if err := errors.Join(pErr, gErr, hErr); err != nil {
		return err
	}
	if d.fresh {
		_, err := tx.ExecContext(ctx, `INSERT INTO draft_sessions
			(id,created_by,status,source_type,staging_payload,draft_patch_json,ops_history_json,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?)`, d.ID, d.CreatedBy, d.Status, d.SourceType,
			string(payload), string(patch), string(history), d.CreatedAt, d.UpdatedAt)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE draft_sessions SET status=?, source_type=?, staging_payload=?,
		draft_patch_json=?, ops_history_json=?, updated_at=? WHERE id=?`, d.Status, d.SourceType,
		string(payload), string(patch), string(history), d.UpdatedAt, d.ID)
	return err
}

func (s *Service) persistDraft(ctx context.Context, d *draft, validate bool) error {
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if validate {
		if _, err := tx.ExecContext(ctx, "SAVEPOINT verify_draft"); err != nil {
			return err
		}
		if err := storage.ApplyPatch(ctx, tx, d.Patch); err != nil {
			return problem(http.StatusUnprocessableEntity, "修改会留下无效的节点、领域或关系。"+err.Error())
		}
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO verify_draft"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "RELEASE verify_draft"); err != nil {
			return err
		}
	}
	if err := saveDraft(ctx, tx, d); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	d.fresh = false
	return nil
}

func (d *draft) addHistory(identity *auth.UserIdentity, actor string, operations []operation) {
	saved := historyEntry{Actor: actor, UserID: identity.UserID, Username: identity.Username,
		At: timestamp(), Operations: operations, Before: d.Patch, Payload: d.Payload, SourceType: d.SourceType}
	d.History = append(d.History, saved)
	if len(d.History) > 30 {
		d.History = d.History[1:]
	}
}

func (s *Service) patch(w http.ResponseWriter, r *http.Request) error {
	var input patchRequest
	if err := decode(w, r, 512<<10, &input); err != nil {
		return err
	}
	if (input.Actor != "" && input.Actor != "human" && input.Actor != "governance_ai") ||
		len(input.Operations) == 0 || len(input.Operations) > 64 {
		return problem(http.StatusBadRequest, "请提供 1 至 64 项有效的草稿操作。")
	}
	if input.Actor == "" {
		input.Actor = "human"
	}
	ctx := r.Context()
	s.writes.Lock()
	defer s.writes.Unlock()
	item, err := s.editableDraft(ctx, user(r), input.SessionID, true)
	if err != nil {
		return err
	}
	if err := s.currentDraft(item); err != nil {
		return err
	}
	item.addHistory(user(r), input.Actor, input.Operations)
	editor := newEditor(s.Graph(), item.Patch)
	focus := item.Payload.Focus
	for _, op := range input.Operations {
		domain, err := editor.apply(op)
		if err != nil {
			return err
		}
		if domain != "" {
			focus = domain
		}
	}
	item.Patch, item.Payload.Focus = editor.patch(), focus
	item.Payload.Sources = approvedSources(item.Payload.Sources, item.Patch.Artifacts)
	if err := s.persistDraft(ctx, item, true); err != nil {
		return err
	}
	s.events.draft(item)
	writeJSON(w, http.StatusOK, item)
	return nil
}

func (s *Service) undo(w http.ResponseWriter, r *http.Request) error {
	var input sessionInput
	if err := decode(w, r, 4096, &input); err != nil {
		return err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	ctx := r.Context()
	item, err := s.editableDraft(ctx, user(r), input.SessionID, false)
	if err != nil {
		return err
	}
	if len(item.History) == 0 {
		return problem(http.StatusConflict, "草稿没有可撤销的操作。")
	}
	last := item.History[len(item.History)-1]
	item.Patch, item.Payload, item.SourceType = last.Before, last.Payload, last.SourceType
	item.History = item.History[:len(item.History)-1]
	if err := s.persistDraft(ctx, item, false); err != nil {
		return err
	}
	s.events.draft(item)
	writeJSON(w, http.StatusOK, item)
	return nil
}

func (s *Service) discard(w http.ResponseWriter, r *http.Request) error {
	var input sessionInput
	if err := decode(w, r, 4096, &input); err != nil {
		return err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	item, err := s.editableDraft(r.Context(), user(r), input.SessionID, false)
	if err != nil {
		return err
	}
	item.Status = "discarded"
	if err := s.persistDraft(r.Context(), item, false); err != nil {
		return err
	}
	s.events.draft(item)
	writeJSON(w, http.StatusOK, item)
	return nil
}

func hasChanges(p ontology.DraftPatch) bool {
	return len(p.Nodes)+len(p.Edges)+len(p.Artifacts)+len(p.RemovedNodes)+len(p.RemovedEdges) != 0
}

func safeText(value string, length int) bool {
	return utf8.RuneCountInString(value) <= length
}
