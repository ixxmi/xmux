package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	gomail "cloud-terminal/internal/mail"
	"cloud-terminal/internal/mail/mailtemplates"
)

// publicAuthSettingsConfig is an internal helper returning the typed auth
// settings (vs publicAuthSettings which returns a map for JSON output).
func (s *Server) publicAuthSettingsConfig() authSettings {
	if s == nil || s.appSettings == nil {
		return defaultAuthSettings()
	}
	return s.appSettings.AuthSettings()
}

// isPlausibleEmail returns true if value parses as an RFC 5322 address.
func isPlausibleEmail(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if _, err := mail.ParseAddress(value); err != nil {
		return false
	}
	if !strings.Contains(value, "@") {
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.SplitN(fwd, ",", 2)[0])
	}
	host := r.RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}
	return strings.Trim(host, "[]")
}

// sendVerificationEmail issues a fresh token and delivers a verification mail.
// Failures are returned to the caller so it can decide whether to surface or
// just log them.
func (s *Server) sendVerificationEmail(r *http.Request, username, email string) error {
	if s == nil || s.accountStore() == nil {
		return errors.New("account store unavailable")
	}
	token, err := s.accountStore().IssueEmailToken(username, email, emailTokenKindVerify, 0)
	if err != nil {
		return err
	}
	baseURL := s.appBaseURL(r)
	if baseURL == "" {
		return errors.New("app_base_url is not configured")
	}
	verifyURL := fmt.Sprintf("%s/cloud-terminal-api/accounts/verify-email?token=%s", baseURL, token)

	htmlBody, textBody, err := mailtemplates.RenderVerifyEmail(mailtemplates.VerifyData{
		Username:  username,
		VerifyURL: verifyURL,
		ExpiresIn: "24 小时",
		RequestIP: clientIP(r),
		UserAgent: requestUserAgent(r),
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if s.mailer == nil {
		return errors.New("mailer not initialized")
	}
	return s.mailer.Send(ctx, gomail.Message{
		To:       email,
		Subject:  "[xmux] 请验证你的邮箱",
		HTMLBody: htmlBody,
		TextBody: textBody,
	})
}

func requestUserAgent(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.Header.Get("User-Agent")
}

// sendRegistrationCode mails a short numeric code that the registration form
// then asks the user to type back to prove control of the address. Plain-text
// only — code emails don't need (or want) marketing chrome.
func (s *Server) sendRegistrationCode(r *http.Request, email, code string) error {
	if s == nil {
		return errors.New("server is nil")
	}
	if s.mailer == nil {
		return errors.New("mailer not initialized")
	}
	textBody := fmt.Sprintf(
		"你正在 xmux 上注册账号。\n\n验证码：%s\n\n验证码有效期为 10 分钟，请尽快在注册页面输入。若不是你本人操作，可忽略此邮件。\n",
		code,
	)
	htmlBody := fmt.Sprintf(
		`<p>你正在 xmux 上注册账号。</p><p>验证码：<strong style="font-size:20px;letter-spacing:4px">%s</strong></p><p>验证码有效期为 10 分钟，请尽快在注册页面输入。若不是你本人操作，可忽略此邮件。</p>`,
		code,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.mailer.Send(ctx, gomail.Message{
		To:       email,
		Subject:  "[xmux] 你的注册验证码",
		HTMLBody: htmlBody,
		TextBody: textBody,
	})
}

// accountRegisterSendCode issues + mails a verification code for the given
// email. The endpoint is rate-limited (resend cooldown enforced at the store
// layer + IP-bucket here) so an attacker can't spam arbitrary inboxes.
type registerSendCodePayload struct {
	Email string `json:"email"`
}

func (s *Server) accountRegisterSendCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.buckets != nil && !rateLimit(s.buckets.register, clientIP(r), w) {
		return
	}
	var payload registerSendCodePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(payload.Email))
	if !isPlausibleEmail(email) {
		http.Error(w, "email format is invalid", http.StatusBadRequest)
		return
	}
	if s.accountStore().IsEmailUsed(email, "") {
		http.Error(w, "email is already used by another account", http.StatusConflict)
		return
	}
	if s.mailer == nil {
		http.Error(w, "邮件服务尚未配置，无法发送验证码，请联系管理员", http.StatusServiceUnavailable)
		return
	}
	code, err := s.accountStore().IssueRegistrationCode(email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.sendRegistrationCode(r, email, code); err != nil {
		s.logger.Warn("send registration code", "email", email, "error", err)
		http.Error(w, "验证码发送失败："+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "email": email, "expires_in": int(registrationCodeLifetime.Seconds())})
}

// accountVerifyEmail consumes the verify token and marks the account as verified.
// Redirects (302) to /user/verified.html?status=ok|invalid so the UI can show
// a friendly confirmation page.
func (s *Server) accountVerifyEmail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	target := "/user/verified.html"
	if token == "" {
		s.redirectExternal(w, r, target+"?status=invalid", http.StatusFound)
		return
	}
	record, err := s.accountStore().ConsumeEmailToken(token, emailTokenKindVerify)
	if err != nil {
		s.redirectExternal(w, r, target+"?status=invalid", http.StatusFound)
		return
	}
	if err := s.accountStore().MarkEmailVerified(record.Username, record.Email); err != nil {
		s.redirectExternal(w, r, target+"?status=invalid", http.StatusFound)
		return
	}
	s.redirectExternal(w, r, target+"?status=ok", http.StatusFound)
}

// accountResendVerify lets a user who hasn't received the email request another
// one. Body: {"username": "...", "password": "..."}. We require password so an
// attacker can't trigger mail floods against arbitrary accounts.
type resendVerifyPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) accountResendVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.buckets != nil && !rateLimit(s.buckets.resendVerify, clientIP(r), w) {
		return
	}
	var payload resendVerifyPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.accountStore().VerifyPassword(payload.Username, payload.Password) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rec, ok := s.accountStore().AccountRecord(payload.Username)
	if !ok || rec.Email == "" {
		http.Error(w, "no email on file", http.StatusBadRequest)
		return
	}
	if rec.EmailVerified {
		writeJSON(w, http.StatusOK, map[string]any{"already_verified": true})
		return
	}
	if err := s.sendVerificationEmail(r, rec.Username, rec.Email); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true})
}

// accountSetEmail handles the old-account back-fill: caller proves possession
// with username+password, supplies an email, we bind it and send verification.
type setEmailPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email"`
}

func (s *Server) accountSetEmail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload setEmailPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(payload.Email))
	if !isPlausibleEmail(email) {
		http.Error(w, "email format is invalid", http.StatusBadRequest)
		return
	}
	if !s.accountStore().VerifyPassword(payload.Username, payload.Password) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.accountStore().IsEmailUsed(email, payload.Username) {
		http.Error(w, "email is already used by another account", http.StatusConflict)
		return
	}
	if err := s.accountStore().SetEmail(payload.Username, email); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.sendVerificationEmail(r, payload.Username, email); err != nil {
		http.Error(w, "email saved but verification mail failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "email": email})
}

// --- password reset ---

type forgotPasswordPayload struct {
	Email string `json:"email"`
}

// accountForgotPassword always returns 200 with the same body so an attacker
// can't probe which emails are registered.
func (s *Server) accountForgotPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.buckets != nil && !rateLimit(s.buckets.forgot, clientIP(r), w) {
		return
	}
	var payload forgotPasswordPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(payload.Email))
	respond := func() {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
	if !isPlausibleEmail(email) {
		// Don't 400 — that would still leak format validity, but specifically
		// reject empty input early.
		respond()
		return
	}
	rec, ok := s.accountStore().AccountByEmail(email)
	if !ok || rec.Username == "" {
		respond()
		return
	}
	token, err := s.accountStore().IssueEmailToken(rec.Username, email, emailTokenKindPasswordReset, 0)
	if err != nil {
		s.logger.Warn("issue reset token", "username", rec.Username, "error", err)
		respond()
		return
	}
	baseURL := s.appBaseURL(r)
	if baseURL == "" {
		s.logger.Warn("forgot password: app_base_url not configured")
		respond()
		return
	}
	resetURL := fmt.Sprintf("%s/user/reset.html?token=%s", baseURL, token)
	htmlBody, textBody, err := mailtemplates.RenderResetEmail(mailtemplates.ResetData{
		Username:  rec.Username,
		ResetURL:  resetURL,
		ExpiresIn: "30 分钟",
		RequestIP: clientIP(r),
		UserAgent: requestUserAgent(r),
	})
	if err != nil {
		s.logger.Warn("render reset email", "error", err)
		respond()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.mailer.Send(ctx, gomail.Message{
		To:       email,
		Subject:  "[xmux] 重置密码",
		HTMLBody: htmlBody,
		TextBody: textBody,
	}); err != nil {
		s.logger.Warn("send reset email", "error", err)
	}
	respond()
}

type resetPasswordPayload struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

func (s *Server) accountResetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload resetPasswordPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if payload.NewPassword == "" {
		http.Error(w, "new_password is required", http.StatusBadRequest)
		return
	}
	record, err := s.accountStore().ConsumeEmailToken(payload.Token, emailTokenKindPasswordReset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.accountStore().SetPassword(record.Username, payload.NewPassword); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A reset implies recovery from a compromised credential — wipe every
	// existing session so any attacker who held the old password is logged out.
	if err := s.accountStore().RevokeAllSessionsFor(record.Username); err != nil {
		s.logger.Warn("revoke sessions after reset", "username", record.Username, "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "username": record.Username})
}
