package spaces

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// RotateToken returns a fresh credential once. Only its digest is persisted.
func (c *Catalog) RotateToken(ctx context.Context, id, actor, scope string) (string, error) {
	if scope != ScopeRead && scope != ScopeSubmit {
		return "", ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := requireOwner(ctx, tx, id, actor); err != nil {
		return "", err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	bearer := "idx_" + base64.RawURLEncoding.EncodeToString(secret[:])
	digest := sha256.Sum256([]byte(bearer))
	if _, err := tx.ExecContext(ctx, `INSERT INTO space_tokens (space_id, scope, token_hash) VALUES (?, ?, ?)
        ON CONFLICT(space_id, scope) DO UPDATE SET token_hash = excluded.token_hash`, id, scope, digest[:]); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return bearer, nil
}

// VerifyToken authenticates both transport types against the current rotation.
func (c *Catalog) VerifyToken(ctx context.Context, id, bearer string) (string, error) {
	if !validNamedID(id) || len(bearer) != len("idx_")+base64.RawURLEncoding.EncodedLen(32) {
		return "", ErrForbidden
	}
	digest := sha256.Sum256([]byte(bearer))
	rows, err := c.db.QueryContext(ctx, `SELECT scope, token_hash FROM space_tokens WHERE space_id = ?`, id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var matched string
	for rows.Next() {
		var scope string
		var stored []byte
		if err := rows.Scan(&scope, &stored); err != nil {
			return "", err
		}
		if subtle.ConstantTimeCompare(stored, digest[:]) == 1 {
			matched = scope
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if matched != ScopeRead && matched != ScopeSubmit {
		return "", ErrForbidden
	}
	return matched, nil
}
