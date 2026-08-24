package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/hkjang/AgentHub/internal/cryptox"
)

var ErrNotFound = errors.New("not found")

// ErrConflict is a request that asks for something already taken — a name, an
// identifier. It is separated from every other store failure because it is the
// caller's mistake to fix, and answering 500 for it both misleads the caller and
// logs a server fault that never happened.
var ErrConflict = errors.New("conflict")

// Conflict carries the sentence a person should read. It answers errors.Is for
// ErrConflict without putting the sentinel's own word in front of the message,
// because err.Error() is printed by more places than the one that classifies it —
// and a message reading "conflict: 같은 이름의…" is the sentinel leaking into the
// console.
type Conflict struct{ Message string }

func (c Conflict) Error() string { return c.Message }

func (c Conflict) Is(target error) bool { return target == ErrConflict }

// ErrInvalid is a request that names something impossible — a kind, a mode, a
// scope this platform does not have, or a number outside the range it accepts.
// It is the caller's to fix, like a Conflict, and separated from it because
// nothing about the stored state has to change: the same request will be refused
// tomorrow.
var ErrInvalid = errors.New("invalid")

// Invalid carries the sentence a person should read, on the same reasoning as
// Conflict: err.Error() is printed by more places than the one that classifies
// it, so the sentinel's own word must not end up in front of the message.
type Invalid struct{ Message string }

func (i Invalid) Error() string { return i.Message }

func (i Invalid) Is(target error) bool { return target == ErrInvalid }

// conflictIfTaken turns a unique-constraint violation into a Conflict with a
// message somebody can act on.
//
// Postgres reports it as `duplicate key value violates unique constraint
// "agent_definitions_owner_id_name_key" (SQLSTATE 23505)`, and that string went
// to the person who had simply reused a name — a database schema detail, in
// English, presented as a failure of the platform rather than as a choice they
// could change.
func conflictIfTaken(err error, message string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Conflict{Message: message}
	}
	return err
}

type Store struct {
	pool   *pgxpool.Pool
	cipher *cryptox.Cipher
}

func Open(ctx context.Context, dsn string, cipher *cryptox.Cipher) (*Store, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse Postgres DSN: %w", err)
	}
	config.MaxConns = 20
	config.MinConns = 2
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 10 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to Postgres: %w", err)
	}
	return &Store{pool: pool, cipher: cipher}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

type User struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	Email       string  `json:"email"`
	DisplayName string  `json:"displayName"`
	Role        string  `json:"role"`
	Status      string  `json:"status"`
	ManagerID   *string `json:"managerId,omitempty"`
	// DepartmentID is nil for somebody who belongs to no department; they get
	// the platform's own limits, the same as a deployment with no departments.
	DepartmentID *string    `json:"departmentId,omitempty"`
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
}

func (s *Store) BootstrapAdmin(ctx context.Context, username, password string) error {
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin'`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	id := uuid.NewString()
	_, err = s.pool.Exec(ctx, `INSERT INTO users(id, username, display_name, password_hash, role) VALUES($1,$2,$3,$4,'admin')`, id, username, username, string(hash))
	if err != nil {
		return err
	}
	return s.EnsureUserKeyring(ctx, id)
}

// userTargets is where userColumns lands, in that order. Two queries read the
// user columns alongside something else and cannot use scanUser; they go through
// here so that adding a column stays one edit. A column added to userColumns and
// forgotten in one of those scans does not fail loudly — the row simply does not
// decode, and for the login query that reads as a wrong password.
func (u *User) scanTargets() []any {
	return []any{&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Role, &u.Status, &u.ManagerID, &u.DepartmentID, &u.LastLoginAt, &u.CreatedAt}
}

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(u.scanTargets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

const userColumns = `id, username, email, display_name, role, status, manager_id, department_id, last_login_at, created_at`

func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1`, id))
}

func (s *Store) AuthenticateLocal(ctx context.Context, username, password string) (User, error) {
	var hash *string
	var user User
	err := s.pool.QueryRow(ctx, `SELECT `+userColumns+`, password_hash FROM users WHERE lower(username)=lower($1) AND status='active'`, strings.TrimSpace(username)).Scan(
		append(user.scanTargets(), &hash)...,
	)
	if errors.Is(err, pgx.ErrNoRows) || hash == nil || bcrypt.CompareHashAndPassword([]byte(*hash), []byte(password)) != nil {
		return User{}, errors.New("invalid username or password")
	}
	if err != nil {
		return User{}, err
	}
	_, _ = s.pool.Exec(ctx, `UPDATE users SET last_login_at=now() WHERE id=$1`, user.ID)
	return user, nil
}

// ErrNoLocalPassword is what an account signed in through SSO gets back: there
// is no password on it to change, and saying so is better than refusing as if
// the current one were wrong.
var ErrNoLocalPassword = errors.New("this account has no local password")

// ChangePassword rotates a local password and ends every other session.
//
// It exists because there was no way to do this at all. The one local account a
// deployment has is created from AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD on first
// start and nothing ever wrote password_hash again — not the product, not a
// restart with a different value. That password lives in a manifest, which is
// the kind of file that reaches a git history, a CI log or a shared runbook, and
// on an offline site it is the only way in.
//
// Every other session for this user goes. A rotation is done because somebody
// might have the old password, and leaving their browser signed in would make
// the rotation a gesture; keeping this request's own session is what stops the
// person doing the right thing from being thrown out for it.
func (s *Store) ChangePassword(ctx context.Context, userID, current, next, keepSession string) error {
	var hash *string
	if err := s.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1 AND status='active'`, userID).Scan(&hash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if hash == nil {
		return ErrNoLocalPassword
	}
	if bcrypt.CompareHashAndPassword([]byte(*hash), []byte(current)) != nil {
		return ErrInvalidPassword
	}
	fresh, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE users SET password_hash=$2, updated_at=now() WHERE id=$1`, userID, string(fresh)); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1 AND id_hash<>$2`, userID, cryptox.TokenHash(keepSession))
	return err
}

// ErrInvalidPassword is a wrong current password, kept apart from a wrong new
// one so the API can say which.
var ErrInvalidPassword = errors.New("current password does not match")

func (s *Store) UpsertOIDCUser(ctx context.Context, subject, username, email, displayName string, admin bool) (User, error) {
	role := "user"
	if admin {
		role = "admin"
	}
	if username == "" {
		username = subject
	}
	if displayName == "" {
		displayName = username
	}
	var existingID, existingUsername string
	existingErr := s.pool.QueryRow(ctx, `SELECT id,username FROM users WHERE oidc_subject=$1`, subject).Scan(&existingID, &existingUsername)
	if existingErr != nil && !errors.Is(existingErr, pgx.ErrNoRows) {
		return User{}, existingErr
	}
	var usernameInUse bool
	if existingID == "" {
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(username)=lower($1))`, username).Scan(&usernameInUse); err != nil {
			return User{}, err
		}
	} else {
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(username)=lower($1) AND id<>$2)`, username, existingID).Scan(&usernameInUse); err != nil {
			return User{}, err
		}
	}
	if usernameInUse {
		if existingUsername != "" {
			username = existingUsername
		} else {
			digest := sha256.Sum256([]byte(subject))
			username = username + "-" + fmt.Sprintf("%x", digest[:4])
		}
	}
	if email != "" {
		var emailInUse bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(email)=lower($1) AND id<>COALESCE(NULLIF($2,''),'__new__'))`, email, existingID).Scan(&emailInUse); err != nil {
			return User{}, err
		}
		if emailInUse {
			email = ""
		}
	}
	var id string
	err := s.pool.QueryRow(ctx, `
INSERT INTO users(id, username, email, display_name, oidc_subject, role, last_login_at)
VALUES($1,$2,$3,$4,$5,$6,now())
ON CONFLICT (oidc_subject) WHERE oidc_subject IS NOT NULL DO UPDATE SET
  username=excluded.username, email=excluded.email, display_name=excluded.display_name,
  role=CASE WHEN users.role='admin' THEN 'admin' ELSE excluded.role END, last_login_at=now(), updated_at=now()
RETURNING id`, uuid.NewString(), username, email, displayName, subject, role).Scan(&id)
	if err != nil {
		return User{}, err
	}
	if err := s.EnsureUserKeyring(ctx, id); err != nil {
		return User{}, err
	}
	return s.UserByID(ctx, id)
}

func (s *Store) CreateSession(ctx context.Context, userID, ip, agent string) (token, csrf string, expires time.Time, err error) {
	token, err = cryptox.RandomToken(32)
	if err != nil {
		return
	}
	csrf, err = cryptox.RandomToken(24)
	if err != nil {
		return
	}
	expires = time.Now().Add(12 * time.Hour)
	_, err = s.pool.Exec(ctx, `INSERT INTO sessions(id_hash,user_id,csrf_hash,expires_at,ip_address,user_agent) VALUES($1,$2,$3,$4,$5,$6)`, cryptox.TokenHash(token), userID, cryptox.TokenHash(csrf), expires, ip, agent)
	return
}

func (s *Store) SessionUser(ctx context.Context, token string) (User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+prefixColumns("u", userColumns)+` FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.id_hash=$1 AND s.expires_at>now() AND u.status='active'`, cryptox.TokenHash(token)))
}

func (s *Store) ValidateCSRF(ctx context.Context, token, csrf string) bool {
	var ok bool
	_ = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id_hash=$1 AND csrf_hash=$2 AND expires_at>now())`, cryptox.TokenHash(token), cryptox.TokenHash(csrf)).Scan(&ok)
	return ok
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id_hash=$1`, cryptox.TokenHash(token))
	return err
}

// SweepExpiredSessions removes sessions that can no longer log anybody in.
//
// This is not history and it is not the operator's decision. Every other sweep on
// this platform is off until somebody chooses a number, because deleting a
// deployment's records by default would be wrong. An expired session is not a
// record of anything: it cannot authenticate a request, nothing reads it, and it
// is on the hot path of every authenticated request through the id_hash index. A
// development deployment with one user had accumulated four hundred and fifty of
// them, so a real one keeps a row per login per user forever.
//
// The grace period is so that a session which expired moments ago is still there
// if somebody is looking at why a request was refused.
func (s *Store) SweepExpiredSessions(ctx context.Context, grace time.Duration) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now() - $1::interval`, grace.String())
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) CreateRuntimeLaunchTicket(ctx context.Context, runtimeID, userID string) (string, time.Time, error) {
	token, err := cryptox.RandomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().UTC().Add(2 * time.Minute)
	_, err = s.pool.Exec(ctx, `WITH expired AS (DELETE FROM runtime_launch_tickets WHERE expires_at<=now()) INSERT INTO runtime_launch_tickets(id_hash,runtime_id,user_id,expires_at) VALUES($1,$2,$3,$4)`, cryptox.TokenHash(token), runtimeID, userID, expires)
	return token, expires, err
}

func (s *Store) ConsumeRuntimeLaunchTicket(ctx context.Context, token string) (runtimeID, userID string, err error) {
	err = s.pool.QueryRow(ctx, `DELETE FROM runtime_launch_tickets WHERE id_hash=$1 AND expires_at>now() RETURNING runtime_id,user_id`, cryptox.TokenHash(token)).Scan(&runtimeID, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

func prefixColumns(prefix, columns string) string {
	parts := strings.Split(columns, ",")
	for i := range parts {
		parts[i] = prefix + "." + strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ", ")
}

func (s *Store) Setting(ctx context.Context, key string, dst any) error {
	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key=$1`, key).Scan(&raw); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func (s *Store) Settings(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.pool.Query(ctx, `SELECT key,value FROM system_settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]json.RawMessage{}
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, rows.Err()
}

func (s *Store) PutSetting(ctx context.Context, key string, value any, secret *string, actor string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var encrypted *string
	if secret != nil && *secret != "" && *secret != "••••••••" {
		v, err := s.cipher.Encrypt([]byte(*secret), "system-setting:"+key)
		if err != nil {
			return err
		}
		encrypted = &v
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO system_settings(key,value,secret_value,updated_by,updated_at) VALUES($1,$2,$3,$4,now()) ON CONFLICT(key) DO UPDATE SET value=excluded.value, secret_value=COALESCE(excluded.secret_value,system_settings.secret_value), updated_by=excluded.updated_by, updated_at=now()`, key, raw, encrypted, actor)
	return err
}

func (s *Store) SettingSecret(ctx context.Context, key string) (string, error) {
	var encrypted *string
	if err := s.pool.QueryRow(ctx, `SELECT secret_value FROM system_settings WHERE key=$1`, key).Scan(&encrypted); err != nil {
		return "", err
	}
	if encrypted == nil {
		return "", nil
	}
	value, err := s.cipher.Decrypt(*encrypted, "system-setting:"+key)
	return string(value), err
}

func (s *Store) EnsureUserKeyring(ctx context.Context, userID string) error {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_keyrings WHERE user_id=$1 AND active)`, userID).Scan(&exists); err != nil || exists {
		return err
	}
	key, err := cryptox.RandomKey()
	if err != nil {
		return err
	}
	encrypted, err := s.cipher.Encrypt(key, "user-key:"+userID+":1")
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO user_keyrings(id,user_id,version,encrypted_data_key) VALUES($1,$2,1,$3) ON CONFLICT DO NOTHING`, uuid.NewString(), userID, encrypted)
	return err
}

func (s *Store) Audit(ctx context.Context, actor *User, action, resourceType, resourceID, outcome, ip string, details any) {
	var actorID any
	actorName := "system"
	if actor != nil {
		actorID = actor.ID
		actorName = actor.Username
	}
	raw, _ := json.Marshal(details)
	_, _ = s.pool.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_name,action,resource_type,resource_id,outcome,ip_address,details) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, actorID, actorName, action, resourceType, resourceID, outcome, ip, raw)
}
