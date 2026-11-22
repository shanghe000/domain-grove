package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"ontology-engine/internal/ontology"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS ontology_nodes (
    id              TEXT PRIMARY KEY,
    domain_id       TEXT,
    node_type       TEXT NOT NULL,
    label           TEXT NOT NULL,
    status          TEXT DEFAULT 'active',
    properties_json TEXT NOT NULL DEFAULT '{}',
    axioms_json     TEXT NOT NULL DEFAULT '[]',
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ontology_edges (
    id              TEXT PRIMARY KEY,
    src_id          TEXT NOT NULL,
    dst_id          TEXT NOT NULL,
    predicate       TEXT NOT NULL,
    label_zh        TEXT NOT NULL,
    cost            REAL NOT NULL,
    metadata_json   TEXT DEFAULT '{}',
    UNIQUE(src_id, dst_id, predicate)
);

CREATE TABLE IF NOT EXISTS artifact_registry (
    node_id          TEXT PRIMARY KEY REFERENCES ontology_nodes(id) ON DELETE CASCADE,
    artifact_type    TEXT NOT NULL,
    read_mode        TEXT NOT NULL,
    physical_ref     TEXT NOT NULL,
    source_url       TEXT DEFAULT '',
    schema_meta_json TEXT DEFAULT '{}',
    schema_hash      TEXT DEFAULT '',
    updated_at       DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS draft_sessions (
    id               TEXT PRIMARY KEY,
    created_by       TEXT NOT NULL,
    status           TEXT NOT NULL,
    source_type      TEXT NOT NULL,
    staging_payload  TEXT NOT NULL,
    draft_patch_json TEXT NOT NULL,
    ops_history_json TEXT NOT NULL DEFAULT '[]',
    created_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at       DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS engine_metadata (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ontology_edges_destination ON ontology_edges(dst_id);
CREATE INDEX IF NOT EXISTS draft_sessions_owner_status ON draft_sessions(created_by, status);
`

type Store struct {
	DB *sql.DB
}

func Open(path string) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o750); err != nil {
		return nil, err
	}
	dsn := url.URL{Scheme: "file", Path: absolute}
	query := dsn.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Set("_txlock", "immediate")
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize ontology database: %w", err)
	}
	return &Store{DB: db}, nil
}

func (s *Store) Close() error {
	return s.DB.Close()
}

type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) LoadGraph(ctx context.Context) (*ontology.Graph, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	data, err := LoadGraph(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return data, nil
}

func LoadGraph(ctx context.Context, q Queryer) (*ontology.Graph, error) {
	nodes, err := fetch(ctx, q, `SELECT id, coalesce(domain_id, ''), node_type, label, coalesce(status, 'active'),
		properties_json, axioms_json, coalesce(cast(updated_at AS TEXT), '') FROM ontology_nodes`, scanNode)
	if err != nil {
		return nil, fmt.Errorf("load nodes: %w", err)
	}
	edges, err := fetch(ctx, q, `SELECT id, src_id, dst_id, predicate, label_zh, cost,
		coalesce(metadata_json, '{}') FROM ontology_edges`, scanEdge)
	if err != nil {
		return nil, fmt.Errorf("load edges: %w", err)
	}
	artifacts, err := fetch(ctx, q, `SELECT node_id, artifact_type, read_mode, physical_ref,
		coalesce(source_url, ''), coalesce(schema_meta_json, '{}'), coalesce(schema_hash, ''),
		coalesce(cast(updated_at AS TEXT), '') FROM artifact_registry`, scanArtifact)
	if err != nil {
		return nil, fmt.Errorf("load artifacts: %w", err)
	}
	return ontology.NewGraph(nodes, edges, artifacts), nil
}

func fetch[T any](ctx context.Context, q Queryer, statement string, scan func(*sql.Rows) (T, error)) ([]T, error) {
	rows, err := q.QueryContext(ctx, statement)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []T{}
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanNode(rows *sql.Rows) (ontology.Node, error) {
	var n ontology.Node
	var properties, axioms string
	if err := rows.Scan(&n.ID, &n.DomainID, &n.NodeType, &n.Label, &n.Status, &properties, &axioms, &n.UpdatedAt); err != nil {
		return n, err
	}
	if err := json.Unmarshal([]byte(properties), &n.Properties); err != nil {
		return n, err
	}
	return n, json.Unmarshal([]byte(axioms), &n.Axioms)
}

func scanEdge(rows *sql.Rows) (ontology.Edge, error) {
	var e ontology.Edge
	var metadata string
	if err := rows.Scan(&e.ID, &e.SrcID, &e.DstID, &e.Predicate, &e.LabelZH, &e.Cost, &metadata); err != nil {
		return e, err
	}
	return e, json.Unmarshal([]byte(metadata), &e.Metadata)
}

func scanArtifact(rows *sql.Rows) (ontology.Artifact, error) {
	var a ontology.Artifact
	var meta string
	if err := rows.Scan(&a.NodeID, &a.ArtifactType, &a.ReadMode, &a.PhysicalRef, &a.SourceURL, &meta, &a.SchemaHash, &a.UpdatedAt); err != nil {
		return a, err
	}
	return a, json.Unmarshal([]byte(meta), &a.SchemaMeta)
}

// ApplyPatch mutates the graph within the caller's transaction. The caller must
// roll back the transaction if this function returns an error.
func ApplyPatch(ctx context.Context, tx *sql.Tx, patch ontology.DraftPatch) error {
	for _, e := range patch.RemovedEdges {
		if _, err := tx.ExecContext(ctx, `DELETE FROM ontology_edges WHERE src_id=? AND dst_id=? AND predicate=?`, e.SrcID, e.DstID, e.Predicate); err != nil {
			return err
		}
	}
	for _, id := range patch.RemovedNodes {
		if _, err := tx.ExecContext(ctx, `DELETE FROM ontology_edges WHERE src_id=? OR dst_id=?`, id, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM ontology_nodes WHERE id=?`, id); err != nil {
			return err
		}
	}
	for _, n := range patch.Nodes {
		n, err := ontology.NormalizeNode(n)
		if err != nil {
			return err
		}
		properties, err := json.Marshal(n.Properties)
		if err != nil {
			return err
		}
		axioms, err := json.Marshal(n.Axioms)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO ontology_nodes (id, domain_id, node_type, label, status, properties_json, axioms_json, updated_at)
			VALUES(?, nullif(?, ''), ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
			ON CONFLICT(id) DO UPDATE SET domain_id=excluded.domain_id, node_type=excluded.node_type, label=excluded.label,
			status=excluded.status, properties_json=excluded.properties_json, axioms_json=excluded.axioms_json, updated_at=excluded.updated_at`,
			n.ID, n.DomainID, n.NodeType, n.Label, n.Status, string(properties), string(axioms))
		if err != nil {
			return err
		}
	}
	for _, e := range patch.Edges {
		e, err := ontology.NormalizeEdge(e)
		if err != nil {
			return err
		}
		metadata, err := json.Marshal(e.Metadata)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO ontology_edges (id, src_id, dst_id, predicate, label_zh, cost, metadata_json)
			VALUES(?, ?, ?, ?, ?, ?, ?) ON CONFLICT(src_id, dst_id, predicate) DO UPDATE SET
			label_zh=excluded.label_zh, cost=excluded.cost, metadata_json=excluded.metadata_json`,
			e.ID, e.SrcID, e.DstID, e.Predicate, e.LabelZH, e.Cost, string(metadata))
		if err != nil {
			return err
		}
	}
	for _, a := range patch.Artifacts {
		a, err := ontology.NormalizeArtifact(a)
		if err != nil {
			return err
		}
		meta, err := json.Marshal(a.SchemaMeta)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO artifact_registry (node_id, artifact_type, read_mode, physical_ref, source_url, schema_meta_json, schema_hash, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) ON CONFLICT(node_id) DO UPDATE SET
			artifact_type=excluded.artifact_type, read_mode=excluded.read_mode, physical_ref=excluded.physical_ref, source_url=excluded.source_url,
			schema_meta_json=excluded.schema_meta_json, schema_hash=excluded.schema_hash, updated_at=excluded.updated_at`,
			a.NodeID, a.ArtifactType, a.ReadMode, a.PhysicalRef, a.SourceURL, string(meta), a.SchemaHash)
		if err != nil {
			return err
		}
	}
	return validateReferences(ctx, tx)
}

func validateReferences(ctx context.Context, tx *sql.Tx) error {
	const query = `
		SELECT 'node ' || n.id || ' belongs to missing or non-domain node ' || n.domain_id FROM ontology_nodes n
		LEFT JOIN ontology_nodes d ON d.id=n.domain_id
		WHERE n.domain_id IS NOT NULL AND (d.id IS NULL OR d.node_type!='Domain')
		UNION ALL
		SELECT 'edge ' || e.id || ' refers to a missing node' FROM ontology_edges e
		LEFT JOIN ontology_nodes s ON s.id=e.src_id LEFT JOIN ontology_nodes d ON d.id=e.dst_id
		WHERE s.id IS NULL OR d.id IS NULL
		UNION ALL
		SELECT 'physical artifact ' || a.node_id || ' requires a node of type Artifact' FROM artifact_registry a
		JOIN ontology_nodes n ON n.id=a.node_id WHERE n.node_type!='Artifact'
		LIMIT 1`
	var invalid string
	if err := tx.QueryRowContext(ctx, query).Scan(&invalid); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	return fmt.Errorf("invalid graph: %s", invalid)
}
