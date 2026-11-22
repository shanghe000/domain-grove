package spaces

import (
	"context"
	"database/sql"
)

type rowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (c *Catalog) Authorize(ctx context.Context, subject, id string) (Space, error) {
	return authorize(ctx, c.db, subject, id)
}

func authorize(ctx context.Context, reader rowReader, subject, id string) (Space, error) {
	if !validSubject(subject) {
		return Space{}, ErrNotFound
	}
	if id == DefaultID {
		return defaultSpace, nil
	}
	if !validNamedID(id) {
		return Space{}, ErrNotFound
	}
	var space Space
	err := reader.QueryRowContext(ctx, `SELECT p.id, p.name, m.role FROM spaces p
        JOIN space_members m ON m.space_id = p.id WHERE p.id = ? AND m.subject = ?`, id, subject).
		Scan(&space.ID, &space.Name, &space.Role)
	return space, onMissing(err)
}

func requireOwner(ctx context.Context, reader rowReader, id, actor string) error {
	space, err := authorize(ctx, reader, actor, id)
	if err != nil {
		return err
	}
	if space.Role != RoleOwner {
		return ErrForbidden
	}
	return nil
}

func (c *Catalog) Members(ctx context.Context, id, actor string) ([]Member, error) {
	if err := requireOwner(ctx, c.db, id, actor); err != nil {
		return nil, err
	}
	rows, err := c.db.QueryContext(ctx, `SELECT subject, role FROM space_members WHERE space_id = ? ORDER BY role, subject`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Member{}
	for rows.Next() {
		var member Member
		if err := rows.Scan(&member.Subject, &member.Role); err != nil {
			return nil, err
		}
		result = append(result, member)
	}
	return result, rows.Err()
}

func (c *Catalog) AddMember(ctx context.Context, id, actor, subject string) (Member, error) {
	if !validSubject(subject) {
		return Member{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Member{}, err
	}
	defer tx.Rollback()
	if err := requireOwner(ctx, tx, id, actor); err != nil {
		return Member{}, err
	}
	if actor == subject {
		return Member{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO space_members (space_id, subject, role)
        VALUES (?, ?, ?) ON CONFLICT(space_id, subject) DO NOTHING`, id, subject, RoleReviewer); err != nil {
		return Member{}, err
	}
	if err := tx.Commit(); err != nil {
		return Member{}, err
	}
	return Member{Subject: subject, Role: RoleReviewer}, nil
}

func (c *Catalog) RemoveMember(ctx context.Context, id, actor, subject string) error {
	if !validSubject(subject) {
		return ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireOwner(ctx, tx, id, actor); err != nil {
		return err
	}
	if actor == subject {
		return ErrForbidden
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM space_members WHERE space_id = ? AND subject = ? AND role = ?`, id, subject, RoleReviewer)
	if err != nil {
		return err
	}
	if err := requireRow(result, ErrNotFound); err != nil {
		return err
	}
	return tx.Commit()
}
