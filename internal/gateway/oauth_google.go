package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	oauthStateCookie    = "xmux-oauth-state"
	oauthReturnToCookie = "xmux-oauth-return-to"
	oauthStateTTL       = 10 * time.Minute
	oauthPendingTable   = "oauth_pending_links" // tracks "needs password to bind" cases

	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleUserinfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
)

// sanitizeOAuthReturnTo limits the post-login redirect to safe local URLs so
// a tampered query string can't bounce users to an attacker's site. We
// accept any same-origin absolute path (must start with "/" and not "//"),
// which lets deployments served under a base path (e.g. /xmux/) hand back
// their own front-page path. Rejecting anything that doesn't begin with "/"
// blocks scheme/host smuggling like "//evil.com" or "https://evil.com".
func sanitizeOAuthReturnTo(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/user/"
	}
	if !strings.HasPrefix(value, "/") {
		return "/user/"
	}
	if strings.HasPrefix(value, "//") || strings.HasPrefix(value, "/\\") {
		return "/user/"
	}
	return value
}

// oauthIdentityLink represents a row in oauth_identities.
type oauthIdentityLink struct {
	Provider string
	Subject  string
	Username string
	Email    string
	LinkedAt time.Time
}

// ensureOAuthTables is idempotent; called lazily from the OAuth handlers so
// older databases self-upgrade on first OAuth use.
func (s *Server) ensureOAuthTables() error {
	store := s.accountStore()
	if store == nil || store.db == nil {
		return errors.New("account store unavailable")
	}
	if _, err := store.db.Exec(`CREATE TABLE IF NOT EXISTS oauth_pending_links (
		token_hash TEXT PRIMARY KEY,
		provider TEXT NOT NULL,
		subject TEXT NOT NULL,
		email TEXT NOT NULL,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	return nil
}

// oauthLinkLookup finds the local username already linked to (provider, subject).
func (s *Server) oauthLinkLookup(provider, subject string) (oauthIdentityLink, bool, error) {
	store := s.accountStore()
	if store == nil || store.db == nil {
		return oauthIdentityLink{}, false, errors.New("account store unavailable")
	}
	row := store.db.QueryRow(`SELECT provider, subject, username, COALESCE(email, ''), linked_at
		FROM oauth_identities WHERE provider = ? AND subject = ?`, provider, subject)
	var link oauthIdentityLink
	var linkedAt string
	if err := row.Scan(&link.Provider, &link.Subject, &link.Username, &link.Email, &linkedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return oauthIdentityLink{}, false, nil
		}
		return oauthIdentityLink{}, false, err
	}
	link.LinkedAt = parseAccountTime(linkedAt)
	return link, true, nil
}

func (s *Server) oauthLinkInsert(link oauthIdentityLink) error {
	store := s.accountStore()
	if store == nil || store.db == nil {
		return errors.New("account store unavailable")
	}
	link.LinkedAt = time.Now()
	_, err := store.db.Exec(`INSERT INTO oauth_identities (provider, subject, username, email, linked_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(provider, subject) DO UPDATE SET username = excluded.username, email = excluded.email, linked_at = excluded.linked_at`,
		link.Provider, link.Subject, link.Username, link.Email,
		formatAccountTime(link.LinkedAt),
	)
	return err
}

// oauthGoogleStart redirects the browser to Google's consent screen, after
// generating a one-time state value bound to a HttpOnly cookie.
func (s *Server) oauthGoogleStart(w http.ResponseWriter, r *http.Request) {
	cfg := s.appSettings.OAuthGoogleSettings()
	if !cfg.Enabled || cfg.ClientID == "" {
		http.Error(w, "google oauth is not enabled", http.StatusServiceUnavailable)
		return
	}
	state, err := randomURLToken(32)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logger.Warn("[OAUTH-DIAG] google start",
		"host", r.Host,
		"forwarded_proto", r.Header.Get("X-Forwarded-Proto"),
		"forwarded_prefix", r.Header.Get("X-Forwarded-Prefix"),
		"secure_cookie", secureRequest(r),
		"return_to", sanitizeOAuthReturnTo(r.URL.Query().Get("return_to")),
		"redirect_uri_sent_to_google", cfg.RedirectURL,
		"client_id_prefix", safeSessionPrefix(cfg.ClientID),
		"hosted_domain", cfg.HostedDomain)
	http.SetCookie(w, &http.Cookie{
		Name:  oauthStateCookie,
		Value: state,
		// Path=/ rather than the OAuth API prefix so the cookie survives
		// reverse-proxy deployments where xmux is mounted under a base path
		// (e.g. https://example.com/xmux/...). With a narrower path the
		// browser would drop the cookie on the callback because the URL the
		// user actually visits is /xmux/cloud-terminal-api/... not
		// /cloud-terminal-api/..., and the cookie's path wouldn't be a
		// prefix of that. The cookie is still HttpOnly + Lax + one-shot.
		Path:     "/",
		MaxAge:   int(oauthStateTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secureRequest(r),
	})
	returnTo := sanitizeOAuthReturnTo(r.URL.Query().Get("return_to"))
	http.SetCookie(w, &http.Cookie{
		Name:     oauthReturnToCookie,
		Value:    returnTo,
		Path:     "/",
		MaxAge:   int(oauthStateTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secureRequest(r),
	})
	params := url.Values{
		"client_id":     []string{cfg.ClientID},
		"redirect_uri":  []string{cfg.RedirectURL},
		"response_type": []string{"code"},
		"scope":         []string{"openid email profile"},
		"access_type":   []string{"online"},
		"state":         []string{state},
		"prompt":        []string{"select_account"},
	}
	if cfg.HostedDomain != "" {
		params.Set("hd", cfg.HostedDomain)
	}
	http.Redirect(w, r, googleAuthURL+"?"+params.Encode(), http.StatusFound)
}

type googleTokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type googleUserInfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	HostedDomain  string `json:"hd"`
}

func (s *Server) oauthGoogleCallback(w http.ResponseWriter, r *http.Request) {
	s.logger.Warn("[OAUTH-DIAG] google callback HIT",
		"host", r.Host,
		"forwarded_proto", r.Header.Get("X-Forwarded-Proto"),
		"url", r.URL.RequestURI(),
		"has_state_cookie", func() bool { _, err := r.Cookie(oauthStateCookie); return err == nil }(),
		"has_error_param", r.URL.Query().Get("error") != "")
	if s.buckets != nil && !rateLimit(s.buckets.oauthCallback, clientIP(r), w) {
		s.logger.Warn("[OAUTH-DIAG] google callback rate-limited")
		return
	}
	cfg := s.appSettings.OAuthGoogleSettings()
	if !cfg.Enabled || cfg.ClientID == "" {
		http.Error(w, "google oauth is not enabled", http.StatusServiceUnavailable)
		return
	}
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		s.redirectOAuthError(w, r, errParam)
		return
	}
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(oauthStateCookie)
	if err != nil || cookie.Value == "" || cookie.Value != state {
		s.redirectOAuthError(w, r, "invalid_state")
		return
	}
	// Clear the state cookie immediately to make it one-shot.
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	returnTo := "/user/"
	if rc, err := r.Cookie(oauthReturnToCookie); err == nil && rc.Value != "" {
		returnTo = sanitizeOAuthReturnTo(rc.Value)
		http.SetCookie(w, &http.Cookie{Name: oauthReturnToCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		s.redirectOAuthError(w, r, "missing_code")
		return
	}

	secret := ""
	if cfg.ClientSecretEnc != "" && s.secrets != nil {
		if plain, err := s.secrets.decryptSecret(cfg.ClientSecretEnc); err == nil {
			secret = plain
		} else {
			s.logger.Warn("decrypt google client_secret", "error", err)
		}
	}

	tok, err := s.exchangeGoogleCode(r.Context(), code, secret, cfg)
	if err != nil {
		s.logger.Warn("[OAUTH-DIAG] token exchange FAILED", "error", err)
		s.redirectOAuthError(w, r, "token_exchange_failed")
		return
	}
	s.logger.Warn("[OAUTH-DIAG] token exchange OK", "access_token_len", len(tok.AccessToken), "id_token_present", tok.IDToken != "")
	info, err := s.fetchGoogleUserInfo(r.Context(), tok.AccessToken)
	if err != nil {
		s.logger.Warn("[OAUTH-DIAG] userinfo FAILED", "error", err)
		s.redirectOAuthError(w, r, "userinfo_failed")
		return
	}
	s.logger.Warn("[OAUTH-DIAG] userinfo OK",
		"sub_prefix", safeSessionPrefix(info.Sub),
		"email", info.Email,
		"email_verified", info.EmailVerified,
		"hd", info.HostedDomain)
	if cfg.HostedDomain != "" && !strings.EqualFold(cfg.HostedDomain, info.HostedDomain) {
		s.logger.Warn("[OAUTH-DIAG] hd mismatch", "want", cfg.HostedDomain, "got", info.HostedDomain)
		s.redirectOAuthError(w, r, "hd_mismatch")
		return
	}
	if !info.EmailVerified {
		s.logger.Warn("[OAUTH-DIAG] google reports email NOT verified", "email", info.Email)
		s.redirectOAuthError(w, r, "google_email_unverified")
		return
	}

	if err := s.ensureOAuthTables(); err != nil {
		s.logger.Warn("[OAUTH-DIAG] ensure oauth tables", "error", err)
	}

	// Case 1: identity already linked → log the user in directly.
	if link, ok, err := s.oauthLinkLookup("google", info.Sub); err == nil && ok {
		s.logger.Warn("[OAUTH-DIAG] case 1: identity already linked", "username", link.Username)
		session, sessionErr := s.accountStore().IssueSession(link.Username)
		if sessionErr != nil {
			s.logger.Warn("[OAUTH-DIAG] IssueSession FAILED for linked oauth", "error", sessionErr, "username", link.Username)
			s.redirectOAuthError(w, r, "session_failed")
			return
		}
		s.logger.Warn("[OAUTH-DIAG] google callback: linked-identity login OK",
			"username", link.Username,
			"session_id_prefix", safeSessionPrefix(session.SessionID),
			"return_to", returnTo,
			"secure_cookie", secureRequest(r),
			"host", r.Host,
			"forwarded_proto", r.Header.Get("X-Forwarded-Proto"))
		s.setWorkbenchCookie(w, r, session.SessionID)
		s.setAccountCookie(w, r, session.SessionID)
		s.redirectExternal(w, r, returnTo, http.StatusFound)
		return
	}

	// Case 2: email matches an existing local account → behavior depends on
	// link_existing_mode.
	existing, hasLocal := s.accountStore().AccountByEmail(strings.ToLower(info.Email))
	if hasLocal {
		s.logger.Warn("[OAUTH-DIAG] case 2: email matches local account",
			"username", existing.Username, "link_mode", cfg.LinkExistingMode)
		switch cfg.LinkExistingMode {
		case "auto":
			if err := s.linkAndLogin(w, r, existing.Username, "google", info, returnTo); err == nil {
				return
			} else {
				s.logger.Warn("[OAUTH-DIAG] linkAndLogin FAILED (case2 auto)", "error", err, "username", existing.Username)
			}
			s.redirectOAuthError(w, r, "link_failed")
			return
		case "deny":
			s.logger.Warn("[OAUTH-DIAG] case 2: denied by link_existing_mode=deny")
			s.redirectOAuthError(w, r, "email_taken_use_password")
			return
		default: // require_password
			s.logger.Warn("[OAUTH-DIAG] case 2: require_password → oauth_bind.html")
			token, err := s.stashPendingOAuth("google", info.Sub, info.Email)
			if err != nil {
				s.logger.Warn("[OAUTH-DIAG] stashPendingOAuth FAILED", "error", err)
				s.redirectOAuthError(w, r, "internal")
				return
			}
			redir := fmt.Sprintf("/user/oauth_bind.html?provider=google&email=%s&token=%s",
				url.QueryEscape(info.Email), url.QueryEscape(token))
			s.redirectExternal(w, r, redir, http.StatusFound)
			return
		}
	}

	// Case 3: brand-new email → auto-register if enabled, else reject.
	authCfg := s.appSettings.AuthSettings()
	s.logger.Warn("[OAUTH-DIAG] case 3: brand-new email", "email", info.Email, "auto_register", authCfg.OAuthGoogleAutoRegister)
	if !authCfg.OAuthGoogleAutoRegister {
		s.redirectOAuthError(w, r, "auto_register_disabled")
		return
	}
	newUsername := strings.ToLower(strings.TrimSpace(info.Email))
	if newUsername == "" {
		s.redirectOAuthError(w, r, "missing_email")
		return
	}
	// Create a random-password account marked as email-verified. Users can set
	// a password later via "forgot password" if they want a non-OAuth fallback.
	randomPw := oauthInitialPassword()
	if _, err := s.accountStore().createAccount(newUsername, randomPw, accountRoleUser, true, s.config.Snapshot().Policy); err != nil {
		s.logger.Warn("[OAUTH-DIAG] createAccount FAILED (case 3)", "error", err, "username", newUsername)
		s.redirectOAuthError(w, r, "create_failed")
		return
	}
	if err := s.accountStore().SetEmail(newUsername, info.Email); err != nil {
		s.logger.Warn("[OAUTH-DIAG] SetEmail post-oauth", "error", err, "username", newUsername)
	}
	if err := s.accountStore().MarkEmailVerified(newUsername, info.Email); err != nil {
		s.logger.Warn("[OAUTH-DIAG] MarkEmailVerified post-oauth", "error", err, "username", newUsername)
	}
	if err := s.linkAndLogin(w, r, newUsername, "google", info, returnTo); err != nil {
		s.logger.Warn("[OAUTH-DIAG] linkAndLogin FAILED (case 3)", "error", err, "username", newUsername)
		s.redirectOAuthError(w, r, "link_failed")
		return
	}
}

// oauthInitialPassword generates a password that satisfies the default
// complexity policy (≥10 chars, upper+lower+digit) so OAuth-only accounts
// don't get rejected at createAccount time when the policy is strict.
// The user never uses this — they log in via Google forever; if they want a
// fallback password they go through "forgot password".
func oauthInitialPassword() string {
	const (
		upper = "ABCDEFGHJKLMNPQRSTUVWXYZ"
		lower = "abcdefghjkmnpqrstuvwxyz"
		digit = "23456789"
		all   = upper + lower + digit
	)
	buf := make([]byte, 1)
	pick := func(set string) byte {
		_, _ = rand.Read(buf)
		return set[int(buf[0])%len(set)]
	}
	out := make([]byte, 32)
	out[0] = pick(upper)
	out[1] = pick(lower)
	out[2] = pick(digit)
	for i := 3; i < len(out); i++ {
		out[i] = pick(all)
	}
	return string(out)
}

func (s *Server) exchangeGoogleCode(ctx context.Context, code, secret string, cfg oauthGoogleSettings) (googleTokenResponse, error) {
	body := url.Values{
		"client_id":     []string{cfg.ClientID},
		"client_secret": []string{secret},
		"code":          []string{code},
		"grant_type":    []string{"authorization_code"},
		"redirect_uri":  []string{cfg.RedirectURL},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return googleTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return googleTokenResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(resp.Body)
		return googleTokenResponse{}, fmt.Errorf("google token http %d: %s", resp.StatusCode, string(data))
	}
	var out googleTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return googleTokenResponse{}, err
	}
	if out.AccessToken == "" {
		return googleTokenResponse{}, errors.New("google token response missing access_token")
	}
	return out, nil
}

func (s *Server) fetchGoogleUserInfo(ctx context.Context, accessToken string) (googleUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserinfoURL, nil)
	if err != nil {
		return googleUserInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return googleUserInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(resp.Body)
		return googleUserInfo{}, fmt.Errorf("google userinfo http %d: %s", resp.StatusCode, string(data))
	}
	var out googleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return googleUserInfo{}, err
	}
	if out.Sub == "" {
		return googleUserInfo{}, errors.New("google userinfo missing sub")
	}
	return out, nil
}

func (s *Server) linkAndLogin(w http.ResponseWriter, r *http.Request, username, provider string, info googleUserInfo, returnTo string) error {
	if err := s.oauthLinkInsert(oauthIdentityLink{
		Provider: provider,
		Subject:  info.Sub,
		Username: username,
		Email:    info.Email,
	}); err != nil {
		s.logger.Warn("[OAUTH-DIAG] oauthLinkInsert FAILED", "error", err, "username", username)
		return err
	}
	session, err := s.accountStore().IssueSession(username)
	if err != nil {
		s.logger.Warn("[OAUTH-DIAG] IssueSession FAILED in linkAndLogin", "error", err, "username", username)
		return err
	}
	s.logger.Warn("[OAUTH-DIAG] google callback: link-and-login OK",
		"username", username,
		"session_id_prefix", safeSessionPrefix(session.SessionID),
		"return_to", returnTo,
		"secure_cookie", secureRequest(r),
		"host", r.Host,
		"forwarded_proto", r.Header.Get("X-Forwarded-Proto"))
	s.setWorkbenchCookie(w, r, session.SessionID)
	s.setAccountCookie(w, r, session.SessionID)
	target := sanitizeOAuthReturnTo(returnTo)
	s.redirectExternal(w, r, target, http.StatusFound)
	return nil
}

func safeSessionPrefix(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > 6 {
		return id[:6] + "…"
	}
	return id
}

// stashPendingOAuth saves a "needs password to bind" record indexed by a
// random token. The token is returned to the browser via redirect; the user
// types their local password to complete the bind via /oauth/google/bind.
func (s *Server) stashPendingOAuth(provider, subject, email string) (string, error) {
	if err := s.ensureOAuthTables(); err != nil {
		return "", err
	}
	store := s.accountStore()
	token, err := randomURLToken(32)
	if err != nil {
		return "", err
	}
	hash := hashEmailToken(token)
	now := time.Now()
	if _, err := store.db.Exec(`INSERT INTO oauth_pending_links (token_hash, provider, subject, email, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`, hash, provider, subject, email, formatAccountTime(now), formatAccountTime(now.Add(15*time.Minute))); err != nil {
		return "", err
	}
	return token, nil
}

type oauthBindPayload struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

func (s *Server) oauthGoogleBind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload oauthBindPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.ensureOAuthTables(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	store := s.accountStore()
	hash := hashEmailToken(payload.Token)
	row := store.db.QueryRow(`SELECT provider, subject, email, expires_at FROM oauth_pending_links WHERE token_hash = ?`, hash)
	var provider, subject, email, expiresAt string
	if err := row.Scan(&provider, &subject, &email, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "invalid or expired token", http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if exp := parseAccountTime(expiresAt); !exp.IsZero() && time.Now().After(exp) {
		http.Error(w, "token expired", http.StatusBadRequest)
		return
	}
	existing, ok := store.AccountByEmail(email)
	if !ok {
		http.Error(w, "no account for that email", http.StatusBadRequest)
		return
	}
	if !store.VerifyPassword(existing.Username, payload.Password) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := s.oauthLinkInsert(oauthIdentityLink{
		Provider: provider,
		Subject:  subject,
		Username: existing.Username,
		Email:    email,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// One-shot — delete the pending row.
	_, _ = store.db.Exec(`DELETE FROM oauth_pending_links WHERE token_hash = ?`, hash)
	session, err := store.IssueSession(existing.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.setWorkbenchCookie(w, r, session.SessionID)
	s.setAccountCookie(w, r, session.SessionID)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "username": existing.Username})
}

func (s *Server) redirectOAuthError(w http.ResponseWriter, r *http.Request, reason string) {
	s.logger.Warn("[OAUTH-DIAG] google callback FAILED", "reason", reason, "host", r.Host, "remote", clientIP(r))
	target := "/user/login.html?oauth_error=" + url.QueryEscape(reason)
	s.redirectExternal(w, r, target, http.StatusFound)
}

func randomURLToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func mustRandomString(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// Fall back to a deterministic but ugly value rather than panicking —
		// the password is never the user's primary login path.
		sum := sha256.Sum256([]byte(time.Now().String()))
		return hex.EncodeToString(sum[:])[:n]
	}
	return base64.RawURLEncoding.EncodeToString(buf)[:n]
}
