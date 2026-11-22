package indexapi

import (
	"fmt"
	"net/http"
	"ontology-engine/internal/auth"
	"ontology-engine/internal/index"
	"strconv"
)

func traversalQuery(r *http.Request) (index.TraverseArgs, error) {
	p := r.URL.Query()
	q := index.TraverseArgs{NodeIDs: p["node_id"]}
	if raw := p.Get("budget"); raw != "" {
		budget, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return q, fmt.Errorf("%w: invalid budget", index.ErrInvalid)
		}
		q.Budget = &budget
	}
	if raw := p.Get("revision"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return q, fmt.Errorf("%w: invalid revision", index.ErrInvalid)
		}
		q.Revision = &v
	}
	if raw := p.Get("include_deprecated"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return q, fmt.Errorf("%w: invalid lifecycle filter", index.ErrInvalid)
		}
		q.IncludeDeprecated = v
	}
	return q, nil
}

func (s indexHTTP) traverse(w http.ResponseWriter, r *http.Request, _ *auth.UserIdentity) {
	q, err := traversalQuery(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var g index.Graph
	switch {
	case r.Method == http.MethodPost:
		var input modelingSubmission
		if !readModelingJSON(w, r, modelingSubmitBodyLimit, &input) {
			return
		}
		if input.BaseRevision == nil || input.Patch == nil {
			writeError(w, 400, "请提供草案和基础版本。")
			return
		}
		g, err = s.store.Graph(r.Context())
		if err == nil && g.Revision != *input.BaseRevision {
			err = index.ErrConflict
		}
		if err == nil {
			g, err = index.Apply(g, *input.Patch)
		}
	case r.PathValue("id") != "":
		g, err = s.store.Preview(r.Context(), r.PathValue("id"))
	default:
		g, err = s.store.PublishedRevision(r.Context(), q.Revision)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out, err := index.Traverse(g, q)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s indexHTTP) changes(w http.ResponseWriter, r *http.Request, _ *auth.UserIdentity) {
	q, err := traversalQuery(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	g, err := s.store.PublishedRevision(r.Context(), q.Revision)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	p := r.URL.Query()
	changes, err := index.ChangesInWindow(g, index.ChangeQuery{From: p.Get("from"), To: p.Get("to"), NodeID: p.Get("node_id"), Scope: p.Get("scope")})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revision": g.Revision, "changes": changes})
}
