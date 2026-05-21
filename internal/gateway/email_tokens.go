package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	emailTokenKindVerify        = "verify_email"
	emailTokenKindPasswordReset = "password_reset"

	emailTokenLifetimeVerify = 24 * time.Hour
	emailTokenLifetimeReset  = 30 * time.Minute
)

// emailTokenRecord is the in-memory shape of an email_tokens row.
type emailTokenRecord struct {
	TokenHash string
	Username  string
	Kind      string
	Email     string
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    time.Time
}

// IssueEmailToken creates a random one-time token for the given username/kind
// and persists its SHA-256 hash. Returns the *plaintext* token (caller must
// include it in the outgoing email and never log/persist it elsewhere).
func (s *accountStore) IssueEmailToken(username, email, kind string, lifetime time.Duration) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("account store not initialized with a database")
	}
	username = strings.TrimSpace(strings.ToLower(username))
	email = strings.TrimSpace(strings.ToLower(email))
	if username == "" {
		return "", errors.New("username is required")
	}
	if email == "" {
		return "", errors.New("email is required")
	}
	switch kind {
	case emailTokenKindVerify, emailTokenKindPasswordReset:
	default:
		return "", fmt.Errorf("unknown email token kind %q", kind)
	}
	if lifetime <= 0 {
		switch kind {
		case emailTokenKindVerify:
			lifetime = emailTokenLifetimeVerify
		case emailTokenKindPasswordReset:
			lifetime = emailTokenLifetimeReset
		}
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := hashEmailToken(token)
	now := time.Now()

	if _, err := s.db.Exec(`INSERT INTO email_tokens (token_hash, username, kind, email, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		hash, username, kind, email,
		formatAccountTime(now),
		formatAccountTime(now.Add(lifetime)),
	); err != nil {
		return "", err
	}
	return token, nil
}

// ConsumeEmailToken atomically validates and marks the token as used. Returns
// the underlying record so callers can decide what to do (verify email vs
// reset password). Returns an error if the token is missing, expired, used,
// or kind-mismatched.
func (s *accountStore) ConsumeEmailToken(token, kind string) (emailTokenRecord, error) {
	var out emailTokenRecord
	if s == nil || s.db == nil {
		return out, errors.New("account store not initialized with a database")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return out, errors.New("token is required")
	}
	hash := hashEmailToken(token)

	tx, err := s.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	row := tx.QueryRow(`SELECT token_hash, username, kind, email, created_at, expires_at, COALESCE(used_at, '')
		FROM email_tokens WHERE token_hash = ?`, hash)
	var usedAt, createdAt, expiresAt string
	if err := row.Scan(&out.TokenHash, &out.Username, &out.Kind, &out.Email, &createdAt, &expiresAt, &usedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, errors.New("token is invalid or already used")
		}
		return out, err
	}
	if out.Kind != kind {
		return out, errors.New("token kind mismatch")
	}
	if usedAt != "" {
		return out, errors.New("token already used")
	}
	out.CreatedAt = parseAccountTime(createdAt)
	out.ExpiresAt = parseAccountTime(expiresAt)
	if !out.ExpiresAt.IsZero() && time.Now().After(out.ExpiresAt) {
		return out, errors.New("token has expired")
	}

	now := formatAccountTime(time.Now())
	if _, err := tx.Exec(`UPDATE email_tokens SET used_at = ? WHERE token_hash = ?`, now, hash); err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	out.UsedAt = time.Now()
	return out, nil
}

// PurgeEmailTokens removes tokens older than the given cutoff. Intended to be
// called from a periodic janitor; not strictly required for correctness.
func (s *accountStore) PurgeEmailTokens(olderThan time.Duration) error {
	if s == nil || s.db == nil {
		return nil
	}
	cutoff := time.Now().Add(-olderThan).Format(time.RFC3339)
	_, err := s.db.Exec(`DELETE FROM email_tokens WHERE expires_at < ? OR used_at != ''`, cutoff)
	return err
}

func hashEmailToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

// SetEmail attaches an email address to an account. Used for both initial
// binding at registration and the "old account back-fill" flow.
func (s *accountStore) SetEmail(username, email string) error {
	if s == nil {
		return errors.New("account store not initialized")
	}
	username = strings.TrimSpace(strings.ToLower(username))
	email = strings.TrimSpace(strings.ToLower(email))
	if username == "" {
		return errors.New("username is required")
	}
	if email == "" {
		return errors.New("email is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.accounts[username]
	if !ok {
		return errors.New("account not found")
	}
	// Reject duplicates (unique index would also catch this on save, but doing
	// it here gives a friendlier error path).
	for other, rec := range s.accounts {
		if other == username {
			continue
		}
		if strings.EqualFold(rec.Email, email) {
			return errors.New("email is already used by another account")
		}
	}
	record.Email = email
	record.EmailVerified = false
	record.EmailVerifiedAt = time.Time{}
	s.accounts[username] = record
	return s.saveLocked(fallbackAccountPolicy())
}

// MarkEmailVerified flips email_verified to true for the given username,
// asserting that the supplied email matches (defense against tokens issued
// pre-rebinding).
func (s *accountStore) MarkEmailVerified(username, email string) error {
	if s == nil {
		return errors.New("account store not initialized")
	}
	username = strings.TrimSpace(strings.ToLower(username))
	email = strings.TrimSpace(strings.ToLower(email))
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.accounts[username]
	if !ok {
		return errors.New("account not found")
	}
	if record.Email != "" && !strings.EqualFold(record.Email, email) {
		return errors.New("token does not match the account's email")
	}
	if record.Email == "" {
		record.Email = email
	}
	record.EmailVerified = true
	record.EmailVerifiedAt = time.Now()
	s.accounts[username] = record
	return s.saveLocked(fallbackAccountPolicy())
}

// AccountByEmail looks up an account by its (lowercased) email. Returns
// false when nothing matches.
func (s *accountStore) AccountByEmail(email string) (accountRecord, bool) {
	if s == nil {
		return accountRecord{}, false
	}
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return accountRecord{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, rec := range s.accounts {
		if strings.EqualFold(rec.Email, email) {
			return rec, true
		}
	}
	return accountRecord{}, false
}

// IsEmailUsed reports whether any account other than excludeUsername owns the
// given email.
func (s *accountStore) IsEmailUsed(email, excludeUsername string) bool {
	rec, ok := s.AccountByEmail(email)
	if !ok {
		return false
	}
	return rec.Username != strings.ToLower(strings.TrimSpace(excludeUsername))
}

// AccountRecord returns the in-memory record for a username (copy).
func (s *accountStore) AccountRecord(username string) (accountRecord, bool) {
	if s == nil {
		return accountRecord{}, false
	}
	username = strings.TrimSpace(strings.ToLower(username))
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.accounts[username]
	return rec, ok
}

// RevokeAllSessionsFor invalidates every session belonging to the user.
// Used after a successful password reset.
func (s *accountStore) RevokeAllSessionsFor(username string) error {
	if s == nil {
		return nil
	}
	username = strings.TrimSpace(strings.ToLower(username))
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, session := range s.sessions {
		if strings.EqualFold(session.Username, username) {
			delete(s.sessions, id)
		}
	}
	return s.saveLocked(fallbackAccountPolicy())
}

// SetPassword forcibly replaces the password hash for username. The caller is
// responsible for validating the new password against the policy and (where
// appropriate) for invalidating existing sessions afterwards.
func (s *accountStore) SetPassword(username, newPassword string) error {
	if s == nil {
		return errors.New("account store not initialized")
	}
	username = strings.TrimSpace(strings.ToLower(username))
	if username == "" {
		return errors.New("username is required")
	}
	if err := validatePassword(newPassword, s.passwordPolicy()); err != nil {
		return err
	}
	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.accounts[username]
	if !ok {
		return errors.New("account not found")
	}
	rec.PasswordHash = hash
	s.accounts[username] = rec
	return s.saveLocked(fallbackAccountPolicy())
}
