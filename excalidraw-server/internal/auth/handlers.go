package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/rramadhan11/excalidraw/excalidraw-server/internal/httpx"
)

const (
	CookieName = "excalidraw_session"

	maxLoginBody = 16 << 10
	// failures allowed per (IP, email) and per IP within loginWindow
	maxFailuresPerAccount = 5
	maxFailuresPerIP      = 30
	loginWindow           = 15 * time.Minute
)

type ctxKey int

const userKey ctxKey = iota

// UserFrom returns the authenticated user set by RequireUser.
func UserFrom(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(userKey).(User)
	return user, ok
}

type Handler struct {
	users        *Users
	sessions     *Sessions
	accountLimit *Limiter
	ipLimit      *Limiter
	cookieSecure bool
	trustProxy   bool
	logger       *slog.Logger
}

type HandlerConfig struct {
	CookieSecure bool
	TrustProxy   bool
}

func NewHandler(users *Users, sessions *Sessions, cfg HandlerConfig, logger *slog.Logger) *Handler {
	return &Handler{
		users:        users,
		sessions:     sessions,
		accountLimit: NewLimiter(maxFailuresPerAccount, loginWindow),
		ipLimit:      NewLimiter(maxFailuresPerIP, loginWindow),
		cookieSecure: cfg.CookieSecure,
		trustProxy:   cfg.TrustProxy,
		logger:       logger,
	}
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

type userResponse struct {
	User User `json:"user"`
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, maxLoginBody, &req); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}

	ip := httpx.ClientIP(r, h.trustProxy)
	accountKey := ip + "|" + NormalizeEmail(req.Email)
	if !h.ipLimit.Allow(ip) || !h.accountLimit.Allow(accountKey) {
		httpx.WriteError(w, http.StatusTooManyRequests, "too_many_attempts",
			"too many failed login attempts, try again later")
		return
	}

	user, err := h.users.Authenticate(r.Context(), req.Email, req.Password)
	if errors.Is(err, ErrInvalidCredentials) {
		h.ipLimit.Fail(ip)
		h.accountLimit.Fail(accountKey)
		h.logger.Warn("login failed", slog.String("ip", ip))
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_credentials", err.Error())
		return
	}
	if err != nil {
		h.logger.Error("login", slog.Any("err", err))
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal server error")
		return
	}
	h.accountLimit.Reset(accountKey)

	token, expiresAt, err := h.sessions.Create(r.Context(), user.ID)
	if err != nil {
		h.logger.Error("create session", slog.Any("err", err))
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal server error")
		return
	}
	httpx.AddLogAttrs(r.Context(), slog.String("user_id", user.ID))
	h.setSessionCookie(w, token, expiresAt)
	httpx.WriteJSON(w, http.StatusOK, userResponse{User: user})
}

// Logout is idempotent and does not require a valid session.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(CookieName); err == nil && cookie.Value != "" {
		if err := h.sessions.Delete(r.Context(), cookie.Value); err != nil {
			h.logger.Error("delete session", slog.Any("err", err))
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal server error")
			return
		}
	}
	h.clearSessionCookie(w)
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	httpx.WriteJSON(w, http.StatusOK, userResponse{User: user})
}

// RequireUser rejects requests without a valid session cookie with 401.
func (h *Handler) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(CookieName)
		if err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "login required")
			return
		}
		user, renewedUntil, err := h.sessions.Lookup(r.Context(), cookie.Value)
		if errors.Is(err, ErrNoSession) {
			h.clearSessionCookie(w)
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "login required")
			return
		}
		if err != nil {
			h.logger.Error("lookup session", slog.Any("err", err))
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal server error")
			return
		}
		if !renewedUntil.IsZero() {
			h.setSessionCookie(w, cookie.Value, renewedUntil)
		}
		httpx.AddLogAttrs(r.Context(), slog.String("user_id", user.ID))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
	})
}
