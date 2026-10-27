package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const (
	bcryptCost = 12
	// bcrypt only looks at the first 72 bytes; longer passwords are rejected
	// instead of being silently truncated.
	MinPasswordLen   = 8
	MaxPasswordBytes = 72
	maxNameLen       = 100
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrEmailTaken         = errors.New("email already registered")
	ErrUserNotFound       = errors.New("user not found")
)

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidateEmail expects an already normalized email.
func ValidateEmail(email string) error {
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > 254 {
		return fmt.Errorf("invalid email address %q", email)
	}
	return nil
}

func ValidateName(name string) error {
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		return fmt.Errorf("name must be 1-%d characters", maxNameLen)
	}
	return nil
}

func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLen {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	}
	if len(password) > MaxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes", MaxPasswordBytes)
	}
	return nil
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// dummyHash is compared against when the email does not exist, so a failed
// login takes the same time whether or not the account exists.
var dummyHash = sync.OnceValue(func() []byte {
	hash, err := bcrypt.GenerateFromPassword([]byte("timing-equalizer"), bcryptCost)
	if err != nil {
		panic(err)
	}
	return hash
})

type Users struct {
	pool *pgxpool.Pool
}

func NewUsers(pool *pgxpool.Pool) *Users {
	return &Users{pool: pool}
}

func (u *Users) Create(ctx context.Context, email, name, password string) (User, error) {
	email = NormalizeEmail(email)
	name = strings.TrimSpace(name)
	if err := ValidateEmail(email); err != nil {
		return User{}, err
	}
	if err := ValidateName(name); err != nil {
		return User{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}

	var user User
	err = u.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3)
		 RETURNING id, email, name, created_at`,
		email, name, hash,
	).Scan(&user.ID, &user.Email, &user.Name, &user.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return User{}, ErrEmailTaken
	}
	return user, err
}

// SetPassword replaces a user's password and revokes all their sessions.
func (u *Users) SetPassword(ctx context.Context, email, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, u.pool, func(tx pgx.Tx) error {
		var userID string
		err := tx.QueryRow(ctx,
			`UPDATE users SET password_hash = $2 WHERE email = $1 RETURNING id`,
			NormalizeEmail(email), hash,
		).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
		return err
	})
}

func (u *Users) List(ctx context.Context) ([]User, error) {
	rows, err := u.pool.Query(ctx, `SELECT id, email, name, created_at FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (User, error) {
		var user User
		err := row.Scan(&user.ID, &user.Email, &user.Name, &user.CreatedAt)
		return user, err
	})
}

// Authenticate returns ErrInvalidCredentials for both unknown emails and
// wrong passwords.
func (u *Users) Authenticate(ctx context.Context, email, password string) (User, error) {
	var user User
	var hash string
	err := u.pool.QueryRow(ctx,
		`SELECT id, email, name, created_at, password_hash FROM users WHERE email = $1`,
		NormalizeEmail(email),
	).Scan(&user.ID, &user.Email, &user.Name, &user.CreatedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, ErrInvalidCredentials
	}
	return user, nil
}
