package index

import (
	"context"
	"encoding/json"
	"reflect"
)

// Each edit is relative to the preview the reviewer saw. The persisted proposal
// remains a patch against its original published revision, including after undo.
func (s *Store) Edit(ctx context.Context, id, actor string, version int64, edit *Patch) (Proposal, Graph, error) {
	fail := func(err error) (Proposal, Graph, error) { return Proposal{}, Graph{}, err }
	if !validActor(actor) {
		return fail(invalid("missing editor"))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	p, err := getProposal(ctx, tx, id)
	if err != nil {
		return fail(err)
	}
	if p.Status != StatusPending || p.EditVersion != version {
		return fail(conflict("draft version or status changed"))
	}
	base, err := readGraph(ctx, tx, &p.BaseRevision)
	if err != nil {
		return fail(err)
	}
	before, err := json.Marshal(p.Patch)
	if err != nil {
		return fail(err)
	}
	head := int64(0)
	if version > 0 {
		if err := tx.QueryRowContext(ctx, "SELECT next_head FROM proposal_edits WHERE proposal_id=? AND version=?", id, version).Scan(&head); err != nil {
			return fail(err)
		}
	}
	next := version + 1
	if edit == nil {
		if head == 0 {
			return fail(conflict("nothing to undo"))
		}
		var prior []byte
		if err := tx.QueryRowContext(ctx, "SELECT previous_head,previous_patch FROM proposal_edits WHERE proposal_id=? AND version=?", id, head).Scan(&next, &prior); err != nil {
			return fail(err)
		}
		p.Patch = Patch{}
		if err := decode(prior, &p.Patch); err != nil {
			return fail(err)
		}
	} else {
		preview, err := Apply(base, p.Patch)
		if err != nil {
			return fail(err)
		}
		edited, err := Apply(preview, *edit)
		if err != nil {
			return fail(err)
		}
		p.Patch = diffGraph(base, edited)
	}
	graph, err := Apply(base, p.Patch)
	if err != nil {
		return fail(err)
	}
	payload, err := json.Marshal(p.Patch)
	if err != nil {
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO proposal_edits VALUES(?,?,?,?,?,?,?)", id, version+1, head, next, actor, timestamp(), before); err != nil {
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE index_proposals SET patch_json=? WHERE id=?", payload, id); err != nil {
		return fail(err)
	}
	p.EditVersion = version + 1
	p.CanUndo = next > 0
	return p, graph, tx.Commit()
}

func changesBetween[T any](before, after []T, key func(T) string) ([]T, []string) {
	old := make(map[string]T, len(before))
	for _, x := range before {
		old[key(x)] = x
	}
	var up []T
	var removed []string
	for _, x := range after {
		id := key(x)
		if prev, ok := old[id]; !ok || !reflect.DeepEqual(prev, x) {
			up = append(up, x)
		}
		delete(old, id)
	}
	for id := range old {
		removed = append(removed, id)
	}
	return up, removed
}

func diffGraph(a, b Graph) Patch {
	var p Patch
	p.UpsertNodes, p.RemoveNodeIDs = changesBetween(a.Nodes, b.Nodes, func(n Node) string { return n.ID })
	p.UpsertEdges, p.RemoveEdgeIDs = changesBetween(a.Edges, b.Edges, func(e Edge) string { return e.ID })
	if b.SchemaVersion == 2 {
		p.SchemaVersion = 2
	}
	p.UpsertChanges, p.RemoveChangeIDs = changesBetween(a.Changes, b.Changes, func(c Change) string { return c.ID })
	return orderedPatch(p)
}
