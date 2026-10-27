// Package boards stores boards and their image files. Every exported Store
// method checks access through authorize, so handlers cannot forget it.
package boards

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is also returned for boards the user may not access, so board
// ids owned by others are indistinguishable from nonexistent ones.
var ErrNotFound = errors.New("board not found")

// ConflictError means the scene was saved by someone else (another tab or
// device) since the client last loaded it.
type ConflictError struct{ CurrentVersion int64 }

func (e *ConflictError) Error() string { return "board was modified elsewhere" }

type Summary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
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

var emptyScene = json.RawMessage(`{"elements":[],"appState":{}}`)

type Permission int

const (
	PermRead Permission = iota
	PermWrite
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// authorize is the single access-control decision for boards. Today only the
// owner has access, for any permission; board sharing will extend this
// function (e.g. a board_members lookup) without touching callers.
func (s *Store) authorize(ctx context.Context, userID, boardID string, _ Permission) error {
	if err := validateBoardID(boardID); err != nil {
		return ErrNotFound
	}
	var ownerID string
	err := s.pool.QueryRow(ctx, `SELECT owner_id FROM boards WHERE id = $1`, boardID).Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && ownerID != userID) {
		return ErrNotFound
	}
	return err
}

func (s *Store) List(ctx context.Context, userID string) ([]Summary, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, created_at, updated_at FROM boards
		  WHERE owner_id = $1 ORDER BY updated_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Summary, error) {
		var b Summary
		err := row.Scan(&b.ID, &b.Name, &b.CreatedAt, &b.UpdatedAt)
		return b, err
	})
}

func (s *Store) Create(ctx context.Context, userID, name string) (Board, error) {
	name, err := normalizeName(name, true)
	if err != nil {
		return Board{}, err
	}
	b := Board{Scene: emptyScene}
	err = s.pool.QueryRow(ctx,
		`INSERT INTO boards (owner_id, name, scene) VALUES ($1, $2, $3)
		 RETURNING id, name, version, created_at, updated_at`,
		userID, name, string(emptyScene),
	).Scan(&b.ID, &b.Name, &b.Version, &b.CreatedAt, &b.UpdatedAt)
	return b, err
}

func (s *Store) Get(ctx context.Context, userID, boardID string) (Board, error) {
	if err := s.authorize(ctx, userID, boardID, PermRead); err != nil {
		return Board{}, err
	}
	var b Board
	var scene string
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, version, scene::text, created_at, updated_at FROM boards WHERE id = $1`,
		boardID,
	).Scan(&b.ID, &b.Name, &b.Version, &scene, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Board{}, ErrNotFound
	}
	b.Scene = json.RawMessage(scene)
	return b, err
}

// SaveScene stores the scene only if the board is still at expectedVersion
// and returns the new version. Otherwise it returns *ConflictError.
func (s *Store) SaveScene(ctx context.Context, userID, boardID string, expectedVersion int64, scene json.RawMessage) (int64, time.Time, error) {
	if err := s.authorize(ctx, userID, boardID, PermWrite); err != nil {
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

func (s *Store) Rename(ctx context.Context, userID, boardID, name string) (Summary, error) {
	if err := s.authorize(ctx, userID, boardID, PermWrite); err != nil {
		return Summary{}, err
	}
	name, err := normalizeName(name, false)
	if err != nil {
		return Summary{}, err
	}
	var b Summary
	err = s.pool.QueryRow(ctx,
		`UPDATE boards SET name = $2, updated_at = now() WHERE id = $1
		 RETURNING id, name, created_at, updated_at`,
		boardID, name,
	).Scan(&b.ID, &b.Name, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Summary{}, ErrNotFound
	}
	return b, err
}

// Delete removes the board; its files go with it via ON DELETE CASCADE.
func (s *Store) Delete(ctx context.Context, userID, boardID string) error {
	if err := s.authorize(ctx, userID, boardID, PermWrite); err != nil {
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
	if err := s.authorize(ctx, userID, boardID, PermWrite); err != nil {
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
	if err := s.authorize(ctx, userID, boardID, PermRead); err != nil {
		return nil, nil, err
	}
	if len(fileIDs) > maxFileIDs {
		return nil, nil, invalid("at most %d file ids per request", maxFileIDs)
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
