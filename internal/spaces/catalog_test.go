package spaces

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"ontology-engine/internal/index"
)

func newCatalog(t *testing.T) (*Catalog, *index.Store, string) {
	t.Helper()
	data := t.TempDir()
	store, err := index.Open(filepath.Join(data, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	root := filepath.Join(data, "projects")
	catalog := openCatalog(t, root, store)
	return catalog, store, root
}

func openCatalog(t *testing.T, root string, store *index.Store) *Catalog {
	t.Helper()
	catalog, err := Open(root, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close() })
	return catalog
}

func requireError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func createSpace(t *testing.T, catalog *Catalog, actor, id, name string) Space {
	t.Helper()
	space, err := catalog.Create(context.Background(), actor, id, name)
	if err != nil {
		t.Fatal(err)
	}
	return space
}

func TestProjectCatalogIsolatesIndexesAndMembers(t *testing.T) {
	ctx := context.Background()
	catalog, existing, root := newCatalog(t)
	owner := "fixture-owner"
	member := "fixture-reviewer"
	retention := createSpace(t, catalog, owner, "r", " 留存经营分析 ")
	agency := createSpace(t, catalog, "fixture-analyst", "agency-data", "投放数据")
	if retention != (Space{ID: "r", Name: "留存经营分析", Role: RoleOwner}) {
		t.Fatalf("space = %+v", retention)
	}
	spaces, err := catalog.List(ctx, owner)
	if err != nil || !reflect.DeepEqual(spaces, []Space{defaultSpace, retention}) {
		t.Fatalf("owner spaces = %+v (error %v)", spaces, err)
	}
	for _, id := range []string{retention.ID, agency.ID, "other-space"} {
		_, err := catalog.Authorize(ctx, member, id)
		requireError(t, err, ErrNotFound)
	}
	_, err = catalog.Authorize(ctx, "", DefaultID)
	requireError(t, err, ErrNotFound)
	_, err = catalog.AddMember(ctx, retention.ID, member, "outsider")
	requireError(t, err, ErrNotFound)
	_, err = catalog.AddMember(ctx, DefaultID, owner, member)
	requireError(t, err, ErrForbidden)
	for range 2 {
		granted, err := catalog.AddMember(ctx, retention.ID, owner, member)
		if err != nil || granted != (Member{Subject: member, Role: RoleReviewer}) {
			t.Fatalf("granted = %+v (error %v)", granted, err)
		}
	}
	_, err = catalog.Members(ctx, retention.ID, member)
	requireError(t, err, ErrForbidden)
	members, err := catalog.Members(ctx, retention.ID, owner)
	if err != nil || !reflect.DeepEqual(members, []Member{{Subject: owner, Role: RoleOwner}, {Subject: member, Role: RoleReviewer}}) {
		t.Fatalf("members = %+v (error %v)", members, err)
	}
	requireError(t, catalog.RemoveMember(ctx, retention.ID, owner, owner), ErrForbidden)

	defaultStore, err := catalog.Store(ctx, DefaultID)
	if err != nil || defaultStore != existing {
		t.Fatal("default project does not resolve the existing index store")
	}
	stores := []*index.Store{defaultStore}
	for _, id := range []string{retention.ID, agency.ID} {
		store, err := catalog.Store(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, store)
	}
	_, err = catalog.Store(ctx, "unregistered")
	requireError(t, err, ErrNotFound)
	_, err = catalog.Store(ctx, "../index.db")
	requireError(t, err, ErrNotFound)
	for i, id := range []string{"legacy-default", "retention-metric", "agency-kpi"} {
		store := stores[i]
		proposal, err := store.Submit(ctx, owner, index.SubmitInput{RequestID: "same-request", Title: id,
			Patch: index.Patch{UpsertNodes: []index.Node{{ID: id, Kind: index.KindMetric, Label: id}}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Review(ctx, proposal.ID, owner, "approve", "verified"); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	if err := existing.Health(ctx); err != nil {
		t.Fatalf("catalog closed the caller-owned index: %v", err)
	}
	catalog = openCatalog(t, root, existing)
	for id, node := range map[string]string{DefaultID: "legacy-default", retention.ID: "retention-metric", agency.ID: "agency-kpi"} {
		store, err := catalog.Store(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		graph, err := store.Graph(ctx)
		if err != nil || graph.Revision != 1 || len(graph.Nodes) != 1 || graph.Nodes[0].ID != node {
			t.Fatalf("space %s graph = %+v (error %v)", id, graph, err)
		}
	}
	if space, err := catalog.Authorize(ctx, member, retention.ID); err != nil || space.Role != RoleReviewer {
		t.Fatalf("membership after reopen = %+v (error %v)", space, err)
	}
	if err := catalog.RemoveMember(ctx, retention.ID, owner, member); err != nil {
		t.Fatal(err)
	}
	_, err = catalog.Authorize(ctx, member, retention.ID)
	requireError(t, err, ErrNotFound)
}

func TestProjectTokenRotationAndPersistence(t *testing.T) {
	ctx := context.Background()
	catalog, existing, root := newCatalog(t)
	actor := "fixture-owner"
	space := createSpace(t, catalog, actor, "balanced", "平衡性")
	other := createSpace(t, catalog, actor, "reporting", "经营")
	read, err := catalog.RotateToken(ctx, space.ID, actor, ScopeRead)
	if err != nil {
		t.Fatal(err)
	}
	submit, err := catalog.RotateToken(ctx, space.ID, actor, ScopeSubmit)
	if err != nil || read == submit {
		t.Fatal("submit token was not generated independently")
	}
	_, err = catalog.RotateToken(ctx, DefaultID, actor, ScopeRead)
	requireError(t, err, ErrForbidden)
	if _, err := catalog.AddMember(ctx, space.ID, actor, "reviewer"); err != nil {
		t.Fatal(err)
	}
	_, err = catalog.RotateToken(ctx, space.ID, "reviewer", ScopeSubmit)
	requireError(t, err, ErrForbidden)
	for _, id := range []string{other.ID, DefaultID, "not-found"} {
		_, err := catalog.VerifyToken(ctx, id, read)
		requireError(t, err, ErrForbidden)
	}
	var stored []byte
	if err := catalog.db.QueryRowContext(ctx, "SELECT token_hash FROM space_tokens WHERE space_id = ? AND scope = ?", space.ID, ScopeRead).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(read))
	if !bytes.Equal(stored, hash[:]) {
		t.Fatal("read credential was not stored as its digest")
	}
	nextRead, err := catalog.RotateToken(ctx, space.ID, actor, ScopeRead)
	if err != nil || nextRead == read {
		t.Fatal("read credential was not rotated")
	}
	_, err = catalog.VerifyToken(ctx, space.ID, read)
	requireError(t, err, ErrForbidden)
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	catalog = openCatalog(t, root, existing)
	for token, want := range map[string]string{nextRead: ScopeRead, submit: ScopeSubmit} {
		if actual, err := catalog.VerifyToken(ctx, space.ID, token); err != nil || actual != want {
			t.Fatalf("scope = %q, want %q (error %v)", actual, want, err)
		}
	}
}

func TestProjectCreationIsExclusiveAndRollsBackOnStorageFailure(t *testing.T) {
	ctx := context.Background()
	catalog, existing, root := newCatalog(t)
	parallel := openCatalog(t, root, existing)
	for _, invalid := range []string{"default", "sse", "", "../escape", "UPPER", "alpha--beta", "a/b", "a_1"} {
		_, err := catalog.Create(ctx, "owner", invalid, "Name")
		requireError(t, err, ErrInvalid)
	}
	indexRoot := filepath.Join(root, "indexes")
	if err := os.Remove(indexRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Create(ctx, "owner", "blocked", "Name"); err == nil {
		t.Fatal("created project without its index")
	}
	_, err := parallel.Authorize(ctx, "owner", "blocked")
	requireError(t, err, ErrNotFound)
	if err := os.Remove(indexRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(indexRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	var simultaneous sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, service := range []*Catalog{catalog, parallel} {
		simultaneous.Go(func() {
			<-start
			_, err := service.Create(ctx, "owner", "blocked", "Name")
			results <- err
		})
	}
	close(start)
	simultaneous.Wait()
	first, second := <-results, <-results
	if first == nil {
		requireError(t, second, ErrConflict)
	} else if second == nil {
		requireError(t, first, ErrConflict)
	} else {
		t.Fatalf("concurrent creation failed twice: %v; %v", first, second)
	}
	files, err := filepath.Glob(filepath.Join(indexRoot, "*.db"))
	if err != nil || len(files) != 1 {
		t.Fatalf("project indexes = %d (error %v)", len(files), err)
	}
	store, err := parallel.Store(ctx, "blocked")
	if err != nil {
		t.Fatal(err)
	}
	if graph, err := store.Graph(ctx); err != nil || graph.Revision != 0 || len(graph.Nodes) != 0 {
		t.Fatalf("new space has data: %+v (error %v)", graph, err)
	}
}
