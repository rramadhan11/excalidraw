// Package access is the single place that decides whether a user may act on a
// workspace, collection or board. Stores call it before every operation.
//
// Today access comes only from workspace membership. Sharing a board with
// users outside its workspace will add a board_shares lookup to Board(), and
// nothing else has to change.
package access

import (
	"context"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/apperr"
)

type Permission int

const (
	Read Permission = iota
	Write
	// Manage covers renaming/deleting the workspace itself.
	Manage
)

type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidUUID guards queries: Postgres rejects malformed uuid parameters with an
// error, which must surface as "not found" rather than a 500.
func ValidUUID(id string) bool {
	return uuidPattern.MatchString(id)
}

func allows(role Role, perm Permission) bool {
	switch perm {
	case Read, Write:
		return role == RoleOwner || role == RoleMember
	case Manage:
		return role == RoleOwner
	}
	return false
}

type Checker struct {
	pool *pgxpool.Pool
}

func NewChecker(pool *pgxpool.Pool) *Checker {
	return &Checker{pool: pool}
}

// decide turns a membership lookup into the access decision: non-members get
// apperr.ErrNotFound, members lacking the permission get apperr.ErrForbidden.
func decide(err error, role *Role, perm Permission) error {
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && role == nil) {
		return apperr.ErrNotFound
	}
	if err != nil {
		return err
	}
	if !allows(*role, perm) {
		return apperr.ErrForbidden
	}
	return nil
}

// Workspace returns the user's role in the workspace.
func (c *Checker) Workspace(ctx context.Context, userID, workspaceID string, perm Permission) (Role, error) {
	if !ValidUUID(workspaceID) {
		return "", apperr.ErrNotFound
	}
	var role *Role
	err := c.pool.QueryRow(ctx,
		`SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`,
		workspaceID, userID,
	).Scan(&role)
	if err := decide(err, role, perm); err != nil {
		return "", err
	}
	return *role, nil
}

// Board returns the id of the workspace the board belongs to.
func (c *Checker) Board(ctx context.Context, userID, boardID string, perm Permission) (workspaceID string, err error) {
	if !ValidUUID(boardID) {
		return "", apperr.ErrNotFound
	}
	var role *Role
	err = c.pool.QueryRow(ctx,
		`SELECT b.workspace_id, m.role
		   FROM boards b
		   LEFT JOIN workspace_members m ON m.workspace_id = b.workspace_id AND m.user_id = $2
		  WHERE b.id = $1`,
		boardID, userID,
	).Scan(&workspaceID, &role)
	if err := decide(err, role, perm); err != nil {
		return "", err
	}
	return workspaceID, nil
}

// Collection returns the id of the workspace the collection belongs to.
func (c *Checker) Collection(ctx context.Context, userID, collectionID string, perm Permission) (workspaceID string, err error) {
	if !ValidUUID(collectionID) {
		return "", apperr.ErrNotFound
	}
	var role *Role
	err = c.pool.QueryRow(ctx,
		`SELECT c.workspace_id, m.role
		   FROM collections c
		   LEFT JOIN workspace_members m ON m.workspace_id = c.workspace_id AND m.user_id = $2
		  WHERE c.id = $1`,
		collectionID, userID,
	).Scan(&workspaceID, &role)
	if err := decide(err, role, perm); err != nil {
		return "", err
	}
	return workspaceID, nil
}
