package gateway

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	appSettingKeyPasswordPolicy = "password_policy"
	appSettingKeyAuthSettings   = "auth_settings"
	appSettingKeySMTP           = "smtp"
	appSettingKeyOAuthGoogle    = "oauth_google"
	appSettingKeyAppBaseURL     = "app_base_url"
)

// appSettingsStore is a key-value store for runtime-tunable admin settings
// that should NOT live in the YAML config — SMTP credentials, OAuth secrets,
// password policy, etc. Values are JSON blobs; sensitive fields are encrypted
// before being passed in (callers do that with secretKeeper.encryptSecret).
type appSettingsStore struct {
	mu    sync.RWMutex
	db    *sql.DB
	cache map[string]json.RawMessage
}

func newAppSettingsStore(db *sql.DB) (*appSettingsStore, error) {
	store := &appSettingsStore{db: db, cache: make(map[string]json.RawMessage)}
	if db == nil {
		return store, nil
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS app_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		updated_by TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return nil, err
	}
	if err := store.reload(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *appSettingsStore) reload() error {
	if s.db == nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT key, value FROM app_settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	next := make(map[string]json.RawMessage)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		next[key] = json.RawMessage(value)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.cache = next
	s.mu.Unlock()
	return nil
}

// GetRaw returns the JSON for a key, or (nil, false) if unset.
func (s *appSettingsStore) GetRaw(key string) (json.RawMessage, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.cache[key]
	if !ok {
		return nil, false
	}
	out := make(json.RawMessage, len(value))
	copy(out, value)
	return out, true
}

// GetInto decodes the value into out. Returns sql.ErrNoRows-equivalent (false)
// when unset; in that case the caller should fall back to defaults.
func (s *appSettingsStore) GetInto(key string, out any) (bool, error) {
	raw, ok := s.GetRaw(key)
	if !ok {
		return false, nil
	}
	if out == nil {
		return true, nil
	}
	return true, json.Unmarshal(raw, out)
}

// Set replaces (or inserts) the value. updatedBy is for audit only.
func (s *appSettingsStore) Set(key string, value any, updatedBy string) error {
	if s == nil {
		return errors.New("app settings store not initialized")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	now := time.Now().Format(time.RFC3339)
	s.mu.Lock()
	s.cache[key] = encoded
	s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	_, err = s.db.Exec(`INSERT INTO app_settings (key, value, updated_at, updated_by)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		key, string(encoded), now, updatedBy,
	)
	return err
}

// PasswordPolicy returns the effective policy: stored value or default.
func (s *appSettingsStore) PasswordPolicy() passwordPolicy {
	out := defaultPasswordPolicy()
	if s == nil {
		return out
	}
	if _, err := s.GetInto(appSettingKeyPasswordPolicy, &out); err != nil {
		return defaultPasswordPolicy()
	}
	if out.MinLength <= 0 {
		out.MinLength = defaultPasswordPolicy().MinLength
	}
	if out.MaxLength <= 0 {
		out.MaxLength = defaultPasswordPolicy().MaxLength
	}
	return out
}

// authSettings governs registration / email-verification policy.
type authSettings struct {
	RequireEmailOnRegister      bool `json:"require_email_on_register"`
	RequireEmailVerifiedToLogin bool `json:"require_email_verified_to_login"`
	OAuthGoogleEnabled          bool `json:"oauth_google_enabled"`
	OAuthGoogleAutoRegister     bool `json:"oauth_google_auto_register"`
}

func defaultAuthSettings() authSettings {
	return authSettings{
		RequireEmailOnRegister:      true,
		RequireEmailVerifiedToLogin: true,
		OAuthGoogleEnabled:          false,
		OAuthGoogleAutoRegister:     true,
	}
}

func (s *appSettingsStore) AuthSettings() authSettings {
	out := defaultAuthSettings()
	if s == nil {
		return out
	}
	if _, err := s.GetInto(appSettingKeyAuthSettings, &out); err != nil {
		return defaultAuthSettings()
	}
	return out
}

// smtpSettings is the persisted form. Password is AES-GCM ciphertext.
type smtpSettings struct {
	Enabled       bool   `json:"enabled"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	PasswordEnc   string `json:"password_enc,omitempty"`
	FromAddress   string `json:"from_address"`
	FromName      string `json:"from_name"`
	UseStartTLS   bool   `json:"use_starttls"`
	UseSMTPS      bool   `json:"use_smtps"`
	SkipTLSVerify bool   `json:"skip_tls_verify"`
}

func (s *appSettingsStore) SMTPSettings() smtpSettings {
	var out smtpSettings
	if s == nil {
		return out
	}
	_, _ = s.GetInto(appSettingKeySMTP, &out)
	return out
}

// oauthGoogleSettings is the persisted form. ClientSecret is AES-GCM ciphertext.
type oauthGoogleSettings struct {
	Enabled          bool   `json:"enabled"`
	ClientID         string `json:"client_id"`
	ClientSecretEnc  string `json:"client_secret_enc,omitempty"`
	RedirectURL      string `json:"redirect_url"`
	HostedDomain     string `json:"hosted_domain,omitempty"`
	LinkExistingMode string `json:"link_existing_mode,omitempty"`
}

func (s *appSettingsStore) OAuthGoogleSettings() oauthGoogleSettings {
	var out oauthGoogleSettings
	if s == nil {
		return out
	}
	_, _ = s.GetInto(appSettingKeyOAuthGoogle, &out)
	if out.LinkExistingMode == "" {
		out.LinkExistingMode = "auto"
	}
	return out
}

// AppBaseURL returns the configured external base URL used to build links in
// outgoing emails (e.g. https://xmux.example.com). When unset, returns "".
func (s *appSettingsStore) AppBaseURL() string {
	if s == nil {
		return ""
	}
	raw, ok := s.GetRaw(appSettingKeyAppBaseURL)
	if !ok {
		return ""
	}
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		return ""
	}
	return out
}

// debugDump is for tests / admin diagnostics only.
func (s *appSettingsStore) debugDump() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.cache))
	for key, value := range s.cache {
		out[key] = fmt.Sprintf("%s", string(value))
	}
	return out
}
