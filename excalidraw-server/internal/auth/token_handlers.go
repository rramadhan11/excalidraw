package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/apperr"
	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/httpx"
)

const (
	maxTokenBody = 4 << 10
	// used when the request leaves expiresInDays out; null means no expiry
	defaultTokenDays = 90
)

// the lifetimes offered in the UI; anything else is rejected
var allowedTokenDays = []int{30, 90, 365}

// RegisterTokens adds the self-service endpoints for the logged-in user's
// own API tokens. They sit behind the session cookie and the CSRF check like
// the rest of the browser API.
func (h *Handler) RegisterTokens(mux *http.ServeMux) {
	mux.Handle("GET /api/tokens", h.RequireUser(http.HandlerFunc(h.listTokens)))
	mux.Handle("POST /api/tokens", h.RequireUser(http.HandlerFunc(h.createToken)))
	mux.Handle("DELETE /api/tokens/{id}", h.RequireUser(http.HandlerFunc(h.revokeToken)))
}

func (h *Handler) listTokens(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	tokens, err := h.tokens.List(r.Context(), user.ID)
	if err != nil {
		apperr.Write(w, h.logger, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

type createTokenResponse struct {
	Token APIToken `json:"token"`
	// the raw token, returned only in this response
	Secret string `json:"secret"`
}

// createToken asks for the password again: a token outlives the session, so a
// borrowed or stolen session must not be enough to mint one.
func (h *Handler) createToken(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	var req struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		// absent: defaultTokenDays; null: never expires
		ExpiresInDays json.RawMessage `json:"expiresInDays"`
	}
	if err := httpx.DecodeJSON(w, r, maxTokenBody, &req); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	expiresAt, err := tokenExpiry(req.ExpiresInDays, time.Now())
	if err != nil {
		apperr.Write(w, h.logger, err)
		return
	}

	ip := httpx.ClientIP(r, h.trustProxy)
	accountKey := ip + "|" + NormalizeEmail(user.Email)
	if !h.ipLimit.Allow(ip) || !h.accountLimit.Allow(accountKey) {
		httpx.WriteError(w, http.StatusTooManyRequests, "too_many_attempts",
			"too many failed password attempts, try again later")
		return
	}
	if _, err := h.users.Authenticate(r.Context(), user.Email, req.Password); err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			h.ipLimit.Fail(ip)
			h.accountLimit.Fail(accountKey)
			h.logger.Warn("token password check failed", slog.String("ip", ip))
			// 403, not 401: the session is fine and the client must not log out
			httpx.WriteError(w, http.StatusForbidden, "invalid_password", "wrong password")
			return
		}
		apperr.Write(w, h.logger, err)
		return
	}
	h.accountLimit.Reset(accountKey)

	token, raw, err := h.tokens.Create(r.Context(), user.ID, req.Name, expiresAt)
	switch {
	case errors.Is(err, ErrTokenName):
		err = apperr.Invalid("%s", err.Error())
	case errors.Is(err, ErrTooManyTokens):
		err = apperr.Conflict("too_many_tokens", err.Error())
	}
	if err != nil {
		apperr.Write(w, h.logger, err)
		return
	}
	httpx.AddLogAttrs(r.Context(), slog.String("token_id", token.ID))
	h.logger.Info("api token created",
		slog.String("user_id", user.ID), slog.String("token_id", token.ID), slog.String("name", token.Name))
	httpx.WriteJSON(w, http.StatusCreated, createTokenResponse{Token: token, Secret: raw})
}

func (h *Handler) revokeToken(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	id := r.PathValue("id")
	err := h.tokens.Revoke(r.Context(), user.ID, id)
	if errors.Is(err, ErrTokenNotFound) {
		err = apperr.ErrNotFound
	}
	if err != nil {
		apperr.Write(w, h.logger, err)
		return
	}
	h.logger.Info("api token revoked", slog.String("user_id", user.ID), slog.String("token_id", id))
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}

// tokenExpiry turns the request's expiresInDays into an expiry time.
func tokenExpiry(raw json.RawMessage, now time.Time) (*time.Time, error) {
	days := defaultTokenDays
	switch {
	case len(raw) == 0:
	case bytes.Equal(raw, []byte("null")):
		return nil, nil
	default:
		if err := json.Unmarshal(raw, &days); err != nil || !slices.Contains(allowedTokenDays, days) {
			return nil, apperr.Invalid("expiresInDays must be one of 30, 90, 365 or null")
		}
	}
	t := now.AddDate(0, 0, days)
	return &t, nil
}
