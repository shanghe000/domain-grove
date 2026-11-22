package action

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"ontology-engine/internal/ontology"
	"ontology-engine/internal/storage"
)

var revisionPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (s *Service) commit(w http.ResponseWriter, r *http.Request) error {
	var input sessionInput
	if err := decode(w, r, 4096, &input); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 50*time.Second)
	defer cancel()
	release, err := s.lockIngestion(ctx)
	if err != nil {
		return workerProblem(http.StatusGatewayTimeout, "正在处理其他资料，请稍后重试发布。")
	}
	defer release()
	s.writes.Lock()
	defer s.writes.Unlock()
	item, err := s.findDraft(ctx, user(r), input.SessionID)
	if err != nil {
		return err
	}
	if item == nil {
		return problem(http.StatusNotFound, "没有找到你的草稿。")
	}
	if item.Status == "committed" {
		version := item.Payload.CommittedVersion
		if version == "" {
			version = item.UpdatedAt
		}
		pending := exportPending(s.refreshExports(s.Graph()))
		writeJSON(w, http.StatusOK, map[string]any{"draft": item, "version_timestamp": version, "snapshot_pending": pending})
		return nil
	}
	if item.Status != "staging" || !hasChanges(item.Patch) {
		return problem(http.StatusConflict, "草稿没有可以发布的变更。")
	}
	if err := s.currentDraft(item); err != nil {
		return err
	}
	// Stage immutable bytes first. The previous graph only references its old versions;
	// failed SQLite commits cannot expose any of the new versions.
	if len(item.Patch.Artifacts) != 0 {
		artifacts, err := s.materialize(ctx, item)
		if err != nil {
			return err
		}
		item.Patch.Artifacts = artifacts
	}

	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := storage.ApplyPatch(ctx, tx, item.Patch); err != nil {
		return problem(http.StatusUnprocessableEntity, err.Error())
	}
	graph, err := storage.LoadGraph(ctx, tx)
	if err != nil {
		return err
	}
	version := timestamp()
	if _, err := tx.ExecContext(ctx, `INSERT INTO engine_metadata(key,value) VALUES('graph_revision',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, version); err != nil {
		return err
	}
	item.Status = "committed"
	item.Payload.CommittedVersion = version
	if err := saveDraft(ctx, tx, item); err != nil {
		return err
	}
	snapshot, err := s.prepareSnapshot(graph)
	if err != nil {
		return err
	}
	defer os.Remove(snapshot)
	s.graphMu.Lock()
	if err := tx.Commit(); err != nil {
		s.graphMu.Unlock()
		return err
	}
	s.graph, s.version = graph, version
	s.graphMu.Unlock()

	exportErr := s.reconcileDocuments(graph)
	if exportErr == nil {
		exportErr = os.Rename(snapshot, filepath.Join(s.config.DataDir, "ontology.yaml"))
	}
	pending := exportPending(exportErr)
	s.events.draft(item)
	s.events.publish("graph_committed", "", map[string]any{
		"version_timestamp": version, "committed_by": user(r).Username,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"draft": item, "version_timestamp": version, "snapshot_pending": pending,
	})
	return nil
}

func (s *Service) materialize(ctx context.Context, item *draft) ([]ontology.Artifact, error) {
	var response struct {
		Artifacts []ontology.Artifact `json:"artifacts"`
	}
	err := s.workerJSON(ctx, 27*time.Second, "/commit", map[string]any{
		"session_id": item.ID, "artifacts": item.Patch.Artifacts, "staging_payload": item.Payload,
	}, &response)
	if err != nil {
		return nil, err
	}
	expected := make(map[string]string, len(item.Patch.Artifacts))
	for _, a := range item.Patch.Artifacts {
		expected[a.NodeID] = a.ArtifactType
	}
	if len(response.Artifacts) != len(expected) {
		return nil, workerProblem(http.StatusBadGateway, "原件发布结果与草稿不一致。")
	}
	for _, a := range response.Artifacts {
		kind, exists := expected[a.NodeID]
		if !exists || kind != a.ArtifactType {
			return nil, workerProblem(http.StatusBadGateway, "原件发布结果与草稿不一致。")
		}
		delete(expected, a.NodeID)
		if a.ArtifactType == ontology.FeishuMarkdown {
			if _, err := s.document(a); err != nil {
				return nil, workerProblem(http.StatusBadGateway, "已准备的策划案不可读，请重新导入。")
			}
		}
	}
	return response.Artifacts, nil
}

func (s *Service) prepareSnapshot(graph *ontology.Graph) (string, error) {
	file, err := os.CreateTemp(s.config.DataDir, ".pending-ontology-*.yaml")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err = ontology.WriteSnapshot(name, graph); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func (s *Service) refreshExports(graph *ontology.Graph) error {
	if err := s.reconcileDocuments(graph); err != nil {
		return err
	}
	return ontology.WriteSnapshot(filepath.Join(s.config.DataDir, "ontology.yaml"), graph)
}

func exportPending(err error) bool {
	if err != nil {
		slog.Error("Regenerating published files failed; SQLite remains authoritative", "type", fmt.Sprintf("%T", err))
	}
	return err != nil
}

func (s *Service) reconcileDocuments(graph *ontology.Graph) error {
	active := publishedArtifacts(graph, ontology.FeishuMarkdown)
	if err := s.exportDocuments(active); err != nil {
		return err
	}
	root, err := os.OpenRoot(s.config.DataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := root.Open("docs")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	keep := make(map[string]bool, len(active))
	for _, artifact := range active {
		keep[artifact.PhysicalRef] = true
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join("docs", name)
		if filepath.Ext(name) != ".md" || keep[path] || !entry.Type().IsRegular() {
			continue
		}
		id := name[:len(name)-len(".md")]
		if !ontology.ValidNodeID(id) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		// Versioned aliases are generated read-only. Unversioned source files are
		// kept because they can be the only bytes for a legacy artifact.
		if info.Mode().Perm() != 0o440 {
			continue
		}
		revisions, err := root.Stat(filepath.Join("docs", "revisions", id))
		if errors.Is(err, os.ErrNotExist) || (err == nil && !revisions.IsDir()) {
			continue
		}
		if err != nil {
			return err
		}
		if err := root.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Service) exportDocuments(artifacts []ontology.Artifact) error {
	for _, a := range artifacts {
		if a.ArtifactType != ontology.FeishuMarkdown || a.SchemaMeta["content_revision"] == nil {
			continue
		}
		canonical, err := ontology.NormalizeArtifact(a)
		if err != nil {
			return err
		}
		data, err := s.document(canonical)
		if err != nil {
			return err
		}
		path := filepath.Join(s.config.DataDir, canonical.PhysicalRef)
		if value, err := os.ReadFile(path); err == nil && bytes.Equal(value, data) {
			continue
		}
		file, err := os.CreateTemp(filepath.Dir(path), ".doc-*.md")
		if err != nil {
			return err
		}
		name := file.Name()
		_, writeErr := file.Write(data)
		err = errors.Join(writeErr, file.Chmod(0o440), file.Sync(), file.Close())
		if err == nil {
			err = os.Rename(name, path)
		}
		if err != nil {
			_ = os.Remove(name)
			return err
		}
	}
	return nil
}

func (s *Service) document(a ontology.Artifact) ([]byte, error) {
	a, err := ontology.NormalizeArtifact(a)
	if err != nil || a.ArtifactType != ontology.FeishuMarkdown {
		return nil, errors.New("invalid Markdown artifact")
	}
	path := a.PhysicalRef
	if value, exists := a.SchemaMeta["content_revision"]; exists {
		revision, ok := value.(string)
		if !ok || !revisionPattern.MatchString(revision) {
			return nil, errors.New("invalid Markdown document revision")
		}
		path = filepath.Join("docs", "revisions", a.NodeID, revision+".md")
	}
	root, err := os.OpenRoot(s.config.DataDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("document is not a regular file")
	}
	const documentLimit = 8 << 20
	data, err := io.ReadAll(io.LimitReader(file, documentLimit+1))
	if err != nil || len(data) > documentLimit {
		return nil, errors.New("published document cannot be read")
	}
	return data, nil
}

func (s *Service) ReadDoc(ctx context.Context, nodeID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	graph := s.Graph()
	a, found := graph.Artifact(nodeID)
	n, _ := graph.Node(nodeID)
	if !found || a.ArtifactType != ontology.FeishuMarkdown || n.Status == ontology.Deprecated {
		return "", problem(http.StatusNotFound, "没有找到该节点的已发布策划案。")
	}
	data, err := s.document(a)
	if err != nil {
		return "", problem(http.StatusInternalServerError, "已发布策划案暂时不可读。")
	}
	return string(data), nil
}

func (s *Service) readDocument(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("node_id")
	text, err := s.ReadDoc(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"node_id": id, "markdown": text})
	return nil
}
