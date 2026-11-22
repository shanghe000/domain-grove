// replace-draft migrates a specific pending model through the same validators
// and transaction as a normal submission. Reads one SubmitInput on stdin.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"ontology-engine/internal/index"
	"ontology-engine/internal/spaces"
)

func main() {
	root := flag.String("data", "", "directory of the running index service")
	space := flag.String("space", "", "named project")
	old := flag.String("old", "", "pending proposal to supersede")
	apply := flag.Bool("apply", false, "replace the specified pending draft")
	flag.Parse()
	if *root == "" || *old == "" || *space == "" || *space == spaces.DefaultID {
		log.Fatal("data directory, named project and old proposal required")
	}
	var input index.SubmitInput
	dec := json.NewDecoder(io.LimitReader(os.Stdin, 8<<20))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(&input); err != nil {
		log.Fatal(err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		log.Fatal("unexpected trailing input")
	}
	if input.Patch.SchemaVersion != 2 {
		log.Fatal("replacement must explicitly use schema 2")
	}
	defaultStore, err := index.Open(filepath.Join(*root, "index.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer defaultStore.Close()
	catalog, err := spaces.Open(filepath.Join(*root, "projects"), defaultStore)
	if err != nil {
		log.Fatal(err)
	}
	defer catalog.Close()
	ctx := context.Background()
	store, err := catalog.Store(ctx, *space)
	if err != nil {
		log.Fatal(err)
	}
	g, err := store.Graph(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if g.Revision != input.BaseRevision {
		log.Fatal("base revision changed")
	}
	previous, err := store.Get(ctx, *old)
	if err != nil {
		log.Fatal(err)
	}
	preview, err := index.Apply(g, input.Patch)
	if err != nil {
		log.Fatal(err)
	}
	result := map[string]any{"space": *space, "published_revision": g.Revision, "old_status": previous.Status, "nodes": len(preview.Nodes), "edges": len(preview.Edges), "changes": len(preview.Changes), "warnings": index.Warnings(preview)}
	if *apply {
		p, err := store.ReplaceDraft(ctx, *old, "deployment:model-migration", input)
		if err != nil {
			log.Fatal(err)
		}
		result["proposal"] = p.ID
		result["status"] = p.Status
	}
	payload, err := json.Marshal(result)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(payload))
}
