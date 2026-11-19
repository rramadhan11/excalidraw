package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// APITokenPrefix makes tokens recognizable (e.g. by secret scanners).
	APITokenPrefix = "exa_"
	// last_used_at is informational; writing it on every request is not worth
	// a row update per MCP call
	lastUsedResolution = time.Minute
	maxActiveTokens    = 20
)

var (
	ErrInvalidToken  = errors.New("invalid or revoked API token")
	ErrTokenNotFound = errors.New("API token not found")
	ErrTokenName     = errors.New("token name must be 1-100 characters")
	ErrTooManyTokens = fmt.Errorf("a user can have at most %d active tokens; revoke one first", maxActiveTokens)
)

type APIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
}

// Tokens manages personal access tokens. They authenticate as their user with
// the user's full access; there are no scopes.
type Tokens struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewTokens(pool *pgxpool.Pool) *Tokens {
	return &Tokens{pool: pool, now: time.Now}
}

// Create issues a token for a user. The returned raw token is not stored and
// cannot be retrieved again. A nil expiresAt means the token lives until
// revoked. A user has at most maxActiveTokens unrevoked, unexpired tokens.
func (t *Tokens) Create(ctx context.Context, userID, name string, expiresAt *time.Time) (APIToken, string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		return APIToken{}, "", ErrTokenName
	}
	now := t.now()
	if expiresAt != nil && !expiresAt.After(now) {
		return APIToken{}, "", errors.New("expiry must be in the future")
	}
	secret, err := newToken()
	if err != nil {
		return APIToken{}, "", err
	}
	raw := APITokenPrefix + secret

	var token APIToken
	err = pgx.BeginFunc(ctx, t.pool, func(tx pgx.Tx) error {
		// lock the user so concurrent creates cannot both pass the limit
		var active int
		err := tx.QueryRow(ctx,
			`SELECT (SELECT count(*) FROM api_tokens
			          WHERE user_id = u.id AND revoked_at IS NULL
			            AND (expires_at IS NULL OR expires_at > $2))
			   FROM users u WHERE u.id::text = $1 FOR UPDATE`,
			userID, now,
		).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return err
		}
		if active >= maxActiveTokens {
			return ErrTooManyTokens
		}
		return tx.QueryRow(ctx,
			`INSERT INTO api_tokens (user_id, name, token_hash, expires_at)
			 VALUES ($1, $2, $3, $4)
			 RETURNING id, name, created_at, expires_at`,
			userID, name, hashToken(raw), expiresAt,
		).Scan(&token.ID, &token.Name, &token.CreatedAt, &token.ExpiresAt)
	})
	if err != nil {
		return APIToken{}, "", err
	}
	return token, raw, nil
}

// Verify returns the user owning a valid (unrevoked, unexpired) token, and
// the token's expiry (nil if it has none).
func (t *Tokens) Verify(ctx context.Context, raw string) (User, *time.Time, error) {
	if !strings.HasPrefix(raw, APITokenPrefix) || len(raw) != len(APITokenPrefix)+tokenEncoded {
		return User{}, nil, ErrInvalidToken
	}
	now := t.now()
	var user User
	var tokenID string
	var lastUsed, expiresAt *time.Time
	err := t.pool.QueryRow(ctx,
		`SELECT u.id, u.email, u.name, u.created_at, t.id, t.last_used_at, t.expires_at
		   FROM api_tokens t JOIN users u ON u.id = t.user_id
		  WHERE t.token_hash = $1 AND t.revoked_at IS NULL
		    AND (t.expires_at IS NULL OR t.expires_at > $2)`,
		hashToken(raw), now,
	).Scan(&user.ID, &user.Email, &user.Name, &user.CreatedAt, &tokenID, &lastUsed, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, nil, ErrInvalidToken
	}
	if err != nil {
		return User{}, nil, err
	}
	if lastUsed == nil || now.Sub(*lastUsed) >= lastUsedResolution {
		if _, err := t.pool.Exec(ctx,
			`UPDATE api_tokens SET last_used_at = $2 WHERE id = $1`, tokenID, now,
		); err != nil {
			return User{}, nil, err
		}
	}
	return user, expiresAt, nil
}

// List returns all tokens of a user, including revoked and expired ones.
func (t *Tokens) List(ctx context.Context, userID string) ([]APIToken, error) {
	rows, err := t.pool.Query(ctx,
		`SELECT id, name, created_at, last_used_at, expires_at, revoked_at
		   FROM api_tokens WHERE user_id::text = $1
		  ORDER BY created_at`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (APIToken, error) {
		var token APIToken
		err := row.Scan(&token.ID, &token.Name, &token.CreatedAt, &token.LastUsedAt, &token.ExpiresAt, &token.RevokedAt)
		return token, err
	})
}

// Revoke disables one of the user's tokens immediately; another user's token
// is ErrTokenNotFound. Revoking twice is not an error.
func (t *Tokens) Revoke(ctx context.Context, userID, id string) error {
	return t.revoke(ctx, `id::text = $1 AND user_id::text = $3`, id, userID)
}

// RevokeAny disables any user's token, for the admin CLI.
func (t *Tokens) RevokeAny(ctx context.Context, id string) error {
	return t.revoke(ctx, `id::text = $1`, id)
}

func (t *Tokens) revoke(ctx context.Context, where string, id string, args ...any) error {
	tag, err := t.pool.Exec(ctx,
		`UPDATE api_tokens SET revoked_at = coalesce(revoked_at, $2) WHERE `+where,
		append([]any{id, t.now()}, args...)...,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTokenNotFound
	}
	return nil
}
