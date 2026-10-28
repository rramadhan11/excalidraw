package workspaces

import (
	"log/slog"
	"net/http"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/apperr"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/auth"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/httpx"
)

const maxBody = 16 << 10

type Handler struct {
	store  *Store
	logger *slog.Logger
}

func NewHandler(store *Store, logger *slog.Logger) *Handler {
	return &Handler{store: store, logger: logger}
}

// Register mounts the workspace routes; wrap must enforce authentication.
func (h *Handler) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/workspaces", wrap(http.HandlerFunc(h.list)))
	mux.Handle("POST /api/workspaces", wrap(http.HandlerFunc(h.create)))
	mux.Handle("PATCH /api/workspaces/{id}", wrap(http.HandlerFunc(h.rename)))
	mux.Handle("DELETE /api/workspaces/{id}", wrap(http.HandlerFunc(h.delete)))
}

func userID(r *http.Request) string {
	user, _ := auth.UserFrom(r.Context())
	return user.ID
}

func workspaceID(r *http.Request) string {
	id := r.PathValue("id")
	httpx.AddLogAttrs(r.Context(), slog.String("workspace_id", id))
	return id
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.List(r.Context(), userID(r))
	if err != nil {
		apperr.Write(w, h.logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"workspaces": list})
}

type nameRequest struct {
	Name string `json:"name"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req nameRequest
	if err := httpx.DecodeJSON(w, r, maxBody, &req); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	ws, err := h.store.Create(r.Context(), userID(r), req.Name)
	if err != nil {
		apperr.Write(w, h.logger, err)
		return
	}
	httpx.AddLogAttrs(r.Context(), slog.String("workspace_id", ws.ID))
	httpx.WriteJSON(w, http.StatusCreated, ws)
}

func (h *Handler) rename(w http.ResponseWriter, r *http.Request) {
	var req nameRequest
	if err := httpx.DecodeJSON(w, r, maxBody, &req); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	ws, err := h.store.Rename(r.Context(), userID(r), workspaceID(r), req.Name)
	if err != nil {
		apperr.Write(w, h.logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ws)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Delete(r.Context(), userID(r), workspaceID(r)); err != nil {
		apperr.Write(w, h.logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}
