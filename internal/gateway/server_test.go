package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"cloud-terminal/internal/config"
	"cloud-terminal/internal/edge"
	"cloud-terminal/internal/mail"
	"cloud-terminal/internal/policy"

	"github.com/gorilla/websocket"
)

func TestRoutesUseCloudTerminalPrefixes(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		Config:   config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{Server: config.ServerConfig{}, Policy: minimalPolicy()}),
		StaticFS: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}},
	})
	handler := server.Routes()

	request := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/health", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("new health prefix status = %d, want 200", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/health", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code == http.StatusOK {
		t.Fatal("old /api prefix should not serve health endpoint")
	}
}

func TestRoutesAcceptExternalPrefixWhenProxyDoesNotStrip(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		Config:   config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{Server: config.ServerConfig{}, Policy: minimalPolicy()}),
		StaticFS: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}},
	})
	handler := server.Routes()

	request := httptest.NewRequest(http.MethodGet, "/xmux/cloud-terminal-api/health", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("prefixed health status = %d, want 200", response.Code)
	}
}

func TestRootRedirectsToMobileWorkbench(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		Config:   config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{Server: config.ServerConfig{}, Policy: minimalPolicy()}),
		StaticFS: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}},
	})
	handler := server.Routes()

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusFound {
		t.Fatalf("root status = %d, want 302", response.Code)
	}
	if got := response.Header().Get("Location"); got != "/mobile/" {
		t.Fatalf("root Location = %q, want /mobile/", got)
	}
}

func TestRootRedirectKeepsForwardedPrefix(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		Config:   config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{Server: config.ServerConfig{}, Policy: minimalPolicy()}),
		StaticFS: fstest.MapFS{"mobile/index.html": &fstest.MapFile{Data: []byte("mobile")}},
	})
	handler := server.Routes()

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Forwarded-Prefix", "/xmux")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusFound {
		t.Fatalf("root status = %d, want 302", response.Code)
	}
	if got := response.Header().Get("Location"); got != "/xmux/mobile/" {
		t.Fatalf("root Location = %q, want /xmux/mobile/", got)
	}
}

func TestUnknownStaticRouteNotServedByRootFileServer(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		Config: config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{Server: config.ServerConfig{}, Policy: minimalPolicy()}),
		StaticFS: fstest.MapFS{
			"index.html":        &fstest.MapFile{Data: []byte("root")},
			"mobile/index.html": &fstest.MapFile{Data: []byte("mobile")},
		},
	})
	handler := server.Routes()

	request := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, want 404", response.Code)
	}
}

func TestPageRouteWithoutTrailingSlashRedirectsWithPrefix(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		Config: config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{Server: config.ServerConfig{}, Policy: minimalPolicy()}),
		StaticFS: fstest.MapFS{
			"mobile/index.html": &fstest.MapFile{Data: []byte("mobile")},
		},
	})
	handler := server.Routes()

	request := httptest.NewRequest(http.MethodGet, "/mobile", nil)
	request.Header.Set("X-Forwarded-Prefix", "/xmux")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusMovedPermanently {
		t.Fatalf("mobile route status = %d, want 301", response.Code)
	}
	if got := response.Header().Get("Location"); got != "/xmux/mobile/" {
		t.Fatalf("mobile route Location = %q, want /xmux/mobile/", got)
	}
}

func TestCheckOriginAllowsSameHost(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{Server: config.ServerConfig{}})
	request := &http.Request{
		Host: "127.0.0.1:18001",
		Header: http.Header{
			"Origin": []string{"http://127.0.0.1:18001"},
		},
	}

	if !server.checkOrigin(request) {
		t.Fatal("expected same origin to be allowed")
	}
}

func TestCheckOriginRejectsDifferentHost(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{Server: config.ServerConfig{}})
	request := &http.Request{
		Host: "127.0.0.1:18001",
		Header: http.Header{
			"Origin": []string{"http://evil.example"},
		},
	}

	if server.checkOrigin(request) {
		t.Fatal("expected different origin to be rejected")
	}
}

func TestCheckOriginAllowsConfiguredHost(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{Server: config.ServerConfig{
		AllowHosts: []string{"terminal.example.com"},
	}})
	request := &http.Request{
		Host: "127.0.0.1:18001",
		Header: http.Header{
			"Origin": []string{"https://terminal.example.com"},
		},
	}

	if !server.checkOrigin(request) {
		t.Fatal("expected allow_hosts origin to be allowed")
	}
}

func TestCheckOriginAllowsForwardedHost(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{Server: config.ServerConfig{}})
	request := &http.Request{
		Host: "127.0.0.1:18001",
		Header: http.Header{
			"Origin":           []string{"https://terminal.example.com"},
			"X-Forwarded-Host": []string{"terminal.example.com"},
		},
	}

	if !server.checkOrigin(request) {
		t.Fatal("expected forwarded host origin to be allowed")
	}
}

func TestAccountSessionAuthorizesLive(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{
		Server: config.ServerConfig{
			AccountStorePath:           filepath.Join(t.TempDir(), "accounts.json"),
			AccountRegistrationEnabled: testBoolPtr(true),
		},
		Policy: minimalPolicy(),
	})
	cookies := registerTestAccount(t, server, "dev@example.com", "TestPassword1")
	request := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/edge", nil)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	if !server.authorized(request) {
		t.Fatal("expected account session cookie to authorize")
	}

	noCookie := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/edge", nil)
	if server.authorized(noCookie) {
		t.Fatal("expected request without account session to be rejected")
	}
}

func TestEdgeInfoDefaultsToAccountAllowPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	globalWorkDir := filepath.Join(root, "global")
	accountWorkDir := filepath.Join(root, "account")
	if err := os.MkdirAll(globalWorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(accountWorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config.Config{
		Server: config.ServerConfig{AccountRegistrationEnabled: testBoolPtr(true)},
		Edge:   config.EdgeConfig{WorkDir: globalWorkDir},
		Policy: policy.Config{
			AllowPaths: []string{globalWorkDir},
			Commands:   map[string]policy.CommandPolicy{"pwd": {Enabled: true}},
		},
	})
	cookies := registerTestAccount(t, server, "user@example.com", "TestPassword1")
	if _, err := server.accountStore().SaveUserSettings("user@example.com", userSettingsUpdatePayload{
		AllowPaths: []string{accountWorkDir},
		Commands:   map[string]adminCommandPayload{"pwd": {Enabled: true}},
	}, server.config.Snapshot().Policy); err != nil {
		t.Fatalf("save user settings: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/edge", nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp := httptest.NewRecorder()
	server.Routes().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("edge status = %d body = %s", resp.Code, resp.Body.String())
	}
	var payload struct {
		WorkDir string `json:"work_dir"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode edge: %v", err)
	}
	if payload.WorkDir != accountWorkDir {
		t.Fatalf("edge work_dir = %q, want %q", payload.WorkDir, accountWorkDir)
	}
}

func TestAccountRegisterLoginAndWorkbenchState(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{
		Server: config.ServerConfig{
			AccountStorePath:           filepath.Join(t.TempDir(), "accounts.json"),
			AccountRegistrationEnabled: testBoolPtr(true),
		},
		Policy: minimalPolicy(),
	})
	handler := server.Routes()
	if server.appSettings != nil {
		_ = server.appSettings.Set(appSettingKeyAuthSettings, authSettings{
			RequireEmailOnRegister:      false,
			RequireEmailVerifiedToLogin: false,
		}, "test-suite")
	}

	registerBody := bytes.NewBufferString(`{"username":"dev@example.com","password":"TestPassword1"}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/cloud-terminal-api/accounts/register", registerBody)
	registerReq.Header.Set("Content-Type", "application/json")
	registerResp := httptest.NewRecorder()
	handler.ServeHTTP(registerResp, registerReq)
	if registerResp.Code != http.StatusOK {
		t.Fatalf("register status = %d body = %s", registerResp.Code, registerResp.Body.String())
	}
	if len(registerResp.Result().Cookies()) == 0 {
		t.Fatal("expected registration to set cookies")
	}

	var registerPayload struct {
		Username string                `json:"username"`
		State    workbenchStatePayload `json:"state"`
	}
	if err := json.NewDecoder(registerResp.Body).Decode(&registerPayload); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if registerPayload.Username != "dev@example.com" {
		t.Fatalf("username = %q", registerPayload.Username)
	}

	stateReq := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/workbench/state", nil)
	for _, cookie := range registerResp.Result().Cookies() {
		stateReq.AddCookie(cookie)
	}
	stateResp := httptest.NewRecorder()
	handler.ServeHTTP(stateResp, stateReq)
	if stateResp.Code != http.StatusOK {
		t.Fatalf("state status = %d body = %s", stateResp.Code, stateResp.Body.String())
	}

	loginBody := bytes.NewBufferString(`{"username":"dev@example.com","password":"TestPassword1"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/cloud-terminal-api/accounts/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp := httptest.NewRecorder()
	handler.ServeHTTP(loginResp, loginReq)
	if loginResp.Code != http.StatusOK {
		t.Fatalf("login status = %d body = %s", loginResp.Code, loginResp.Body.String())
	}
}

func TestAccountCanUpdateOwnPassword(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{
		Server: config.ServerConfig{AccountRegistrationEnabled: testBoolPtr(true)},
		Policy: minimalPolicy(),
	})
	handler := server.Routes()
	cookies := registerTestAccount(t, server, "dev@example.com", "TestPassword1")

	body := bytes.NewBufferString(`{"current_password":"wrong","new_password":"ChangedPass1"}`)
	req := httptest.NewRequest(http.MethodPut, "/cloud-terminal-api/accounts/me", body)
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("wrong password update status = %d body = %s", resp.Code, resp.Body.String())
	}

	body = bytes.NewBufferString(`{"current_password":"TestPassword1","new_password":"ChangedPass1"}`)
	req = httptest.NewRequest(http.MethodPut, "/cloud-terminal-api/accounts/me", body)
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("password update status = %d body = %s", resp.Code, resp.Body.String())
	}
	if !server.accountStore().VerifyPassword("dev@example.com", "ChangedPass1") {
		t.Fatal("expected updated password to verify")
	}
}

func TestDefaultAdminCanManageAccounts(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{
		Server: config.ServerConfig{
			AdminUsername:              "admin",
			AdminPassword:              "admin123456",
			AccountStorePath:           filepath.Join(t.TempDir(), "accounts.json"),
			AccountRegistrationEnabled: testBoolPtr(true),
		},
		Policy: minimalPolicy(),
	})
	handler := server.Routes()

	adminCookies := loginTestAccount(t, server, "admin", "admin123456")
	createBody := bytes.NewBufferString(`{"username":"managed@example.com","password":"TestPassword1","role":"user"}`)
	createReq := httptest.NewRequest(http.MethodPost, "/cloud-terminal-api/admin/accounts", createBody)
	createReq.Header.Set("Content-Type", "application/json")
	for _, cookie := range adminCookies {
		createReq.AddCookie(cookie)
	}
	createResp := httptest.NewRecorder()
	handler.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create account status = %d body = %s", createResp.Code, createResp.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/admin/accounts", nil)
	for _, cookie := range adminCookies {
		listReq.AddCookie(cookie)
	}
	listResp := httptest.NewRecorder()
	handler.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list accounts status = %d body = %s", listResp.Code, listResp.Body.String())
	}
	if !bytes.Contains(listResp.Body.Bytes(), []byte(`"role":"admin"`)) || !bytes.Contains(listResp.Body.Bytes(), []byte("managed@example.com")) {
		t.Fatalf("accounts response = %s", listResp.Body.String())
	}
}

func TestRegistrationGatedByEmailVerification(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{
		Server: config.ServerConfig{
			AccountStorePath:           filepath.Join(t.TempDir(), "accounts.json"),
			AccountRegistrationEnabled: testBoolPtr(true),
		},
		Policy: minimalPolicy(),
	})
	_ = server.appSettings.Set(appSettingKeyAuthSettings, authSettings{
		RequireEmailOnRegister:      true,
		RequireEmailVerifiedToLogin: false,
	}, "test-suite")
	server.reloadMailerLocked()
	captured := &capturingMailSender{}
	server.mailer.Set(captured)
	handler := server.Routes()

	postJSON := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}

	// Registering without a code while email is required should fail.
	resp := postJSON("/cloud-terminal-api/accounts/register", `{"username":"pending@example.com","password":"TestPassword1","email":"pending@example.com"}`)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "code") {
		t.Fatalf("expected 400 code-required, got %d body=%s", resp.Code, resp.Body.String())
	}

	// Ask for a code, capture from the mail body.
	sendResp := postJSON("/cloud-terminal-api/accounts/register/send-code", `{"email":"pending@example.com"}`)
	if sendResp.Code != http.StatusOK {
		t.Fatalf("send-code status = %d body = %s", sendResp.Code, sendResp.Body.String())
	}
	msg := captured.Last()
	if msg.To != "pending@example.com" {
		t.Fatalf("send-code email To = %q", msg.To)
	}
	code := extractRegistrationCode(t, msg.HTMLBody+msg.TextBody)

	// Wrong code path: should fail, leave account unborn.
	wrong := postJSON("/cloud-terminal-api/accounts/register", `{"username":"pending@example.com","password":"TestPassword1","email":"pending@example.com","code":"000000"}`)
	if wrong.Code != http.StatusBadRequest {
		t.Fatalf("wrong code expected 400, got %d body=%s", wrong.Code, wrong.Body.String())
	}
	if _, err := server.accountStore().Login("pending@example.com", "TestPassword1"); err == nil {
		t.Fatal("expected login to fail after wrong-code register attempt")
	}

	// Correct code path: account is created + auto-logged-in (cookies set).
	ok := postJSON("/cloud-terminal-api/accounts/register", `{"username":"pending@example.com","password":"TestPassword1","email":"pending@example.com","code":"`+code+`"}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("register OK status = %d body = %s", ok.Code, ok.Body.String())
	}
	if cookies := ok.Result().Cookies(); len(cookies) == 0 {
		t.Fatal("expected session cookies on successful register")
	}
	if _, err := server.accountStore().Login("pending@example.com", "TestPassword1"); err != nil {
		t.Fatalf("expected post-register login to succeed: %v", err)
	}
	rec, _ := server.accountStore().AccountRecord("pending@example.com")
	if !rec.EmailVerified {
		t.Fatal("expected email_verified=true after code-based register")
	}

	// Reusing the same code should now fail (one-shot).
	replay := postJSON("/cloud-terminal-api/accounts/register", `{"username":"other@example.com","password":"TestPassword1","email":"other@example.com","code":"`+code+`"}`)
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("replay expected 400, got %d body=%s", replay.Code, replay.Body.String())
	}
}

type capturingMailSender struct {
	mu   sync.Mutex
	msgs []mail.Message
}

func (c *capturingMailSender) Send(_ context.Context, msg mail.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, msg)
	return nil
}

func (c *capturingMailSender) Kind() string { return "test-capture" }

func (c *capturingMailSender) Last() mail.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.msgs) == 0 {
		return mail.Message{}
	}
	return c.msgs[len(c.msgs)-1]
}

func extractRegistrationCode(t *testing.T, body string) string {
	t.Helper()
	re := regexp.MustCompile(`\b\d{6}\b`)
	match := re.FindString(body)
	if match == "" {
		t.Fatalf("6-digit code not found in mail body:\n%s", body)
	}
	return match
}

// Regression: session cookies must be SameSite=Lax so they survive the
// cross-site → same-site redirect chain that OAuth (Google) walks through.
// Strict would cause the browser to drop the cookie on the redirect to /user/
// and bounce the user back to the login screen.
func TestSessionCookiesAreSameSiteLax(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{
		Server: config.ServerConfig{
			AccountStorePath:           filepath.Join(t.TempDir(), "accounts.json"),
			AccountRegistrationEnabled: testBoolPtr(true),
		},
		Policy: minimalPolicy(),
	})
	handler := server.Routes()
	cookies := registerTestAccount(t, server, "lax@example.com", "TestPassword1")
	if len(cookies) == 0 {
		t.Fatal("expected cookies from registerTestAccount")
	}
	for _, cookie := range cookies {
		switch cookie.Name {
		case accountCookieName, workbenchCookieName:
			if cookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("cookie %s SameSite = %v, want Lax", cookie.Name, cookie.SameSite)
			}
		}
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/cloud-terminal-api/accounts/login", bytes.NewBufferString(`{"username":"lax@example.com","password":"TestPassword1"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp := httptest.NewRecorder()
	handler.ServeHTTP(loginResp, loginReq)
	if loginResp.Code != http.StatusOK {
		t.Fatalf("login status = %d", loginResp.Code)
	}
	for _, cookie := range loginResp.Result().Cookies() {
		switch cookie.Name {
		case accountCookieName, workbenchCookieName:
			if cookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("login cookie %s SameSite = %v, want Lax", cookie.Name, cookie.SameSite)
			}
		}
	}
}

func TestAdminCanDisableAndResetAccount(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{
		Server: config.ServerConfig{
			AdminUsername:              "admin",
			AdminPassword:              "admin123456",
			AccountStorePath:           filepath.Join(t.TempDir(), "accounts.json"),
			AccountRegistrationEnabled: testBoolPtr(true),
		},
		Policy: minimalPolicy(),
	})
	handler := server.Routes()

	adminCookies := loginTestAccount(t, server, "admin", "admin123456")
	createBody := bytes.NewBufferString(`{"username":"managed@example.com","password":"TestPassword1","role":"user"}`)
	createReq := httptest.NewRequest(http.MethodPost, "/cloud-terminal-api/admin/accounts", createBody)
	createReq.Header.Set("Content-Type", "application/json")
	for _, cookie := range adminCookies {
		createReq.AddCookie(cookie)
	}
	createResp := httptest.NewRecorder()
	handler.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create account status = %d body = %s", createResp.Code, createResp.Body.String())
	}

	manage := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/cloud-terminal-api/admin/accounts/manage", bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		for _, cookie := range adminCookies {
			req.AddCookie(cookie)
		}
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}

	resp := manage(`{"action":"disable","username":"managed@example.com"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("disable status = %d body = %s", resp.Code, resp.Body.String())
	}
	if _, err := server.accountStore().Login("managed@example.com", "TestPassword1"); err == nil {
		t.Fatal("expected disabled account login to fail")
	}

	resp = manage(`{"action":"enable","username":"managed@example.com"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("enable status = %d body = %s", resp.Code, resp.Body.String())
	}
	if _, err := server.accountStore().Login("managed@example.com", "TestPassword1"); err != nil {
		t.Fatalf("expected enabled account to log in: %v", err)
	}

	resp = manage(`{"action":"reset_password","username":"managed@example.com","password":"RotatedPass1"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("reset status = %d body = %s", resp.Code, resp.Body.String())
	}
	if !server.accountStore().VerifyPassword("managed@example.com", "RotatedPass1") {
		t.Fatal("expected rotated password to verify")
	}

	resp = manage(`{"action":"disable","username":"admin"}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("self-disable status = %d body = %s", resp.Code, resp.Body.String())
	}
}

func TestUserAccountCannotAccessAdmin(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{
		Server: config.ServerConfig{
			AccountStorePath:           filepath.Join(t.TempDir(), "accounts.json"),
			AccountRegistrationEnabled: testBoolPtr(true),
		},
		Policy: minimalPolicy(),
	})
	handler := server.Routes()
	userCookies := registerTestAccount(t, server, "user@example.com", "TestPassword1")

	req := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/admin/accounts", nil)
	for _, cookie := range userCookies {
		req.AddCookie(cookie)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("admin account list status = %d, want 401", resp.Code)
	}
}

func TestUserStaticRequiresAccountButNotAdmin(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		Config: config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{
			Server: config.ServerConfig{AccountRegistrationEnabled: testBoolPtr(true)},
			Policy: minimalPolicy(),
		}),
		StaticFS: fstest.MapFS{
			"index.html":      &fstest.MapFile{Data: []byte("root")},
			"user/index.html": &fstest.MapFile{Data: []byte("user")},
			"user/login.html": &fstest.MapFile{Data: []byte("login")},
			"user/app.js":     &fstest.MapFile{Data: []byte("app")},
		},
	})
	handler := server.Routes()

	req := httptest.NewRequest(http.MethodGet, "/user/", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusFound {
		t.Fatalf("anonymous user console status = %d, want 302", resp.Code)
	}

	cookies := registerTestAccount(t, server, "user@example.com", "TestPassword1")
	req = httptest.NewRequest(http.MethodGet, "/user/", nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("ordinary user console status = %d body = %s", resp.Code, resp.Body.String())
	}
	if resp.Body.String() != "user" {
		t.Fatalf("user console body = %q", resp.Body.String())
	}
}

func TestUserStaticRedirectKeepsForwardedPrefix(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		Config: config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{
			Server: config.ServerConfig{AccountRegistrationEnabled: testBoolPtr(true)},
			Policy: minimalPolicy(),
		}),
		StaticFS: fstest.MapFS{
			"user/index.html": &fstest.MapFile{Data: []byte("user")},
			"user/login.html": &fstest.MapFile{Data: []byte("login")},
		},
	})
	handler := server.Routes()

	req := httptest.NewRequest(http.MethodGet, "/user/", nil)
	req.Header.Set("X-Forwarded-Prefix", "/xmux")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusFound {
		t.Fatalf("anonymous user console status = %d, want 302", resp.Code)
	}
	if got := resp.Header().Get("Location"); got != "/xmux/user/login.html" {
		t.Fatalf("anonymous user console Location = %q, want /xmux/user/login.html", got)
	}
}

func TestOAuthErrorRedirectKeepsExternalPrefix(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		Config: config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{
			Server: config.ServerConfig{AccountRegistrationEnabled: testBoolPtr(true)},
			Policy: minimalPolicy(),
		}),
		StaticFS: fstest.MapFS{
			"user/login.html": &fstest.MapFile{Data: []byte("login")},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/accounts/oauth/google/callback", nil)
	req.Header.Set("X-Forwarded-Prefix", "/xmux")
	resp := httptest.NewRecorder()
	server.redirectOAuthError(resp, req, "token_exchange_failed")

	if resp.Code != http.StatusFound {
		t.Fatalf("oauth error status = %d, want 302", resp.Code)
	}
	want := "/xmux/user/login.html?oauth_error=token_exchange_failed"
	if got := resp.Header().Get("Location"); got != want {
		t.Fatalf("oauth error Location = %q, want %q", got, want)
	}
}

func TestOAuthStartAcceptsExternalPrefixWhenProxyDoesNotStrip(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		Config: config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{
			Server: config.ServerConfig{AccountRegistrationEnabled: testBoolPtr(true)},
			Policy: minimalPolicy(),
		}),
		StaticFS: fstest.MapFS{
			"user/login.html": &fstest.MapFile{Data: []byte("login")},
		},
	})
	if server.appSettings == nil {
		t.Fatal("expected app settings")
	}
	if err := server.appSettings.Set(appSettingKeyOAuthGoogle, oauthGoogleSettings{
		Enabled:     true,
		ClientID:    "client-id",
		RedirectURL: "https://ops.example.com/xmux/cloud-terminal-api/accounts/oauth/google/callback",
	}, "test-suite"); err != nil {
		t.Fatalf("set oauth settings: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/xmux/cloud-terminal-api/accounts/oauth/google/start?return_to=%2Fxmux%2Fuser%2F", nil)
	resp := httptest.NewRecorder()
	server.Routes().ServeHTTP(resp, req)

	if resp.Code != http.StatusFound {
		t.Fatalf("oauth start status = %d body = %s", resp.Code, resp.Body.String())
	}
	location := resp.Header().Get("Location")
	if !strings.HasPrefix(location, googleAuthURL+"?") {
		t.Fatalf("oauth start Location = %q, want google auth URL", location)
	}
	for _, cookie := range resp.Result().Cookies() {
		if cookie.Name == oauthReturnToCookie && cookie.Value != "/xmux/user/" {
			t.Fatalf("return_to cookie = %q, want /xmux/user/", cookie.Value)
		}
	}
}

func TestExternalPathPrefixCanComeFromAppBaseURL(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		Config: config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{
			Server: config.ServerConfig{AccountRegistrationEnabled: testBoolPtr(true)},
			Policy: minimalPolicy(),
		}),
		StaticFS: fstest.MapFS{
			"user/login.html": &fstest.MapFile{Data: []byte("login")},
		},
	})
	if server.appSettings == nil {
		t.Fatal("expected app settings")
	}
	if err := server.appSettings.Set(appSettingKeyAppBaseURL, "https://ops.example.com/xmux/", "test-suite"); err != nil {
		t.Fatalf("set app base URL: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/user/", nil)
	resp := httptest.NewRecorder()
	server.redirectExternal(resp, req, "/user/login.html", http.StatusFound)

	if resp.Code != http.StatusFound {
		t.Fatalf("redirect status = %d, want 302", resp.Code)
	}
	if got := resp.Header().Get("Location"); got != "/xmux/user/login.html" {
		t.Fatalf("redirect Location = %q, want /xmux/user/login.html", got)
	}
}

func TestExternalPathPrefixCanComeFromOAuthRedirectURL(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		Config: config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{
			Server: config.ServerConfig{AccountRegistrationEnabled: testBoolPtr(true)},
			Policy: minimalPolicy(),
		}),
		StaticFS: fstest.MapFS{
			"user/login.html": &fstest.MapFile{Data: []byte("login")},
		},
	})
	if server.appSettings == nil {
		t.Fatal("expected app settings")
	}
	if err := server.appSettings.Set(appSettingKeyOAuthGoogle, oauthGoogleSettings{
		Enabled:     true,
		ClientID:    "client-id",
		RedirectURL: "https://ops.example.com/xmux/cloud-terminal-api/accounts/oauth/google/callback",
	}, "test-suite"); err != nil {
		t.Fatalf("set oauth settings: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/accounts/oauth/google/callback", nil)
	resp := httptest.NewRecorder()
	server.redirectOAuthError(resp, req, "token_exchange_failed")

	if resp.Code != http.StatusFound {
		t.Fatalf("oauth error status = %d, want 302", resp.Code)
	}
	want := "/xmux/user/login.html?oauth_error=token_exchange_failed"
	if got := resp.Header().Get("Location"); got != want {
		t.Fatalf("oauth error Location = %q, want %q", got, want)
	}
}

func TestDiscoveryGatewayIncludesForwardedPrefix(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		Config:   config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{Server: config.ServerConfig{}, Policy: minimalPolicy()}),
		StaticFS: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("root")}},
	})
	handler := server.Routes()

	req := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/discovery/gateway", nil)
	req.Host = "internal.local"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "ops.example.com")
	req.Header.Set("X-Forwarded-Prefix", "/xmux")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("discovery status = %d body = %s", resp.Code, resp.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode discovery response: %v", err)
	}
	if got := payload["gateway_url"]; got != "https://ops.example.com/xmux" {
		t.Fatalf("gateway_url = %q, want https://ops.example.com/xmux", got)
	}
}

func TestCloudTunnelUsesCurrentAdminSession(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "policy.yaml")
	server := testServerWithPath(t, configPath, config.Config{
		Server: config.ServerConfig{
			AdminUsername:              "admin",
			AdminPassword:              "admin123456",
			AccountStorePath:           filepath.Join(t.TempDir(), "accounts.json"),
			AccountRegistrationEnabled: testBoolPtr(true),
		},
		Policy: minimalPolicy(),
	})
	handler := server.Routes()
	adminCookies := loginTestAccount(t, server, "admin", "admin123456")

	body := bytes.NewBufferString(`{"account_store_path":"` + server.config.AccountStorePath() + `","account_registration_enabled":true,"cloud_tunnel":{"enabled":true,"gateway_url":"https://cloud.example.com","use_current_account":true},"commands":{"pwd":{"enabled":true,"max_args":0}}}`)
	req := httptest.NewRequest(http.MethodPut, "/cloud-terminal-api/admin/config", body)
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range adminCookies {
		req.AddCookie(cookie)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("admin config status = %d body = %s", resp.Code, resp.Body.String())
	}
	cfg := server.config.Snapshot()
	if cfg.CloudTunnel.Account != "admin" || cfg.CloudTunnel.SessionID == "" {
		t.Fatalf("cloud tunnel = %+v, want current admin session", cfg.CloudTunnel)
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/cloud-terminal-api/accounts/logout", nil)
	for _, cookie := range adminCookies {
		logoutReq.AddCookie(cookie)
	}
	logoutResp := httptest.NewRecorder()
	handler.ServeHTTP(logoutResp, logoutReq)
	if logoutResp.Code != http.StatusOK {
		t.Fatalf("logout status = %d body = %s", logoutResp.Code, logoutResp.Body.String())
	}
	if !server.accountStore().VerifySession(cfg.CloudTunnel.SessionID, cfg.CloudTunnel.Account) {
		t.Fatal("expected bound tunnel session to survive browser logout")
	}
}

func TestUserDoesNotReuseAdminConfiguredTunnel(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Server: config.ServerConfig{
			AdminUsername:              "admin",
			AdminPassword:              "admin123456",
			AccountStorePath:           filepath.Join(t.TempDir(), "accounts.json"),
			AccountRegistrationEnabled: testBoolPtr(true),
		},
		CloudTunnel: config.CloudTunnelConfig{Account: "admin"},
		Policy:      minimalPolicy(),
	}
	store := config.NewStore(filepath.Join(t.TempDir(), "policy.yaml"), &cfg)
	server := NewServer(Options{
		Config:   store,
		StaticFS: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}},
	})
	server.tunnel.set(&tunnelClient{
		hub:          server.tunnel,
		account:      "admin",
		edgeID:       "edge-admin",
		edgeName:     "Admin Edge",
		workDir:      "/workspace",
		allowPaths:   []string{"/workspace"},
		previewPorts: []int{3000},
		agents:       []workbenchAgentInfo{{ID: "codex", Label: "Codex", Command: "codex", Enabled: true}},
		sessions:     []workbenchSessionInfo{{ID: "admin-session", Account: "admin", Agent: "codex", WorkDir: "/workspace", LastActive: "2026-05-16T16:00:00+08:00", Running: true}},
		pending:      make(map[string]chan tunnelEnvelope),
		exitWaiters:  make(map[string]chan workbenchServerMessage),
	})

	userCookies := registerTestAccount(t, server, "user@example.com", "TestPassword1")
	req := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/workbench/state", nil)
	for _, cookie := range userCookies {
		req.AddCookie(cookie)
	}
	resp := httptest.NewRecorder()
	server.Routes().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("state status = %d body = %s", resp.Code, resp.Body.String())
	}
	var state workbenchStatePayload
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.EdgeOnline || state.EdgeID == "edge-admin" {
		t.Fatalf("state = %+v, ordinary user should not reuse admin tunnel", state)
	}
	for _, session := range state.Sessions {
		if session.ID == "admin-session" {
			t.Fatalf("ordinary user should not see admin session in state: %+v", state.Sessions)
		}
	}
}

func TestUserSettingsPersistAndConstrainPolicy(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	allowedChild := filepath.Join(root, "child")
	if err := os.MkdirAll(allowedChild, 0o755); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config.Config{
		Server: config.ServerConfig{AccountRegistrationEnabled: testBoolPtr(true)},
		Edge:   config.EdgeConfig{WorkDir: root},
		Policy: policy.Config{
			AllowPaths: []string{root},
			Commands: map[string]policy.CommandPolicy{
				"pwd": {Enabled: true},
				"ls":  {Enabled: true, MaxArgs: 4},
			},
		},
	})
	handler := server.Routes()
	userCookies := registerTestAccount(t, server, "user@example.com", "TestPassword1")

	getReq := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/user/settings", nil)
	for _, cookie := range userCookies {
		getReq.AddCookie(cookie)
	}
	getResp := httptest.NewRecorder()
	handler.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("settings status = %d body = %s", getResp.Code, getResp.Body.String())
	}
	var settings userSettingsPayload
	if err := json.NewDecoder(getResp.Body).Decode(&settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if settings.Account != "user@example.com" || settings.Role != accountRoleUser {
		t.Fatalf("settings identity = %+v", settings)
	}
	if len(settings.AllowPaths) != 0 {
		t.Fatalf("settings allow_paths = %v, want empty account paths", settings.AllowPaths)
	}
	if len(settings.PolicyLimits.AllowPaths) != 0 {
		t.Fatalf("policy_limits allow_paths = %v, want hidden from account policy", settings.PolicyLimits.AllowPaths)
	}

	// Per-user allow_paths are not bounded by the global policy; a path
	// outside the global roots should still be persisted (each agent has
	// its own filesystem, so the cloud admin can't enumerate them).
	outsidePathBody := bytes.NewBufferString(`{"allow_paths":["/definitely-outside"],"commands":{"pwd":{"enabled":true}}}`)
	outsidePathReq := httptest.NewRequest(http.MethodPut, "/cloud-terminal-api/user/settings", outsidePathBody)
	outsidePathReq.Header.Set("Content-Type", "application/json")
	for _, cookie := range userCookies {
		outsidePathReq.AddCookie(cookie)
	}
	outsidePathResp := httptest.NewRecorder()
	handler.ServeHTTP(outsidePathResp, outsidePathReq)
	if outsidePathResp.Code != http.StatusOK {
		t.Fatalf("outside path should be accepted now, got status = %d body = %s", outsidePathResp.Code, outsidePathResp.Body.String())
	}

	validBody := bytes.NewBufferString(`{"cloud_tunnel_enabled":true,"allow_paths":["` + allowedChild + `"],"commands":{"pwd":{"enabled":true},"git":{"enabled":true}}}`)
	validReq := httptest.NewRequest(http.MethodPut, "/cloud-terminal-api/user/settings", validBody)
	validReq.Header.Set("Content-Type", "application/json")
	for _, cookie := range userCookies {
		validReq.AddCookie(cookie)
	}
	validResp := httptest.NewRecorder()
	handler.ServeHTTP(validResp, validReq)
	if validResp.Code != http.StatusBadRequest {
		t.Fatalf("unknown command settings status = %d body = %s", validResp.Code, validResp.Body.String())
	}

	validBody = bytes.NewBufferString(`{"cloud_tunnel_enabled":true,"allow_paths":["` + allowedChild + `"],"commands":{"pwd":{"enabled":true},"ls":{"enabled":false,"max_args":4}}}`)
	validReq = httptest.NewRequest(http.MethodPut, "/cloud-terminal-api/user/settings", validBody)
	validReq.Header.Set("Content-Type", "application/json")
	for _, cookie := range userCookies {
		validReq.AddCookie(cookie)
	}
	validResp = httptest.NewRecorder()
	handler.ServeHTTP(validResp, validReq)
	if validResp.Code != http.StatusOK {
		t.Fatalf("valid settings status = %d body = %s", validResp.Code, validResp.Body.String())
	}

	stateReq := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/workbench/state", nil)
	for _, cookie := range userCookies {
		stateReq.AddCookie(cookie)
	}
	stateResp := httptest.NewRecorder()
	handler.ServeHTTP(stateResp, stateReq)
	if stateResp.Code != http.StatusOK {
		t.Fatalf("state status = %d body = %s", stateResp.Code, stateResp.Body.String())
	}
	var state workbenchStatePayload
	if err := json.NewDecoder(stateResp.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if len(state.AllowPaths) != 1 || state.AllowPaths[0] != allowedChild {
		t.Fatalf("state allow_paths = %v, want %s", state.AllowPaths, allowedChild)
	}
	if state.WorkDir != allowedChild {
		t.Fatalf("state work_dir = %q, want %q", state.WorkDir, allowedChild)
	}
}

func TestUserArchiveSyncFiltersWorkbenchState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	project := filepath.Join(root, "project")
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config.Config{
		Server: config.ServerConfig{AccountRegistrationEnabled: testBoolPtr(true)},
		Policy: policy.Config{
			AllowPaths: []string{root},
			Commands:   map[string]policy.CommandPolicy{"pwd": {Enabled: true}},
		},
	})
	cookies := registerTestAccount(t, server, "user@example.com", "TestPassword1")
	if _, err := server.accountStore().SaveUserSettings("user@example.com", userSettingsUpdatePayload{
		AllowPaths: []string{root},
		Commands:   map[string]adminCommandPayload{"pwd": {Enabled: true}},
	}, server.config.Snapshot().Policy); err != nil {
		t.Fatalf("save user settings: %v", err)
	}
	server.workbench.sessions["session-archived"] = &workbenchSession{
		id:          "session-archived",
		account:     "user@example.com",
		agent:       "codex",
		workDir:     project,
		startedAt:   time.Now(),
		lastActive:  time.Now(),
		submitted:   true,
		title:       "archived task",
		attachments: map[uint64]func(workbenchServerMessage){},
	}
	server.workbench.sessions["session-visible"] = &workbenchSession{
		id:          "session-visible",
		account:     "user@example.com",
		agent:       "codex",
		workDir:     other,
		startedAt:   time.Now(),
		lastActive:  time.Now(),
		submitted:   true,
		title:       "visible task",
		attachments: map[uint64]func(workbenchServerMessage){},
	}

	body, err := json.Marshal(userArchiveUpdatePayload{
		ArchivedFolders:  []string{project},
		ArchivedSessions: []string{"session-archived"},
	})
	if err != nil {
		t.Fatalf("marshal archive update: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/cloud-terminal-api/user/archive", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp := httptest.NewRecorder()
	server.Routes().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("archive status = %d body = %s", resp.Code, resp.Body.String())
	}

	stateReq := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/workbench/state", nil)
	for _, cookie := range cookies {
		stateReq.AddCookie(cookie)
	}
	stateResp := httptest.NewRecorder()
	server.Routes().ServeHTTP(stateResp, stateReq)
	if stateResp.Code != http.StatusOK {
		t.Fatalf("state status = %d body = %s", stateResp.Code, stateResp.Body.String())
	}
	var state workbenchStatePayload
	if err := json.NewDecoder(stateResp.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if len(state.Sessions) != 1 || state.Sessions[0].ID != "session-visible" {
		t.Fatalf("visible sessions = %+v, want only session-visible", state.Sessions)
	}
	if len(state.ArchivedFolders) != 1 || state.ArchivedFolders[0] != project {
		t.Fatalf("archived folders = %+v, want %s", state.ArchivedFolders, project)
	}
	if len(state.ArchivedSessionItems) != 1 || state.ArchivedSessionItems[0].ID != "session-archived" {
		t.Fatalf("archived session items = %+v, want session-archived", state.ArchivedSessionItems)
	}
}

func TestWorkbenchStateFiltersSessionsOutsideAccountAllowPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	globalWorkDir := filepath.Join(root, "global")
	accountWorkDir := filepath.Join(root, "account")
	if err := os.MkdirAll(globalWorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(accountWorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config.Config{
		Server: config.ServerConfig{AccountRegistrationEnabled: testBoolPtr(true)},
		Edge:   config.EdgeConfig{WorkDir: globalWorkDir},
		Policy: policy.Config{
			AllowPaths: []string{globalWorkDir},
			Commands:   map[string]policy.CommandPolicy{"pwd": {Enabled: true}},
		},
	})
	cookies := registerTestAccount(t, server, "user@example.com", "TestPassword1")
	if _, err := server.accountStore().SaveUserSettings("user@example.com", userSettingsUpdatePayload{
		AllowPaths: []string{accountWorkDir},
		Commands:   map[string]adminCommandPayload{"pwd": {Enabled: true}},
	}, server.config.Snapshot().Policy); err != nil {
		t.Fatalf("save user settings: %v", err)
	}
	server.workbench.sessions["outside"] = &workbenchSession{
		id:          "outside",
		account:     "user@example.com",
		agent:       "codex",
		workDir:     globalWorkDir,
		startedAt:   time.Now(),
		lastActive:  time.Now(),
		submitted:   true,
		attachments: map[uint64]func(workbenchServerMessage){},
	}
	server.workbench.sessions["inside"] = &workbenchSession{
		id:          "inside",
		account:     "user@example.com",
		agent:       "codex",
		workDir:     accountWorkDir,
		startedAt:   time.Now(),
		lastActive:  time.Now().Add(time.Second),
		submitted:   true,
		attachments: map[uint64]func(workbenchServerMessage){},
	}

	req := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/workbench/state", nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp := httptest.NewRecorder()
	server.Routes().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("state status = %d body = %s", resp.Code, resp.Body.String())
	}
	var state workbenchStatePayload
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if len(state.Sessions) != 1 || state.Sessions[0].ID != "inside" {
		t.Fatalf("sessions = %+v, want only inside session", state.Sessions)
	}
}

func TestTerminalWSDefaultsToAccountAllowPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	globalWorkDir := filepath.Join(root, "global")
	accountWorkDir := filepath.Join(root, "account")
	if err := os.MkdirAll(globalWorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(accountWorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config.Config{
		Server: config.ServerConfig{AccountRegistrationEnabled: testBoolPtr(true)},
		Edge:   config.EdgeConfig{WorkDir: globalWorkDir},
		Policy: policy.Config{
			AllowPaths: []string{globalWorkDir},
			Commands:   map[string]policy.CommandPolicy{"pwd": {Enabled: true}},
		},
	})
	cookies := registerTestAccount(t, server, "user@example.com", "TestPassword1")
	if _, err := server.accountStore().SaveUserSettings("user@example.com", userSettingsUpdatePayload{
		AllowPaths: []string{accountWorkDir},
		Commands:   map[string]adminCommandPayload{"pwd": {Enabled: true}},
	}, server.config.Snapshot().Policy); err != nil {
		t.Fatalf("save user settings: %v", err)
	}

	httpServer := httptest.NewServer(server.Routes())
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/cloud-terminal-api/ws/terminal"
	header := http.Header{}
	for _, cookie := range cookies {
		header.Add("Cookie", cookie.String())
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("dial terminal websocket: %v", err)
	}
	defer conn.Close()

	var ready serverMessage
	if err := conn.ReadJSON(&ready); err != nil {
		t.Fatalf("read ready: %v", err)
	}
	if ready.Type != "ready" {
		t.Fatalf("ready type = %q, want ready", ready.Type)
	}
	if ready.WorkDir != accountWorkDir {
		t.Fatalf("ready work_dir = %q, want %q", ready.WorkDir, accountWorkDir)
	}
}

func TestUserFSOnlyListsAccountAllowedRoots(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	inside := filepath.Join(root, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	server := testServer(t, config.Config{
		Server: config.ServerConfig{AccountRegistrationEnabled: testBoolPtr(true)},
		Policy: policy.Config{
			AllowPaths: []string{root},
			Commands:   map[string]policy.CommandPolicy{"pwd": {Enabled: true}},
		},
	})
	handler := server.Routes()
	cookies := registerTestAccount(t, server, "user@example.com", "TestPassword1")

	req := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/user/fs", nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("user fs roots status = %d body = %s", resp.Code, resp.Body.String())
	}
	var roots fsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&roots); err != nil {
		t.Fatalf("decode roots: %v", err)
	}
	if len(roots.Entries) != 0 {
		t.Fatalf("roots entries = %+v, want empty before account path is saved", roots.Entries)
	}

	if _, err := server.accountStore().SaveUserSettings("user@example.com", userSettingsUpdatePayload{
		AllowPaths: []string{inside},
		Commands:   map[string]adminCommandPayload{"pwd": {Enabled: true}},
	}, server.config.Snapshot().Policy); err != nil {
		t.Fatalf("save user settings: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/user/fs", nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("user fs roots after account path status = %d body = %s", resp.Code, resp.Body.String())
	}
	if err := json.NewDecoder(resp.Body).Decode(&roots); err != nil {
		t.Fatalf("decode roots after account path: %v", err)
	}
	if len(roots.Entries) != 1 || roots.Entries[0].Path != inside {
		t.Fatalf("roots entries = %+v, want only account path %s", roots.Entries, inside)
	}

	req = httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/user/fs?path="+urlQueryEscape(inside), nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("user fs inside status = %d body = %s", resp.Code, resp.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/user/fs?path="+urlQueryEscape(outside), nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("user fs outside status = %d body = %s", resp.Code, resp.Body.String())
	}
}

func TestAgentBindClearsAccountAllowPaths(t *testing.T) {
	t.Parallel()

	var putPayload userSettingsUpdatePayload
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/cloud-terminal-api/user/settings":
			writeJSON(w, http.StatusOK, userSettingsPayload{
				Account:            "user@example.com",
				CloudTunnelEnabled: true,
				Commands: map[string]adminCommandPayload{
					"pwd": {Enabled: true},
				},
				AllowPaths: []string{"/old/client/path"},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/cloud-terminal-api/user/settings":
			if err := json.NewDecoder(r.Body).Decode(&putPayload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusOK, userSettingsPayload{Account: "user@example.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cloud.Close()

	configPath := filepath.Join(t.TempDir(), "agent.yaml")
	cfg := config.Config{
		Server:      config.ServerConfig{Addr: "127.0.0.1:0"},
		CloudTunnel: config.CloudTunnelConfig{GatewayURL: cloud.URL},
		Policy:      minimalPolicy(),
	}
	server := NewServer(Options{
		Config:    config.NewStore(configPath, &cfg),
		AgentMode: true,
	})
	handler := server.AgentRoutes(cloud.URL)

	body := bytes.NewBufferString(`{"account":"user@example.com","session_id":"session-123"}`)
	req := httptest.NewRequest(http.MethodPost, "/cloud-terminal-api/agent/bind", body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("bind status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(putPayload.AllowPaths) != 0 {
		t.Fatalf("bind allow_paths = %v, want cleared", putPayload.AllowPaths)
	}

	putPayload.AllowPaths = []string{"/not-cleared"}
	req = httptest.NewRequest(http.MethodPost, "/cloud-terminal-api/agent/bind", bytes.NewBufferString(`{"clear":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("unbind status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(putPayload.AllowPaths) != 0 {
		t.Fatalf("unbind allow_paths = %v, want cleared", putPayload.AllowPaths)
	}
}

func TestAgentPolicyPersistsLocalConfigAndPublishesUpdate(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	allowed := filepath.Join(root, "workspace")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "agent.yaml")
	cfg := config.Config{
		Server: config.ServerConfig{Addr: "127.0.0.1:0"},
		Policy: policy.Config{
			Deny: []string{"old-deny"},
			Commands: map[string]policy.CommandPolicy{
				"pwd":   {Enabled: true},
				"codex": {Enabled: true, Interactive: true},
			},
		},
	}
	published := 0
	server := NewServer(Options{
		Config:    config.NewStore(configPath, &cfg),
		AgentMode: true,
		AgentPolicyUpdate: func() error {
			published++
			return nil
		},
	})
	handler := server.AgentRoutes("")

	body := bytes.NewBufferString(`{"deny":["sudo"],"allow_paths":["` + allowed + `"],"commands":{"pwd":{"enabled":false},"codex":{"enabled":true,"interactive":true}}}`)
	req := httptest.NewRequest(http.MethodPut, "/cloud-terminal-api/agent/policy", body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("agent policy status = %d body = %s", resp.Code, resp.Body.String())
	}
	snapshot := server.config.Snapshot()
	if len(snapshot.Policy.Deny) != 1 || snapshot.Policy.Deny[0] != "sudo" {
		t.Fatalf("local deny = %v, want sudo", snapshot.Policy.Deny)
	}
	if len(snapshot.Policy.AllowPaths) != 1 || snapshot.Policy.AllowPaths[0] != allowed {
		t.Fatalf("local allow paths = %v, want %s", snapshot.Policy.AllowPaths, allowed)
	}
	if snapshot.Policy.Commands["pwd"].Enabled {
		t.Fatalf("pwd command should be disabled in local policy: %+v", snapshot.Policy.Commands["pwd"])
	}
	if !snapshot.Policy.Commands["codex"].Enabled || !snapshot.Policy.Commands["codex"].Interactive {
		t.Fatalf("codex command = %+v, want enabled interactive", snapshot.Policy.Commands["codex"])
	}
	if published != 1 {
		t.Fatalf("publish count = %d, want 1", published)
	}
}

func TestLegacyAccountsMigrateToSQLiteOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "accounts.json")
	dbPath := filepath.Join(dir, "xmux.db")
	hash, err := hashPassword("TestPassword1")
	if err != nil {
		t.Fatal(err)
	}
	state := accountStoreFile{
		Version: 1,
		Accounts: map[string]accountRecord{
			"legacy@example.com": {
				Username:     "legacy@example.com",
				Role:         accountRoleUser,
				PasswordHash: hash,
				CreatedAt:    time.Now(),
			},
		},
		Sessions: map[string]accountSession{
			"legacy-session": {
				SessionID: "legacy-session",
				Username:  "legacy@example.com",
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			},
		},
	}
	content, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := newAccountStore(dbPath, legacyPath, true, "admin", "admin123456", minimalPolicy())
	if err != nil {
		t.Fatalf("new account store: %v", err)
	}
	if !store.VerifyPassword("legacy@example.com", "TestPassword1") {
		t.Fatal("expected migrated legacy account to verify")
	}
	if !store.VerifySession("legacy-session", "legacy@example.com") {
		t.Fatal("expected valid legacy session to migrate")
	}

	store, err = newAccountStore(dbPath, legacyPath, true, "admin", "admin123456", minimalPolicy())
	if err != nil {
		t.Fatalf("reopen account store: %v", err)
	}
	var legacyCount int
	for _, account := range store.List() {
		if account.Username == "legacy@example.com" {
			legacyCount++
		}
	}
	if legacyCount != 1 {
		t.Fatalf("legacy account count = %d, want 1", legacyCount)
	}
}

func TestAdminIPAllowlist(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{Server: config.ServerConfig{
		AdminIPAllowlist: []string{"127.0.0.1"},
	}})

	allowed := &http.Request{RemoteAddr: "127.0.0.1:1234"}
	if !server.adminIPAllowed(allowed) {
		t.Fatal("expected loopback admin IP to be allowed")
	}
	blocked := &http.Request{RemoteAddr: "10.0.0.1:1234"}
	if server.adminIPAllowed(blocked) {
		t.Fatal("expected non-allowlisted admin IP to be blocked")
	}
}

func TestCommandCompletionsOnlyReturnEnabledAllowedCommands(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{
		Policy: policy.Config{
			Deny: []string{"rm"},
			Commands: map[string]policy.CommandPolicy{
				"pwd":    {Enabled: true},
				"ls":     {Enabled: true},
				"docker": {Enabled: false},
				"rm":     {Enabled: true},
			},
		},
	})

	got := server.commandCompletions()
	want := []string{"ls", "pwd"}
	if len(got) != len(want) {
		t.Fatalf("completions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("completions = %v, want %v", got, want)
		}
	}
}

func TestPathCompletionsStayInsideAllowedPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "audit.log"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "secret.log"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := testServer(t, config.Config{
		Edge: config.EdgeConfig{WorkDir: root},
		Policy: policy.Config{
			AllowPaths: []string{root},
			Commands:   map[string]policy.CommandPolicy{"pwd": {Enabled: true}},
		},
	})

	matches, err := server.pathCompletions("a", root)
	if err != nil {
		t.Fatalf("pathCompletions: %v", err)
	}
	if len(matches) != 2 || matches[0] != "app/" || matches[1] != "audit.log" {
		t.Fatalf("matches = %v", matches)
	}

	matches, err = server.pathCompletions(filepath.Join(other, "s"), root)
	if err != nil {
		t.Fatalf("pathCompletions outside root: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("outside matches = %v, want none", matches)
	}
}

func testServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	return testServerWithPath(t, t.TempDir()+"/policy.yaml", cfg)
}

func testServerWithPath(t *testing.T, configPath string, cfg config.Config) *Server {
	t.Helper()
	if cfg.Server.DatabasePath == "" {
		cfg.Server.DatabasePath = filepath.Join(t.TempDir(), "xmux.db")
	}
	if cfg.Server.AccountStorePath == "" {
		cfg.Server.AccountStorePath = filepath.Join(t.TempDir(), "accounts.json")
	}
	store := config.NewStore(configPath, &cfg)
	runtime := edge.NewRuntime(edge.Options{
		PolicyProvider: store,
		DefaultEnv:     cfg.Edge.Env,
		DefaultDir:     cfg.Edge.WorkDir,
		CommandTimeout: cfg.Edge.CommandTimeout.Duration,
		MaxOutputSize:  cfg.Edge.MaxOutputBytes,
	})
	return NewServer(Options{Config: store, Runtime: NewLocalRuntime(runtime)})
}

func loginTestAccount(t *testing.T, server *Server, username string, password string) []*http.Cookie {
	t.Helper()
	handler := server.Routes()
	body, err := json.Marshal(accountAuthPayload{Username: username, Password: password})
	if err != nil {
		t.Fatalf("marshal account payload: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/cloud-terminal-api/accounts/login", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d body = %s", response.Code, response.Body.String())
	}
	return response.Result().Cookies()
}

func registerTestAccount(t *testing.T, server *Server, username string, password string) []*http.Cookie {
	t.Helper()
	if server != nil && server.appSettings != nil {
		// Tests stay on the pre-Stage 2 flow (no email required, no verify gate)
		// so the suite doesn't need to mock SMTP. Production deployments still
		// see the strict defaults from defaultAuthSettings().
		_ = server.appSettings.Set(appSettingKeyAuthSettings, authSettings{
			RequireEmailOnRegister:      false,
			RequireEmailVerifiedToLogin: false,
		}, "test-suite")
	}
	handler := server.Routes()
	body, err := json.Marshal(accountAuthPayload{Username: username, Password: password})
	if err != nil {
		t.Fatalf("marshal account payload: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/cloud-terminal-api/accounts/register", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("register status = %d body = %s", response.Code, response.Body.String())
	}
	return response.Result().Cookies()
}

func minimalPolicy() policy.Config {
	return policy.Config{Commands: map[string]policy.CommandPolicy{"pwd": {Enabled: true}}}
}

func testBoolPtr(value bool) *bool {
	return &value
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(value)
}
