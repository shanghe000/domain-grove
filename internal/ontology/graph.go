package ontology

import (
	"cmp"
	"maps"
	"slices"
)

// Graph is a published snapshot. Its data and indexes are read-only after construction.
type Graph struct {
	data      DraftPatch
	nodes     map[string]Node
	artifacts map[string]Artifact
	out       map[string][]Edge
	in        map[string][]Edge
}

func NewGraph(nodes []Node, edges []Edge, artifacts []Artifact) *Graph {
	graph := &Graph{
		data: DraftPatch{
			Nodes: append([]Node{}, nodes...), Edges: append([]Edge{}, edges...),
			Artifacts: append([]Artifact{}, artifacts...),
		},
		nodes: make(map[string]Node, len(nodes)), artifacts: make(map[string]Artifact, len(artifacts)),
		out: make(map[string][]Edge), in: make(map[string][]Edge),
	}
	slices.SortFunc(graph.data.Nodes, func(a, b Node) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(graph.data.Edges, CompareEdges)
	slices.SortFunc(graph.data.Artifacts, func(a, b Artifact) int { return cmp.Compare(a.NodeID, b.NodeID) })
	for i, node := range graph.data.Nodes {
		node = cloneNode(node)
		graph.data.Nodes[i] = node
		graph.nodes[node.ID] = node
	}
	for i, edge := range graph.data.Edges {
		edge.Metadata = cloneJSONObject(edge.Metadata)
		graph.data.Edges[i] = edge
		graph.out[edge.SrcID] = append(graph.out[edge.SrcID], edge)
		graph.in[edge.DstID] = append(graph.in[edge.DstID], edge)
	}
	for i, artifact := range graph.data.Artifacts {
		if node, present := graph.nodes[artifact.NodeID]; present {
			artifact.Label = node.Label
		}
		artifact.SchemaMeta = cloneJSONObject(artifact.SchemaMeta)
		graph.data.Artifacts[i] = artifact
		graph.artifacts[artifact.NodeID] = artifact
	}
	return graph
}

func CompareEdges(a, b Edge) int {
	return cmp.Or(cmp.Compare(a.SrcID, b.SrcID), cmp.Compare(a.DstID, b.DstID), cmp.Compare(a.Predicate, b.Predicate))
}

func (g *Graph) Data() DraftPatch {
	out := DraftPatch{
		Nodes: make([]Node, len(g.data.Nodes)), Edges: make([]Edge, len(g.data.Edges)),
		Artifacts: make([]Artifact, len(g.data.Artifacts)),
	}
	for i, node := range g.data.Nodes {
		out.Nodes[i] = cloneNode(node)
	}
	for i, edge := range g.data.Edges {
		edge.Metadata = cloneJSONObject(edge.Metadata)
		out.Edges[i] = edge
	}
	for i, artifact := range g.data.Artifacts {
		artifact.SchemaMeta = cloneJSONObject(artifact.SchemaMeta)
		out.Artifacts[i] = artifact
	}
	return out
}

func (g *Graph) Node(id string) (Node, bool) {
	node, found := g.nodes[id]
	return cloneNode(node), found
}

func (g *Graph) Artifact(id string) (Artifact, bool) {
	artifact, found := g.artifacts[id]
	artifact.SchemaMeta = cloneJSONObject(artifact.SchemaMeta)
	return artifact, found
}

func cloneNode(node Node) Node {
	node.Properties = cloneJSONObject(node.Properties)
	node.Axioms = slices.Clone(node.Axioms)
	return node
}

// Graph metadata comes from decoded JSON, with typed slices accepted for seed data.
func cloneJSONObject(value map[string]any) map[string]any {
	out := maps.Clone(value)
	for key, inner := range out {
		out[key] = cloneJSONValue(inner)
	}
	return out
}

func cloneJSONValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneJSONObject(v)
	case []any:
		out := slices.Clone(v)
		for i, part := range out {
			out[i] = cloneJSONValue(part)
		}
		return out
	case []string:
		return slices.Clone(v)
	default:
		return v
	}
}
