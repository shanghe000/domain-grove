package action

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"ontology-engine/internal/ontology"
)

type operation struct {
	Op           string          `json:"op"`
	Node         *ontology.Node  `json:"node,omitempty"`
	Edge         *ontology.Edge  `json:"edge,omitempty"`
	NodeID       string          `json:"node_id,omitempty"`
	TargetDomain string          `json:"target_domain,omitempty"`
	Axiom        *ontology.Axiom `json:"axiom,omitempty"`
	AxiomIndex   *int            `json:"axiom_index,omitempty"`
	SrcID        string          `json:"src_id,omitempty"`
	DstID        string          `json:"dst_id,omitempty"`
	Predicate    string          `json:"predicate,omitempty"`
	ColumnName   string          `json:"column_name,omitempty"`
	Comment      *string         `json:"comment,omitempty"`
	Confidence   string          `json:"confidence,omitempty"`
}

type editor struct {
	graph        *ontology.Graph
	nodes        map[string]ontology.Node
	edges        map[ontology.EdgeKey]ontology.Edge
	artifacts    map[string]ontology.Artifact
	removedNodes map[string]bool
	removedEdges map[ontology.EdgeKey]bool
}

func newEditor(graph *ontology.Graph, patch ontology.DraftPatch) *editor {
	e := &editor{
		graph: graph, nodes: map[string]ontology.Node{}, edges: map[ontology.EdgeKey]ontology.Edge{},
		artifacts: map[string]ontology.Artifact{}, removedNodes: map[string]bool{}, removedEdges: map[ontology.EdgeKey]bool{},
	}
	e.merge(patch)
	return e
}

func (e *editor) merge(patch ontology.DraftPatch) {
	for _, id := range patch.RemovedNodes {
		e.removeNode(id)
	}
	for _, edge := range patch.RemovedEdges {
		e.removeEdge(edge)
	}
	for _, node := range patch.Nodes {
		e.nodes[node.ID] = node
		delete(e.removedNodes, node.ID)
	}
	for _, edge := range patch.Edges {
		e.edges[edge.Key()] = edge
		delete(e.removedEdges, edge.Key())
	}
	for _, artifact := range patch.Artifacts {
		e.artifacts[artifact.NodeID] = artifact
	}
}

func (e *editor) patch() ontology.DraftPatch {
	patch := emptyPatch
	for _, id := range slices.Sorted(maps.Keys(e.nodes)) {
		patch.Nodes = append(patch.Nodes, e.nodes[id])
	}
	patch.Edges = append(patch.Edges, slices.Collect(maps.Values(e.edges))...)
	slices.SortFunc(patch.Edges, ontology.CompareEdges)
	for _, id := range slices.Sorted(maps.Keys(e.artifacts)) {
		patch.Artifacts = append(patch.Artifacts, e.artifacts[id])
	}
	patch.RemovedNodes = slices.Sorted(maps.Keys(e.removedNodes))
	patch.RemovedEdges = slices.Collect(maps.Keys(e.removedEdges))
	slices.SortFunc(patch.RemovedEdges, func(a, b ontology.EdgeKey) int {
		return ontology.CompareEdges(ontology.Edge{SrcID: a.SrcID, DstID: a.DstID, Predicate: a.Predicate},
			ontology.Edge{SrcID: b.SrcID, DstID: b.DstID, Predicate: b.Predicate})
	})
	return patch
}

func (e *editor) node(id string) (ontology.Node, bool) {
	if e.removedNodes[id] {
		return ontology.Node{}, false
	}
	if n, ok := e.nodes[id]; ok {
		return n, true
	}
	return e.graph.Node(id)
}

func (e *editor) removeNode(id string) {
	delete(e.nodes, id)
	delete(e.artifacts, id)
	if _, published := e.graph.Node(id); published {
		e.removedNodes[id] = true
	}
	for key := range e.edges {
		if key.SrcID == id || key.DstID == id {
			delete(e.edges, key)
		}
	}
}

func (e *editor) removeEdge(key ontology.EdgeKey) {
	delete(e.edges, key)
	e.removedEdges[key] = true
}

func (e *editor) apply(op operation) (string, error) {
	invalid := func(reason string) (string, error) {
		return "", problem(http.StatusUnprocessableEntity, reason)
	}
	switch op.Op {
	case "upsert_node":
		if op.Node == nil || !safeText(op.Node.Label, 160) {
			return invalid("请填写有效的节点名称。")
		}
		node, err := ontology.NormalizeNode(*op.Node)
		if err != nil {
			return invalid(err.Error())
		}
		e.nodes[node.ID] = node
		delete(e.removedNodes, node.ID)
		return node.DomainID, nil
	case "upsert_edge":
		if op.Edge == nil || !safeText(op.Edge.LabelZH, 120) {
			return invalid("缺少有效的关系。")
		}
		edge, err := ontology.NormalizeEdge(*op.Edge)
		if err != nil {
			return invalid(err.Error())
		}
		e.edges[edge.Key()] = edge
		delete(e.removedEdges, edge.Key())
		return "", nil
	case "remove_edge":
		raw := ontology.Edge{SrcID: op.SrcID, DstID: op.DstID, Predicate: op.Predicate}
		edge, err := ontology.NormalizeEdge(raw)
		if err != nil {
			return invalid(err.Error())
		}
		e.removeEdge(edge.Key())
		return "", nil
	case "update_artifact_column":
		return e.updateColumn(op)
	case "move_node_domain", "remove_node", "add_axiom", "update_axiom", "remove_axiom":
		node, exists := e.node(op.NodeID)
		if !exists {
			return invalid("操作指向的节点不存在。")
		}
		if op.Op == "remove_node" {
			e.removeNode(node.ID)
			return node.DomainID, nil
		}
		if op.Op == "move_node_domain" {
			target, ok := e.node(op.TargetDomain)
			if !ok || target.NodeType != ontology.Domain || target.Status == ontology.Deprecated || node.NodeType == ontology.Domain {
				return invalid("请选择有效的目标领域。")
			}
			node.DomainID = target.ID
		} else {
			if err := changeAxiom(&node, op); err != nil {
				return invalid(err.Error())
			}
		}
		normalized, err := ontology.NormalizeNode(node)
		if err != nil {
			return invalid(err.Error())
		}
		e.nodes[node.ID] = normalized
		return node.DomainID, nil
	default:
		return invalid("不支持的草稿操作。")
	}
}

func changeAxiom(node *ontology.Node, op operation) error {
	if node.NodeType != ontology.Concept && node.NodeType != ontology.Metric {
		return fmt.Errorf("只能编辑业务概念和指标的公理。")
	}
	node.Axioms = slices.Clone(node.Axioms)
	if op.Op != "remove_axiom" && (op.Axiom == nil || !safeText(op.Axiom.Rule, 2000)) {
		return fmt.Errorf("请填写公理类型与规则。")
	}
	if op.Op == "add_axiom" {
		if len(node.Axioms) >= 64 {
			return fmt.Errorf("每个节点最多保留 64 条公理。")
		}
		node.Axioms = append(node.Axioms, *op.Axiom)
		return nil
	}
	if op.AxiomIndex == nil || *op.AxiomIndex < 0 || *op.AxiomIndex >= len(node.Axioms) {
		return fmt.Errorf("请重新选择需要编辑的公理。")
	}
	index := *op.AxiomIndex
	if op.Op == "remove_axiom" {
		node.Axioms = slices.Delete(node.Axioms, index, index+1)
	} else {
		node.Axioms[index] = *op.Axiom
	}
	return nil
}

func (e *editor) updateColumn(op operation) (string, error) {
	node, exists := e.node(op.NodeID)
	if !exists || node.NodeType != ontology.ArtifactNode || node.Status == ontology.Deprecated {
		return "", problem(http.StatusUnprocessableEntity, "只能校正仍在使用的 Excel 字段。")
	}
	a, ok := e.artifacts[op.NodeID]
	if !ok {
		a, ok = e.graph.Artifact(op.NodeID)
	}
	if !ok || a.ArtifactType != ontology.DuckDBTable {
		return "", problem(http.StatusUnprocessableEntity, "只能校正 Excel 字段。")
	}
	if (op.Comment != nil && !safeText(*op.Comment, 1000)) ||
		(op.Confidence != "" && op.Confidence != "low" && op.Confidence != "high") {
		return "", problem(http.StatusUnprocessableEntity, "请提供有效的字段说明或置信度。")
	}
	columns, _ := a.SchemaMeta["columns"].([]any)
	for index, raw := range columns {
		column, _ := raw.(map[string]any)
		if column["name"] != op.ColumnName {
			continue
		}
		newColumn := maps.Clone(column)
		if op.Comment != nil {
			newColumn["comment"] = strings.TrimSpace(*op.Comment)
		}
		if op.Confidence != "" {
			newColumn["confidence"] = op.Confidence
		}
		result := slices.Clone(columns)
		result[index] = newColumn
		a.SchemaMeta = maps.Clone(a.SchemaMeta)
		a.SchemaMeta["columns"] = result
		e.artifacts[a.NodeID] = a
		return "", nil
	}
	return "", problem(http.StatusNotFound, "没有找到指定的草稿字段。")
}

func approvedSources(sources []source, artifacts []ontology.Artifact) []source {
	key := func(nodeID, kind string) string { return nodeID + "/" + kind }
	allowed := make(map[string]bool, len(artifacts))
	for _, artifact := range artifacts {
		allowed[key(artifact.NodeID, artifact.ArtifactType)] = true
	}
	last := make(map[string]source, len(sources))
	for _, entry := range sources {
		if k := key(entry.NodeID, entry.Type); allowed[k] {
			last[k] = entry
		}
	}
	result := []source{}
	for _, k := range slices.Sorted(maps.Keys(last)) {
		result = append(result, last[k])
	}
	return result
}
