package index

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

type Proposal struct {
	EditVersion  int64  `json:"edit_version"`
	CanUndo      bool   `json:"can_undo"`
	ID           string `json:"id"`
	Title        string `json:"title"`
	Rationale    string `json:"rationale"`
	Status       string `json:"status"`
	SubmittedBy  string `json:"submitted_by"`
	CreatedAt    string `json:"created_at"`
	ReviewedBy   string `json:"reviewed_by,omitempty"`
	ReviewedAt   string `json:"reviewed_at,omitempty"`
	ReviewNote   string `json:"review_note,omitempty"`
	BaseRevision int64  `json:"base_revision"`
	Patch        Patch  `json:"patch"`
}

type SubmitInput struct {
	RequestID    string `json:"request_id"`
	Title        string `json:"title"`
	Rationale    string `json:"rationale,omitempty"`
	BaseRevision int64  `json:"base_revision"`
	Patch        Patch  `json:"patch"`
}

// Keep stored request digests stable when public JSON makes fields optional.
type fingerprintPatch struct {
	SchemaVersion   int      `json:"schema_version,omitempty"`
	UpsertChanges   []Change `json:"upsert_changes,omitempty"`
	RemoveChangeIDs []string `json:"remove_change_ids,omitempty"`
	UpsertNodes     []Node   `json:"upsert_nodes"`
	RemoveNodeIDs   []string `json:"remove_node_ids"`
	UpsertEdges     []Edge   `json:"upsert_edges"`
	RemoveEdgeIDs   []string `json:"remove_edge_ids"`
}

type fingerprintInput struct {
	RequestID    string           `json:"request_id"`
	Title        string           `json:"title"`
	Rationale    string           `json:"rationale"`
	BaseRevision int64            `json:"base_revision"`
	Patch        fingerprintPatch `json:"patch"`
}

// Application ID distinguishes the index database from the legacy engine.
const sqliteApplicationID = 0x4f494458

const schema = `
CREATE TABLE IF NOT EXISTS index_revisions (
    revision INTEGER PRIMARY KEY CHECK (revision >= 0),
    graph_json BLOB NOT NULL,
    proposal_id TEXT UNIQUE
);
CREATE TABLE IF NOT EXISTS index_proposals (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE,
    request_hash TEXT NOT NULL,
    base_revision INTEGER NOT NULL REFERENCES index_revisions(revision),
    title TEXT NOT NULL,
    rationale TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected')),
    submitted_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    reviewed_by TEXT NOT NULL DEFAULT '',
    reviewed_at TEXT NOT NULL DEFAULT '',
    review_note TEXT NOT NULL DEFAULT '',
    patch_json BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS source_sync (node_id TEXT PRIMARY KEY, revision INTEGER NOT NULL, shape TEXT NOT NULL, digest TEXT NOT NULL, synced_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS proposal_edits (
    proposal_id TEXT NOT NULL REFERENCES index_proposals(id),
    version INTEGER NOT NULL, previous_head INTEGER NOT NULL, next_head INTEGER NOT NULL,
    actor TEXT NOT NULL, created_at TEXT NOT NULL, previous_patch BLOB NOT NULL,
    PRIMARY KEY(proposal_id, version)
);
CREATE INDEX IF NOT EXISTS index_proposals_status_created ON index_proposals(status, created_at DESC, id DESC);
`

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, invalid("index database path is required")
	}
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
	query.Set("_txlock", "immediate")
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := initialize(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize index database: %w", err)
	}
	return &Store{db: db}, nil
}

func initialize(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var identity int
	if err := tx.QueryRow("PRAGMA application_id").Scan(&identity); err != nil {
		return err
	}
	switch identity {
	case 0:
		var count int
		if err := tx.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return invalid("database belongs to another application")
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA application_id = %d", sqliteApplicationID)); err != nil {
			return err
		}
	case sqliteApplicationID:
	default:
		return invalid("database belongs to another application")
	}
	if _, err := tx.Exec(schema); err != nil {
		return err
	}
	empty, err := json.Marshal(orderedGraph(Graph{SchemaVersion: SchemaVersion}))
	if err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO index_revisions (revision, graph_json) VALUES (0, ?) ON CONFLICT(revision) DO NOTHING", empty); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = db.Exec("PRAGMA journal_mode = WAL")
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Health(ctx context.Context) error {
	var revision int64
	return s.db.QueryRowContext(ctx, "SELECT revision FROM index_revisions ORDER BY revision DESC LIMIT 1").Scan(&revision)
}

type rowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type rowScanner interface {
	Scan(...any) error
}

func (s *Store) Graph(ctx context.Context) (Graph, error) {
	return readGraph(ctx, s.db, nil)
}

// PublishedRevision resolves a traversal cursor against the same immutable
// publication. Drafts live in a separate table and cannot be read here.
func (s *Store) PublishedRevision(ctx context.Context, revision *int64) (Graph, error) {
	if revision != nil && *revision < 0 {
		return Graph{}, invalid("invalid revision")
	}
	g, err := readGraph(ctx, s.db, revision)
	if errors.Is(err, sql.ErrNoRows) {
		return Graph{}, ErrNotFound
	}
	return g, err
}

func readGraph(ctx context.Context, reader rowReader, revision *int64) (Graph, error) {
	statement := "SELECT revision, graph_json FROM index_revisions"
	var args []any
	if revision == nil {
		statement += " ORDER BY revision DESC LIMIT 1"
	} else {
		statement += " WHERE revision = ?"
		args = append(args, *revision)
	}
	var payload []byte
	var stored int64
	if err := reader.QueryRowContext(ctx, statement, args...).Scan(&stored, &payload); err != nil {
		return Graph{}, err
	}
	var graph Graph
	if err := decode(payload, &graph); err != nil {
		return Graph{}, err
	}
	if graph.Revision != stored || (graph.SchemaVersion != SchemaVersion && graph.SchemaVersion != SemanticVersion) {
		return Graph{}, errors.New("stored index snapshot does not match its revision or schema version")
	}
	return graph, nil
}

func decode(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	return decoder.Decode(target)
}

const proposalColumns = `id, request_hash, base_revision, title, rationale, status, submitted_by, created_at,
    reviewed_by, reviewed_at, review_note, patch_json,
    COALESCE((SELECT MAX(version) FROM proposal_edits WHERE proposal_id=index_proposals.id),0),
    COALESCE((SELECT next_head FROM proposal_edits WHERE proposal_id=index_proposals.id ORDER BY version DESC LIMIT 1),0) > 0`

func scanProposal(row rowScanner) (Proposal, string, error) {
	var result Proposal
	var hash string
	var payload []byte
	err := row.Scan(&result.ID, &hash, &result.BaseRevision, &result.Title, &result.Rationale,
		&result.Status, &result.SubmittedBy, &result.CreatedAt, &result.ReviewedBy,
		&result.ReviewedAt, &result.ReviewNote, &payload, &result.EditVersion, &result.CanUndo)
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, "", ErrNotFound
	}
	if err != nil {
		return Proposal{}, "", err
	}
	if err := decode(payload, &result.Patch); err != nil {
		return Proposal{}, "", err
	}
	return result, hash, nil
}

func getProposal(ctx context.Context, reader rowReader, id string) (Proposal, error) {
	proposal, _, err := scanProposal(reader.QueryRowContext(ctx, "SELECT "+proposalColumns+" FROM index_proposals WHERE id = ?", id))
	return proposal, err
}

func (s *Store) Get(ctx context.Context, id string) (Proposal, error) {
	return getProposal(ctx, s.db, id)
}

func (s *Store) List(ctx context.Context, status string) ([]Proposal, error) {
	query := "SELECT " + proposalColumns + " FROM index_proposals"
	var params []any
	switch status {
	case "":
	case StatusPending, StatusApproved, StatusRejected:
		query += " WHERE status = ?"
		params = append(params, status)
	default:
		return nil, invalid("unknown proposal status")
	}
	rows, err := s.db.QueryContext(ctx, query+" ORDER BY created_at DESC, id DESC", params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	proposals := []Proposal{}
	for rows.Next() {
		proposal, _, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, proposal)
	}
	return proposals, rows.Err()
}

func (s *Store) Submit(ctx context.Context, actor string, input SubmitInput) (Proposal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Proposal{}, err
	}
	defer tx.Rollback()
	p, err := submitTx(ctx, tx, actor, input)
	if err != nil {
		return Proposal{}, err
	}
	return p, tx.Commit()
}

func submitTx(ctx context.Context, tx *sql.Tx, actor string, input SubmitInput) (Proposal, error) {
	actor = strings.TrimSpace(actor)
	input.Title = strings.TrimSpace(input.Title)
	input.Rationale = strings.TrimSpace(input.Rationale)
	if !validID(input.RequestID) || input.BaseRevision < 0 || !validActor(actor) ||
		input.Title == "" || !textValid(input.Title, 256, false) || !textValid(input.Rationale, 16_384, true) {
		return Proposal{}, invalid("invalid request ID, base revision, submitter, title or rationale")
	}
	input.Patch = orderedPatch(input.Patch)
	request, err := json.Marshal(fingerprintInput{
		RequestID: input.RequestID, Title: input.Title, Rationale: input.Rationale,
		BaseRevision: input.BaseRevision, Patch: fingerprintPatch(input.Patch),
	})
	if err != nil {
		return Proposal{}, invalid("proposal must contain JSON-compatible values")
	}
	hash := sha256.Sum256(append([]byte(actor+"\x00"), request...))
	digest := hex.EncodeToString(hash[:])
	patch, err := json.Marshal(input.Patch)
	if err != nil {
		return Proposal{}, err
	}
	saved, savedDigest, err := scanProposal(tx.QueryRowContext(ctx, "SELECT "+proposalColumns+" FROM index_proposals WHERE request_id = ?", input.RequestID))
	if err == nil {
		if savedDigest != digest {
			return Proposal{}, conflict("request ID is already used for another proposal")
		}
		return saved, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Proposal{}, err
	}
	base, err := readGraph(ctx, tx, nil)
	if err != nil {
		return Proposal{}, err
	}
	if input.BaseRevision != base.Revision {
		return Proposal{}, conflict("base revision %d is stale, current revision is %d", input.BaseRevision, base.Revision)
	}
	if _, err := Apply(base, input.Patch); err != nil {
		return Proposal{}, err
	}
	result := Proposal{
		ID: "p_" + strings.ToLower(rand.Text()), Title: input.Title, Rationale: input.Rationale,
		Status: StatusPending, SubmittedBy: actor, CreatedAt: timestamp(), BaseRevision: input.BaseRevision, Patch: input.Patch,
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO index_proposals
		(id, request_id, request_hash, base_revision, title, rationale, status, submitted_by, created_at, patch_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, result.ID, input.RequestID, digest, result.BaseRevision, result.Title,
		result.Rationale, result.Status, result.SubmittedBy, result.CreatedAt, patch)
	if err != nil {
		return Proposal{}, err
	}
	return result, nil
}

// Preview applies the proposal to its original base snapshot, including after
// review. Its revision is the base revision, since a preview is not a publication.
func (s *Store) Preview(ctx context.Context, id string) (Graph, error) {
	proposal, err := s.Get(ctx, id)
	if err != nil {
		return Graph{}, err
	}
	base, err := readGraph(ctx, s.db, &proposal.BaseRevision)
	if err != nil {
		return Graph{}, err
	}
	return Apply(base, proposal.Patch)
}

func (s *Store) Review(ctx context.Context, id, actor, decision, note string) (Proposal, error) {
	return s.ReviewVersion(ctx, id, actor, decision, note, 0)
}

func (s *Store) ReviewVersion(ctx context.Context, id, actor, decision, note string, version int64) (Proposal, error) {
	actor, note = strings.TrimSpace(actor), strings.TrimSpace(note)
	next := ""
	switch decision {
	case "approve":
		next = StatusApproved
	case "reject":
		if note == "" {
			return Proposal{}, invalid("请填写驳回原因。")
		}
		next = StatusRejected
	default:
		return Proposal{}, invalid("review decision must be approve or reject")
	}
	if !validActor(actor) || !textValid(note, 16_384, true) {
		return Proposal{}, invalid("invalid reviewer or note")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Proposal{}, err
	}
	defer tx.Rollback()
	result, err := getProposal(ctx, tx, id)
	if err != nil {
		return Proposal{}, err
	}
	if result.Status != StatusPending {
		if result.Status == next && result.ReviewedBy == actor && result.ReviewNote == note {
			return result, nil
		}
		return Proposal{}, conflict("proposal has already been reviewed")
	}
	if result.EditVersion != version {
		return Proposal{}, conflict("proposal was edited; refresh before review")
	}
	if decision == "approve" {
		if err := publish(ctx, tx, result); err != nil {
			return Proposal{}, err
		}
	}
	result.Status, result.ReviewedBy, result.ReviewedAt, result.ReviewNote = next, actor, timestamp(), note
	_, err = tx.ExecContext(ctx, `UPDATE index_proposals SET status = ?, reviewed_by = ?, reviewed_at = ?, review_note = ?
		WHERE id = ? AND status = 'pending'`, result.Status, result.ReviewedBy, result.ReviewedAt, result.ReviewNote, id)
	if err != nil {
		return Proposal{}, err
	}
	return result, tx.Commit()
}

func publish(ctx context.Context, tx *sql.Tx, proposal Proposal) error {
	base, err := readGraph(ctx, tx, nil)
	if err != nil {
		return err
	}
	if base.Revision != proposal.BaseRevision {
		return conflict("proposal is based on revision %d, current revision is %d", proposal.BaseRevision, base.Revision)
	}
	if base.Revision == math.MaxInt64 {
		return conflict("index revision space has been exhausted")
	}
	candidate, err := Apply(base, proposal.Patch)
	if err != nil {
		return err
	}
	candidate.Revision++
	payload, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO index_revisions (revision, graph_json, proposal_id) VALUES (?, ?, ?)",
		candidate.Revision, payload, proposal.ID)
	return err
}

func timestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func validActor(actor string) bool {
	return actor != "" && textValid(actor, 256, false)
}

func (s *Store) SyncSource(ctx context.Context, revision int64, id, shape, previous, digest string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	graph, err := readGraph(ctx, tx, nil)
	if err != nil {
		return false, err
	}
	if graph.Revision != revision {
		return false, conflict("published source definition changed")
	}
	var prior string
	err = tx.QueryRowContext(ctx, "SELECT digest FROM source_sync WHERE node_id=? AND shape=? AND revision=?", id, shape, revision).Scan(&prior)
	if errors.Is(err, sql.ErrNoRows) {
		prior = previous
	} else if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO source_sync VALUES(?,?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET revision=excluded.revision,shape=excluded.shape,digest=excluded.digest,synced_at=excluded.synced_at`, id, revision, shape, digest, timestamp()); err != nil {
		return false, err
	}
	return prior != digest, tx.Commit()
}

// ReplaceDraft performs an operator-authorized model migration in one transaction.
// It cannot publish and requires the exact pending, unedited source proposal.
func (s *Store) ReplaceDraft(ctx context.Context, oldID, actor string, input SubmitInput) (Proposal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Proposal{}, err
	}
	defer tx.Rollback()
	old, err := getProposal(ctx, tx, oldID)
	if err != nil {
		return Proposal{}, err
	}
	p, err := submitTx(ctx, tx, actor, input)
	if err != nil {
		return Proposal{}, err
	}
	note := "建模规范更新，由新版待审提案 " + p.ID + " 取代。"
	if old.ID == p.ID || p.Status != StatusPending || old.BaseRevision != input.BaseRevision || old.EditVersion != 0 {
		return Proposal{}, conflict("source or replacement changed")
	}
	if old.Status == StatusRejected && old.ReviewNote == note && old.ReviewedBy == actor {
		return p, nil
	}
	if old.Status != StatusPending {
		return Proposal{}, conflict("source proposal is no longer pending")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE index_proposals SET status=?,reviewed_by=?,reviewed_at=?,review_note=? WHERE id=?", StatusRejected, actor, timestamp(), note, oldID); err != nil {
		return Proposal{}, err
	}
	return p, tx.Commit()
}
