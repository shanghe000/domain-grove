package index

import (
	"slices"
	"strings"
)

func orderedPatch(patch Patch) Patch {
	patch.UpsertChanges = slices.Clone(patch.UpsertChanges)
	patch.RemoveChangeIDs = slices.Clone(patch.RemoveChangeIDs)
	slices.SortFunc(patch.UpsertChanges, func(a, b Change) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(patch.RemoveChangeIDs)
	patch.UpsertNodes = append([]Node{}, patch.UpsertNodes...)
	patch.RemoveNodeIDs = append([]string{}, patch.RemoveNodeIDs...)
	patch.UpsertEdges = append([]Edge{}, patch.UpsertEdges...)
	patch.RemoveEdgeIDs = append([]string{}, patch.RemoveEdgeIDs...)
	slices.SortFunc(patch.UpsertNodes, func(a, b Node) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(patch.UpsertEdges, func(a, b Edge) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(patch.RemoveNodeIDs)
	slices.Sort(patch.RemoveEdgeIDs)
	return patch
}

// Apply previews and validates a patch against a graph without persisting it.
func Apply(base Graph, patch Patch) (Graph, error) {
	if len(patch.UpsertNodes)+len(patch.RemoveNodeIDs)+len(patch.UpsertEdges)+len(patch.RemoveEdgeIDs)+len(patch.UpsertChanges)+len(patch.RemoveChangeIDs) == 0 {
		return Graph{}, invalid("proposal must contain at least one change")
	}
	nodes, err := patchItems(
		base.Nodes, patch.UpsertNodes, patch.RemoveNodeIDs,
		func(node Node) string { return node.ID },
		"node",
	)
	if err != nil {
		return Graph{}, err
	}
	edges, err := patchItems(
		base.Edges, patch.UpsertEdges, patch.RemoveEdgeIDs,
		func(edge Edge) string { return edge.ID },
		"edge",
	)
	if err != nil {
		return Graph{}, err
	}
	version := base.SchemaVersion
	if patch.SchemaVersion != 0 {
		if patch.SchemaVersion < version {
			return Graph{}, invalid("schema downgrade is not supported")
		}
		version = patch.SchemaVersion
	}
	changes, err := patchItems(base.Changes, patch.UpsertChanges, patch.RemoveChangeIDs, func(c Change) string { return c.ID }, "change")
	if err != nil {
		return Graph{}, err
	}
	result := orderedGraph(Graph{SchemaVersion: version, Revision: base.Revision, Nodes: nodes, Edges: edges, Changes: changes})
	if err := Validate(result); err != nil {
		return Graph{}, err
	}
	return result, nil
}

func patchItems[T any](original, upserts []T, removals []string, idOf func(T) string, kind string) ([]T, error) {
	items := make(map[string]T, len(original)+len(upserts))
	for _, item := range original {
		items[idOf(item)] = item
	}
	changed := make(map[string]bool, len(upserts)+len(removals))
	for _, id := range removals {
		if !validID(id) || changed[id] {
			return nil, invalid("duplicate or invalid %s removal %q", kind, id)
		}
		if _, found := items[id]; !found {
			return nil, invalid("cannot remove missing %s %s", kind, id)
		}
		changed[id] = true
		delete(items, id)
	}
	for _, item := range upserts {
		id := idOf(item)
		if changed[id] {
			return nil, invalid("multiple changes for %s %s", kind, id)
		}
		changed[id] = true
		items[id] = item
	}
	result := make([]T, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	return result, nil
}
