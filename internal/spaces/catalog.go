// Package spaces manages access and storage for independent ontology indexes.
package spaces

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"ontology-engine/internal/index"
)

const (
	DefaultID    = "default"
	RoleOwner    = "owner"
	RoleReviewer = "reviewer"
	ScopeRead    = "read"
	ScopeSubmit  = "submit"

	applicationID = 0x4f535043
)

var (
	ErrNotFound  = errors.New("project space not found")
	ErrForbidden = errors.New("project space access denied")
	ErrConflict  = errors.New("project space conflict")
	ErrInvalid   = errors.New("invalid project space input")

	spaceIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

type Space struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type Member struct {
	Subject string `json:"subject"`
	Role    string `json:"role"`
}

var defaultSpace = Space{ID: DefaultID, Name: "默认空间", Role: RoleReviewer}

const schema = `
CREATE TABLE IF NOT EXISTS spaces (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    db_key TEXT NOT NULL UNIQUE
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS space_members (
    space_id TEXT NOT NULL REFERENCES spaces(id),
    subject TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('owner', 'reviewer')),
    PRIMARY KEY (space_id, subject)
) WITHOUT ROWID;
CREATE UNIQUE INDEX IF NOT EXISTS single_space_owner ON space_members(space_id) WHERE role = 'owner';
CREATE INDEX IF NOT EXISTS spaces_by_subject ON space_members(subject, space_id);
CREATE TABLE IF NOT EXISTS space_tokens (
    space_id TEXT NOT NULL REFERENCES spaces(id),
    scope TEXT NOT NULL CHECK (scope IN ('read', 'submit')),
    token_hash BLOB NOT NULL CHECK (length(token_hash) = 32),
    PRIMARY KEY (space_id, scope)
) WITHOUT ROWID;
`

type Catalog struct {
	db           *sql.DB
	indexRoot    string
	defaultStore *index.Store
	mutex        sync.Mutex
	stores       map[string]*index.Store
	closed       bool
}

// Open owns the catalog and named indexes. The caller owns defaultStore.
func Open(root string, defaultStore *index.Store) (*Catalog, error) {
	if strings.TrimSpace(root) == "" || defaultStore == nil {
		return nil, ErrInvalid
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	indexRoot := filepath.Join(root, "indexes")
	if err := os.MkdirAll(indexRoot, 0o700); err != nil {
		return nil, err
	}
	filename := filepath.Join(root, "catalog.db")
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	dsn := url.URL{Scheme: "file", Path: filename}
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
		return nil, fmt.Errorf("initialize project catalog: %w", err)
	}
	return &Catalog{db: db, indexRoot: indexRoot, defaultStore: defaultStore, stores: make(map[string]*index.Store)}, nil
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
		var tables int
		if err := tx.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return err
		}
		if tables != 0 {
			return errors.New("database belongs to another application")
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA application_id = %d", applicationID)); err != nil {
			return err
		}
	case applicationID:
	default:
		return errors.New("database belongs to another application")
	}
	if _, err := tx.Exec(schema); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = db.Exec("PRAGMA journal_mode = WAL")
	return err
}

func (c *Catalog) Close() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	errs := []error{c.db.Close()}
	for _, store := range c.stores {
		errs = append(errs, store.Close())
	}
	return errors.Join(errs...)
}

func (c *Catalog) List(ctx context.Context, subject string) ([]Space, error) {
	if !validSubject(subject) {
		return nil, ErrForbidden
	}
	rows, err := c.db.QueryContext(ctx, `SELECT p.id, p.name, m.role FROM space_members m
        JOIN spaces p ON p.id = m.space_id WHERE m.subject = ? ORDER BY p.id`, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Space{defaultSpace}
	for rows.Next() {
		var space Space
		if err := rows.Scan(&space.ID, &space.Name, &space.Role); err != nil {
			return nil, err
		}
		result = append(result, space)
	}
	return result, rows.Err()
}

func (c *Catalog) Create(ctx context.Context, subject, id, name string) (Space, error) {
	name = strings.TrimSpace(name)
	if !validNamedID(id) || !validLabel(name, 120) || !validSubject(subject) {
		return Space{}, ErrInvalid
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Space{}, err
	}
	key := hex.EncodeToString(nonce[:])
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Space{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO spaces (id, name, db_key) VALUES (?, ?, ?) ON CONFLICT(id) DO NOTHING`, id, name, key)
	if err != nil {
		return Space{}, err
	}
	if err := requireRow(result, ErrConflict); err != nil {
		return Space{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO space_members (space_id, subject, role) VALUES (?, ?, ?)`, id, subject, RoleOwner); err != nil {
		return Space{}, err
	}
	if err := createIndex(c.indexPath(key)); err != nil {
		return Space{}, fmt.Errorf("initialize project index: %w", err)
	}
	// Once commit begins its outcome is authoritative. A failed commit leaves
	// any disconnected file with a unique key that cannot be reused by a retry.
	if err := tx.Commit(); err != nil {
		return Space{}, err
	}
	return Space{ID: id, Name: name, Role: RoleOwner}, nil
}

func createIndex(filename string) error {
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(filename)
		return err
	}
	store, err := index.Open(filename)
	if err != nil {
		_ = os.Remove(filename)
		return err
	}
	return store.Close()
}

// Store resolves an already authorized project ID. HTTP handlers must authorize
// the human or machine identity independently on each incoming request.
func (c *Catalog) Store(ctx context.Context, id string) (*index.Store, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.closed {
		return nil, sql.ErrConnDone
	}
	if id == DefaultID {
		return c.defaultStore, nil
	}
	if !validNamedID(id) {
		return nil, ErrNotFound
	}
	if store, found := c.stores[id]; found {
		return store, nil
	}
	var key string
	if err := c.db.QueryRowContext(ctx, "SELECT db_key FROM spaces WHERE id = ?", id).Scan(&key); err != nil {
		return nil, onMissing(err)
	}
	decoded, err := hex.DecodeString(key)
	if err != nil || len(decoded) != 16 {
		return nil, errors.New("invalid stored project index key")
	}
	filename := c.indexPath(key)
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, fmt.Errorf("locate project index: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("project index is not a regular file")
	}
	store, err := index.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("open project index: %w", err)
	}
	c.stores[id] = store
	return store, nil
}

func (c *Catalog) indexPath(key string) string {
	return filepath.Join(c.indexRoot, key+".db")
}

func validNamedID(id string) bool {
	return len(id) <= 48 && id != DefaultID && id != "sse" && spaceIDPattern.MatchString(id)
}

func validSubject(subject string) bool {
	return strings.TrimSpace(subject) == subject && validLabel(subject, 256)
}

func validLabel(label string, maxRunes int) bool {
	return label != "" && utf8.ValidString(label) && utf8.RuneCountInString(label) <= maxRunes &&
		strings.IndexFunc(label, unicode.IsControl) < 0
}

func onMissing(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func requireRow(result sql.Result, absent error) error {
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return absent
	}
	return err
}
