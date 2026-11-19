// Package server wires the HTTP routes and middleware together.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/access"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/auth"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/boards"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/collections"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/httpx"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/mcpserver"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/workspaces"
)

type Config struct {
	// CookieSecure must be true in production (HTTPS); false only for local dev.
	CookieSecure bool
	// TrustProxy honours X-Real-IP from the reverse proxy for rate limiting.
	TrustProxy bool
	// PublicURL is the app's origin, used for board links in MCP results.
	PublicURL string
}

func NewHandler(pool *pgxpool.Pool, cfg Config, logger *slog.Logger) http.Handler {
	authHandler := auth.NewHandler(
		auth.NewUsers(pool),
		auth.NewSessions(pool),
		auth.HandlerConfig{CookieSecure: cfg.CookieSecure, TrustProxy: cfg.TrustProxy},
		logger,
	)
	checker := access.NewChecker(pool)
	boardStore := boards.NewStore(pool, checker)
	collectionStore := collections.NewStore(pool, checker)
	workspaceStore := workspaces.NewStore(pool, checker)
	boardsHandler := boards.NewHandler(boardStore, logger)
	collectionsHandler := collections.NewHandler(collectionStore, logger)
	workspacesHandler := workspaces.NewHandler(workspaceStore, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			httpx.WriteError(w, http.StatusServiceUnavailable, "db_unavailable", "database unavailable")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/auth/login", authHandler.Login)
	mux.HandleFunc("POST /api/auth/logout", authHandler.Logout)
	mux.Handle("GET /api/auth/me", authHandler.RequireUser(http.HandlerFunc(authHandler.Me)))
	workspacesHandler.Register(mux, authHandler.RequireUser)
	collectionsHandler.Register(mux, authHandler.RequireUser)
	boardsHandler.Register(mux, authHandler.RequireUser)

	// The MCP endpoint authenticates with bearer tokens only, which browsers
	// never attach on their own, so it sits outside the CSRF check that the
	// cookie-authenticated API needs.
	root := http.NewServeMux()
	root.Handle("/api/mcp", mcpserver.Handler(mcpserver.Deps{
		Tokens:      auth.NewTokens(pool),
		Workspaces:  workspaceStore,
		Collections: collectionStore,
		Boards:      boardStore,
		PublicURL:   cfg.PublicURL,
		Logger:      logger,
	}))
	root.Handle("/", httpx.RequireCSRFHeader(mux))

	return httpx.Logger(logger)(root)
}
