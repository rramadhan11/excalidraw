package boards

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

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
	route("GET /api/boards", h.list)
	route("POST /api/boards", h.create)
	route("GET /api/boards/{id}", h.get)
	route("PATCH /api/boards/{id}", h.rename)
	route("DELETE /api/boards/{id}", h.delete)
	route("PUT /api/boards/{id}/scene", h.saveScene)
	route("GET /api/boards/{id}/files", h.getFiles)
	route("PUT /api/boards/{id}/files/{fileId}", h.putFile)
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

func (h *Handler) writeStoreError(w http.ResponseWriter, err error) {
	var conflict *ConflictError
	var tooLarge *FileTooLargeError
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.As(err, &conflict):
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":   map[string]string{"code": "conflict", "message": err.Error()},
			"version": conflict.CurrentVersion,
		})
	case errors.As(err, &tooLarge):
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "file_too_large", err.Error())
	case IsValidationError(err):
		httpx.WriteError(w, http.StatusBadRequest, "invalid", err.Error())
	default:
		h.logger.Error("board store", slog.Any("err", err))
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	boards, err := h.store.List(r.Context(), userID(r))
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"boards": boards})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := httpx.DecodeJSON(w, r, maxSmallBody, &req); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	board, err := h.store.Create(r.Context(), userID(r), req.Name)
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	httpx.AddLogAttrs(r.Context(), slog.String("board_id", board.ID))
	httpx.WriteJSON(w, http.StatusCreated, board)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	board, err := h.store.Get(r.Context(), userID(r), boardID(r))
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, board)
}

func (h *Handler) rename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := httpx.DecodeJSON(w, r, maxSmallBody, &req); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	board, err := h.store.Rename(r.Context(), userID(r), boardID(r), req.Name)
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, board)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Delete(r.Context(), userID(r), boardID(r)); err != nil {
		h.writeStoreError(w, err)
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
		httpx.WriteError(w, http.StatusBadRequest, "invalid", "version is required")
		return
	}
	version, updatedAt, err := h.store.SaveScene(r.Context(), userID(r), boardID(r), *req.Version, req.Scene)
	if err != nil {
		h.writeStoreError(w, err)
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
		h.writeStoreError(w, err)
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
		h.writeStoreError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"files":      files,
		"erroredIds": missing,
	})
}
