// Package workspaces manages workspaces, the unit that owns boards and
// collections and that users are members of.
package workspaces

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/access"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/apperr"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/auth"
)

const maxNameLen = 100

type Workspace struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Role       access.Role `json:"role"`
	BoardCount int64       `json:"boardCount"`
	CreatedAt  time.Time   `json:"createdAt"`
	UpdatedAt  time.Time   `json:"updatedAt"`
}

func NormalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		return "", apperr.Invalid("workspace name must be 1-%d characters", maxNameLen)
	}
	return name, nil
}

// PersonalName is the name of the workspace every new user starts with.
func PersonalName(userName string) string {
	name := userName + "'s workspace"
	if utf8.RuneCountInString(name) > maxNameLen {
		name = string([]rune(name)[:maxNameLen])
	}
	return name
}

// CreateInTx creates a workspace with userID as its owner inside tx, so it can
// be part of a larger transaction (e.g. creating the user itself).
func CreateInTx(ctx context.Context, tx pgx.Tx, userID, name string) (Workspace, error) {
	name, err := NormalizeName(name)
	if err != nil {
		return Workspace{}, err
	}
	w := Workspace{Name: name, Role: access.RoleOwner}
	err = tx.QueryRow(ctx,
		`INSERT INTO workspaces (name) VALUES ($1) RETURNING id, created_at, updated_at`,
		name,
	).Scan(&w.ID, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return Workspace{}, err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, $3)`,
		w.ID, userID, access.RoleOwner,
	)
	return w, err
}

// ProvisionPersonal gives a new user their own workspace. It is passed to
// auth.Users.Create so it runs in the user's creation transaction.
func ProvisionPersonal(ctx context.Context, tx pgx.Tx, user auth.User) error {
	_, err := CreateInTx(ctx, tx, user.ID, PersonalName(user.Name))
	return err
}

type Store struct {
	pool   *pgxpool.Pool
	access *access.Checker
}

func NewStore(pool *pgxpool.Pool, checker *access.Checker) *Store {
	return &Store{pool: pool, access: checker}
}

const selectWorkspace = `
	SELECT w.id, w.name, m.role, w.created_at, w.updated_at,
	       (SELECT count(*) FROM boards b WHERE b.workspace_id = w.id)
	  FROM workspace_members m
	  JOIN workspaces w ON w.id = m.workspace_id`

func scanWorkspace(row pgx.Row) (Workspace, error) {
	var w Workspace
	err := row.Scan(&w.ID, &w.Name, &w.Role, &w.CreatedAt, &w.UpdatedAt, &w.BoardCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, apperr.ErrNotFound
	}
	return w, err
}

// List returns the workspaces the user is a member of, by name.
func (s *Store) List(ctx context.Context, userID string) ([]Workspace, error) {
	rows, err := s.pool.Query(ctx,
		selectWorkspace+` WHERE m.user_id = $1 ORDER BY lower(w.name), w.id`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Workspace, error) {
		return scanWorkspace(row)
	})
}

func (s *Store) get(ctx context.Context, userID, workspaceID string) (Workspace, error) {
	return scanWorkspace(s.pool.QueryRow(ctx,
		selectWorkspace+` WHERE m.user_id = $1 AND w.id = $2`,
		userID, workspaceID,
	))
}

func (s *Store) Create(ctx context.Context, userID, name string) (Workspace, error) {
	var w Workspace
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		w, err = CreateInTx(ctx, tx, userID, name)
		return err
	})
	return w, err
}

func (s *Store) Rename(ctx context.Context, userID, workspaceID, name string) (Workspace, error) {
	if _, err := s.access.Workspace(ctx, userID, workspaceID, access.Manage); err != nil {
		return Workspace{}, err
	}
	name, err := NormalizeName(name)
	if err != nil {
		return Workspace{}, err
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE workspaces SET name = $2, updated_at = now() WHERE id = $1`,
		workspaceID, name,
	); err != nil {
		return Workspace{}, err
	}
	return s.get(ctx, userID, workspaceID)
}

// Delete removes an empty workspace (its collections go with it). A workspace
// that still has boards, or the user's only workspace, is refused.
func (s *Store) Delete(ctx context.Context, userID, workspaceID string) error {
	if _, err := s.access.Workspace(ctx, userID, workspaceID, access.Manage); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var memberships int
		// lock the user's memberships so two concurrent deletes can't both
		// pass the "not the last workspace" check
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM (SELECT 1 FROM workspace_members WHERE user_id = $1 FOR UPDATE) m`,
			userID,
		).Scan(&memberships); err != nil {
			return err
		}
		if memberships <= 1 {
			return apperr.Conflict("last_workspace", "you can't delete your only workspace")
		}
		_, err := tx.Exec(ctx, `DELETE FROM workspaces WHERE id = $1`, workspaceID)
		var pgErr *pgconn.PgError
		// boards.workspace_id is ON DELETE RESTRICT
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return apperr.Conflict("workspace_not_empty",
				"this workspace still has boards; delete them first")
		}
		return err
	})
}
