package spaceapi

import (
	"errors"
	"net/http"
	"strings"

	"ontology-engine/internal/index"
	"ontology-engine/internal/indexapi"
	"ontology-engine/internal/spaces"
)

// NamedMCP preserves the default MCP routes and adds project-specific transports.
// The caller should mount it at /mcp and /mcp/ (or matching exact routes).
func (a *API) NamedMCP(defaultHandler http.Handler) (http.Handler, error) {
	named, err := indexapi.NewResolvedMCPHandler(a.resolveMCP)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		project, routed := mcpProject(r.URL.Path)
		if !routed {
			http.NotFound(w, r)
			return
		}
		if project == spaces.DefaultID {
			if defaultHandler == nil {
				http.NotFound(w, r)
				return
			}
			defaultHandler.ServeHTTP(w, r)
			return
		}
		named.ServeHTTP(w, r)
	}), nil
}

func (a *API) resolveMCP(r *http.Request, bearer string) (*index.Store, string, error) {
	project, allowed := mcpProject(r.URL.Path)
	if !allowed || project == spaces.DefaultID {
		return nil, "", nil
	}
	ctx := r.Context()
	scope, err := a.catalog.VerifyToken(ctx, project, bearer)
	if errors.Is(err, spaces.ErrForbidden) || errors.Is(err, spaces.ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	actor := ""
	switch scope {
	case spaces.ScopeRead:
	case spaces.ScopeSubmit:
		actor = a.submitterID
	default:
		return nil, "", nil
	}
	store, err := a.catalog.Store(ctx, project)
	return store, actor, err
}

func mcpProject(path string) (string, bool) {
	path = strings.TrimSuffix(path, "/")
	if path == "/mcp" || path == "/mcp/sse" {
		return spaces.DefaultID, true
	}
	tail, found := strings.CutPrefix(path, "/mcp/")
	if !found {
		return "", false
	}
	project, suffix, hasMore := strings.Cut(tail, "/")
	if project == "" || project == "sse" || (hasMore && suffix != "sse") {
		return "", false
	}
	return project, true
}
