// Package server wires the HTTP routes and middleware together.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/auth"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/boards"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/httpx"
)

type Config struct {
	// CookieSecure must be true in production (HTTPS); false only for local dev.
	CookieSecure bool
	// TrustProxy honours X-Real-IP from the reverse proxy for rate limiting.
	TrustProxy bool
}

func NewHandler(pool *pgxpool.Pool, cfg Config, logger *slog.Logger) http.Handler {
	authHandler := auth.NewHandler(
		auth.NewUsers(pool),
		auth.NewSessions(pool),
		auth.HandlerConfig{CookieSecure: cfg.CookieSecure, TrustProxy: cfg.TrustProxy},
		logger,
	)
	boardsHandler := boards.NewHandler(boards.NewStore(pool), logger)

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
	boardsHandler.Register(mux, authHandler.RequireUser)

	return httpx.Logger(logger)(httpx.RequireCSRFHeader(mux))
}
