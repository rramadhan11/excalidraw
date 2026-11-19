package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/apperr"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/boards"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/collections"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/scene"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/workspaces"
)

const (
	// summaries of very large boards are cut off to fit a model's context
	maxSummaryElements = 1000
	// optimistic-concurrency retries when a board changes during an edit
	saveAttempts = 3
)

func readOnly() *mcp.ToolAnnotations { return &mcp.ToolAnnotations{ReadOnlyHint: true} }

func additive(idempotent bool) *mcp.ToolAnnotations {
	no := false
	return &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: idempotent}
}

func destructive() *mcp.ToolAnnotations {
	yes := true
	return &mcp.ToolAnnotations{DestructiveHint: &yes}
}

// ---------------------------------------------------------------------------
// inputs and outputs

type noInput struct{}

type workspaceInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"workspace id from list_workspaces"`
}

type listBoardsInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"workspace id from list_workspaces"`
	Collection  string `json:"collection,omitempty" jsonschema:"a collection id, or unsorted for boards in no collection; omit for all boards"`
	Cursor      string `json:"cursor,omitempty" jsonschema:"nextCursor from the previous page"`
	Limit       int    `json:"limit,omitempty" jsonschema:"page size, 1-100 (default 50)"`
}

type searchInput struct {
	Query       string `json:"query" jsonschema:"text to find in board names and in the text on boards"`
	WorkspaceID string `json:"workspace_id,omitempty" jsonschema:"limit the search to one workspace"`
}

type boardInput struct {
	BoardID string `json:"board_id" jsonschema:"board id"`
}

type getBoardInput struct {
	BoardID string `json:"board_id" jsonschema:"board id"`
	Detail  string `json:"detail,omitempty" jsonschema:"summary (default): compact element list; full: raw Excalidraw element JSON"`
}

type createBoardInput struct {
	WorkspaceID  string `json:"workspace_id" jsonschema:"workspace to create the board in"`
	Name         string `json:"name,omitempty" jsonschema:"board name (default Untitled)"`
	CollectionID string `json:"collection_id,omitempty" jsonschema:"collection to put the board in; omit for unsorted"`
}

type renameBoardInput struct {
	BoardID string `json:"board_id" jsonschema:"board id"`
	Name    string `json:"name" jsonschema:"new name"`
}

type moveBoardInput struct {
	BoardID      string `json:"board_id" jsonschema:"board id"`
	CollectionID string `json:"collection_id,omitempty" jsonschema:"target collection in the same workspace; omit or empty to move the board out of any collection"`
}

type createCollectionInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"workspace id"`
	Name        string `json:"name" jsonschema:"collection name"`
}

type renameCollectionInput struct {
	CollectionID string `json:"collection_id" jsonschema:"collection id"`
	Name         string `json:"name" jsonschema:"new name"`
}

type addElementsInput struct {
	BoardID  string           `json:"board_id" jsonschema:"board id"`
	Elements []scene.Skeleton `json:"elements" jsonschema:"elements to add, in drawing order (later ones on top); at most 500"`
}

type updateElementsInput struct {
	BoardID string         `json:"board_id" jsonschema:"board id"`
	Updates []scene.Update `json:"updates" jsonschema:"changes to existing elements; at most 500"`
}

type deleteElementsInput struct {
	BoardID string   `json:"board_id" jsonschema:"board id"`
	IDs     []string `json:"ids" jsonschema:"ids of elements to delete; a shape's label and a frame's contents go with it, attached arrows are detached"`
}

type workspacesOutput struct {
	Workspaces []workspaceOut `json:"workspaces"`
}

type workspaceOut struct {
	workspaces.Workspace
	URL string `json:"url"`
}

type collectionsOutput struct {
	Collections []collections.Collection `json:"collections"`
}

type boardOut struct {
	boards.Summary
	URL string `json:"url"`
}

type boardsOutput struct {
	Boards     []boardOut `json:"boards"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

type searchResultOut struct {
	boards.SearchResult
	URL string `json:"url"`
}

type searchOutput struct {
	Boards []searchResultOut `json:"boards"`
}

type getBoardOutput struct {
	boardOut
	Version int64 `json:"version"`
	// one of the two, depending on the requested detail
	Summary   *scene.Summary  `json:"summary,omitempty"`
	Elements  []scene.Element `json:"elements,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
}

type collectionOutput struct {
	Collection collections.Collection `json:"collection"`
}

type editOutput struct {
	URL     string `json:"url"`
	Version int64  `json:"version"`
	// add_elements: ids of the elements created, in input order
	CreatedIDs []string `json:"createdIds,omitempty"`
	// delete_elements: every id deleted, including labels and frame contents
	DeletedIDs []string `json:"deletedIds,omitempty"`
}

// ---------------------------------------------------------------------------
// registration

func (t *tools) register(s *mcp.Server) {
	add(s, t, &mcp.Tool{
		Name:        "list_workspaces",
		Description: "List the workspaces you belong to, with your role and how many boards each has.",
		Annotations: readOnly(),
	}, t.listWorkspaces)
	add(s, t, &mcp.Tool{
		Name:        "list_collections",
		Description: "List a workspace's collections (folders of boards).",
		Annotations: readOnly(),
	}, t.listCollections)
	add(s, t, &mcp.Tool{
		Name:        "list_boards",
		Description: "List a workspace's boards, most recently edited first, optionally only one collection. Paginated with nextCursor.",
		Annotations: readOnly(),
	}, t.listBoards)
	add(s, t, &mcp.Tool{
		Name:        "search_boards",
		Description: "Find boards by name or by text written on them (labels, text, frame names), across your workspaces. Returns up to 20, name matches first.",
		Annotations: readOnly(),
	}, t.searchBoards)
	add(s, t, &mcp.Tool{
		Name:        "get_board",
		Description: "Read a board: by default a compact list of its elements (labels folded into their shapes, arrows as from/to) with the bounds of all content.",
		Annotations: readOnly(),
	}, t.getBoard)
	add(s, t, &mcp.Tool{
		Name:        "create_board",
		Description: "Create an empty board in a workspace, optionally in a collection.",
		Annotations: additive(false),
	}, t.createBoard)
	add(s, t, &mcp.Tool{
		Name:        "rename_board",
		Description: "Rename a board.",
		Annotations: additive(true),
	}, t.renameBoard)
	add(s, t, &mcp.Tool{
		Name:        "move_board",
		Description: "Move a board into a collection of its workspace, or out of any collection.",
		Annotations: additive(true),
	}, t.moveBoard)
	add(s, t, &mcp.Tool{
		Name:        "create_collection",
		Description: "Create a collection (folder) in a workspace.",
		Annotations: additive(false),
	}, t.createCollection)
	add(s, t, &mcp.Tool{
		Name:        "rename_collection",
		Description: "Rename a collection.",
		Annotations: additive(true),
	}, t.renameCollection)
	add(s, t, &mcp.Tool{
		Name: "add_elements",
		Description: "Draw on a board: add rectangles, ellipses, diamonds, text, arrows, lines and frames. " +
			"Use label to put text inside a shape or on an arrow, and start/end ids to connect an arrow to shapes " +
			"(created in the same call or already on the board). Nothing is added if any element is invalid.",
		Annotations: additive(false),
	}, t.addElements)
	add(s, t, &mcp.Tool{
		Name: "update_elements",
		Description: "Change existing elements: move, resize, recolor, or change a text or label. " +
			"Labels and attached arrows follow their shapes. Nothing changes if any update is invalid.",
		Annotations: additive(false),
	}, t.updateElements)
	add(s, t, &mcp.Tool{
		Name: "delete_elements",
		Description: "Delete elements from a board. A shape's label and a frame's contents are deleted with it; " +
			"arrows attached to a deleted shape stay but are detached. Undo is possible in the editor's history only while the board is open there.",
		Annotations: destructive(),
	}, t.deleteElements)
}

// ---------------------------------------------------------------------------
// read

func (t *tools) listWorkspaces(ctx context.Context, userID string, _ noInput) (workspacesOutput, error) {
	list, err := t.Workspaces.List(ctx, userID)
	if err != nil {
		return workspacesOutput{}, err
	}
	out := workspacesOutput{Workspaces: make([]workspaceOut, len(list))}
	for i, ws := range list {
		out.Workspaces[i] = workspaceOut{Workspace: ws, URL: t.workspaceURL(ws.ID)}
	}
	return out, nil
}

func (t *tools) listCollections(ctx context.Context, userID string, in workspaceInput) (collectionsOutput, error) {
	list, err := t.Collections.List(ctx, userID, in.WorkspaceID)
	if err != nil {
		return collectionsOutput{}, err
	}
	return collectionsOutput{Collections: list}, nil
}

func (t *tools) listBoards(ctx context.Context, userID string, in listBoardsInput) (boardsOutput, error) {
	filter, err := boards.ParseCollectionFilter(in.Collection)
	if err != nil {
		return boardsOutput{}, err
	}
	after, err := boards.DecodeCursor(in.Cursor)
	if err != nil {
		return boardsOutput{}, err
	}
	limit := in.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return boardsOutput{}, apperr.Invalid("limit must be 1-100")
	}
	list, next, err := t.Boards.List(ctx, userID, in.WorkspaceID, filter, limit, after)
	if err != nil {
		return boardsOutput{}, err
	}
	out := boardsOutput{Boards: make([]boardOut, len(list)), NextCursor: next}
	for i, b := range list {
		out.Boards[i] = boardOut{Summary: b, URL: t.boardURL(b.ID)}
	}
	return out, nil
}

func (t *tools) searchBoards(ctx context.Context, userID string, in searchInput) (searchOutput, error) {
	results, err := t.Boards.Search(ctx, userID, in.Query, in.WorkspaceID)
	if err != nil {
		return searchOutput{}, err
	}
	out := searchOutput{Boards: make([]searchResultOut, len(results))}
	for i, r := range results {
		out.Boards[i] = searchResultOut{SearchResult: r, URL: t.boardURL(r.ID)}
	}
	return out, nil
}

func (t *tools) getBoard(ctx context.Context, userID string, in getBoardInput) (getBoardOutput, error) {
	if in.Detail != "" && in.Detail != "summary" && in.Detail != "full" {
		return getBoardOutput{}, apperr.Invalid("detail must be summary or full")
	}
	board, err := t.Boards.Get(ctx, userID, in.BoardID)
	if err != nil {
		return getBoardOutput{}, err
	}
	sc, err := scene.Parse(board.Scene)
	if err != nil {
		return getBoardOutput{}, err
	}
	out := getBoardOutput{
		boardOut: boardOut{Summary: board.Summary, URL: t.boardURL(board.ID)},
		Version:  board.Version,
	}
	if in.Detail == "full" {
		out.Elements, out.Truncated = sc.LiveElements(maxSummaryElements)
	} else {
		summary := sc.Summarize(maxSummaryElements)
		out.Summary = &summary
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// manage

func optional(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

func (t *tools) createBoard(ctx context.Context, userID string, in createBoardInput) (boardOut, error) {
	board, err := t.Boards.Create(ctx, userID, in.WorkspaceID, in.Name, optional(in.CollectionID))
	if err != nil {
		return boardOut{}, err
	}
	return boardOut{Summary: board.Summary, URL: t.boardURL(board.ID)}, nil
}

func (t *tools) renameBoard(ctx context.Context, userID string, in renameBoardInput) (boardOut, error) {
	summary, err := t.Boards.Update(ctx, userID, in.BoardID, boards.Update{Name: &in.Name})
	if err != nil {
		return boardOut{}, err
	}
	return boardOut{Summary: summary, URL: t.boardURL(summary.ID)}, nil
}

func (t *tools) moveBoard(ctx context.Context, userID string, in moveBoardInput) (boardOut, error) {
	summary, err := t.Boards.Update(ctx, userID, in.BoardID, boards.Update{
		MoveToCollection: true,
		CollectionID:     optional(in.CollectionID),
	})
	if err != nil {
		return boardOut{}, err
	}
	return boardOut{Summary: summary, URL: t.boardURL(summary.ID)}, nil
}

func (t *tools) createCollection(ctx context.Context, userID string, in createCollectionInput) (collectionOutput, error) {
	c, err := t.Collections.Create(ctx, userID, in.WorkspaceID, in.Name)
	return collectionOutput{Collection: c}, err
}

func (t *tools) renameCollection(ctx context.Context, userID string, in renameCollectionInput) (collectionOutput, error) {
	c, err := t.Collections.Rename(ctx, userID, in.CollectionID, in.Name)
	return collectionOutput{Collection: c}, err
}

// ---------------------------------------------------------------------------
// draw

// editScene applies edit to the board's current scene and saves it. If the
// board is saved by someone else in between, the edit is re-applied to the
// newer scene: edits are declarative (ids and values), so this is safe.
func (t *tools) editScene(ctx context.Context, userID, boardID string, edit func(*scene.Scene) error) (int64, error) {
	for attempt := 1; ; attempt++ {
		board, err := t.Boards.Get(ctx, userID, boardID)
		if err != nil {
			return 0, err
		}
		sc, err := scene.Parse(board.Scene)
		if err != nil {
			return 0, err
		}
		if err := edit(sc); err != nil {
			return 0, err
		}
		raw, err := sc.Marshal()
		if err != nil {
			return 0, err
		}
		version, _, err := t.Boards.SaveScene(ctx, userID, boardID, board.Version, raw)
		var conflict *boards.ConflictError
		if errors.As(err, &conflict) && attempt < saveAttempts {
			continue
		}
		if err != nil {
			return 0, err
		}
		// the stored thumbnail no longer matches; the editor renders a new
		// one the next time the board is opened
		if err := t.Boards.DeleteThumbnail(ctx, userID, boardID); err != nil {
			return 0, err
		}
		return version, nil
	}
}

func (t *tools) addElements(ctx context.Context, userID string, in addElementsInput) (editOutput, error) {
	var created []string
	version, err := t.editScene(ctx, userID, in.BoardID, func(sc *scene.Scene) error {
		var err error
		created, err = sc.Add(in.Elements)
		return err
	})
	if err != nil {
		return editOutput{}, err
	}
	return editOutput{URL: t.boardURL(in.BoardID), Version: version, CreatedIDs: created}, nil
}

func (t *tools) updateElements(ctx context.Context, userID string, in updateElementsInput) (editOutput, error) {
	version, err := t.editScene(ctx, userID, in.BoardID, func(sc *scene.Scene) error {
		return sc.Apply(in.Updates)
	})
	if err != nil {
		return editOutput{}, err
	}
	return editOutput{URL: t.boardURL(in.BoardID), Version: version}, nil
}

func (t *tools) deleteElements(ctx context.Context, userID string, in deleteElementsInput) (editOutput, error) {
	var deleted []string
	version, err := t.editScene(ctx, userID, in.BoardID, func(sc *scene.Scene) error {
		var err error
		deleted, err = sc.Delete(in.IDs)
		return err
	})
	if err != nil {
		return editOutput{}, err
	}
	return editOutput{URL: t.boardURL(in.BoardID), Version: version, DeletedIDs: deleted}, nil
}
