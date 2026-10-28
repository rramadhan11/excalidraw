// Package boards stores boards, their image files and thumbnails. Every
// exported Store method checks access through the access package first, so
// handlers cannot forget it.
package boards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/access"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/apperr"
)

var (
	ErrNotFound           = apperr.NotFound("board not found")
	ErrCollectionNotFound = apperr.NotFound("collection not found in this workspace")
)

// ConflictError means the scene was saved by someone else (another tab,
// device or member) since the client last loaded it.
type ConflictError struct{ CurrentVersion int64 }

func (e *ConflictError) Error() string { return "board was modified elsewhere" }

type UserRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Summary struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	WorkspaceID  string  `json:"workspaceId"`
	CollectionID *string `json:"collectionId"`
	// nil once the creator's account is deleted
	CreatedBy *UserRef  `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	// last change to the content or name (not to its collection)
	UpdatedAt          time.Time  `json:"updatedAt"`
	ThumbnailUpdatedAt *time.Time `json:"thumbnailUpdatedAt"`
}

type Board struct {
	Summary
	Version int64           `json:"version"`
	Scene   json.RawMessage `json:"scene"`
}

// File mirrors excalidraw's BinaryFileData.
type File struct {
	ID       string `json:"id"`
	MimeType string `json:"mimeType"`
	DataURL  string `json:"dataURL"`
	Created  int64  `json:"created"`
	Version  int32  `json:"version"`
}

type Thumbnail struct {
	MimeType  string
	Data      []byte
	UpdatedAt time.Time
}

// Update is a partial board update. A nil field is left unchanged; to move a
// board to "unsorted" set MoveToCollection with a nil CollectionID.
type Update struct {
	Name             *string
	MoveToCollection bool
	CollectionID     *string
}

var emptyScene = json.RawMessage(`{"elements":[],"appState":{}}`)

type Store struct {
	pool   *pgxpool.Pool
	access *access.Checker
}

func NewStore(pool *pgxpool.Pool, checker *access.Checker) *Store {
	return &Store{pool: pool, access: checker}
}

// boardAccess maps "no access" to the board-specific not-found error.
func (s *Store) boardAccess(ctx context.Context, userID, boardID string, perm access.Permission) error {
	_, err := s.access.Board(ctx, userID, boardID, perm)
	if errors.Is(err, apperr.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

const (
	summaryColumns = `b.id, b.name, b.workspace_id, b.collection_id, b.created_at, b.updated_at,
	       u.id, u.name, t.updated_at`
	boardJoins = `
	  FROM boards b
	  LEFT JOIN users u ON u.id = b.created_by
	  LEFT JOIN board_thumbnails t ON t.board_id = b.id`
	selectSummary = "SELECT " + summaryColumns + boardJoins
	selectBoard   = "SELECT " + summaryColumns + ", b.version, b.scene::text" + boardJoins
)

func scanSummary(row pgx.Row, extra ...any) (Summary, error) {
	var b Summary
	var creatorID, creatorName *string
	dest := append([]any{
		&b.ID, &b.Name, &b.WorkspaceID, &b.CollectionID, &b.CreatedAt, &b.UpdatedAt,
		&creatorID, &creatorName, &b.ThumbnailUpdatedAt,
	}, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Summary{}, ErrNotFound
		}
		return Summary{}, err
	}
	if creatorID != nil {
		b.CreatedBy = &UserRef{ID: *creatorID, Name: *creatorName}
	}
	return b, nil
}

// List returns one page of a workspace's boards, most recently updated first,
// and the cursor of the next page ("" when there is none).
func (s *Store) List(ctx context.Context, userID, workspaceID string, filter CollectionFilter, limit int, after *cursor) ([]Summary, string, error) {
	if _, err := s.access.Workspace(ctx, userID, workspaceID, access.Read); err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > maxPageSize {
		limit = defaultPageSize
	}

	conditions := []string{"b.workspace_id = $1"}
	args := []any{workspaceID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	switch {
	case filter.Unsorted:
		conditions = append(conditions, "b.collection_id IS NULL")
	case filter.CollectionID != "":
		conditions = append(conditions, "b.collection_id = "+arg(filter.CollectionID))
	}
	if after != nil {
		conditions = append(conditions,
			fmt.Sprintf("(b.updated_at, b.id) < (%s, %s)", arg(after.UpdatedAt), arg(after.ID)))
	}
	// one extra row tells whether there is a next page
	query := selectSummary + " WHERE " + strings.Join(conditions, " AND ") +
		" ORDER BY b.updated_at DESC, b.id DESC LIMIT " + arg(limit+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	boards, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Summary, error) {
		return scanSummary(row)
	})
	if err != nil {
		return nil, "", err
	}

	next := ""
	if len(boards) > limit {
		boards = boards[:limit]
		last := boards[limit-1]
		next = cursor{UpdatedAt: last.UpdatedAt, ID: last.ID}.encode()
	}
	return boards, next, nil
}

func (s *Store) Create(ctx context.Context, userID, workspaceID, name string, collectionID *string) (Board, error) {
	if _, err := s.access.Workspace(ctx, userID, workspaceID, access.Write); err != nil {
		return Board{}, err
	}
	name, err := normalizeName(name, true)
	if err != nil {
		return Board{}, err
	}
	if collectionID != nil && !access.ValidUUID(*collectionID) {
		return Board{}, ErrCollectionNotFound
	}

	// the collection must belong to the same workspace; checked in the same
	// statement as the insert
	var id string
	err = s.pool.QueryRow(ctx,
		`INSERT INTO boards (workspace_id, created_by, name, scene, collection_id)
		 SELECT $1, $2, $3, $4, $5::uuid
		  WHERE $5::uuid IS NULL
		     OR EXISTS (SELECT 1 FROM collections c WHERE c.id = $5::uuid AND c.workspace_id = $1)
		 RETURNING id`,
		workspaceID, userID, name, string(emptyScene), collectionID,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Board{}, ErrCollectionNotFound
	}
	if err != nil {
		return Board{}, err
	}
	return s.get(ctx, id)
}

func (s *Store) get(ctx context.Context, boardID string) (Board, error) {
	var b Board
	var scene string
	summary, err := scanSummary(
		s.pool.QueryRow(ctx, selectBoard+` WHERE b.id = $1`, boardID),
		&b.Version, &scene,
	)
	if err != nil {
		return Board{}, err
	}
	b.Summary = summary
	b.Scene = json.RawMessage(scene)
	return b, nil
}

func (s *Store) Get(ctx context.Context, userID, boardID string) (Board, error) {
	if err := s.boardAccess(ctx, userID, boardID, access.Read); err != nil {
		return Board{}, err
	}
	return s.get(ctx, boardID)
}

// SaveScene stores the scene only if the board is still at expectedVersion
// and returns the new version. Otherwise it returns *ConflictError.
func (s *Store) SaveScene(ctx context.Context, userID, boardID string, expectedVersion int64, scene json.RawMessage) (int64, time.Time, error) {
	if err := s.boardAccess(ctx, userID, boardID, access.Write); err != nil {
		return 0, time.Time{}, err
	}
	scene, err := normalizeScene(scene)
	if err != nil {
		return 0, time.Time{}, err
	}

	var version int64
	var updatedAt time.Time
	err = s.pool.QueryRow(ctx,
		`UPDATE boards SET scene = $3, version = version + 1, updated_at = now()
		  WHERE id = $1 AND version = $2
		 RETURNING version, updated_at`,
		boardID, expectedVersion, string(scene),
	).Scan(&version, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		var current int64
		err := s.pool.QueryRow(ctx, `SELECT version FROM boards WHERE id = $1`, boardID).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, time.Time{}, ErrNotFound
		}
		if err != nil {
			return 0, time.Time{}, err
		}
		return 0, time.Time{}, &ConflictError{CurrentVersion: current}
	}
	return version, updatedAt, err
}

// Update renames and/or moves a board. Renaming counts as a modification
// (bumps updated_at); moving between collections does not.
func (s *Store) Update(ctx context.Context, userID, boardID string, u Update) (Summary, error) {
	if err := s.boardAccess(ctx, userID, boardID, access.Write); err != nil {
		return Summary{}, err
	}
	if u.Name == nil && !u.MoveToCollection {
		return Summary{}, apperr.Invalid("nothing to update: send name and/or collectionId")
	}
	if u.Name != nil {
		name, err := normalizeName(*u.Name, false)
		if err != nil {
			return Summary{}, err
		}
		u.Name = &name
	}
	if u.CollectionID != nil && !access.ValidUUID(*u.CollectionID) {
		return Summary{}, ErrCollectionNotFound
	}

	// the target collection must belong to the board's own workspace; checked
	// in the same statement as the update
	tag, err := s.pool.Exec(ctx,
		`UPDATE boards SET
		    name = COALESCE($2, name),
		    updated_at = CASE WHEN $2::text IS NULL THEN updated_at ELSE now() END,
		    collection_id = CASE WHEN $3 THEN $4::uuid ELSE collection_id END
		  WHERE id = $1
		    AND (NOT $3 OR $4::uuid IS NULL OR EXISTS (
		          SELECT 1 FROM collections c
		           WHERE c.id = $4::uuid AND c.workspace_id = boards.workspace_id))`,
		boardID, u.Name, u.MoveToCollection, u.CollectionID,
	)
	if err != nil {
		return Summary{}, err
	}
	if tag.RowsAffected() == 0 {
		if u.MoveToCollection && u.CollectionID != nil {
			return Summary{}, ErrCollectionNotFound
		}
		return Summary{}, ErrNotFound
	}
	return scanSummary(s.pool.QueryRow(ctx, selectSummary+` WHERE b.id = $1`, boardID))
}

// Delete removes the board; its files and thumbnail go with it (ON DELETE
// CASCADE).
func (s *Store) Delete(ctx context.Context, userID, boardID string) error {
	if err := s.boardAccess(ctx, userID, boardID, access.Write); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM boards WHERE id = $1`, boardID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// PutFile stores a file. Re-uploading the same or an older version is a
// no-op, so retries are safe; a newer version replaces the stored one.
func (s *Store) PutFile(ctx context.Context, userID, boardID string, f File) error {
	if err := s.boardAccess(ctx, userID, boardID, access.Write); err != nil {
		return err
	}
	if err := validateFile(f); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO board_files (board_id, file_id, mime_type, data_url, file_created, version)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (board_id, file_id) DO UPDATE
		    SET mime_type = EXCLUDED.mime_type,
		        data_url = EXCLUDED.data_url,
		        file_created = EXCLUDED.file_created,
		        version = EXCLUDED.version,
		        updated_at = now()
		  WHERE board_files.version < EXCLUDED.version`,
		boardID, f.ID, f.MimeType, f.DataURL, f.Created, f.Version,
	)
	return err
}

// GetFiles returns the requested files that exist and the ids that do not.
func (s *Store) GetFiles(ctx context.Context, userID, boardID string, fileIDs []string) ([]File, []string, error) {
	if err := s.boardAccess(ctx, userID, boardID, access.Read); err != nil {
		return nil, nil, err
	}
	if len(fileIDs) > maxFileIDs {
		return nil, nil, apperr.Invalid("at most %d file ids per request", maxFileIDs)
	}
	for _, id := range fileIDs {
		if err := validateFileID(id); err != nil {
			return nil, nil, err
		}
	}

	rows, err := s.pool.Query(ctx,
		`SELECT file_id, mime_type, data_url, file_created, version
		   FROM board_files WHERE board_id = $1 AND file_id = ANY($2)`,
		boardID, fileIDs,
	)
	if err != nil {
		return nil, nil, err
	}
	files, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (File, error) {
		var f File
		err := row.Scan(&f.ID, &f.MimeType, &f.DataURL, &f.Created, &f.Version)
		return f, err
	})
	if err != nil {
		return nil, nil, err
	}

	found := make(map[string]bool, len(files))
	for _, f := range files {
		found[f.ID] = true
	}
	missing := []string{}
	for _, id := range fileIDs {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	return files, missing, nil
}

func (s *Store) PutThumbnail(ctx context.Context, userID, boardID, mimeType string, data []byte) (time.Time, error) {
	if err := s.boardAccess(ctx, userID, boardID, access.Write); err != nil {
		return time.Time{}, err
	}
	if err := validateThumbnail(mimeType, data); err != nil {
		return time.Time{}, err
	}
	var updatedAt time.Time
	err := s.pool.QueryRow(ctx,
		`INSERT INTO board_thumbnails (board_id, mime_type, data) VALUES ($1, $2, $3)
		 ON CONFLICT (board_id) DO UPDATE
		    SET mime_type = EXCLUDED.mime_type, data = EXCLUDED.data, updated_at = now()
		 RETURNING updated_at`,
		boardID, mimeType, data,
	).Scan(&updatedAt)
	return updatedAt, err
}

func (s *Store) DeleteThumbnail(ctx context.Context, userID, boardID string) error {
	if err := s.boardAccess(ctx, userID, boardID, access.Write); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM board_thumbnails WHERE board_id = $1`, boardID)
	return err
}

func (s *Store) GetThumbnail(ctx context.Context, userID, boardID string) (Thumbnail, error) {
	if err := s.boardAccess(ctx, userID, boardID, access.Read); err != nil {
		return Thumbnail{}, err
	}
	var t Thumbnail
	err := s.pool.QueryRow(ctx,
		`SELECT mime_type, data, updated_at FROM board_thumbnails WHERE board_id = $1`,
		boardID,
	).Scan(&t.MimeType, &t.Data, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Thumbnail{}, apperr.NotFound("board has no thumbnail")
	}
	return t, err
}
