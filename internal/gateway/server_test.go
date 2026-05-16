package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"cloud-terminal/internal/config"
	"cloud-terminal/internal/edge"
	"cloud-terminal/internal/policy"
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
	cookies := registerTestAccount(t, server, "dev@example.com", "secret123")
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

	registerBody := bytes.NewBufferString(`{"username":"dev@example.com","password":"secret123"}`)
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

	loginBody := bytes.NewBufferString(`{"username":"dev@example.com","password":"secret123"}`)
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
	cookies := registerTestAccount(t, server, "dev@example.com", "secret123")

	body := bytes.NewBufferString(`{"current_password":"wrong","new_password":"changed123"}`)
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

	body = bytes.NewBufferString(`{"current_password":"secret123","new_password":"changed123"}`)
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
	if !server.accountStore().VerifyPassword("dev@example.com", "changed123") {
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
	createBody := bytes.NewBufferString(`{"username":"managed@example.com","password":"secret123","role":"user"}`)
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
	userCookies := registerTestAccount(t, server, "user@example.com", "secret123")

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

	cookies := registerTestAccount(t, server, "user@example.com", "secret123")
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

	userCookies := registerTestAccount(t, server, "user@example.com", "secret123")
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
	userCookies := registerTestAccount(t, server, "user@example.com", "secret123")

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
	if len(settings.AllowPaths) != 1 || settings.AllowPaths[0] != root {
		t.Fatalf("settings allow_paths = %v, want %s", settings.AllowPaths, root)
	}

	invalidBody := bytes.NewBufferString(`{"allow_paths":["/definitely-outside"],"commands":{"pwd":{"enabled":true}}}`)
	invalidReq := httptest.NewRequest(http.MethodPut, "/cloud-terminal-api/user/settings", invalidBody)
	invalidReq.Header.Set("Content-Type", "application/json")
	for _, cookie := range userCookies {
		invalidReq.AddCookie(cookie)
	}
	invalidResp := httptest.NewRecorder()
	handler.ServeHTTP(invalidResp, invalidReq)
	if invalidResp.Code != http.StatusBadRequest {
		t.Fatalf("invalid settings status = %d body = %s", invalidResp.Code, invalidResp.Body.String())
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
}

func TestUserFSOnlyListsGlobalAllowedRoots(t *testing.T) {
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
	cookies := registerTestAccount(t, server, "user@example.com", "secret123")

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
	if len(roots.Entries) != 1 || roots.Entries[0].Path != root {
		t.Fatalf("roots entries = %+v, want only %s", roots.Entries, root)
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

func TestLegacyAccountsMigrateToSQLiteOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "accounts.json")
	dbPath := filepath.Join(dir, "xmux.db")
	hash, err := hashPassword("secret123")
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
	if !store.VerifyPassword("legacy@example.com", "secret123") {
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
