package boards

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/apperr"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/auth"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/httpx"
)

const (
	maxSmallBody = 16 << 10
	maxSceneBody = 50 << 20
	maxFileBody  = maxDataURLLen + 4<<10
)

type Handler struct {
	store  *Store
	logger *slog.Logger
}

func NewHandler(store *Store, logger *slog.Logger) *Handler {
	return &Handler{store: store, logger: logger}
}

// Register mounts the board routes; wrap must enforce authentication.
func (h *Handler) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	route := func(pattern string, fn http.HandlerFunc) {
		mux.Handle(pattern, wrap(fn))
	}
	route("GET /api/workspaces/{id}/boards", h.list)
	route("POST /api/workspaces/{id}/boards", h.create)
	route("GET /api/boards/{id}", h.get)
	route("PATCH /api/boards/{id}", h.update)
	route("DELETE /api/boards/{id}", h.delete)
	route("PUT /api/boards/{id}/scene", h.saveScene)
	route("GET /api/boards/{id}/files", h.getFiles)
	route("PUT /api/boards/{id}/files/{fileId}", h.putFile)
	route("GET /api/boards/{id}/thumbnail", h.getThumbnail)
	route("PUT /api/boards/{id}/thumbnail", h.putThumbnail)
	route("DELETE /api/boards/{id}/thumbnail", h.deleteThumbnail)
}

func userID(r *http.Request) string {
	user, _ := auth.UserFrom(r.Context())
	return user.ID
}

func boardID(r *http.Request) string {
	id := r.PathValue("id")
	httpx.AddLogAttrs(r.Context(), slog.String("board_id", id))
	return id
}

func (h *Handler) writeError(w http.ResponseWriter, err error) {
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":   map[string]string{"code": "conflict", "message": err.Error()},
			"version": conflict.CurrentVersion,
		})
		return
	}
	apperr.Write(w, h.logger, err)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter, err := parseCollectionFilter(q.Get("collection"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	after, err := decodeCursor(q.Get("cursor"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil || limit < 1 || limit > maxPageSize {
			h.writeError(w, apperr.Invalid("limit must be 1-%d", maxPageSize))
			return
		}
	}

	workspaceID := r.PathValue("id")
	httpx.AddLogAttrs(r.Context(), slog.String("workspace_id", workspaceID))
	boards, next, err := h.store.List(r.Context(), userID(r), workspaceID, filter, limit, after)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"boards": boards, "nextCursor": next})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string  `json:"name"`
		CollectionID *string `json:"collectionId"`
	}
	if err := httpx.DecodeJSON(w, r, maxSmallBody, &req); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	board, err := h.store.Create(r.Context(), userID(r), r.PathValue("id"), req.Name, req.CollectionID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.AddLogAttrs(r.Context(), slog.String("board_id", board.ID))
	httpx.WriteJSON(w, http.StatusCreated, board)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	board, err := h.store.Get(r.Context(), userID(r), boardID(r))
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, board)
}

type updateRequest struct {
	Name *string `json:"name"`
	// raw so an absent field can be told apart from null
	CollectionID json.RawMessage `json:"collectionId"`
}

// parseUpdate tells an absent collectionId (leave as is) apart from
// `"collectionId": null` (move to unsorted).
func parseUpdate(req updateRequest) (Update, error) {
	u := Update{Name: req.Name}
	if req.CollectionID == nil {
		return u, nil
	}
	u.MoveToCollection = true
	if string(req.CollectionID) == "null" {
		return u, nil
	}
	var id string
	if err := json.Unmarshal(req.CollectionID, &id); err != nil {
		return Update{}, apperr.Invalid("collectionId must be a string or null")
	}
	u.CollectionID = &id
	return u, nil
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	var req updateRequest
	if err := httpx.DecodeJSON(w, r, maxSmallBody, &req); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	u, err := parseUpdate(req)
	if err != nil {
		h.writeError(w, err)
		return
	}
	board, err := h.store.Update(r.Context(), userID(r), boardID(r), u)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, board)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Delete(r.Context(), userID(r), boardID(r)); err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}

func (h *Handler) saveScene(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version *int64          `json:"version"`
		Scene   json.RawMessage `json:"scene"`
	}
	if err := httpx.DecodeJSON(w, r, maxSceneBody, &req); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	if req.Version == nil {
		h.writeError(w, apperr.Invalid("version is required"))
		return
	}
	version, updatedAt, err := h.store.SaveScene(r.Context(), userID(r), boardID(r), *req.Version, req.Scene)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		Version   int64     `json:"version"`
		UpdatedAt time.Time `json:"updatedAt"`
	}{version, updatedAt})
}

func (h *Handler) putFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MimeType string `json:"mimeType"`
		DataURL  string `json:"dataURL"`
		Created  int64  `json:"created"`
		// optional in BinaryFileData; defaults to 1
		Version *int32 `json:"version"`
	}
	if err := httpx.DecodeJSON(w, r, maxFileBody, &req); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	f := File{
		ID:       r.PathValue("fileId"),
		MimeType: req.MimeType,
		DataURL:  req.DataURL,
		Created:  req.Created,
		Version:  1,
	}
	if req.Version != nil {
		f.Version = *req.Version
	}
	if err := h.store.PutFile(r.Context(), userID(r), boardID(r), f); err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}

func (h *Handler) getFiles(w http.ResponseWriter, r *http.Request) {
	var ids []string
	if raw := r.URL.Query().Get("ids"); raw != "" {
		ids = strings.Split(raw, ",")
	}
	files, missing, err := h.store.GetFiles(r.Context(), userID(r), boardID(r), ids)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"files":      files,
		"erroredIds": missing,
	})
}

// putThumbnail takes the raw image as the body (not JSON).
func (h *Handler) putThumbnail(w http.ResponseWriter, r *http.Request) {
	mimeType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxThumbnailBytes+1))
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		h.writeError(w, apperr.TooLarge("thumbnail exceeds 512 KiB"))
		return
	}
	if err != nil {
		h.writeError(w, apperr.Invalid("couldn't read thumbnail: %v", err))
		return
	}
	updatedAt, err := h.store.PutThumbnail(r.Context(), userID(r), boardID(r), mimeType, data)
	if err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"thumbnailUpdatedAt": updatedAt})
}

func (h *Handler) deleteThumbnail(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DeleteThumbnail(r.Context(), userID(r), boardID(r)); err != nil {
		h.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}

func (h *Handler) getThumbnail(w http.ResponseWriter, r *http.Request) {
	t, err := h.store.GetThumbnail(r.Context(), userID(r), boardID(r))
	if err != nil {
		h.writeError(w, err)
		return
	}
	header := w.Header()
	header.Set("Content-Type", t.MimeType)
	header.Set("Content-Length", strconv.Itoa(len(t.Data)))
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'none'")
	// clients request ?v=<thumbnailUpdatedAt>, so a URL never changes content
	header.Set("Cache-Control", "private, max-age=31536000, immutable")
	header.Set("Last-Modified", t.UpdatedAt.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(t.Data)
}
