package ontology

type Skeleton struct {
	Nodes []SkeletonNode `json:"nodes"`
	Edges []EdgeKey      `json:"edges"`
}

type SkeletonNode struct {
	ID       string   `json:"id"`
	NodeType string   `json:"node_type"`
	DomainID string   `json:"domain_id"`
	Label    string   `json:"label"`
	Aliases  []string `json:"aliases"`
}

func (g *Graph) Skeleton(domainID string) Skeleton {
	skeleton := Skeleton{Nodes: []SkeletonNode{}, Edges: []EdgeKey{}}
	included := make(map[string]bool)
	for _, node := range g.data.Nodes {
		if node.Status == Deprecated || (domainID != "" && node.DomainID != domainID && node.ID != domainID) {
			continue
		}
		included[node.ID] = true
		skeleton.Nodes = append(skeleton.Nodes, SkeletonNode{
			ID: node.ID, NodeType: node.NodeType, DomainID: node.DomainID,
			Label: node.Label, Aliases: nodeAliases(node.Properties["aliases"]),
		})
	}
	for _, edge := range g.data.Edges {
		if included[edge.SrcID] && included[edge.DstID] {
			skeleton.Edges = append(skeleton.Edges, edge.Key())
		}
	}
	return skeleton
}

func nodeAliases(value any) []string {
	switch aliases := value.(type) {
	case []string:
		return append([]string{}, aliases...)
	case []any:
		result := []string{}
		for _, alias := range aliases {
			if text, ok := alias.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return []string{}
	}
}
