package auth

import (
	"context"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repo struct {
	pool          *pgxpool.Pool
	accessTTL     time.Duration
	refreshTTL    time.Duration
	sessionMaxAge time.Duration // ПР5 "5": ограничение общей длительности сессии
}

// NewRepo reads TTLs from the environment so the grading step
// "запустите сервер с --ttl 60" (mock-server.js flag) has a server-side
// equivalent here: ACCESS_TOKEN_TTL_SECONDS=60 go run .
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{
		pool:          pool,
		accessTTL:     envSeconds("ACCESS_TOKEN_TTL_SECONDS", 900),
		refreshTTL:    envSeconds("REFRESH_TOKEN_TTL_SECONDS", 7*24*3600),
		sessionMaxAge: envSeconds("SESSION_MAX_AGE_SECONDS", 8*3600),
	}
}

func envSeconds(key string, fallback int) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return time.Duration(fallback) * time.Second
}

func (r *Repo) SessionMaxAge() time.Duration { return r.sessionMaxAge }

// --- users ---

func (r *Repo) FindByUsername(ctx context.Context, username string) (*User, string, error) {
	var u User
	var role string
	var hash string
	err := r.pool.QueryRow(ctx,
		`SELECT id, username, full_name, role, password_hash, client_id
		 FROM app_users WHERE lower(username) = lower($1) AND deleted_at IS NULL`, username,
	).Scan(&u.ID, &u.Username, &u.FullName, &role, &hash, &u.ClientID)
	if err == pgx.ErrNoRows {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	u.Role = Role(role)
	return &u, hash, nil
}

func (r *Repo) FindByID(ctx context.Context, id int) (*User, error) {
	var u User
	var role string
	err := r.pool.QueryRow(ctx,
		`SELECT id, username, full_name, role, client_id
		 FROM app_users WHERE id = $1 AND deleted_at IS NULL`, id,
	).Scan(&u.ID, &u.Username, &u.FullName, &role, &u.ClientID)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.Role = Role(role)
	return &u, nil
}

// CreateBuyer inserts the client row (personal/license data) and the linked
// app_users row in one transaction — "покупатель — это клиент, получивший
// логин", not two independently-managed records.
func (r *Repo) CreateBuyer(ctx context.Context, username, passwordHash string, c ClientInput) (*User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var clientID int
	err = tx.QueryRow(ctx,
		`INSERT INTO clients (full_name, email, phone, license_number, license_issued_at, license_expires_at,
		                       birth_date, passport_series, passport_number)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
		c.FullName, c.Email, c.Phone, c.LicenseNumber, c.LicenseIssuedAt, c.LicenseExpiresAt,
		c.BirthDate, c.PassportSeries, c.PassportNumber,
	).Scan(&clientID)
	if isUniqueViolation(err, "ux_clients_email_lower") {
		return nil, errClientEmailTaken
	}
	if err != nil {
		return nil, err
	}

	var userID int
	err = tx.QueryRow(ctx,
		`INSERT INTO app_users (username, password_hash, full_name, role, client_id)
		 VALUES ($1,$2,$3,'buyer',$4) RETURNING id`,
		username, passwordHash, c.FullName, clientID,
	).Scan(&userID)
	if isUniqueViolation(err, "ux_app_users_username_lower") {
		return nil, errUsernameTaken
	}
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &User{ID: userID, Username: username, FullName: c.FullName, Role: RoleBuyer, ClientID: &clientID}, nil
}

type ClientInput struct {
	FullName          string
	Email             string
	Phone             string
	LicenseNumber     string
	LicenseIssuedAt   time.Time
	LicenseExpiresAt  time.Time
	BirthDate         time.Time
	PassportSeries    string
	PassportNumber    string
}

var (
	errUsernameTaken    = errors.New("username taken")
	errClientEmailTaken = errors.New("client email taken")
)

func ErrUsernameTaken(err error) bool    { return errors.Is(err, errUsernameTaken) }
func ErrClientEmailTaken(err error) bool { return errors.Is(err, errClientEmailTaken) }

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

// --- users management (admin) ---

type UserRow struct {
	ID       int
	Username string
	FullName string
	Role     Role
}

func (r *Repo) ListUsers(ctx context.Context) ([]UserRow, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, username, full_name, role FROM app_users WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserRow{}
	for rows.Next() {
		var u UserRow
		var role string
		if err := rows.Scan(&u.ID, &u.Username, &u.FullName, &role); err != nil {
			return nil, err
		}
		u.Role = Role(role)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *Repo) SetRole(ctx context.Context, id int, role Role) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE app_users SET role = $1 WHERE id = $2 AND deleted_at IS NULL`, string(role), id)
	return tag.RowsAffected() > 0, err
}

// --- sessions ---

type TokenPair struct {
	AccessToken       string
	RefreshToken      string
	AccessExpiresAt   time.Time
	RefreshExpiresAt  time.Time
}

func (r *Repo) CreateSession(ctx context.Context, userID int) (*TokenPair, error) {
	access, err := randomToken()
	if err != nil {
		return nil, err
	}
	refresh, err := randomToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tp := &TokenPair{
		AccessToken:      access,
		RefreshToken:     refresh,
		AccessExpiresAt:  now.Add(r.accessTTL),
		RefreshExpiresAt: now.Add(r.refreshTTL),
	}
	_, err = r.pool.Exec(ctx,
		`INSERT INTO sessions (access_token, refresh_token, user_id, access_expires_at, refresh_expires_at)
		 VALUES ($1,$2,$3,$4,$5)`,
		tp.AccessToken, tp.RefreshToken, userID, tp.AccessExpiresAt, tp.RefreshExpiresAt)
	if err != nil {
		return nil, err
	}
	return tp, nil
}

// UserByAccessToken returns nil (no error) both when the token doesn't
// exist and when it's expired — the caller (middleware) treats both as
// "not authenticated" the same way.
func (r *Repo) UserByAccessToken(ctx context.Context, token string) (*User, error) {
	var userID int
	var expiresAt time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT user_id, access_expires_at FROM sessions WHERE access_token = $1`, token,
	).Scan(&userID, &expiresAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(expiresAt) {
		return nil, nil
	}
	return r.FindByID(ctx, userID)
}

// Refresh rotates both tokens (не только access) — так украденный refresh
// нельзя переиспользовать параллельно со легитимным клиентом незаметно.
func (r *Repo) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	var userID int
	var expiresAt time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT user_id, refresh_expires_at FROM sessions WHERE refresh_token = $1`, refreshToken,
	).Scan(&userID, &expiresAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(expiresAt) {
		_, _ = r.pool.Exec(ctx, `DELETE FROM sessions WHERE refresh_token = $1`, refreshToken)
		return nil, nil
	}
	if _, err := r.pool.Exec(ctx, `DELETE FROM sessions WHERE refresh_token = $1`, refreshToken); err != nil {
		return nil, err
	}
	return r.CreateSession(ctx, userID)
}

func (r *Repo) Logout(ctx context.Context, accessToken string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM sessions WHERE access_token = $1`, accessToken)
	return err
}
