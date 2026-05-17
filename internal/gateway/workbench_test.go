package gateway

import (
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cloud-terminal/internal/config"
	"cloud-terminal/internal/policy"
)

func TestWorkbenchAuthorizedUsesCookie(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{
		Policy: minimalPolicy(),
	})
	cookies := registerTestAccount(t, server, "mobile@example.com", "secret123")
	request := &http.Request{Header: http.Header{}, URL: &url.URL{}}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}

	if !server.workbenchAuthorized(request) {
		t.Fatal("expected account session cookie to authorize")
	}
}

func TestWorkbenchPreviewPortAllowlist(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{
		Edge:   config.EdgeConfig{PreviewPorts: []int{3000, 5173}},
		Policy: minimalPolicy(),
	})

	if !server.previewPortAllowed(3000) {
		t.Fatal("expected configured preview port to be allowed")
	}
	if server.previewPortAllowed(22) {
		t.Fatal("expected non-configured preview port to be rejected")
	}
}

func TestWorkbenchSessionReplayIsBounded(t *testing.T) {
	t.Parallel()

	session := &workbenchSession{
		id:          "session",
		edgeID:      "edge",
		edgeName:    "Edge",
		agent:       "codex",
		startedAt:   time.Now(),
		lastActive:  time.Now(),
		running:     true,
		attachments: map[uint64]func(workbenchServerMessage){},
	}

	session.appendOutput(make([]byte, workbenchReplayMax+128))
	snapshot := session.snapshot()

	if len(snapshot.Replay) != workbenchReplayMax {
		t.Fatalf("replay length = %d, want %d", len(snapshot.Replay), workbenchReplayMax)
	}
}

func TestWorkbenchSessionsPersistToDisk(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "workbench_sessions.json")
	startedAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	lastActive := time.Now().Truncate(time.Second)

	manager := &workbenchManager{
		edgeID:    "edge",
		edgeName:  "Edge",
		statePath: statePath,
		logger:    slog.Default(),
		sessions: map[string]*workbenchSession{
			"session-1": {
				id:          "session-1",
				requestID:   "request-1",
				edgeID:      "edge",
				edgeName:    "Edge",
				agent:       "codex",
				workDir:     "/tmp/project",
				startedAt:   startedAt,
				lastActive:  lastActive,
				running:     false,
				exitCode:    0,
				duration:    "1s",
				replay:      []byte("──ok\n"),
				attachments: map[uint64]func(workbenchServerMessage){},
			},
		},
	}
	if err := manager.saveState(); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	loaded := &workbenchManager{
		edgeID:    "edge",
		edgeName:  "Edge",
		statePath: statePath,
		logger:    slog.Default(),
		sessions:  map[string]*workbenchSession{},
	}
	if err := loaded.loadState(); err != nil {
		t.Fatalf("loadState: %v", err)
	}

	session := loaded.sessions["session-1"]
	if session == nil {
		t.Fatal("expected session to be loaded")
	}
	snapshot := session.snapshot()
	if snapshot.Agent != "codex" || snapshot.WorkDir != "/tmp/project" || snapshot.Replay != "──ok\n" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Running {
		t.Fatal("persisted sessions should load as non-running")
	}
}

func TestWorkbenchStateIncludesPreviewPorts(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{
		Edge:   config.EdgeConfig{PreviewPorts: []int{3000, 8080}},
		Policy: policy.Config{AllowPaths: []string{"/tmp"}, Commands: map[string]policy.CommandPolicy{"pwd": {Enabled: true}}},
	})

	state := server.workbenchStatePayload("")
	if len(state.PreviewPorts) != 2 || state.PreviewPorts[0] != 3000 || state.PreviewPorts[1] != 8080 {
		t.Fatalf("PreviewPorts = %v", state.PreviewPorts)
	}
}

func TestWorkbenchStateIncludesAgents(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{
		Policy: policy.Config{
			Deny: []string{"gemini"},
			Commands: map[string]policy.CommandPolicy{
				"codex":  {Enabled: true, Interactive: true},
				"claude": {Enabled: true, Interactive: true},
				"gemini": {Enabled: true, Interactive: true},
			},
		},
	})

	state := server.workbenchStatePayload("")
	if len(state.Agents) != 3 {
		t.Fatalf("Agents length = %d, want 3", len(state.Agents))
	}
	if state.Agents[0].ID != "codex" || !state.Agents[0].Enabled {
		t.Fatalf("codex agent = %+v, want enabled codex", state.Agents[0])
	}
	if state.Agents[1].ID != "claude" || !state.Agents[1].Enabled {
		t.Fatalf("claude agent = %+v, want enabled claude", state.Agents[1])
	}
	if state.Agents[2].ID != "gemini" || state.Agents[2].Enabled {
		t.Fatalf("gemini agent = %+v, want disabled gemini", state.Agents[2])
	}
}

func TestTunnelWorkbenchStateIsFilteredByUserPolicy(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	denied := filepath.Join(root, "denied")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(denied, 0o755); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Options{
		Config: config.NewStore(filepath.Join(t.TempDir(), "policy.yaml"), &config.Config{
			Server: config.ServerConfig{
				DatabasePath:               filepath.Join(t.TempDir(), "xmux.db"),
				AccountRegistrationEnabled: testBoolPtr(true),
			},
			Policy: policy.Config{
				AllowPaths: []string{root},
				Commands: map[string]policy.CommandPolicy{
					"codex":  {Enabled: true, Interactive: true},
					"claude": {Enabled: true, Interactive: true},
				},
			},
		}),
	})
	registerTestAccount(t, server, "user@example.com", "secret123")
	if _, err := server.accountStore().SaveUserSettings("user@example.com", userSettingsUpdatePayload{
		CloudTunnelEnabled: true,
		AllowPaths:         []string{allowed},
		Commands: map[string]adminCommandPayload{
			"codex":  {Enabled: true, Interactive: true},
			"claude": {Enabled: false, Interactive: true},
		},
	}, server.config.Snapshot().Policy); err != nil {
		t.Fatalf("save user settings: %v", err)
	}
	server.tunnel.set(&tunnelClient{
		hub:          server.tunnel,
		account:      "user@example.com",
		edgeID:       "edge-user",
		edgeName:     "User Edge",
		workDir:      denied,
		allowPaths:   []string{allowed, denied},
		previewPorts: []int{3000},
		agents: []workbenchAgentInfo{
			{ID: "codex", Label: "Codex", Command: "codex", Enabled: true},
			{ID: "claude", Label: "Claude Code", Command: "claude", Enabled: true},
		},
		pending:     make(map[string]chan tunnelEnvelope),
		exitWaiters: make(map[string]chan workbenchServerMessage),
	})

	state := server.workbenchStatePayload("user@example.com")
	if state.WorkDir != allowed {
		t.Fatalf("WorkDir = %q, want %q", state.WorkDir, allowed)
	}
	if len(state.AllowPaths) != 1 || state.AllowPaths[0] != allowed {
		t.Fatalf("AllowPaths = %v, want %s", state.AllowPaths, allowed)
	}
	for _, agent := range state.Agents {
		if agent.ID == "claude" && agent.Enabled {
			t.Fatalf("claude should be disabled by user policy: %+v", state.Agents)
		}
	}
}

func TestTunnelResolveStartUsesUserPolicy(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	denied := filepath.Join(root, "denied")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(denied, 0o755); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Options{
		Config: config.NewStore(filepath.Join(t.TempDir(), "policy.yaml"), &config.Config{
			Server: config.ServerConfig{
				DatabasePath:               filepath.Join(t.TempDir(), "xmux.db"),
				AccountRegistrationEnabled: testBoolPtr(true),
			},
			Policy: policy.Config{
				AllowPaths: []string{root},
				Commands: map[string]policy.CommandPolicy{
					"codex":  {Enabled: true, Bin: "/usr/local/bin/codex", Interactive: true},
					"claude": {Enabled: true, Interactive: true},
				},
			},
		}),
	})
	registerTestAccount(t, server, "user@example.com", "secret123")
	if _, err := server.accountStore().SaveUserSettings("user@example.com", userSettingsUpdatePayload{
		CloudTunnelEnabled: true,
		AllowPaths:         []string{allowed},
		Commands: map[string]adminCommandPayload{
			"codex":  {Enabled: true, Bin: "/usr/local/bin/codex", Interactive: true},
			"claude": {Enabled: false, Interactive: true},
		},
	}, server.config.Snapshot().Policy); err != nil {
		t.Fatalf("save user settings: %v", err)
	}
	server.tunnel.set(&tunnelClient{
		hub:        server.tunnel,
		account:    "user@example.com",
		edgeID:     "edge-user",
		edgeName:   "User Edge",
		workDir:    denied,
		allowPaths: []string{allowed, denied},
		agents: []workbenchAgentInfo{
			{ID: "codex", Label: "Codex", Command: "codex", Enabled: true},
			{ID: "claude", Label: "Claude Code", Command: "claude", Enabled: true},
		},
		pending:     make(map[string]chan tunnelEnvelope),
		exitWaiters: make(map[string]chan workbenchServerMessage),
	})
	runtime := server.runtime.(*tunnelRuntime)

	resolved, err := runtime.ResolveWorkbenchStart(workbenchStartOptions{
		Account: "user@example.com",
		Agent:   "codex",
	})
	if err != nil {
		t.Fatalf("ResolveWorkbenchStart codex: %v", err)
	}
	if resolved.WorkDir != allowed {
		t.Fatalf("resolved work dir = %q, want %q", resolved.WorkDir, allowed)
	}
	if resolved.Agent.Command != "codex" {
		t.Fatalf("resolved command = %q, want agent id", resolved.Agent.Command)
	}
	decision, err := server.config.UserPolicyEngine("user@example.com", server.accountStore())
	if err != nil {
		t.Fatalf("user policy engine: %v", err)
	}
	interactive, err := decision.Decide("codex", []string{"/usr/local/bin/codex"})
	if err != nil {
		t.Fatalf("Decide codex override: %v", err)
	}
	if interactive.Bin != "/usr/local/bin/codex" {
		t.Fatalf("interactive bin = %q, want user policy bin", interactive.Bin)
	}
	if _, err := runtime.ResolveWorkbenchStart(workbenchStartOptions{
		Account: "user@example.com",
		Agent:   "claude",
		WorkDir: allowed,
	}); err == nil {
		t.Fatal("expected user-disabled claude to be rejected")
	}
	if _, err := runtime.ResolveWorkbenchStart(workbenchStartOptions{
		Account: "user@example.com",
		Agent:   "codex",
		WorkDir: denied,
	}); err == nil {
		t.Fatal("expected denied path to be rejected")
	}
}

func TestResolveWorkbenchAgentRequiresInteractivePolicy(t *testing.T) {
	t.Parallel()

	_, err := resolveWorkbenchAgent(config.Config{
		Policy: policy.Config{Commands: map[string]policy.CommandPolicy{
			"claude": {Enabled: true, Interactive: false},
		}},
	}, "claude")
	if err == nil {
		t.Fatal("expected non-interactive claude policy to be rejected")
	}

	agent, err := resolveWorkbenchAgent(config.Config{
		Policy: policy.Config{Commands: map[string]policy.CommandPolicy{
			"gemini": {Enabled: true, Interactive: true},
		}},
	}, "gemini")
	if err != nil {
		t.Fatalf("resolveWorkbenchAgent gemini: %v", err)
	}
	if agent.ID != "gemini" || agent.Command != "gemini" {
		t.Fatalf("agent = %+v, want gemini", agent)
	}
}

func TestResolveWorkbenchTargetFileStartsInParentDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	workDir, args, err := resolveWorkbenchTarget(config.Config{
		Policy: policy.Config{AllowPaths: []string{root}},
	}, "", file)
	if err != nil {
		t.Fatalf("resolveWorkbenchTarget: %v", err)
	}
	if workDir != root {
		t.Fatalf("workDir = %q, want %q", workDir, root)
	}
	if len(args) != 1 || args[0] != "main.go" {
		t.Fatalf("args = %v, want [main.go]", args)
	}
}

func TestResolveWorkbenchTargetRejectsOutsideAllowedPaths(t *testing.T) {
	t.Parallel()

	allowed := t.TempDir()
	other := t.TempDir()

	_, _, err := resolveWorkbenchTarget(config.Config{
		Policy: policy.Config{AllowPaths: []string{allowed}},
	}, other, "")
	if err == nil {
		t.Fatal("expected outside work_dir to be rejected")
	}
}

func TestWarmupWorkbenchTreeSkipsHeavyDirs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "pkg", "index.js"), []byte("module.exports = {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := warmupWorkbenchTree(t.Context(), root, []string{root})
	if result.Dirs != 2 {
		t.Fatalf("Dirs = %d, want 2", result.Dirs)
	}
	if result.Files != 1 {
		t.Fatalf("Files = %d, want 1", result.Files)
	}
	if result.Skipped == 0 {
		t.Fatal("expected skipped heavy directory")
	}
}
