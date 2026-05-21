package gateway

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	registrationCodeLifetime    = 10 * time.Minute
	registrationCodeResendCool  = 60 * time.Second
	registrationCodeMaxAttempts = 5
	registrationCodeLength      = 6
)

// IssueRegistrationCode generates a fresh 6-digit code for `email`, stores its
// hash (so the raw value never lives in the DB), and returns the plaintext so
// the caller can deliver it via mail. Re-issuing for the same email replaces
// the prior row, but the prior row's last_sent_at is honored to throttle
// resends to one per registrationCodeResendCool.
func (s *accountStore) IssueRegistrationCode(email string) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("account store not initialized with a database")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", errors.New("email is required")
	}

	now := time.Now()
	var lastSentAt string
	if err := s.db.QueryRow(`SELECT last_sent_at FROM email_verification_codes WHERE email = ?`, email).Scan(&lastSentAt); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if lastSentAt != "" {
		prev := parseAccountTime(lastSentAt)
		if !prev.IsZero() && now.Sub(prev) < registrationCodeResendCool {
			wait := registrationCodeResendCool - now.Sub(prev)
			return "", fmt.Errorf("发送过于频繁，请 %d 秒后重试", int(wait.Seconds()+0.5))
		}
	}

	code, err := randomDigitCode(registrationCodeLength)
	if err != nil {
		return "", err
	}
	codeHash := hashEmailToken(code)
	expires := now.Add(registrationCodeLifetime)

	_, err = s.db.Exec(`INSERT INTO email_verification_codes (email, code_hash, created_at, expires_at, attempts, last_sent_at)
		VALUES (?, ?, ?, ?, 0, ?)
		ON CONFLICT(email) DO UPDATE SET
			code_hash = excluded.code_hash,
			created_at = excluded.created_at,
			expires_at = excluded.expires_at,
			attempts = 0,
			last_sent_at = excluded.last_sent_at`,
		email, codeHash, formatAccountTime(now), formatAccountTime(expires), formatAccountTime(now),
	)
	if err != nil {
		return "", err
	}
	return code, nil
}

// ConsumeRegistrationCode validates `code` against the stored hash for
// `email`. On success the row is deleted (one-shot). On a bad code the
// attempt counter is bumped and the row is wiped after registrationCodeMaxAttempts.
func (s *accountStore) ConsumeRegistrationCode(email string, code string) error {
	if s == nil || s.db == nil {
		return errors.New("account store not initialized with a database")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	code = strings.TrimSpace(code)
	if email == "" || code == "" {
		return errors.New("email and code are required")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var storedHash, createdAt, expiresAt string
	var attempts int
	if err := tx.QueryRow(`SELECT code_hash, created_at, expires_at, attempts FROM email_verification_codes WHERE email = ?`, email).Scan(&storedHash, &createdAt, &expiresAt, &attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("验证码不存在或已失效，请重新获取")
		}
		return err
	}
	if exp := parseAccountTime(expiresAt); !exp.IsZero() && time.Now().After(exp) {
		_, _ = tx.Exec(`DELETE FROM email_verification_codes WHERE email = ?`, email)
		_ = tx.Commit()
		return errors.New("验证码已过期，请重新获取")
	}
	if storedHash != hashEmailToken(code) {
		attempts++
		if attempts >= registrationCodeMaxAttempts {
			_, _ = tx.Exec(`DELETE FROM email_verification_codes WHERE email = ?`, email)
			_ = tx.Commit()
			return errors.New("验证码错误次数过多，请重新获取")
		}
		if _, err := tx.Exec(`UPDATE email_verification_codes SET attempts = ? WHERE email = ?`, attempts, email); err != nil {
			return err
		}
		_ = tx.Commit()
		return errors.New("验证码错误")
	}
	if _, err := tx.Exec(`DELETE FROM email_verification_codes WHERE email = ?`, email); err != nil {
		return err
	}
	return tx.Commit()
}

func randomDigitCode(n int) (string, error) {
	if n <= 0 || n > 12 {
		return "", fmt.Errorf("invalid digit count %d", n)
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	v := binary.BigEndian.Uint64(buf)
	mod := uint64(1)
	for i := 0; i < n; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", n, v%mod), nil
}
