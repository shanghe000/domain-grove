package index

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"
)

// TraverseArgs accepts boundary node IDs returned by a previous call. Passing
// the associated revision prevents silently continuing into a newer snapshot.
type TraverseArgs struct {
	NodeIDs           []string `json:"node_ids"`
	Budget            *float64 `json:"budget,omitempty"`
	Revision          *int64   `json:"revision,omitempty"`
	IncludeDeprecated bool     `json:"include_deprecated,omitempty"`
}
type Boundary struct {
	NodeID   string `json:"node_id"`
	EdgeID   string `json:"edge_id"`
	Revision int64  `json:"revision"`
}
type Subgraph struct {
	Revision int64      `json:"revision"`
	Budget   float64    `json:"budget"`
	Nodes    []Node     `json:"nodes"`
	Edges    []Edge     `json:"edges"`
	Boundary []Boundary `json:"boundary"`
}

const defaultBudget = 2.5

func edgeSteps(e Edge) int {
	switch e.Relation {
	case "depends_on", "defined_in", "configured_by", "foreign_key_to":
		return 1
	}
	return 2
}

// Integer half-cost buckets implement shortest paths. Incoming seed edges
// contribute immediate context only; they never enqueue additional searches.
func Traverse(g Graph, q TraverseArgs) (Subgraph, error) {
	budget := defaultBudget
	if q.Budget != nil {
		budget = *q.Budget
	}
	if math.IsNaN(budget) || math.IsInf(budget, 0) || budget < 0 || budget > 16 || len(q.NodeIDs) == 0 || len(q.NodeIDs) > 64 {
		return Subgraph{}, invalid("use 1–64 seed node IDs and a finite budget between 0 and 16")
	}
	if q.Revision != nil && *q.Revision != g.Revision {
		return Subgraph{}, conflict("snapshot revision has changed")
	}
	result := Subgraph{Revision: g.Revision, Budget: budget, Nodes: []Node{}, Edges: []Edge{}, Boundary: []Boundary{}}
	nodes := map[string]Node{}
	for _, n := range g.Nodes {
		if q.IncludeDeprecated || property(n, "lifecycle") != "deprecated" {
			nodes[n.ID] = n
		}
	}
	out, in := map[string][]Edge{}, map[string][]Edge{}
	for _, e := range g.Edges {
		if nodes[e.To].ID != "" && nodes[e.From].ID != "" {
			out[e.From] = append(out[e.From], e)
			in[e.To] = append(in[e.To], e)
		}
	}
	buckets := make([][]string, int(budget*2)+1)
	best := map[string]int{}
	visited, edges := map[string]bool{}, map[string]bool{}
	frontier := map[string]Edge{}
	for _, id := range q.NodeIDs {
		n, ok := nodes[id]
		if !ok {
			return Subgraph{}, ErrNotFound
		}
		if _, seen := best[id]; seen {
			continue
		}
		best[id] = 0
		buckets[0] = append(buckets[0], id)
		visited[id] = true
		if property(n, "hub_role") != "sink" {
			for _, e := range in[id] {
				if float64(edgeSteps(e)) <= budget*2 {
					visited[e.From] = true
					edges[e.ID] = true
				}
			}
		}
	}
	for steps, ids := range buckets {
		for _, id := range ids {
			if steps != best[id] || property(nodes[id], "hub_role") == "sink" {
				continue
			}
			for _, e := range out[id] {
				cost := steps + edgeSteps(e)
				if cost >= len(buckets) {
					if previous, ok := frontier[e.To]; !ok || e.ID < previous.ID {
						frontier[e.To] = e
					}
					continue
				}
				visited[e.To] = true
				edges[e.ID] = true
				if prev, ok := best[e.To]; !ok || cost < prev {
					best[e.To] = cost
					buckets[cost] = append(buckets[cost], e.To)
				}
			}
		}
	}
	for _, n := range g.Nodes {
		if visited[n.ID] {
			result.Nodes = append(result.Nodes, n)
		}
	}
	for _, e := range g.Edges {
		if edges[e.ID] {
			result.Edges = append(result.Edges, e)
		}
	}
	for id, e := range frontier {
		if !visited[id] {
			result.Boundary = append(result.Boundary, Boundary{id, e.ID, g.Revision})
		}
	}
	slices.SortFunc(result.Nodes, func(a, b Node) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(result.Edges, func(a, b Edge) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(result.Boundary, func(a, b Boundary) int { return strings.Compare(a.NodeID, b.NodeID) })
	return result, nil
}

type ChangeQuery struct {
	Revision *int64 `json:"revision,omitempty"`
	From     string `json:"from"`
	To       string `json:"to"`
	Scope    string `json:"scope,omitempty"`
	NodeID   string `json:"node_id,omitempty"`
}

func ChangesInWindow(g Graph, q ChangeQuery) ([]Change, error) {
	from, err := time.Parse(time.RFC3339, q.From)
	if err != nil {
		return nil, invalid("from must be an RFC3339 time")
	}
	to, err := time.Parse(time.RFC3339, q.To)
	if err != nil || !from.Before(to) {
		return nil, invalid("to must be an RFC3339 time after from")
	}
	found := []Change{}
	for _, c := range g.Changes {
		if q.Scope != "" && c.Scope != "" && c.Scope != q.Scope || q.NodeID != "" && !slices.Contains(c.NodeIDs, q.NodeID) {
			continue
		}
		start, end, err := changeInterval(c)
		if err != nil {
			return nil, err
		}
		if start.Before(to) && (start.Equal(end) && !start.Before(from) || end.After(from)) {
			found = append(found, c)
		}
	}
	slices.SortFunc(found, func(a, b Change) int {
		at, _, _ := changeInterval(a)
		bt, _, _ := changeInterval(b)
		return cmp.Or(at.Compare(bt), strings.Compare(a.ID, b.ID))
	})
	return found, nil
}
