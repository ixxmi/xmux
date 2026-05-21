package gateway

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"cloud-terminal/internal/policy"

	_ "modernc.org/sqlite"
)

const (
	accountCookieName        = "cloud-terminal-account"
	accountPasswordMinLength = 8
	accountSessionTTL        = 30 * 24 * time.Hour
	passwordHashIterations   = 120000

	accountRoleAdmin = "admin"
	accountRoleUser  = "user"
)

type accountStore struct {
	mu             sync.RWMutex
	path           string
	legacyPath     string
	enabled        bool
	adminAccount   string
	accounts       map[string]accountRecord
	sessions       map[string]accountSession
	userSettings   map[string]userSettings
	db             *sql.DB
	policyResolver func() passwordPolicy
}

type accountRecord struct {
	Username        string    `json:"username"`
	Role            string    `json:"role"`
	PasswordHash    string    `json:"password_hash"`
	CreatedAt       time.Time `json:"created_at"`
	LastLoginAt     time.Time `json:"last_login_at,omitempty"`
	Email           string    `json:"email,omitempty"`
	EmailVerified   bool      `json:"email_verified,omitempty"`
	EmailVerifiedAt time.Time `json:"email_verified_at,omitempty"`
	Disabled        bool      `json:"disabled,omitempty"`
}

type accountSession struct {
	SessionID string    `json:"session_id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type accountStoreFile struct {
	Version      int                       `json:"version"`
	Updated      time.Time                 `json:"updated"`
	Accounts     map[string]accountRecord  `json:"accounts"`
	Sessions     map[string]accountSession `json:"sessions"`
	UserSettings map[string]userSettings   `json:"user_settings,omitempty"`
}

type accountPublicInfo struct {
	Username      string `json:"username"`
	Role          string `json:"role"`
	CreatedAt     string `json:"created_at"`
	LastLoginAt   string `json:"last_login_at,omitempty"`
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified"`
	Disabled      bool   `json:"disabled"`
}

type userSettings struct {
	Username           string
	CloudTunnelEnabled bool
	Commands           map[string]policy.CommandPolicy
	AllowPaths         []string
	ArchivedFolders    []string
	ArchivedSessions   []string
	ForgottenFolders   []string
	ForgottenSessions  []string
	QuickCommands      []userQuickCommand
}

type userQuickCommand struct {
	Label   string `json:"label"`
	Command string `json:"command"`
}

type userSettingsPayload struct {
	Account            string                         `json:"account"`
	Role               string                         `json:"role"`
	CloudTunnelEnabled bool                           `json:"cloud_tunnel_enabled"`
	GatewayURL         string                         `json:"gateway_url"`
	Commands           map[string]adminCommandPayload `json:"commands"`
	AllowPaths         []string                       `json:"allow_paths"`
	ArchivedFolders    []string                       `json:"archived_folders,omitempty"`
	ArchivedSessions   []string                       `json:"archived_sessions,omitempty"`
	ForgottenFolders   []string                       `json:"forgotten_folders,omitempty"`
	ForgottenSessions  []string                       `json:"forgotten_sessions,omitempty"`
	QuickCommands      []userQuickCommand             `json:"quick_commands,omitempty"`
	PolicyLimits       userPolicyLimitsPayload        `json:"policy_limits"`
}

type userPolicyLimitsPayload struct {
	Commands   map[string]adminCommandPayload `json:"commands"`
	AllowPaths []string                       `json:"allow_paths"`
	Deny       []string                       `json:"deny"`
}

type userSettingsUpdatePayload struct {
	CloudTunnelEnabled bool                           `json:"cloud_tunnel_enabled"`
	Commands           map[string]adminCommandPayload `json:"commands"`
	AllowPaths         []string                       `json:"allow_paths"`
	ArchivedFolders    []string                       `json:"archived_folders,omitempty"`
	ArchivedSessions   []string                       `json:"archived_sessions,omitempty"`
	ForgottenFolders   []string                       `json:"forgotten_folders,omitempty"`
	ForgottenSessions  []string                       `json:"forgotten_sessions,omitempty"`
}

type userArchiveUpdatePayload struct {
	ArchivedFolders   []string `json:"archived_folders"`
	ArchivedSessions  []string `json:"archived_sessions"`
	ForgottenFolders  []string `json:"forgotten_folders"`
	ForgottenSessions []string `json:"forgotten_sessions"`
}

type userQuickCommandsUpdatePayload struct {
	QuickCommands []userQuickCommand `json:"quick_commands"`
}

type accountProfileUpdatePayload struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func newAccountStore(databasePath string, legacyPath string, registrationEnabled bool, adminUsername string, adminPassword string, defaults policy.Config) (*accountStore, error) {
	if len(defaults.Commands) == 0 {
		defaults = fallbackAccountPolicy()
	}
	store := &accountStore{
		path:         strings.TrimSpace(databasePath),
		legacyPath:   strings.TrimSpace(legacyPath),
		enabled:      registrationEnabled,
		accounts:     make(map[string]accountRecord),
		sessions:     make(map[string]accountSession),
		userSettings: make(map[string]userSettings),
	}
	if normalized, err := normalizeAccountUsername(adminUsername); err == nil {
		store.adminAccount = normalized
	}
	if store.path == "" {
		return store, store.ensureAdmin(adminUsername, adminPassword, defaults)
	}
	if err := store.open(defaults); err != nil {
		return nil, err
	}
	if err := store.ensureAdmin(adminUsername, adminPassword, defaults); err != nil {
		return nil, err
	}
	return store, nil
}

func newFallbackAccountStore(databasePath string, legacyPath string, registrationEnabled bool, adminUsername string) *accountStore {
	store := &accountStore{
		path:         strings.TrimSpace(databasePath),
		legacyPath:   strings.TrimSpace(legacyPath),
		enabled:      registrationEnabled,
		adminAccount: strings.TrimSpace(strings.ToLower(adminUsername)),
		accounts:     make(map[string]accountRecord),
		sessions:     make(map[string]accountSession),
		userSettings: make(map[string]userSettings),
	}
	return store
}

func fallbackAccountPolicy() policy.Config {
	return policy.Config{Commands: map[string]policy.CommandPolicy{"pwd": {Enabled: true}}}
}

func (s *accountStore) open(defaults policy.Config) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		return err
	}
	s.db = db
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return err
	}
	if err := s.initSchema(); err != nil {
		return err
	}
	if err := s.migrateLegacy(defaults); err != nil {
		return err
	}
	return s.load()
}

func (s *accountStore) initSchema() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS accounts (
			username TEXT PRIMARY KEY,
			role TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			created_at TEXT NOT NULL,
			last_login_at TEXT,
			email TEXT,
			email_verified INTEGER NOT NULL DEFAULT 0,
			email_verified_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			session_id TEXT PRIMARY KEY,
			username TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS email_tokens (
			token_hash TEXT PRIMARY KEY,
			username TEXT NOT NULL,
			kind TEXT NOT NULL,
			email TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			used_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS pending_registrations (
			token_hash TEXT PRIMARY KEY,
			username TEXT NOT NULL,
			email TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'user',
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			used_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS email_verification_codes (
			email TEXT PRIMARY KEY,
			code_hash TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_sent_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS oauth_identities (
			provider TEXT NOT NULL,
			subject TEXT NOT NULL,
			username TEXT NOT NULL,
			email TEXT,
			linked_at TEXT NOT NULL,
			PRIMARY KEY (provider, subject)
		)`,
		`CREATE TABLE IF NOT EXISTS user_settings (
			username TEXT PRIMARY KEY,
			cloud_tunnel_enabled INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS user_commands (
			username TEXT NOT NULL,
			name TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			bin TEXT NOT NULL DEFAULT '',
			interactive INTEGER NOT NULL,
			subcommands TEXT NOT NULL DEFAULT '[]',
			allow_paths TEXT NOT NULL DEFAULT '[]',
			max_args INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (username, name)
		)`,
		`CREATE TABLE IF NOT EXISTS user_allow_paths (
			username TEXT NOT NULL,
			path TEXT NOT NULL,
			PRIMARY KEY (username, path)
		)`,
		`CREATE TABLE IF NOT EXISTS user_archived_folders (
			username TEXT NOT NULL,
			path TEXT NOT NULL,
			PRIMARY KEY (username, path)
		)`,
		`CREATE TABLE IF NOT EXISTS user_archived_sessions (
			username TEXT NOT NULL,
			session_id TEXT NOT NULL,
			PRIMARY KEY (username, session_id)
		)`,
		`CREATE TABLE IF NOT EXISTS user_forgotten_folders (
			username TEXT NOT NULL,
			path TEXT NOT NULL,
			PRIMARY KEY (username, path)
		)`,
		`CREATE TABLE IF NOT EXISTS user_forgotten_sessions (
			username TEXT NOT NULL,
			session_id TEXT NOT NULL,
			PRIMARY KEY (username, session_id)
		)`,
		`CREATE TABLE IF NOT EXISTS user_quick_commands (
			username TEXT NOT NULL,
			position INTEGER NOT NULL,
			label TEXT NOT NULL DEFAULT '',
			command TEXT NOT NULL,
			PRIMARY KEY (username, position)
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	return s.migrateAccountsSchema()
}

// migrateAccountsSchema brings older DB files up to date by adding columns
// and indexes that weren't part of the original CREATE TABLE. Each step is
// idempotent and tolerant of "duplicate column" errors so it can run on
// every startup.
func (s *accountStore) migrateAccountsSchema() error {
	addColumn := func(table, column, definition string) error {
		_, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, definition))
		if err == nil {
			return nil
		}
		if strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return nil
		}
		return err
	}
	if err := addColumn("accounts", "email", "TEXT"); err != nil {
		return err
	}
	if err := addColumn("accounts", "email_verified", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := addColumn("accounts", "email_verified_at", "TEXT"); err != nil {
		return err
	}
	if err := addColumn("accounts", "disabled", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS accounts_email_uidx ON accounts(email) WHERE email IS NOT NULL AND email != ''`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS email_tokens_user_kind_idx ON email_tokens(username, kind)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS pending_registrations_email_idx ON pending_registrations(email)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS pending_registrations_username_idx ON pending_registrations(username)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS oauth_identities_user_idx ON oauth_identities(username)`); err != nil {
		return err
	}
	return nil
}

func (s *accountStore) load() error {
	if s.db == nil {
		return s.loadLegacyJSON()
	}
	accounts, err := s.loadAccounts()
	if err != nil {
		return err
	}
	sessions, err := s.loadSessions()
	if err != nil {
		return err
	}
	settings, err := s.loadAllUserSettings()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.accounts = accounts
	s.sessions = sessions
	s.userSettings = settings
	s.mu.Unlock()
	return nil
}

func (s *accountStore) loadLegacyJSON() error {
	if s.legacyPath == "" {
		s.legacyPath = s.path
	}
	content, err := os.ReadFile(s.legacyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var state accountStoreFile
	if err := json.Unmarshal(content, &state); err != nil {
		return err
	}
	if state.Accounts != nil {
		s.accounts = state.Accounts
		for username, record := range s.accounts {
			record.Role = normalizeAccountRole(record.Role)
			if record.Role == "" {
				record.Role = accountRoleUser
			}
			if s.adminAccount != "" && username == s.adminAccount {
				record.Role = accountRoleAdmin
			}
			s.accounts[username] = record
		}
	}
	if state.Sessions != nil {
		now := time.Now()
		for sessionID, session := range state.Sessions {
			if session.ExpiresAt.After(now) && strings.TrimSpace(session.Username) != "" {
				if strings.TrimSpace(session.SessionID) == "" {
					session.SessionID = sessionID
				}
				s.sessions[session.SessionID] = session
			}
		}
	}
	if state.UserSettings != nil {
		s.userSettings = make(map[string]userSettings, len(state.UserSettings))
		for username, settings := range state.UserSettings {
			username = normalizeTunnelAccount(firstNonEmpty(settings.Username, username))
			if username == "" {
				continue
			}
			settings.Username = username
			if settings.Commands == nil {
				settings.Commands = make(map[string]policy.CommandPolicy)
			}
			settings.AllowPaths = cleanPaths(settings.AllowPaths)
			settings.ArchivedFolders = cleanPaths(settings.ArchivedFolders)
			settings.ForgottenFolders = cleanPaths(settings.ForgottenFolders)
			settings.ArchivedSessions = cleanList(settings.ArchivedSessions)
			settings.ForgottenSessions = cleanList(settings.ForgottenSessions)
			s.userSettings[username] = settings
		}
	}
	return nil
}

func (s *accountStore) ensureAdmin(username string, password string, defaults policy.Config) error {
	if s == nil {
		return nil
	}
	username, err := normalizeAccountUsername(username)
	if err != nil {
		return err
	}
	if len(password) < accountPasswordMinLength {
		return fmt.Errorf("admin password must be at least %d characters", accountPasswordMinLength)
	}
	s.adminAccount = username

	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.accounts[username]
	if ok {
		if record.Role != accountRoleAdmin {
			record.Role = accountRoleAdmin
			s.accounts[username] = record
			return s.saveLocked(defaults)
		}
		if _, exists := s.userSettings[username]; !exists {
			s.userSettings[username] = defaultUserSettings(username, defaults)
			return s.saveLocked(defaults)
		}
		return nil
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	now := time.Now()
	s.accounts[username] = accountRecord{
		Username:     username,
		Role:         accountRoleAdmin,
		PasswordHash: hash,
		CreatedAt:    now,
	}
	s.userSettings[username] = defaultUserSettings(username, defaults)
	return s.saveLocked(defaults)
}

func (s *accountStore) saveLocked(defaults policy.Config) error {
	if s == nil {
		return nil
	}
	if len(defaults.Commands) == 0 {
		defaults = fallbackAccountPolicy()
	}
	if s.db != nil {
		return s.saveSQLiteLocked(defaults)
	}
	if s.path == "" {
		return nil
	}
	state := accountStoreFile{
		Version:      1,
		Updated:      time.Now(),
		Accounts:     s.accounts,
		Sessions:     s.sessions,
		UserSettings: s.userSettings,
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *accountStore) SetPasswordPolicyResolver(fn func() passwordPolicy) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.policyResolver = fn
	s.mu.Unlock()
}

func (s *accountStore) passwordPolicy() passwordPolicy {
	if s == nil {
		return defaultPasswordPolicy()
	}
	s.mu.RLock()
	fn := s.policyResolver
	s.mu.RUnlock()
	if fn == nil {
		return defaultPasswordPolicy()
	}
	return fn()
}

func (s *accountStore) Register(username string, password string, defaults policy.Config) (accountSession, error) {
	return s.createAccount(username, password, accountRoleUser, false, defaults)
}

func (s *accountStore) CreateAccount(username string, password string, role string, defaults policy.Config) error {
	_, err := s.createAccount(username, password, role, true, defaults)
	return err
}

func (s *accountStore) createAccount(username string, password string, role string, byAdmin bool, defaults policy.Config) (accountSession, error) {
	if s == nil {
		return accountSession{}, errors.New("account store is not configured")
	}
	username, err := normalizeAccountUsername(username)
	if err != nil {
		return accountSession{}, err
	}
	if err := validatePassword(password, s.passwordPolicy()); err != nil {
		return accountSession{}, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return accountSession{}, err
	}
	role = normalizeAccountRole(role)
	if role == "" {
		role = accountRoleUser
	}
	now := time.Now()
	session, err := newAccountSession(username, now)
	if err != nil {
		return accountSession{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[username]; ok {
		return accountSession{}, errors.New("account already exists")
	}
	if !byAdmin && role == accountRoleAdmin {
		return accountSession{}, errors.New("admin accounts can only be created by an administrator")
	}
	if !byAdmin && !s.enabled {
		return accountSession{}, errors.New("account registration is disabled")
	}
	s.accounts[username] = accountRecord{
		Username:     username,
		Role:         role,
		PasswordHash: hash,
		CreatedAt:    now,
		LastLoginAt:  now,
	}
	s.userSettings[username] = defaultUserSettings(username, defaults)
	if !byAdmin {
		s.sessions[session.SessionID] = session
	}
	if err := s.saveLocked(defaults); err != nil {
		return accountSession{}, err
	}
	return session, nil
}

func (s *accountStore) Login(username string, password string) (accountSession, error) {
	if s == nil {
		return accountSession{}, errors.New("account store is not configured")
	}
	username, err := normalizeAccountUsername(username)
	if err != nil {
		return accountSession{}, err
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.accounts[username]
	if !ok || !verifyPassword(password, record.PasswordHash) {
		return accountSession{}, errors.New("invalid username or password")
	}
	if record.Disabled {
		return accountSession{}, errors.New("account is disabled")
	}
	session, err := newAccountSession(username, now)
	if err != nil {
		return accountSession{}, err
	}
	record.LastLoginAt = now
	s.accounts[username] = record
	s.sessions[session.SessionID] = session
	if _, ok := s.userSettings[username]; !ok {
		s.userSettings[username] = userSettings{Username: username}
	}
	if err := s.saveLocked(policy.Config{}); err != nil {
		return accountSession{}, err
	}
	return session, nil
}

func (s *accountStore) IssueSession(username string) (accountSession, error) {
	if s == nil {
		return accountSession{}, errors.New("account store is not configured")
	}
	username, err := normalizeAccountUsername(username)
	if err != nil {
		return accountSession{}, err
	}
	now := time.Now()
	session, err := newAccountSession(username, now)
	if err != nil {
		return accountSession{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.accounts[username]
	if !ok {
		return accountSession{}, errors.New("account does not exist")
	}
	record.LastLoginAt = now
	s.accounts[username] = record
	s.sessions[session.SessionID] = session
	if err := s.saveLocked(policy.Config{}); err != nil {
		return accountSession{}, err
	}
	return session, nil
}

func (s *accountStore) ValidateSession(sessionID string) (string, bool) {
	account, ok := s.ValidateSessionInfo(sessionID)
	if !ok {
		return "", false
	}
	return account.Username, true
}

func (s *accountStore) ValidateSessionInfo(sessionID string) (accountPublicInfo, bool) {
	if s == nil {
		return accountPublicInfo{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return accountPublicInfo{}, false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	record := s.accounts[session.Username]
	if !ok || session.ExpiresAt.Before(now) || record.Username == "" || record.Disabled {
		if ok {
			delete(s.sessions, sessionID)
			_ = s.saveLocked(policy.Config{})
		}
		return accountPublicInfo{}, false
	}
	return accountPublicInfo{
		Username:      record.Username,
		Role:          normalizeAccountRole(record.Role),
		CreatedAt:     formatAccountTime(record.CreatedAt),
		LastLoginAt:   formatAccountTime(record.LastLoginAt),
		Email:         record.Email,
		EmailVerified: record.EmailVerified,
		Disabled:      record.Disabled,
	}, true
}

func (s *accountStore) RevokeSession(sessionID string) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return
	}
	delete(s.sessions, sessionID)
	_ = s.saveLocked(policy.Config{})
}

func (s *accountStore) VerifyPassword(username string, password string) bool {
	if s == nil {
		return false
	}
	username, err := normalizeAccountUsername(username)
	if err != nil {
		return false
	}
	s.mu.RLock()
	record, ok := s.accounts[username]
	s.mu.RUnlock()
	return ok && verifyPassword(password, record.PasswordHash)
}

func (s *accountStore) VerifySession(sessionID string, username string) bool {
	account, ok := s.ValidateSessionInfo(sessionID)
	if !ok {
		return false
	}
	normalized, err := normalizeAccountUsername(username)
	if err != nil {
		return false
	}
	return account.Username == normalized
}

func (s *accountStore) UpdatePassword(username string, currentPassword string, newPassword string) error {
	if s == nil {
		return errors.New("account store is not configured")
	}
	username, err := normalizeAccountUsername(username)
	if err != nil {
		return err
	}
	if strings.TrimSpace(currentPassword) == "" {
		return errors.New("current password is required")
	}
	if err := validatePassword(newPassword, s.passwordPolicy()); err != nil {
		return err
	}
	nextHash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.accounts[username]
	if !ok || !verifyPassword(currentPassword, record.PasswordHash) {
		return errors.New("current password is invalid")
	}
	record.PasswordHash = nextHash
	s.accounts[username] = record
	return s.saveLocked(policy.Config{})
}

func (s *accountStore) SetAccountDisabled(username string, disabled bool) error {
	if s == nil {
		return errors.New("account store is not configured")
	}
	username, err := normalizeAccountUsername(username)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.accounts[username]
	if !ok {
		return errors.New("account not found")
	}
	if s.adminAccount != "" && username == s.adminAccount && disabled {
		return errors.New("cannot disable the bootstrap admin account")
	}
	if record.Disabled == disabled {
		return nil
	}
	record.Disabled = disabled
	s.accounts[username] = record
	if disabled {
		for sessionID, session := range s.sessions {
			if session.Username == username {
				delete(s.sessions, sessionID)
			}
		}
	}
	return s.saveLocked(policy.Config{})
}

func (s *accountStore) AdminResetPassword(username string, newPassword string) error {
	if s == nil {
		return errors.New("account store is not configured")
	}
	username, err := normalizeAccountUsername(username)
	if err != nil {
		return err
	}
	if err := validatePassword(newPassword, s.passwordPolicy()); err != nil {
		return err
	}
	nextHash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.accounts[username]
	if !ok {
		return errors.New("account not found")
	}
	record.PasswordHash = nextHash
	s.accounts[username] = record
	for sessionID, session := range s.sessions {
		if session.Username == username {
			delete(s.sessions, sessionID)
		}
	}
	return s.saveLocked(policy.Config{})
}

func (s *accountStore) List() []accountPublicInfo {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]accountPublicInfo, 0, len(s.accounts))
	for _, account := range s.accounts {
		items = append(items, accountPublicInfo{
			Username:      account.Username,
			Role:          normalizeAccountRole(account.Role),
			CreatedAt:     formatAccountTime(account.CreatedAt),
			LastLoginAt:   formatAccountTime(account.LastLoginAt),
			Email:         account.Email,
			EmailVerified: account.EmailVerified,
			Disabled:      account.Disabled,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Username < items[j].Username
	})
	return items
}

func (s *accountStore) HasAccounts() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.accounts) > 0
}

func (s *accountStore) RegistrationEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled || len(s.accounts) == 0
}

func (s *accountStore) SetRegistrationEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = enabled
	_ = s.saveRegistrationLocked()
}

func (s *accountStore) UserSettings(username string, defaults policy.Config) (userSettings, error) {
	if len(defaults.Commands) == 0 {
		defaults = fallbackAccountPolicy()
	}
	username, err := normalizeAccountUsername(username)
	if err != nil {
		return userSettings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	settings, ok := s.userSettings[username]
	if !ok {
		settings = defaultUserSettings(username, defaults)
		s.userSettings[username] = settings
		if err := s.saveLocked(defaults); err != nil {
			return userSettings{}, err
		}
	} else if settings.Commands == nil {
		settings.Commands = make(map[string]policy.CommandPolicy)
		s.userSettings[username] = settings
	}
	settings = normalizeUserSettings(settings)
	s.userSettings[username] = settings
	return cloneUserSettings(settings), nil
}

func (s *accountStore) SaveUserSettings(username string, update userSettingsUpdatePayload, global policy.Config) (userSettings, error) {
	if len(global.Commands) == 0 {
		global = fallbackAccountPolicy()
	}
	username, err := normalizeAccountUsername(username)
	if err != nil {
		return userSettings{}, err
	}
	next, err := userSettingsFromPayload(username, update, global)
	if err != nil {
		return userSettings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[username]; !ok {
		return userSettings{}, errors.New("account does not exist")
	}
	existing := normalizeUserSettings(s.userSettings[username])
	if update.ArchivedFolders == nil {
		next.ArchivedFolders = slices.Clone(existing.ArchivedFolders)
	}
	if update.ArchivedSessions == nil {
		next.ArchivedSessions = slices.Clone(existing.ArchivedSessions)
	}
	if update.ForgottenFolders == nil {
		next.ForgottenFolders = slices.Clone(existing.ForgottenFolders)
	}
	if update.ForgottenSessions == nil {
		next.ForgottenSessions = slices.Clone(existing.ForgottenSessions)
	}
	// QuickCommands are managed via a dedicated endpoint and must survive
	// general settings updates that don't carry them.
	next.QuickCommands = slices.Clone(existing.QuickCommands)
	s.userSettings[username] = next
	if err := s.saveLocked(global); err != nil {
		return userSettings{}, err
	}
	return cloneUserSettings(next), nil
}

func (s *accountStore) UpdateUserArchive(username string, update userArchiveUpdatePayload, global policy.Config) (userSettings, error) {
	if len(global.Commands) == 0 {
		global = fallbackAccountPolicy()
	}
	username, err := normalizeAccountUsername(username)
	if err != nil {
		return userSettings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[username]; !ok {
		return userSettings{}, errors.New("account does not exist")
	}
	settings := normalizeUserSettings(s.userSettings[username])
	if settings.Username == "" {
		settings = defaultUserSettings(username, global)
	}
	settings.ArchivedFolders = cleanPaths(update.ArchivedFolders)
	settings.ArchivedSessions = cleanList(update.ArchivedSessions)
	settings.ForgottenFolders = cleanPaths(update.ForgottenFolders)
	settings.ForgottenSessions = cleanList(update.ForgottenSessions)
	settings.ArchivedFolders = subtractStrings(settings.ArchivedFolders, settings.ForgottenFolders)
	settings.ArchivedSessions = subtractStrings(settings.ArchivedSessions, settings.ForgottenSessions)
	s.userSettings[username] = settings
	if err := s.saveLocked(global); err != nil {
		return userSettings{}, err
	}
	return cloneUserSettings(settings), nil
}

func (s *accountStore) UpdateUserQuickCommands(username string, update userQuickCommandsUpdatePayload, global policy.Config) (userSettings, error) {
	if len(global.Commands) == 0 {
		global = fallbackAccountPolicy()
	}
	username, err := normalizeAccountUsername(username)
	if err != nil {
		return userSettings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[username]; !ok {
		return userSettings{}, errors.New("account does not exist")
	}
	settings := normalizeUserSettings(s.userSettings[username])
	if settings.Username == "" {
		settings = defaultUserSettings(username, global)
	}
	settings.QuickCommands = normalizeQuickCommands(update.QuickCommands)
	s.userSettings[username] = settings
	if err := s.saveLocked(global); err != nil {
		return userSettings{}, err
	}
	return cloneUserSettings(settings), nil
}

func (s *accountStore) UserPolicy(username string, global policy.Config) (policy.Config, error) {
	if len(global.Commands) == 0 {
		global = fallbackAccountPolicy()
	}
	username = normalizeTunnelAccount(username)
	if username == "" || s.isAdmin(username) {
		return clonePolicyConfig(global), nil
	}
	settings, err := s.UserSettings(username, global)
	if err != nil {
		return policy.Config{}, err
	}
	return policyFromUserSettings(settings, global)
}

func (s *accountStore) UserCloudTunnelEnabled(username string) bool {
	username = normalizeTunnelAccount(username)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.userSettings[username].CloudTunnelEnabled
}

func (s *accountStore) TunnelAllowed(username string) bool {
	username = normalizeTunnelAccount(username)
	if username == "" || s.isAdmin(username) {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.userSettings[username].CloudTunnelEnabled
}

func (s *accountStore) isAdmin(username string) bool {
	username = normalizeTunnelAccount(username)
	s.mu.RLock()
	defer s.mu.RUnlock()
	record := s.accounts[username]
	return normalizeAccountRole(record.Role) == accountRoleAdmin
}

func (s *accountStore) loadAccounts() (map[string]accountRecord, error) {
	rows, err := s.db.Query(`SELECT username, role, password_hash, created_at, COALESCE(last_login_at, ''), COALESCE(email, ''), COALESCE(email_verified, 0), COALESCE(email_verified_at, ''), COALESCE(disabled, 0) FROM accounts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := make(map[string]accountRecord)
	for rows.Next() {
		var username, role, passwordHash, createdAt, lastLoginAt, email, emailVerifiedAt string
		var emailVerified int
		var disabled int
		if err := rows.Scan(&username, &role, &passwordHash, &createdAt, &lastLoginAt, &email, &emailVerified, &emailVerifiedAt, &disabled); err != nil {
			return nil, err
		}
		record := accountRecord{
			Username:        username,
			Role:            normalizeAccountRole(role),
			PasswordHash:    passwordHash,
			CreatedAt:       parseAccountTime(createdAt),
			LastLoginAt:     parseAccountTime(lastLoginAt),
			Email:           strings.ToLower(strings.TrimSpace(email)),
			EmailVerified:   emailVerified != 0,
			EmailVerifiedAt: parseAccountTime(emailVerifiedAt),
			Disabled:        disabled != 0,
		}
		if s.adminAccount != "" && username == s.adminAccount {
			record.Role = accountRoleAdmin
			record.Disabled = false
		}
		accounts[username] = record
	}
	return accounts, rows.Err()
}

func (s *accountStore) loadSessions() (map[string]accountSession, error) {
	rows, err := s.db.Query(`SELECT session_id, username, created_at, expires_at FROM sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := make(map[string]accountSession)
	now := time.Now()
	for rows.Next() {
		var sessionID, username, createdAt, expiresAt string
		if err := rows.Scan(&sessionID, &username, &createdAt, &expiresAt); err != nil {
			return nil, err
		}
		session := accountSession{
			SessionID: sessionID,
			Username:  username,
			CreatedAt: parseAccountTime(createdAt),
			ExpiresAt: parseAccountTime(expiresAt),
		}
		if session.ExpiresAt.After(now) && strings.TrimSpace(session.Username) != "" {
			sessions[session.SessionID] = session
		}
	}
	return sessions, rows.Err()
}

func (s *accountStore) loadAllUserSettings() (map[string]userSettings, error) {
	rows, err := s.db.Query(`SELECT username, cloud_tunnel_enabled FROM user_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := make(map[string]userSettings)
	for rows.Next() {
		var username string
		var tunnelEnabled int
		if err := rows.Scan(&username, &tunnelEnabled); err != nil {
			return nil, err
		}
		settings[username] = userSettings{
			Username:           username,
			CloudTunnelEnabled: tunnelEnabled != 0,
			Commands:           make(map[string]policy.CommandPolicy),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	commandRows, err := s.db.Query(`SELECT username, name, enabled, bin, interactive, subcommands, allow_paths, max_args FROM user_commands`)
	if err != nil {
		return nil, err
	}
	defer commandRows.Close()
	for commandRows.Next() {
		var username, name, bin, subcommandsJSON, allowPathsJSON string
		var enabled, interactive, maxArgs int
		if err := commandRows.Scan(&username, &name, &enabled, &bin, &interactive, &subcommandsJSON, &allowPathsJSON, &maxArgs); err != nil {
			return nil, err
		}
		setting := settings[username]
		if setting.Username == "" {
			setting.Username = username
			setting.Commands = make(map[string]policy.CommandPolicy)
		}
		if setting.Commands == nil {
			setting.Commands = make(map[string]policy.CommandPolicy)
		}
		setting.Commands[name] = policy.CommandPolicy{
			Enabled:     enabled != 0,
			Bin:         bin,
			Interactive: interactive != 0,
			Subcommands: decodeStringList(subcommandsJSON),
			AllowPaths:  decodeStringList(allowPathsJSON),
			MaxArgs:     maxArgs,
		}
		settings[username] = setting
	}
	if err := commandRows.Err(); err != nil {
		return nil, err
	}
	pathRows, err := s.db.Query(`SELECT username, path FROM user_allow_paths ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer pathRows.Close()
	for pathRows.Next() {
		var username, path string
		if err := pathRows.Scan(&username, &path); err != nil {
			return nil, err
		}
		setting := settings[username]
		if setting.Username == "" {
			setting.Username = username
			setting.Commands = make(map[string]policy.CommandPolicy)
		}
		setting.AllowPaths = append(setting.AllowPaths, path)
		settings[username] = setting
	}
	if err := pathRows.Err(); err != nil {
		return nil, err
	}
	if err := loadUserSettingStrings(s.db, settings, "user_archived_folders", "path", func(setting *userSettings, values []string) {
		setting.ArchivedFolders = values
	}); err != nil {
		return nil, err
	}
	if err := loadUserSettingStrings(s.db, settings, "user_archived_sessions", "session_id", func(setting *userSettings, values []string) {
		setting.ArchivedSessions = values
	}); err != nil {
		return nil, err
	}
	if err := loadUserSettingStrings(s.db, settings, "user_forgotten_folders", "path", func(setting *userSettings, values []string) {
		setting.ForgottenFolders = values
	}); err != nil {
		return nil, err
	}
	if err := loadUserSettingStrings(s.db, settings, "user_forgotten_sessions", "session_id", func(setting *userSettings, values []string) {
		setting.ForgottenSessions = values
	}); err != nil {
		return nil, err
	}
	if err := loadUserQuickCommands(s.db, settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func loadUserQuickCommands(db *sql.DB, settings map[string]userSettings) error {
	rows, err := db.Query(`SELECT username, label, command FROM user_quick_commands ORDER BY username, position`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var username, label, command string
		if err := rows.Scan(&username, &label, &command); err != nil {
			return err
		}
		setting := settings[username]
		if setting.Username == "" {
			setting.Username = username
			setting.Commands = make(map[string]policy.CommandPolicy)
		}
		setting.QuickCommands = append(setting.QuickCommands, userQuickCommand{Label: label, Command: command})
		settings[username] = setting
	}
	return rows.Err()
}

func loadUserSettingStrings(db *sql.DB, settings map[string]userSettings, table string, column string, assign func(*userSettings, []string)) error {
	rows, err := db.Query(fmt.Sprintf(`SELECT username, %s FROM %s ORDER BY %s`, column, table, column))
	if err != nil {
		return err
	}
	defer rows.Close()
	grouped := make(map[string][]string)
	for rows.Next() {
		var username, value string
		if err := rows.Scan(&username, &value); err != nil {
			return err
		}
		grouped[username] = append(grouped[username], value)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for username, values := range grouped {
		setting := settings[username]
		if setting.Username == "" {
			setting.Username = username
			setting.Commands = make(map[string]policy.CommandPolicy)
		}
		assign(&setting, values)
		settings[username] = setting
	}
	return nil
}

func (s *accountStore) saveSQLiteLocked(defaults policy.Config) error {
	if len(defaults.Commands) == 0 {
		defaults = fallbackAccountPolicy()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM accounts`); err != nil {
		return err
	}
	for _, record := range s.accounts {
		var emailValue any
		if email := strings.ToLower(strings.TrimSpace(record.Email)); email != "" {
			emailValue = email
		}
		if _, err := tx.Exec(
			`INSERT INTO accounts (username, role, password_hash, created_at, last_login_at, email, email_verified, email_verified_at, disabled) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.Username,
			normalizeAccountRole(record.Role),
			record.PasswordHash,
			formatAccountTime(record.CreatedAt),
			nullableAccountTime(record.LastLoginAt),
			emailValue,
			boolInt(record.EmailVerified),
			nullableAccountTime(record.EmailVerifiedAt),
			boolInt(record.Disabled),
		); err != nil {
			return err
		}
		if _, ok := s.userSettings[record.Username]; !ok {
			s.userSettings[record.Username] = defaultUserSettings(record.Username, defaults)
		}
	}
	if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
		return err
	}
	for _, session := range s.sessions {
		if _, err := tx.Exec(
			`INSERT INTO sessions (session_id, username, created_at, expires_at) VALUES (?, ?, ?, ?)`,
			session.SessionID,
			session.Username,
			formatAccountTime(session.CreatedAt),
			formatAccountTime(session.ExpiresAt),
		); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM user_settings`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_commands`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_allow_paths`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_archived_folders`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_archived_sessions`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_forgotten_folders`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_forgotten_sessions`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_quick_commands`); err != nil {
		return err
	}
	now := formatAccountTime(time.Now())
	for username, settings := range s.userSettings {
		if _, ok := s.accounts[username]; !ok {
			continue
		}
		if settings.Commands == nil {
			settings.Commands = make(map[string]policy.CommandPolicy)
		}
		if _, err := tx.Exec(
			`INSERT INTO user_settings (username, cloud_tunnel_enabled, updated_at) VALUES (?, ?, ?)`,
			username,
			boolInt(settings.CloudTunnelEnabled),
			now,
		); err != nil {
			return err
		}
		for name, rule := range settings.Commands {
			if _, err := tx.Exec(
				`INSERT INTO user_commands (username, name, enabled, bin, interactive, subcommands, allow_paths, max_args) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				username,
				name,
				boolInt(rule.Enabled),
				rule.Bin,
				boolInt(rule.Interactive),
				encodeStringList(rule.Subcommands),
				encodeStringList(rule.AllowPaths),
				rule.MaxArgs,
			); err != nil {
				return err
			}
		}
		for _, path := range settings.AllowPaths {
			if _, err := tx.Exec(`INSERT INTO user_allow_paths (username, path) VALUES (?, ?)`, username, path); err != nil {
				return err
			}
		}
		for _, path := range settings.ArchivedFolders {
			if _, err := tx.Exec(`INSERT INTO user_archived_folders (username, path) VALUES (?, ?)`, username, path); err != nil {
				return err
			}
		}
		for _, id := range settings.ArchivedSessions {
			if _, err := tx.Exec(`INSERT INTO user_archived_sessions (username, session_id) VALUES (?, ?)`, username, id); err != nil {
				return err
			}
		}
		for _, path := range settings.ForgottenFolders {
			if _, err := tx.Exec(`INSERT INTO user_forgotten_folders (username, path) VALUES (?, ?)`, username, path); err != nil {
				return err
			}
		}
		for _, id := range settings.ForgottenSessions {
			if _, err := tx.Exec(`INSERT INTO user_forgotten_sessions (username, session_id) VALUES (?, ?)`, username, id); err != nil {
				return err
			}
		}
		for index, qc := range settings.QuickCommands {
			if _, err := tx.Exec(
				`INSERT INTO user_quick_commands (username, position, label, command) VALUES (?, ?, ?, ?)`,
				username, index, qc.Label, qc.Command,
			); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO meta (key, value) VALUES ('account_registration_enabled', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		fmt.Sprintf("%t", s.enabled),
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *accountStore) saveRegistrationLocked() error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.Exec(
		`INSERT INTO meta (key, value) VALUES ('account_registration_enabled', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		fmt.Sprintf("%t", s.enabled),
	)
	return err
}

func (s *accountStore) migrateLegacy(defaults policy.Config) error {
	if s.legacyPath == "" {
		return nil
	}
	if migrated := s.metaValue("legacy_accounts_migrated"); migrated == "true" {
		return nil
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES ('legacy_accounts_migrated', 'true') ON CONFLICT(key) DO UPDATE SET value = excluded.value`)
		return err
	}
	content, err := os.ReadFile(s.legacyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES ('legacy_accounts_migrated', 'true') ON CONFLICT(key) DO UPDATE SET value = excluded.value`)
			return err
		}
		return err
	}
	var state accountStoreFile
	if err := json.Unmarshal(content, &state); err != nil {
		return err
	}
	s.accounts = make(map[string]accountRecord)
	s.sessions = make(map[string]accountSession)
	s.userSettings = make(map[string]userSettings)
	for username, record := range state.Accounts {
		record.Role = normalizeAccountRole(record.Role)
		if s.adminAccount != "" && username == s.adminAccount {
			record.Role = accountRoleAdmin
		}
		s.accounts[username] = record
		s.userSettings[username] = defaultUserSettings(username, defaults)
	}
	now := time.Now()
	for sessionID, session := range state.Sessions {
		if session.ExpiresAt.After(now) && strings.TrimSpace(session.Username) != "" {
			if strings.TrimSpace(session.SessionID) == "" {
				session.SessionID = sessionID
			}
			s.sessions[session.SessionID] = session
		}
	}
	if err := s.saveSQLiteLocked(defaults); err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO meta (key, value) VALUES ('legacy_accounts_migrated', 'true') ON CONFLICT(key) DO UPDATE SET value = excluded.value`)
	return err
}

func (s *accountStore) metaValue(key string) string {
	if s.db == nil {
		return ""
	}
	var value string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value); err != nil {
		return ""
	}
	return value
}

func newAccountSession(username string, now time.Time) (accountSession, error) {
	sessionID, err := randomSessionID(32)
	if err != nil {
		return accountSession{}, err
	}
	return accountSession{
		SessionID: sessionID,
		Username:  username,
		CreatedAt: now,
		ExpiresAt: now.Add(accountSessionTTL),
	}, nil
}

func normalizeAccountUsername(username string) (string, error) {
	username = strings.TrimSpace(strings.ToLower(username))
	if len(username) < 3 || len(username) > 64 {
		return "", errors.New("username must be 3 to 64 characters")
	}
	for _, r := range username {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' || r == '@' {
			continue
		}
		return "", errors.New("username may only contain letters, numbers, dot, dash, underscore, or @")
	}
	return username, nil
}

func normalizeAccountRole(role string) string {
	switch strings.TrimSpace(strings.ToLower(role)) {
	case accountRoleAdmin:
		return accountRoleAdmin
	default:
		return accountRoleUser
	}
}

func defaultUserSettings(username string, defaults policy.Config) userSettings {
	return userSettings{
		Username: normalizeTunnelAccount(username),
		Commands: clonePolicyCommands(defaults.Commands),
	}
}

func cloneUserSettings(settings userSettings) userSettings {
	settings = normalizeUserSettings(settings)
	return userSettings{
		Username:           settings.Username,
		CloudTunnelEnabled: settings.CloudTunnelEnabled,
		Commands:           clonePolicyCommands(settings.Commands),
		AllowPaths:         slices.Clone(settings.AllowPaths),
		ArchivedFolders:    slices.Clone(settings.ArchivedFolders),
		ArchivedSessions:   slices.Clone(settings.ArchivedSessions),
		ForgottenFolders:   slices.Clone(settings.ForgottenFolders),
		ForgottenSessions:  slices.Clone(settings.ForgottenSessions),
		QuickCommands:      slices.Clone(settings.QuickCommands),
	}
}

func normalizeUserSettings(settings userSettings) userSettings {
	settings.Username = normalizeTunnelAccount(settings.Username)
	if settings.Commands == nil {
		settings.Commands = make(map[string]policy.CommandPolicy)
	}
	settings.AllowPaths = cleanPaths(settings.AllowPaths)
	settings.ArchivedFolders = cleanPaths(settings.ArchivedFolders)
	settings.ForgottenFolders = cleanPaths(settings.ForgottenFolders)
	settings.ArchivedSessions = cleanList(settings.ArchivedSessions)
	settings.ForgottenSessions = cleanList(settings.ForgottenSessions)
	settings.ArchivedFolders = subtractStrings(settings.ArchivedFolders, settings.ForgottenFolders)
	settings.ArchivedSessions = subtractStrings(settings.ArchivedSessions, settings.ForgottenSessions)
	settings.QuickCommands = normalizeQuickCommands(settings.QuickCommands)
	return settings
}

const userQuickCommandMaxCount = 32
const userQuickCommandMaxLabel = 32
const userQuickCommandMaxCommand = 512

func normalizeQuickCommands(items []userQuickCommand) []userQuickCommand {
	if len(items) == 0 {
		return nil
	}
	out := make([]userQuickCommand, 0, len(items))
	for _, qc := range items {
		command := strings.TrimSpace(qc.Command)
		if command == "" {
			continue
		}
		if len(command) > userQuickCommandMaxCommand {
			command = command[:userQuickCommandMaxCommand]
		}
		label := strings.TrimSpace(qc.Label)
		if len([]rune(label)) > userQuickCommandMaxLabel {
			label = string([]rune(label)[:userQuickCommandMaxLabel])
		}
		out = append(out, userQuickCommand{Label: label, Command: command})
		if len(out) >= userQuickCommandMaxCount {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func clonePolicyConfig(cfg policy.Config) policy.Config {
	return policy.Config{
		Deny:             slices.Clone(cfg.Deny),
		AllowPaths:       slices.Clone(cfg.AllowPaths),
		Commands:         clonePolicyCommands(cfg.Commands),
		RequirePathMatch: cfg.RequirePathMatch,
	}
}

func clonePolicyCommands(commands map[string]policy.CommandPolicy) map[string]policy.CommandPolicy {
	if commands == nil {
		return nil
	}
	next := make(map[string]policy.CommandPolicy, len(commands))
	for name, rule := range commands {
		rule.Subcommands = slices.Clone(rule.Subcommands)
		rule.AllowPaths = slices.Clone(rule.AllowPaths)
		next[name] = rule
	}
	return next
}

func userSettingsFromPayload(username string, payload userSettingsUpdatePayload, global policy.Config) (userSettings, error) {
	settings := userSettings{
		Username:           normalizeTunnelAccount(username),
		CloudTunnelEnabled: payload.CloudTunnelEnabled,
		Commands:           make(map[string]policy.CommandPolicy),
		AllowPaths:         cleanPaths(payload.AllowPaths),
		ArchivedFolders:    cleanPaths(payload.ArchivedFolders),
		ArchivedSessions:   cleanList(payload.ArchivedSessions),
		ForgottenFolders:   cleanPaths(payload.ForgottenFolders),
		ForgottenSessions:  cleanList(payload.ForgottenSessions),
	}
	settings.ArchivedFolders = subtractStrings(settings.ArchivedFolders, settings.ForgottenFolders)
	settings.ArchivedSessions = subtractStrings(settings.ArchivedSessions, settings.ForgottenSessions)
	// Per-user allow_paths are independent across agents — each client has
	// its own filesystem, so the cloud admin's global list cannot bound
	// what each user is allowed to see locally.
	denied := make(map[string]struct{}, len(global.Deny))
	for _, command := range global.Deny {
		command = strings.TrimSpace(command)
		if command != "" {
			denied[command] = struct{}{}
		}
	}
	for rawName, incoming := range payload.Commands {
		name := strings.TrimSpace(rawName)
		if name == "" {
			continue
		}
		globalRule, ok := global.Commands[name]
		if !ok {
			return userSettings{}, fmt.Errorf("command %s is not in global policy", name)
		}
		if _, blocked := denied[name]; blocked {
			return userSettings{}, fmt.Errorf("command %s is globally denied", name)
		}
		if incoming.Enabled && !globalRule.Enabled {
			return userSettings{}, fmt.Errorf("command %s is disabled globally", name)
		}
		rule := policy.CommandPolicy{
			Enabled:     incoming.Enabled && globalRule.Enabled,
			Bin:         globalRule.Bin,
			Interactive: incoming.Interactive && globalRule.Interactive,
			Subcommands: intersectStrings(cleanList(incoming.Subcommands), globalRule.Subcommands),
			AllowPaths:  cleanPaths(incoming.AllowPaths),
			MaxArgs:     incoming.MaxArgs,
		}
		if globalRule.MaxArgs > 0 && (rule.MaxArgs <= 0 || rule.MaxArgs > globalRule.MaxArgs) {
			rule.MaxArgs = globalRule.MaxArgs
		}
		if globalRule.MaxArgs == 0 {
			rule.MaxArgs = 0
		}
		if len(globalRule.Subcommands) == 0 {
			rule.Subcommands = nil
		}
		settings.Commands[name] = rule
	}
	return settings, nil
}

func policyFromUserSettings(settings userSettings, global policy.Config) (policy.Config, error) {
	next := policy.Config{
		Deny:             slices.Clone(global.Deny),
		AllowPaths:       slices.Clone(settings.AllowPaths),
		Commands:         make(map[string]policy.CommandPolicy),
		RequirePathMatch: true,
	}
	denied := make(map[string]struct{}, len(global.Deny))
	for _, command := range global.Deny {
		command = strings.TrimSpace(command)
		if command != "" {
			denied[command] = struct{}{}
		}
	}
	for name, globalRule := range global.Commands {
		userRule, ok := settings.Commands[name]
		if !ok {
			continue
		}
		if _, blocked := denied[name]; blocked {
			continue
		}
		rule := globalRule
		rule.Enabled = userRule.Enabled && globalRule.Enabled
		rule.Interactive = userRule.Interactive && globalRule.Interactive
		rule.Subcommands = intersectStrings(userRule.Subcommands, globalRule.Subcommands)
		if len(globalRule.Subcommands) == 0 {
			rule.Subcommands = nil
		}
		rule.AllowPaths = slices.Clone(userRule.AllowPaths)
		if globalRule.MaxArgs > 0 && (userRule.MaxArgs <= 0 || userRule.MaxArgs > globalRule.MaxArgs) {
			rule.MaxArgs = globalRule.MaxArgs
		} else {
			rule.MaxArgs = userRule.MaxArgs
		}
		next.Commands[name] = rule
	}
	return next, nil
}

func userSettingsToPayload(settings userSettings, account accountPublicInfo, global policy.Config, gatewayURL string) userSettingsPayload {
	settings = normalizeUserSettings(settings)
	return userSettingsPayload{
		Account:            account.Username,
		Role:               account.Role,
		CloudTunnelEnabled: settings.CloudTunnelEnabled,
		GatewayURL:         gatewayURL,
		Commands:           adminPayloadCommands(settings.Commands),
		AllowPaths:         slices.Clone(settings.AllowPaths),
		ArchivedFolders:    slices.Clone(settings.ArchivedFolders),
		ArchivedSessions:   slices.Clone(settings.ArchivedSessions),
		ForgottenFolders:   slices.Clone(settings.ForgottenFolders),
		ForgottenSessions:  slices.Clone(settings.ForgottenSessions),
		QuickCommands:      slices.Clone(settings.QuickCommands),
		PolicyLimits: userPolicyLimitsPayload{
			Commands:   adminPayloadCommands(global.Commands),
			AllowPaths: nil,
			Deny:       slices.Clone(global.Deny),
		},
	}
}

func adminPayloadCommands(commands map[string]policy.CommandPolicy) map[string]adminCommandPayload {
	out := make(map[string]adminCommandPayload, len(commands))
	for name, rule := range commands {
		out[name] = adminCommandPayload{
			Enabled:     rule.Enabled,
			Bin:         rule.Bin,
			Interactive: rule.Interactive,
			Subcommands: slices.Clone(rule.Subcommands),
			AllowPaths:  slices.Clone(rule.AllowPaths),
			MaxArgs:     rule.MaxArgs,
		}
	}
	return out
}

func intersectStrings(values []string, allowed []string) []string {
	if len(allowed) == 0 {
		return slices.Clone(values)
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[value] = struct{}{}
	}
	var out []string
	for _, value := range values {
		if _, ok := allowedSet[value]; ok {
			out = append(out, value)
		}
	}
	return out
}

func subtractStrings(values []string, blocked []string) []string {
	if len(values) == 0 || len(blocked) == 0 {
		return slices.Clone(values)
	}
	blockedSet := make(map[string]struct{}, len(blocked))
	for _, value := range blocked {
		blockedSet[value] = struct{}{}
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := blockedSet[value]; !ok {
			out = append(out, value)
		}
	}
	return out
}

func filterAllowedPaths(values []string, roots []string) []string {
	var out []string
	for _, value := range values {
		if pathWithinAllowed(value, roots) && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

func encodeStringList(values []string) string {
	content, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(content)
}

func decodeStringList(value string) []string {
	var values []string
	if err := json.Unmarshal([]byte(value), &values); err != nil {
		return nil
	}
	return values
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableAccountTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatAccountTime(value)
}

func parseAccountTime(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func hashPassword(password string) (string, error) {
	salt, err := randomBytes(16)
	if err != nil {
		return "", err
	}
	derived := pbkdf2SHA256([]byte(password), salt, passwordHashIterations, 32)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", passwordHashIterations, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(derived)), nil
}

func verifyPassword(password string, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	var iterations int
	if _, err := fmt.Sscanf(parts[1], "%d", &iterations); err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, iterations, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func pbkdf2SHA256(password []byte, salt []byte, iterations int, keyLen int) []byte {
	hashLen := sha256.Size
	blocks := (keyLen + hashLen - 1) / hashLen
	out := make([]byte, 0, blocks*hashLen)
	for block := 1; block <= blocks; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func randomSessionID(length int) (string, error) {
	value, err := randomBytes(length)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func randomBytes(length int) ([]byte, error) {
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		return nil, err
	}
	return value, nil
}

func formatAccountTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}
