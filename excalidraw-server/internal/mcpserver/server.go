// Package mcpserver exposes boards to AI tools over the Model Context
// Protocol (streamable HTTP), authenticated with personal access tokens.
//
// Tools act as the token's user through the same stores, and therefore the
// same access checks, as the browser API.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/apperr"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/auth"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/boards"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/collections"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/scene"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/workspaces"
)

// Version is reported to clients in the MCP handshake.
const Version = "1.0.0"

// maxRequestBody bounds one MCP request; 500 elements with labels fit easily.
const maxRequestBody = 4 << 20

type Deps struct {
	Tokens      *auth.Tokens
	Workspaces  *workspaces.Store
	Collections *collections.Store
	Boards      *boards.Store
	// PublicURL is the app's origin (e.g. https://draw.example.com), used to
	// give clients links to boards; relative links when empty.
	PublicURL string
	Logger    *slog.Logger
}

const instructions = `Tools for the user's self-hosted Excalidraw whiteboards.

Boards live in workspaces and may be grouped in collections. Find boards with
search_boards or list_workspaces + list_boards, read one with get_board, and
create one with create_board. Give the user the board's url when you are done.

Drawing: coordinates are pixels, origin top-left, y grows downwards. Read the
board first (get_board) and place new content beside its bounds, not on top
of it. Typical boxes are 160-220 x 70-100 with 80-120 px gaps. Put text
inside shapes with label (not a separate text element) and connect shapes with
arrows given start/end ids, so arrows stay attached when things move. Give
elements your own short ids (e.g. "api", "db") to refer to them in the same
or later calls.

A board the user has open in a browser stops autosaving after you change it
("Changed elsewhere - reload"); tell the user to reload it to see your edits.`

// Handler serves the MCP endpoint. Only bearer tokens are accepted (no session
// cookies), so it needs no CSRF protection.
func Handler(d Deps) http.Handler {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "excalidraw-boards", Title: "Excalidraw boards", Version: Version},
		&mcp.ServerOptions{Instructions: instructions, Logger: d.Logger},
	)
	t := &tools{Deps: d}
	t.register(server)

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			// no server-to-client messages are needed: plain JSON responses,
			// no sessions to keep, nothing for proxies to buffer
			Stateless:           true,
			JSONResponse:        true,
			Logger:              d.Logger,
			MaxRequestBodyBytes: maxRequestBody,
		},
	)
	return mcpauth.RequireBearerToken(t.verifyToken, &mcpauth.RequireBearerTokenOptions{
		// tokens without an expiry are valid until revoked
		AllowMissingExpiration: true,
	})(handler)
}

type tools struct {
	Deps
}

// verifyToken adapts Tokens.Verify to the SDK. The SDK sends the error text to
// the client, so internal failures are logged and replaced.
func (t *tools) verifyToken(ctx context.Context, raw string, _ *http.Request) (*mcpauth.TokenInfo, error) {
	user, expiresAt, err := t.Tokens.Verify(ctx, raw)
	if errors.Is(err, auth.ErrInvalidToken) {
		return nil, fmt.Errorf("%w: %w", mcpauth.ErrInvalidToken, err)
	}
	if err != nil {
		t.Logger.Error("verify API token", slog.Any("err", err))
		return nil, errors.New("token verification failed")
	}
	info := &mcpauth.TokenInfo{UserID: user.ID}
	if expiresAt != nil {
		info.Expiration = *expiresAt
	}
	return info, nil
}

// handler is a tool body: it runs as userID and returns the structured result.
type handler[In, Out any] func(ctx context.Context, userID string, in In) (Out, error)

// add registers a tool with logging and error mapping around its body.
func add[In, Out any](s *mcp.Server, t *tools, tool *mcp.Tool, h handler[In, Out]) {
	mcp.AddTool(s, tool, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		info := req.Extra.TokenInfo
		if info == nil || info.UserID == "" {
			// unreachable behind RequireBearerToken
			return nil, zero, errors.New("not authenticated")
		}
		start := time.Now()
		out, err := h(ctx, info.UserID, in)
		attrs := []any{
			slog.String("tool", tool.Name),
			slog.String("user_id", info.UserID),
			slog.Duration("duration", time.Since(start)),
		}
		if err != nil {
			msg, internal := describe(err)
			if internal {
				t.Logger.Error("mcp tool failed", append(attrs, slog.Any("err", err))...)
			} else {
				t.Logger.Info("mcp tool rejected", append(attrs, slog.String("reason", msg))...)
			}
			return nil, zero, errors.New(msg)
		}
		t.Logger.Info("mcp tool", attrs...)
		return nil, out, nil
	})
}

// describe turns an error into a message for the caller, and reports
// whether it was a server fault (whose details must not be shown).
func describe(err error) (msg string, internal bool) {
	var appErr *apperr.Error
	var conflict *boards.ConflictError
	switch {
	case scene.IsInputError(err):
		return err.Error(), false
	case errors.As(err, &appErr):
		return appErr.Message, false
	case errors.Is(err, apperr.ErrNotFound):
		return "not found, or you don't have access to it", false
	case errors.Is(err, apperr.ErrForbidden):
		return err.Error(), false
	case errors.As(err, &conflict):
		return "the board kept changing while saving; try again", false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "request cancelled", false
	}
	return "internal server error", true
}

func (t *tools) boardURL(id string) string {
	return strings.TrimRight(t.PublicURL, "/") + "/board/" + id
}

func (t *tools) workspaceURL(id string) string {
	return strings.TrimRight(t.PublicURL, "/") + "/w/" + id
}
