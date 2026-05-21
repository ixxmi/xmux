package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cloud-terminal/internal/mail"
)

const adminUpdaterUnknown = "unknown"

// reloadMailerLocked rebuilds the mail sender from current app_settings.
// Falls back to LogSender on misconfiguration so flows that depend on mail
// (verification, reset) at least emit the link to the log instead of failing
// outright.
func (s *Server) reloadMailerLocked() {
	if s == nil || s.mailer == nil {
		return
	}
	cfg := s.appSettings.SMTPSettings()
	if !cfg.Enabled || strings.TrimSpace(cfg.Host) == "" {
		s.mailer.Set(&mail.LogSender{Logger: s.logger})
		return
	}
	password := ""
	if cfg.PasswordEnc != "" && s.secrets != nil {
		if plain, err := s.secrets.decryptSecret(cfg.PasswordEnc); err == nil {
			password = plain
		} else {
			s.logger.Warn("decrypt smtp password", "error", err)
		}
	}
	sender, err := mail.NewSMTPSender(mail.SMTPConfig{
		Host:          cfg.Host,
		Port:          cfg.Port,
		Username:      cfg.Username,
		Password:      password,
		FromAddress:   cfg.FromAddress,
		FromName:      cfg.FromName,
		UseStartTLS:   cfg.UseStartTLS,
		UseSMTPS:      cfg.UseSMTPS,
		SkipTLSVerify: cfg.SkipTLSVerify,
		Timeout:       15 * time.Second,
	})
	if err != nil {
		s.logger.Warn("build smtp sender", "error", err)
		s.mailer.Set(&mail.LogSender{Logger: s.logger})
		return
	}
	s.mailer.Set(sender)
}

// appBaseURL prefers the admin-configured app_base_url; falls back to deriving
// from the incoming request (scheme+host) when admin hasn't set one yet.
func (s *Server) appBaseURL(r *http.Request) string {
	if s == nil {
		return ""
	}
	if s.appSettings != nil {
		if configured := normalizeBaseURL(s.appSettings.AppBaseURL()); configured != "" {
			return configured
		}
	}
	if r == nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		scheme = strings.ToLower(strings.TrimSpace(strings.SplitN(v, ",", 2)[0]))
	}
	host := r.Host
	if v := r.Header.Get("X-Forwarded-Host"); v != "" {
		host = strings.TrimSpace(strings.SplitN(v, ",", 2)[0])
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func normalizeBaseURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return ""
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return value
	}
	u.Path = strings.TrimRight(u.EscapedPath(), "/")
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// --- HTTP handlers ---

type authSettingsPayload struct {
	Password   passwordPolicy `json:"password_policy"`
	Auth       authSettings   `json:"auth_settings"`
	AppBaseURL string         `json:"app_base_url"`
}

func (s *Server) adminAuthSettings(w http.ResponseWriter, r *http.Request) {
	if s.appSettings == nil {
		http.Error(w, "app settings unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, authSettingsPayload{
			Password:   s.appSettings.PasswordPolicy(),
			Auth:       s.appSettings.AuthSettings(),
			AppBaseURL: s.appSettings.AppBaseURL(),
		})
	case http.MethodPut:
		var payload authSettingsPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if payload.Password.MinLength <= 0 {
			payload.Password.MinLength = defaultPasswordPolicy().MinLength
		}
		if payload.Password.MaxLength <= 0 {
			payload.Password.MaxLength = defaultPasswordPolicy().MaxLength
		}
		updatedBy := adminUpdater(r)
		if err := s.appSettings.Set(appSettingKeyPasswordPolicy, payload.Password, updatedBy); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.appSettings.Set(appSettingKeyAuthSettings, payload.Auth, updatedBy); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		baseURL := strings.TrimSpace(payload.AppBaseURL)
		if baseURL != "" {
			if _, err := url.ParseRequestURI(baseURL); err != nil {
				http.Error(w, "app_base_url is not a valid URL", http.StatusBadRequest)
				return
			}
		}
		if err := s.appSettings.Set(appSettingKeyAppBaseURL, baseURL, updatedBy); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, authSettingsPayload{
			Password:   s.appSettings.PasswordPolicy(),
			Auth:       s.appSettings.AuthSettings(),
			AppBaseURL: s.appSettings.AppBaseURL(),
		})
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// smtpSettingsPayload mirrors the persisted smtpSettings struct but exposes
// `password` as a write-only plaintext field that the admin types in once.
// On GET we return `password_set: true` to indicate whether the stored
// ciphertext is non-empty, without ever returning the secret itself.
type smtpSettingsPayload struct {
	Enabled       bool   `json:"enabled"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	Password      string `json:"password,omitempty"`
	PasswordSet   bool   `json:"password_set"`
	FromAddress   string `json:"from_address"`
	FromName      string `json:"from_name"`
	UseStartTLS   bool   `json:"use_starttls"`
	UseSMTPS      bool   `json:"use_smtps"`
	SkipTLSVerify bool   `json:"skip_tls_verify"`
}

func (s *Server) adminSMTPSettings(w http.ResponseWriter, r *http.Request) {
	if s.appSettings == nil {
		http.Error(w, "app settings unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg := s.appSettings.SMTPSettings()
		writeJSON(w, http.StatusOK, smtpSettingsPayload{
			Enabled:       cfg.Enabled,
			Host:          cfg.Host,
			Port:          cfg.Port,
			Username:      cfg.Username,
			PasswordSet:   cfg.PasswordEnc != "",
			FromAddress:   cfg.FromAddress,
			FromName:      cfg.FromName,
			UseStartTLS:   cfg.UseStartTLS,
			UseSMTPS:      cfg.UseSMTPS,
			SkipTLSVerify: cfg.SkipTLSVerify,
		})
	case http.MethodPut:
		var payload smtpSettingsPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		current := s.appSettings.SMTPSettings()
		next := smtpSettings{
			Enabled:       payload.Enabled,
			Host:          strings.TrimSpace(payload.Host),
			Port:          payload.Port,
			Username:      strings.TrimSpace(payload.Username),
			PasswordEnc:   current.PasswordEnc,
			FromAddress:   strings.TrimSpace(payload.FromAddress),
			FromName:      strings.TrimSpace(payload.FromName),
			UseStartTLS:   payload.UseStartTLS,
			UseSMTPS:      payload.UseSMTPS,
			SkipTLSVerify: payload.SkipTLSVerify,
		}
		if next.Enabled {
			if next.Host == "" {
				http.Error(w, "host is required when enabled", http.StatusBadRequest)
				return
			}
			if next.Port <= 0 || next.Port > 65535 {
				http.Error(w, "port is out of range", http.StatusBadRequest)
				return
			}
			if next.FromAddress == "" {
				http.Error(w, "from_address is required when enabled", http.StatusBadRequest)
				return
			}
		}
		if payload.Password != "" {
			if s.secrets == nil {
				http.Error(w, "secret keeper unavailable", http.StatusInternalServerError)
				return
			}
			enc, err := s.secrets.encryptSecret(payload.Password)
			if err != nil {
				http.Error(w, "encrypt password: "+err.Error(), http.StatusInternalServerError)
				return
			}
			next.PasswordEnc = enc
		}
		if err := s.appSettings.Set(appSettingKeySMTP, next, adminUpdater(r)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.reloadMailerLocked()
		writeJSON(w, http.StatusOK, smtpSettingsPayload{
			Enabled:       next.Enabled,
			Host:          next.Host,
			Port:          next.Port,
			Username:      next.Username,
			PasswordSet:   next.PasswordEnc != "",
			FromAddress:   next.FromAddress,
			FromName:      next.FromName,
			UseStartTLS:   next.UseStartTLS,
			UseSMTPS:      next.UseSMTPS,
			SkipTLSVerify: next.SkipTLSVerify,
		})
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type oauthGoogleSettingsPayload struct {
	Enabled          bool   `json:"enabled"`
	ClientID         string `json:"client_id"`
	ClientSecret     string `json:"client_secret,omitempty"`
	ClientSecretSet  bool   `json:"client_secret_set"`
	RedirectURL      string `json:"redirect_url"`
	HostedDomain     string `json:"hosted_domain,omitempty"`
	LinkExistingMode string `json:"link_existing_mode,omitempty"`
}

func (s *Server) adminOAuthGoogleSettings(w http.ResponseWriter, r *http.Request) {
	if s.appSettings == nil {
		http.Error(w, "app settings unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg := s.appSettings.OAuthGoogleSettings()
		writeJSON(w, http.StatusOK, oauthGoogleSettingsPayload{
			Enabled:          cfg.Enabled,
			ClientID:         cfg.ClientID,
			ClientSecretSet:  cfg.ClientSecretEnc != "",
			RedirectURL:      cfg.RedirectURL,
			HostedDomain:     cfg.HostedDomain,
			LinkExistingMode: cfg.LinkExistingMode,
		})
	case http.MethodPut:
		var payload oauthGoogleSettingsPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		current := s.appSettings.OAuthGoogleSettings()
		next := oauthGoogleSettings{
			Enabled:          payload.Enabled,
			ClientID:         strings.TrimSpace(payload.ClientID),
			ClientSecretEnc:  current.ClientSecretEnc,
			RedirectURL:      strings.TrimSpace(payload.RedirectURL),
			HostedDomain:     strings.TrimSpace(payload.HostedDomain),
			LinkExistingMode: strings.TrimSpace(payload.LinkExistingMode),
		}
		if next.LinkExistingMode == "" {
			next.LinkExistingMode = "require_password"
		}
		switch next.LinkExistingMode {
		case "require_password", "auto", "deny":
		default:
			http.Error(w, "link_existing_mode must be require_password|auto|deny", http.StatusBadRequest)
			return
		}
		if next.Enabled {
			if next.ClientID == "" {
				http.Error(w, "client_id is required when enabled", http.StatusBadRequest)
				return
			}
			if next.RedirectURL == "" {
				http.Error(w, "redirect_url is required when enabled", http.StatusBadRequest)
				return
			}
			if _, err := url.ParseRequestURI(next.RedirectURL); err != nil {
				http.Error(w, "redirect_url is not a valid URL", http.StatusBadRequest)
				return
			}
		}
		if payload.ClientSecret != "" {
			if s.secrets == nil {
				http.Error(w, "secret keeper unavailable", http.StatusInternalServerError)
				return
			}
			enc, err := s.secrets.encryptSecret(payload.ClientSecret)
			if err != nil {
				http.Error(w, "encrypt client_secret: "+err.Error(), http.StatusInternalServerError)
				return
			}
			next.ClientSecretEnc = enc
		}
		if err := s.appSettings.Set(appSettingKeyOAuthGoogle, next, adminUpdater(r)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, oauthGoogleSettingsPayload{
			Enabled:          next.Enabled,
			ClientID:         next.ClientID,
			ClientSecretSet:  next.ClientSecretEnc != "",
			RedirectURL:      next.RedirectURL,
			HostedDomain:     next.HostedDomain,
			LinkExistingMode: next.LinkExistingMode,
		})
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func adminUpdater(r *http.Request) string {
	if r == nil {
		return adminUpdaterUnknown
	}
	if cookie, err := r.Cookie(accountCookieName); err == nil && cookie != nil {
		return cookie.Value
	}
	return adminUpdaterUnknown
}

// publicAuthSettings returns the subset of auth settings safe to expose to
// the login/register page (e.g. whether OAuth is enabled). Used by the
// unauthenticated public-config endpoint.
func (s *Server) publicAuthSettings() map[string]any {
	if s == nil || s.appSettings == nil {
		return map[string]any{}
	}
	auth := s.appSettings.AuthSettings()
	policy := s.appSettings.PasswordPolicy()
	google := s.appSettings.OAuthGoogleSettings()
	return map[string]any{
		"oauth_google_enabled":            auth.OAuthGoogleEnabled && google.Enabled,
		"require_email_on_register":       auth.RequireEmailOnRegister,
		"require_email_verified_to_login": auth.RequireEmailVerifiedToLogin,
		"password_policy":                 policy,
	}
}

// staticErrAuthDisabled is a small helper kept here so the package compiles
// even if other Stage 2+ files reference it before they're wired.
var staticErrAuthDisabled = errors.New("authentication is disabled")

var _ = staticErrAuthDisabled // suppress unused warning until Stage 2 uses it

// authPublicConfig is an unauthenticated GET that the login/register page
// uses to decide which controls to show (Google button, password rules, etc.).
func (s *Server) authPublicConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.publicAuthSettings())
}

type smtpTestPayload struct {
	To string `json:"to"`
}

// adminSMTPTest sends a one-off probe email using the currently-saved SMTP
// settings. Useful for verifying credentials without going through the full
// register/verify flow.
func (s *Server) adminSMTPTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload smtpTestPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	to := strings.TrimSpace(payload.To)
	if to == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "to is required"})
		return
	}
	if s.mailer == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "mailer not initialized"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	start := time.Now()
	err := s.mailer.Send(ctx, mail.Message{
		To:       to,
		Subject:  "[xmux] SMTP test",
		HTMLBody: `<p>这是一封 xmux SMTP 配置测试邮件。如果你收到了，说明发件链路通了。</p>`,
		TextBody: "这是一封 xmux SMTP 配置测试邮件。如果你收到了，说明发件链路通了。",
	})
	tookMS := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          false,
			"to":          to,
			"sender_kind": s.mailer.Kind(),
			"took_ms":     tookMS,
			"error":       err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"sent":        true,
		"to":          to,
		"sender_kind": s.mailer.Kind(),
		"took_ms":     tookMS,
		"message":     "测试邮件已发送，请到收件箱查收",
	})
}
