package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"ontology-engine/internal/ontology"
)

const seedDoc = `---
node_id: Artifact_RetentionSpec
title: 留存分析示例
feishu_url: ""
updated_at: 2024-01-01
---

# 留存分析示例

该文档为虚构样例。以账号首次活跃日为第 0 日，次日仍活跃的同批账号数除以第 0 日账号数，得到次日留存率。

## 排查顺序

1. 核对账号首次活跃与回访的采集口径。
2. 对比同期版本、实验分流及活动投放。
3. 根据活动发奖条件核对奖励组，并标记还需要查验的邮件系统。
`

func (s *Store) Seed(ctx context.Context, dataDir string, includeDemo bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var initialized int
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM engine_metadata WHERE key='retention_seed')
		OR EXISTS(SELECT 1 FROM ontology_nodes)`).Scan(&initialized)
	if err != nil {
		return err
	}
	if initialized != 0 {
		return tx.Commit()
	}

	patch := retentionSeed(includeDemo)
	if err := ApplyPatch(ctx, tx, patch); err != nil {
		return err
	}
	if includeDemo {
		if err := writeSeedDoc(filepath.Join(dataDir, "docs", "Artifact_RetentionSpec.md")); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO engine_metadata (key, value) VALUES ('retention_seed', '1')`); err != nil {
		return err
	}
	return tx.Commit()
}

func writeSeedDoc(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(seedDoc)
	err = errors.Join(writeErr, file.Sync(), file.Close())
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

func retentionSeed(includeDemo bool) ontology.DraftPatch {
	patch := ontology.DraftPatch{
		Nodes: []ontology.Node{
			{ID: "Domain_BI", NodeType: ontology.Domain, Label: "经营分析", Properties: map[string]any{"description": "指标口径、埋点与分析手册"}},
			{ID: "Domain_LiveOps", NodeType: ontology.Domain, Label: "活动运营", Properties: map[string]any{"description": "活动、奖励与发放条件"}},
			{ID: "Metric_Retention", DomainID: "Domain_BI", NodeType: ontology.Metric, Label: "留存率",
				Properties: map[string]any{"aliases": []string{"留存", "次留", "7日留存"}, "formula": "第 N 日回访的入组账号数 / 第 0 日入组账号数"},
				Axioms: []ontology.Axiom{
					{Type: "Mandatory_Check", Rule: "分析留存波动时同步排查版本、实验和运营活动"},
					{Type: "Pitfall", Rule: "账号首次活跃与角色创建使用不同统计口径"},
				}},
			{ID: "Concept_Activity", DomainID: "Domain_LiveOps", NodeType: ontology.Concept, Label: "运营活动",
				Axioms: []ontology.Axiom{{Type: "Cross_Validation", Rule: "核对策划案时间窗口与实际生效配置"}}},
			{ID: "Concept_RewardGroup", DomainID: "Domain_LiveOps", NodeType: ontology.Concept, Label: "奖励组",
				Axioms: []ontology.Axiom{{Type: "Pruning_Condition", Rule: "活动时间与入组日期无交集时停止核对"}}},
			{ID: "Concept_MailTemplate", DomainID: "Domain_LiveOps", NodeType: ontology.Concept, Label: "邮件模板"},
			{ID: "Concept_MailSystem", DomainID: "Domain_LiveOps", NodeType: ontology.Concept, Label: "邮件发奖"},
			{ID: "Concept_AnalysisPlaybook", DomainID: "Domain_BI", NodeType: ontology.Concept, Label: "指标分析手册"},
		},
		Edges: []ontology.Edge{
			{SrcID: "Metric_Retention", DstID: "Concept_Activity", Predicate: "impacted_by"},
			{SrcID: "Concept_Activity", DstID: "Concept_RewardGroup", Predicate: "impacted_by", LabelZH: "受发奖条件影响"},
			{SrcID: "Concept_RewardGroup", DstID: "Concept_MailTemplate", Predicate: "foreign_key_to", Metadata: map[string]any{"join_clause": "reward_group.mail_template_id = mail.template_id"}},
			{SrcID: "Concept_MailTemplate", DstID: "Concept_MailSystem", Predicate: "configured_by"},
			{SrcID: "Concept_AnalysisPlaybook", DstID: "Metric_Retention", Predicate: "depends_on"},
		},
	}
	if includeDemo {
		patch.Nodes = append(patch.Nodes, ontology.Node{ID: "Artifact_RetentionSpec", DomainID: "Domain_BI", NodeType: ontology.ArtifactNode, Label: "留存分析示例"})
		patch.Edges = append(patch.Edges, ontology.Edge{SrcID: "Metric_Retention", DstID: "Artifact_RetentionSpec", Predicate: "defined_in"})
		patch.Artifacts = []ontology.Artifact{
			{NodeID: "Artifact_RetentionSpec", ArtifactType: ontology.FeishuMarkdown, ReadMode: "full_text", PhysicalRef: "docs/Artifact_RetentionSpec.md"},
		}
	}
	return patch
}
