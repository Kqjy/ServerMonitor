package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrNoUser             = errors.New("no user")
	ErrSessionExpired     = errors.New("session expired")
	ErrAlreadyInitialized = errors.New("admin already exists")
	ErrAccountLocked      = errors.New("account temporarily locked")
	ErrWeakPassword       = errors.New("password is too weak")
)

const (
	SessionCookie     = "sm_session"
	SessionTTL        = 30 * 24 * time.Hour
	MinPasswordLength = 12
	MaxFailedLogins   = 10
	LockoutDuration   = 15 * time.Minute
)

var weakPasswordPrefixes = []string{
	"password", "passw0rd", "letmein", "welcome", "admin", "administrator",
	"qwerty", "asdfgh", "zxcvbn", "iloveyou", "monkey", "dragon",
	"sunshine", "princess", "football", "baseball", "master",
	"changeme", "secret",
}

var dummyPasswordHash = mustGenerateDummy()

func mustGenerateDummy() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte("dummy-comparison-placeholder"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return h
}

func validatePassword(p string) error {
	if len(p) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	lower := strings.ToLower(p)
	for _, w := range weakPasswordPrefixes {
		if strings.HasPrefix(lower, w) {
			return ErrWeakPassword
		}
	}
	return nil
}

type User struct {
	ID        int32
	Username  string
	Role      string
	CreatedAt time.Time
}

type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func (s *Service) HasAnyUser(ctx context.Context) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Service) CreateAdmin(ctx context.Context, username, password string) (User, error) {
	if username == "" || password == "" {
		return User{}, errors.New("username and password required")
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(0x5e72_4d6f_4e69_7400)); err != nil {
		return User{}, err
	}

	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return User{}, err
	}
	if n > 0 {
		return User{}, ErrAlreadyInitialized
	}

	var u User
	err = tx.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, $2, 'admin') RETURNING id, username, role, created_at
	`, username, hash).Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt)
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, err
	}
	return u, nil
}

func (s *Service) ChangePassword(ctx context.Context, userID int32, current, next string) (string, error) {
	if current == "" || next == "" {
		return "", errors.New("current and new password required")
	}
	if err := validatePassword(next); err != nil {
		return "", err
	}
	if current == next {
		return "", errors.New("new password must differ from current password")
	}
	var hash []byte
	err := s.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, userID).Scan(&hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNoUser
		}
		return "", err
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(current)) != nil {
		return "", ErrInvalidCredentials
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $1 WHERE id = $2`, newHash, userID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	expires := time.Now().Add(SessionTTL)
	if _, err := tx.Exec(ctx, `
		INSERT INTO sessions (token, user_id, expires_at) VALUES ($1, $2, $3)
	`, hashToken(raw), userID, expires); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (s *Service) ResetPassword(ctx context.Context, username, password string) error {
	if username == "" || password == "" {
		return errors.New("username and password required")
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID int32
	err = tx.QueryRow(ctx, `SELECT id FROM users WHERE username = $1`, username).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoUser
		}
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $1 WHERE id = $2`, newHash, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Login(ctx context.Context, username, password string) (User, string, error) {
	var (
		u           User
		hash        []byte
		failed      int
		lockedUntil *time.Time
		userExists  = true
	)
	err := s.pool.QueryRow(ctx, `
		SELECT id, username, role, created_at, password_hash, failed_login_count, locked_until
		FROM users WHERE username = $1
	`, username).Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt, &hash, &failed, &lockedUntil)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return User{}, "", err
		}
		userExists = false
		hash = dummyPasswordHash
	}
	now := time.Now()
	if userExists && lockedUntil != nil && now.Before(*lockedUntil) {
		_ = bcrypt.CompareHashAndPassword(hash, []byte(password))
		return User{}, "", ErrAccountLocked
	}
	bcryptOK := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	if !userExists || !bcryptOK {
		if userExists {
			var newCount int
			err := s.pool.QueryRow(ctx, `
				UPDATE users SET failed_login_count = failed_login_count + 1
				WHERE id = $1 RETURNING failed_login_count
			`, u.ID).Scan(&newCount)
			if err == nil && newCount >= MaxFailedLogins {
				_, _ = s.pool.Exec(ctx, `
					UPDATE users SET failed_login_count = 0, locked_until = $1 WHERE id = $2
				`, now.Add(LockoutDuration), u.ID)
				return User{}, "", ErrAccountLocked
			}
		}
		return User{}, "", ErrInvalidCredentials
	}
	if failed != 0 || lockedUntil != nil {
		_, _ = s.pool.Exec(ctx, `UPDATE users SET failed_login_count = 0, locked_until = NULL WHERE id = $1`, u.ID)
	}
	token, err := s.createSession(ctx, u.ID)
	if err != nil {
		return User{}, "", err
	}
	return u, token, nil
}

func (s *Service) createSession(ctx context.Context, userID int32) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	expires := time.Now().Add(SessionTTL)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (token, user_id, expires_at) VALUES ($1, $2, $3)
	`, hashToken(raw), userID, expires)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (s *Service) ResolveSession(ctx context.Context, token string) (User, error) {
	raw, err := hex.DecodeString(token)
	if err != nil {
		return User{}, ErrInvalidCredentials
	}
	hashed := hashToken(raw)
	var (
		u       User
		expires time.Time
	)
	err = s.pool.QueryRow(ctx, `
		SELECT u.id, u.username, u.role, u.created_at, s.expires_at
		FROM sessions s JOIN users u ON s.user_id = u.id
		WHERE s.token = $1
	`, hashed).Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt, &expires)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrInvalidCredentials
		}
		return User{}, err
	}
	if time.Now().After(expires) {
		_, _ = s.pool.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, hashed)
		return User{}, ErrSessionExpired
	}
	return u, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	raw, err := hex.DecodeString(token)
	if err != nil {
		return nil
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, hashToken(raw))
	return err
}

func (s *Service) PurgeExpired(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	return err
}

func hashToken(raw []byte) []byte {
	sum := sha256.Sum256(raw)
	return sum[:]
}
