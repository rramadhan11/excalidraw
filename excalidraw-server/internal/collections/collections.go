// Package collections manages collections: folders that group the boards of a
// workspace. A board is in at most one collection; boards without one are
// "unsorted".
package collections

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/access"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/apperr"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/auth"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/httpx"
)

const (
	maxNameLen = 100
	maxBody    = 16 << 10
)

type Collection struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Name        string    `json:"name"`
	BoardCount  int64     `json:"boardCount"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		return "", apperr.Invalid("collection name must be 1-%d characters", maxNameLen)
	}
	return name, nil
}

type Store struct {
	pool   *pgxpool.Pool
	access *access.Checker
}

func NewStore(pool *pgxpool.Pool, checker *access.Checker) *Store {
	return &Store{pool: pool, access: checker}
}

const selectCollection = `
	SELECT c.id, c.workspace_id, c.name, c.created_at, c.updated_at,
	       (SELECT count(*) FROM boards b WHERE b.collection_id = c.id)
	  FROM collections c`

func scanCollection(row pgx.Row) (Collection, error) {
	var c Collection
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.CreatedAt, &c.UpdatedAt, &c.BoardCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return Collection{}, apperr.ErrNotFound
	}
	return c, err
}

func (s *Store) List(ctx context.Context, userID, workspaceID string) ([]Collection, error) {
	if _, err := s.access.Workspace(ctx, userID, workspaceID, access.Read); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		selectCollection+` WHERE c.workspace_id = $1 ORDER BY lower(c.name), c.id`,
		workspaceID,
	)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Collection, error) {
		return scanCollection(row)
	})
}

func (s *Store) Create(ctx context.Context, userID, workspaceID, name string) (Collection, error) {
	if _, err := s.access.Workspace(ctx, userID, workspaceID, access.Write); err != nil {
		return Collection{}, err
	}
	name, err := normalizeName(name)
	if err != nil {
		return Collection{}, err
	}
	var id string
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO collections (workspace_id, name, created_by) VALUES ($1, $2, $3) RETURNING id`,
		workspaceID, name, userID,
	).Scan(&id); err != nil {
		return Collection{}, err
	}
	return scanCollection(s.pool.QueryRow(ctx, selectCollection+` WHERE c.id = $1`, id))
}

func (s *Store) Rename(ctx context.Context, userID, collectionID, name string) (Collection, error) {
	if _, err := s.access.Collection(ctx, userID, collectionID, access.Write); err != nil {
		return Collection{}, err
	}
	name, err := normalizeName(name)
	if err != nil {
		return Collection{}, err
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE collections SET name = $2, updated_at = now() WHERE id = $1`,
		collectionID, name,
	); err != nil {
		return Collection{}, err
	}
	return scanCollection(s.pool.QueryRow(ctx, selectCollection+` WHERE c.id = $1`, collectionID))
}

// Delete removes the collection; its boards become unsorted (ON DELETE SET
// NULL), they are never deleted with it.
func (s *Store) Delete(ctx context.Context, userID, collectionID string) error {
	if _, err := s.access.Collection(ctx, userID, collectionID, access.Write); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM collections WHERE id = $1`, collectionID)
	return err
}

// ---------------------------------------------------------------------------

type Handler struct {
	store  *Store
	logger *slog.Logger
}

func NewHandler(store *Store, logger *slog.Logger) *Handler {
	return &Handler{store: store, logger: logger}
}

// Register mounts the collection routes; wrap must enforce authentication.
func (h *Handler) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/workspaces/{id}/collections", wrap(http.HandlerFunc(h.list)))
	mux.Handle("POST /api/workspaces/{id}/collections", wrap(http.HandlerFunc(h.create)))
	mux.Handle("PATCH /api/collections/{id}", wrap(http.HandlerFunc(h.rename)))
	mux.Handle("DELETE /api/collections/{id}", wrap(http.HandlerFunc(h.delete)))
}

func userID(r *http.Request) string {
	user, _ := auth.UserFrom(r.Context())
	return user.ID
}

type nameRequest struct {
	Name string `json:"name"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.List(r.Context(), userID(r), r.PathValue("id"))
	if err != nil {
		apperr.Write(w, h.logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"collections": list})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req nameRequest
	if err := httpx.DecodeJSON(w, r, maxBody, &req); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	c, err := h.store.Create(r.Context(), userID(r), r.PathValue("id"), req.Name)
	if err != nil {
		apperr.Write(w, h.logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, c)
}

func (h *Handler) rename(w http.ResponseWriter, r *http.Request) {
	var req nameRequest
	if err := httpx.DecodeJSON(w, r, maxBody, &req); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	c, err := h.store.Rename(r.Context(), userID(r), r.PathValue("id"), req.Name)
	if err != nil {
		apperr.Write(w, h.logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Delete(r.Context(), userID(r), r.PathValue("id")); err != nil {
		apperr.Write(w, h.logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}
