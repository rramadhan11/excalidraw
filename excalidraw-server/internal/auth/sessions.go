package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	SessionTTL   = 30 * 24 * time.Hour
	tokenBytes   = 32
	renewEvery   = 24 * time.Hour
	tokenEncoded = 43 // base64.RawURLEncoding length of tokenBytes
)

var ErrNoSession = errors.New("no valid session")

func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

type Sessions struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewSessions(pool *pgxpool.Pool) *Sessions {
	return &Sessions{pool: pool, now: time.Now}
}

func (s *Sessions) Create(ctx context.Context, userID string) (token string, expiresAt time.Time, err error) {
	token, err = newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt = s.now().Add(SessionTTL)
	_, err = s.pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		hashToken(token), userID, expiresAt,
	)
	return token, expiresAt, err
}

// Lookup returns the user owning a valid session. Sessions slide: once a
// session is a day old its expiry is pushed back to a full TTL, and the new
// expiry is returned so the caller can refresh the cookie. A zero renewedUntil
// means the session was not renewed.
func (s *Sessions) Lookup(ctx context.Context, token string) (user User, renewedUntil time.Time, err error) {
	if len(token) != tokenEncoded {
		return User{}, time.Time{}, ErrNoSession
	}
	now := s.now()
	var expiresAt time.Time
	err = s.pool.QueryRow(ctx,
		`SELECT u.id, u.email, u.name, u.created_at, s.expires_at
		   FROM sessions s JOIN users u ON u.id = s.user_id
		  WHERE s.token_hash = $1 AND s.expires_at > $2`,
		hashToken(token), now,
	).Scan(&user.ID, &user.Email, &user.Name, &user.CreatedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, time.Time{}, ErrNoSession
	}
	if err != nil {
		return User{}, time.Time{}, err
	}

	if expiresAt.Sub(now) < SessionTTL-renewEvery {
		renewedUntil = now.Add(SessionTTL)
		if _, err := s.pool.Exec(ctx,
			`UPDATE sessions SET expires_at = $2 WHERE token_hash = $1`,
			hashToken(token), renewedUntil,
		); err != nil {
			return User{}, time.Time{}, err
		}
	}
	return user, renewedUntil, nil
}

func (s *Sessions) Delete(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hashToken(token))
	return err
}

func (s *Sessions) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= $1`, s.now())
	return tag.RowsAffected(), err
}
