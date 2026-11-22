package ontology

import (
	"cmp"
	"container/heap"
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
)

type visit struct {
	nodeID string
	cost   float64
}

type visitQueue []visit

func (q visitQueue) Len() int { return len(q) }

func (q visitQueue) Less(i, j int) bool {
	return cmp.Or(cmp.Compare(q[i].cost, q[j].cost), cmp.Compare(q[i].nodeID, q[j].nodeID)) < 0
}

func (q visitQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }

func (q *visitQueue) Push(v any) { *q = append(*q, v.(visit)) }

func (q *visitQueue) Pop() any {
	last := (*q)[len(*q)-1]
	*q = (*q)[:len(*q)-1]
	return last
}

type frontierCandidate struct {
	edge Edge
	cost float64
}

var (
	logicalConfigTable  = regexp.MustCompile(`^cfg_[a-z0-9_]{1,76}$`)
	configRevisionTable = regexp.MustCompile(`^cfg_rev_[a-f0-9]{32}$`)
)

// GetNodeContext ranks forward paths by cumulative cost. Starting-node incoming
// edges affect the result only; their sources never seed forward paths.
func (g *Graph) GetNodeContext(seedID string, budget float64, includeDeprecated bool) (NodeContext, error) {
	if budget < 0 || math.IsNaN(budget) || math.IsInf(budget, 0) {
		return NodeContext{}, errors.New("budget must be a finite non-negative number")
	}
	allowed := func(id string) bool {
		node, exists := g.nodes[id]
		return exists && (includeDeprecated || node.Status != Deprecated)
	}
	if !allowed(seedID) {
		return NodeContext{}, fmt.Errorf("%w: %s", ErrUnknownNode, seedID)
	}

	best := map[string]float64{seedID: 0}
	accepted := map[string]bool{seedID: true}
	edges := make(map[EdgeKey]Edge)
	frontier := make(map[string]frontierCandidate)
	queue := &visitQueue{{nodeID: seedID}}
	for queue.Len() > 0 {
		current := heap.Pop(queue).(visit)
		if current.cost != best[current.nodeID] {
			continue
		}
		for _, edge := range g.out[current.nodeID] {
			if !allowed(edge.DstID) {
				continue
			}
			cost := current.cost + edge.Cost
			if cost > budget {
				previous, exists := frontier[edge.DstID]
				if !exists || cost < previous.cost || (cost == previous.cost && CompareEdges(edge, previous.edge) < 0) {
					frontier[edge.DstID] = frontierCandidate{edge, cost}
				}
				continue
			}
			accepted[edge.DstID] = true
			edges[edge.Key()] = edge
			previous, exists := best[edge.DstID]
			if !exists || cost < previous {
				best[edge.DstID] = cost
				heap.Push(queue, visit{edge.DstID, cost})
			}
		}
	}

	if budget >= 2 {
		for _, edge := range g.in[seedID] {
			if allowed(edge.SrcID) {
				accepted[edge.SrcID] = true
				edges[edge.Key()] = edge
			}
		}
	}
	result := NodeContext{
		SeedNodeID: seedID, Budget: budget,
		Subgraph:  Subgraph{Nodes: []Node{}, Edges: []Edge{}},
		Artifacts: []Artifact{}, FrontierNodes: []FrontierNode{},
	}
	for _, id := range slices.Sorted(maps.Keys(accepted)) {
		node := cloneNode(g.nodes[id])
		node.UpdatedAt = ""
		result.Subgraph.Nodes = append(result.Subgraph.Nodes, node)
		if artifact, found := g.artifacts[node.ID]; found {
			artifact.SchemaHash, artifact.UpdatedAt = "", ""
			if artifact.ArtifactType == DuckDBTable {
				// The consumer can query the logical alias. The published registry
				// retains the immutable DuckDB table used by the Worker.
				if alias, ok := artifact.SchemaMeta["table_name"].(string); ok &&
					logicalConfigTable.MatchString(alias) && !configRevisionTable.MatchString(alias) {
					artifact.PhysicalRef = alias
				}
			}
			artifact.SchemaMeta = cloneJSONObject(artifact.SchemaMeta)
			result.Artifacts = append(result.Artifacts, artifact)
		}
	}
	for _, edge := range edges {
		edge.ID = ""
		edge.Metadata = cloneJSONObject(edge.Metadata)
		result.Subgraph.Edges = append(result.Subgraph.Edges, edge)
	}
	slices.SortFunc(result.Subgraph.Edges, CompareEdges)
	for _, id := range slices.Sorted(maps.Keys(frontier)) {
		if !accepted[id] {
			node, candidate := g.nodes[id], frontier[id]
			result.FrontierNodes = append(result.FrontierNodes, FrontierNode{
				NodeID: node.ID, Label: node.Label, ViaEdge: candidate.edge.Predicate, FromNode: candidate.edge.SrcID,
			})
		}
	}
	return result, nil
}
