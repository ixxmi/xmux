package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"cloud-terminal/internal/config"
	"cloud-terminal/internal/edge"
	"cloud-terminal/internal/policy"
)

func TestRoutesUseCloudTerminalPrefixes(t *testing.T) {
	t.Parallel()

	server := NewServer(Options{
		Config:   config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{Server: config.ServerConfig{AuthToken: "token", AdminToken: "admin"}, Policy: minimalPolicy()}),
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
		Config:   config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{Server: config.ServerConfig{AuthToken: "token", AdminToken: "admin"}, Policy: minimalPolicy()}),
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

	server := testServer(t, config.Config{Server: config.ServerConfig{AuthToken: "token", AdminToken: "admin"}})
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

	server := testServer(t, config.Config{Server: config.ServerConfig{AuthToken: "token", AdminToken: "admin"}})
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
		AuthToken:  "token",
		AdminToken: "admin",
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

	server := testServer(t, config.Config{Server: config.ServerConfig{AuthToken: "token", AdminToken: "admin"}})
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

func TestTerminalTokenReadsLiveConfig(t *testing.T) {
	t.Parallel()

	store := config.NewStore(t.TempDir()+"/policy.yaml", &config.Config{
		Server: config.ServerConfig{AuthToken: "old", AdminToken: "admin"},
		Policy: minimalPolicy(),
	})
	server := NewServer(Options{Config: store})

	request := &http.Request{Header: http.Header{"Authorization": []string{"Bearer old"}}}
	if !server.authorized(request) {
		t.Fatal("expected old token to be accepted before update")
	}

	next := store.Snapshot()
	next.Server.AuthToken = "new"
	if err := store.Update(next); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if server.authorized(request) {
		t.Fatal("expected old token to be rejected after update")
	}
	request.Header.Set("Authorization", "Bearer new")
	if !server.authorized(request) {
		t.Fatal("expected new token to be accepted after update")
	}
}

func TestAdminIPAllowlist(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{Server: config.ServerConfig{
		AuthToken:        "token",
		AdminToken:       "admin",
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
		Server: config.ServerConfig{AuthToken: "token", AdminToken: "admin"},
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
		Server: config.ServerConfig{AuthToken: "token", AdminToken: "admin"},
		Edge:   config.EdgeConfig{WorkDir: root},
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
	store := config.NewStore(t.TempDir()+"/policy.yaml", &cfg)
	runtime := edge.NewRuntime(edge.Options{
		PolicyProvider: store,
		DefaultEnv:     cfg.Edge.Env,
		DefaultDir:     cfg.Edge.WorkDir,
		CommandTimeout: cfg.Edge.CommandTimeout.Duration,
		MaxOutputSize:  cfg.Edge.MaxOutputBytes,
	})
	return NewServer(Options{Config: store, Runtime: NewLocalRuntime(runtime)})
}

func minimalPolicy() policy.Config {
	return policy.Config{Commands: map[string]policy.CommandPolicy{"pwd": {Enabled: true}}}
}
