package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloud-terminal/internal/config"
	"cloud-terminal/internal/edge"
	"cloud-terminal/internal/policy"
)

type tunnelRoundTripConn struct {
	handler func(tunnelEnvelope) tunnelEnvelope
	writes  chan tunnelEnvelope
}

func newTunnelRoundTripConn(handler func(tunnelEnvelope) tunnelEnvelope) *tunnelRoundTripConn {
	return &tunnelRoundTripConn{handler: handler, writes: make(chan tunnelEnvelope, 8)}
}

func (c *tunnelRoundTripConn) WriteJSON(env any) error {
	req := env.(tunnelEnvelope)
	response := c.handler(req)
	if response.Type == "" {
		response.Type = "response"
	}
	if response.ID == "" {
		response.ID = req.ID
	}
	c.writes <- response
	return nil
}

func (c *tunnelRoundTripConn) Close() error {
	close(c.writes)
	return nil
}

func (c *tunnelRoundTripConn) SetWriteDeadline(time.Time) error {
	return nil
}

func (c *tunnelRoundTripConn) ReadJSON(any) error {
	<-make(chan struct{})
	return nil
}

func TestWorkbenchAuthorizedUsesCookie(t *testing.T) {
	t.Parallel()

	server := testServer(t, config.Config{
		Policy: minimalPolicy(),
	})
	cookies := registerTestAccount(t, server, "mobile@example.com", "TestPassword1")
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

func TestWorkbenchHistoricalReplayForAttachIsTailBounded(t *testing.T) {
	t.Parallel()

	replay := strings.Repeat("a", workbenchHistoricalReplayMax+128)
	got := replayForAttach(workbenchSnapshot{
		Running: false,
		Replay:  replay,
	})

	if !strings.Contains(got, "showing last 128 KiB") {
		t.Fatalf("historical replay marker missing: %q", got[:64])
	}
	if !strings.HasSuffix(got, strings.Repeat("a", workbenchHistoricalReplayMax)) {
		t.Fatal("historical replay should include the tail of saved output")
	}
	if len(got) >= len(replay) {
		t.Fatalf("historical replay length = %d, want less than original %d", len(got), len(replay))
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
				submitted:   true,
				title:       "fix bug",
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
	if snapshot.Agent != "codex" || snapshot.WorkDir != "/tmp/project" || snapshot.Replay != "──ok\n" || snapshot.Title != "fix bug" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Running {
		t.Fatal("persisted sessions should load as non-running")
	}
}

func TestWorkbenchLoadStateClearsRestartUnavailableError(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "workbench_sessions.json")
	startedAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	state := workbenchPersistedState{
		Version: 1,
		Sessions: []workbenchPersistedSession{
			{
				ID:         "session-1",
				Agent:      "codex",
				WorkDir:    "/tmp/project",
				StartedAt:  startedAt,
				LastActive: startedAt,
				Submitted:  true,
				Running:    true,
				Error:      workbenchRestartUnavailableError,
				Replay:     "previous output\n",
			},
		},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}

	manager := &workbenchManager{
		edgeID:    "edge",
		edgeName:  "Edge",
		statePath: statePath,
		logger:    slog.Default(),
		sessions:  map[string]*workbenchSession{},
	}
	if err := manager.loadState(); err != nil {
		t.Fatalf("loadState: %v", err)
	}

	snapshot := manager.sessions["session-1"].snapshot()
	if snapshot.Running {
		t.Fatal("restarted persisted session should load as historical")
	}
	if snapshot.Error != "" {
		t.Fatalf("snapshot error = %q, want cleared restart placeholder", snapshot.Error)
	}
}

func TestWorkbenchSessionsIgnoreUnsubmittedSessions(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "workbench_sessions.json")
	submittedAt := time.Now().Truncate(time.Second)
	manager := &workbenchManager{
		edgeID:    "edge",
		edgeName:  "Edge",
		statePath: statePath,
		logger:    slog.Default(),
		sessions: map[string]*workbenchSession{
			"submitted": {
				id:          "submitted",
				edgeID:      "edge",
				edgeName:    "Edge",
				agent:       "codex",
				workDir:     "/tmp/project",
				startedAt:   submittedAt,
				lastActive:  submittedAt,
				submitted:   true,
				title:       "implement task",
				running:     true,
				attachments: map[uint64]func(workbenchServerMessage){},
			},
			"empty": {
				id:          "empty",
				edgeID:      "edge",
				edgeName:    "Edge",
				agent:       "codex",
				workDir:     "/tmp/project",
				startedAt:   submittedAt,
				lastActive:  submittedAt,
				running:     true,
				attachments: map[uint64]func(workbenchServerMessage){},
			},
		},
	}

	listed := manager.list("", []string{"/tmp"}, true)
	if len(listed) != 1 || listed[0].ID != "submitted" || listed[0].Title != "implement task" {
		t.Fatalf("listed sessions = %+v, want only submitted session", listed)
	}
	if err := manager.saveState(); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	var persisted workbenchPersistedState
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read persisted state: %v", err)
	}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("decode persisted state: %v", err)
	}
	if len(persisted.Sessions) != 1 || persisted.Sessions[0].ID != "submitted" {
		t.Fatalf("persisted sessions = %+v, want only submitted session", persisted.Sessions)
	}
}

func TestWorkbenchGetOrCreateReturnsHistoricalSessionReadOnly(t *testing.T) {
	t.Parallel()

	session := &workbenchSession{
		id:          "history",
		account:     "user@example.com",
		edgeID:      "edge",
		edgeName:    "Edge",
		agent:       "codex",
		workDir:     "/tmp/project",
		startedAt:   time.Now().Add(-time.Minute),
		lastActive:  time.Now(),
		submitted:   true,
		title:       "old task",
		running:     false,
		replay:      []byte("previous output\n"),
		attachments: map[uint64]func(workbenchServerMessage){},
	}
	manager := &workbenchManager{
		runtime:  historicalRuntime{},
		logger:   slog.Default(),
		sessions: map[string]*workbenchSession{"history": session},
	}

	got, created, err := manager.getOrCreate(workbenchStartOptions{
		SessionID: "history",
		Account:   "user@example.com",
		Rows:      24,
		Cols:      80,
	})
	if err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}
	if created {
		t.Fatal("historical session should not create a new process")
	}
	if got != session {
		t.Fatal("expected existing historical session")
	}
	snap := got.snapshot()
	if snap.Running {
		t.Fatalf("snapshot running = true, want false")
	}
	if err := got.Write([]byte("input")); !errors.Is(err, errWorkbenchSessionNotRunning) {
		t.Fatalf("historical Write error = %v, want %v", err, errWorkbenchSessionNotRunning)
	}
	if err := got.Resize(24, 80); !errors.Is(err, errWorkbenchSessionNotRunning) {
		t.Fatalf("historical Resize error = %v, want %v", err, errWorkbenchSessionNotRunning)
	}
}

func TestWorkbenchGetOrCreateResumesExistingTunnelSession(t *testing.T) {
	t.Parallel()

	done := make(chan edge.ExecResult, 1)
	runtime := &resumeRuntime{session: &stubInteractiveSession{done: done}}
	session := &workbenchSession{
		id:          "session-1",
		requestID:   "request-1",
		account:     "user@example.com",
		edgeID:      "edge",
		edgeName:    "Edge",
		agent:       "codex",
		workDir:     "/tmp/project",
		startedAt:   time.Now().Add(-time.Minute),
		lastActive:  time.Now(),
		submitted:   true,
		title:       "resume task",
		running:     false,
		errText:     "session unavailable after service restart",
		attachments: map[uint64]func(workbenchServerMessage){},
	}
	manager := &workbenchManager{
		runtime:  runtime,
		logger:   slog.Default(),
		sessions: map[string]*workbenchSession{"session-1": session},
	}

	got, created, err := manager.getOrCreate(workbenchStartOptions{
		SessionID: "session-1",
		Account:   "user@example.com",
		Rows:      30,
		Cols:      100,
	})
	if err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}
	if created {
		t.Fatal("resume should reuse existing session record")
	}
	if got != session {
		t.Fatal("expected resumed existing session")
	}
	if runtime.resumeCalls != 1 {
		t.Fatalf("resume calls = %d, want 1", runtime.resumeCalls)
	}
	snap := got.snapshot()
	if !snap.Running || snap.Error != "" {
		t.Fatalf("snapshot = %+v, want running without restart error", snap)
	}
	if err := got.Write([]byte("hello")); err != nil {
		t.Fatalf("write resumed session: %v", err)
	}
	if string(runtime.session.writes[0]) != "hello" {
		t.Fatalf("writes = %q, want hello", string(runtime.session.writes[0]))
	}

	done <- edge.ExecResult{RequestID: "request-1", ExitCode: 0, Duration: "1ms"}
}

func TestWorkbenchSessionCapturesFirstSubmittedTitle(t *testing.T) {
	t.Parallel()

	session := &workbenchSession{
		id:          "session",
		agent:       "codex",
		workDir:     "/tmp/project",
		startedAt:   time.Now(),
		lastActive:  time.Now(),
		running:     true,
		attachments: map[uint64]func(workbenchServerMessage){},
	}
	if _, ok := session.captureSubmitTitle("fix"); ok {
		t.Fatal("partial input should not submit")
	}
	if title, ok := session.captureSubmitTitle(" tests\r"); !ok || title != "fix tests" {
		t.Fatalf("submitted title = %q, %v; want fix tests, true", title, ok)
	}
	if title, ok := session.captureSubmitTitle("another\r"); ok || title != "" {
		t.Fatalf("second submit = %q, %v; want ignored", title, ok)
	}
	snap := session.snapshot()
	if !snap.Submitted || snap.Title != "fix tests" {
		t.Fatalf("snapshot = %+v, want submitted title", snap)
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
	registerTestAccount(t, server, "user@example.com", "TestPassword1")
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
	if len(state.EdgePaths) != 1 || state.EdgePaths[0] != allowed {
		t.Fatalf("EdgePaths = %v, want %s", state.EdgePaths, allowed)
	}
	for _, agent := range state.Agents {
		if agent.ID == "claude" && agent.Enabled {
			t.Fatalf("claude should be disabled by user policy: %+v", state.Agents)
		}
	}
}

func TestTunnelWorkbenchStateShowsAccountPathsEvenBeforeAgentPolicySync(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	accountPath := filepath.Join(root, "account")
	agentPath := filepath.Join(root, "agent")
	if err := os.MkdirAll(accountPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agentPath, 0o755); err != nil {
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
				Commands:   map[string]policy.CommandPolicy{"codex": {Enabled: true, Interactive: true}},
			},
		}),
	})
	registerTestAccount(t, server, "user@example.com", "TestPassword1")
	if _, err := server.accountStore().SaveUserSettings("user@example.com", userSettingsUpdatePayload{
		CloudTunnelEnabled: true,
		AllowPaths:         []string{accountPath},
		Commands:           map[string]adminCommandPayload{"codex": {Enabled: true, Interactive: true}},
	}, server.config.Snapshot().Policy); err != nil {
		t.Fatalf("save user settings: %v", err)
	}
	server.tunnel.set(&tunnelClient{
		hub:        server.tunnel,
		account:    "user@example.com",
		edgeID:     "edge-user",
		edgeName:   "User Edge",
		workDir:    agentPath,
		allowPaths: []string{agentPath},
		agents:     []workbenchAgentInfo{{ID: "codex", Label: "Codex", Command: "codex", Enabled: true}},
		pending:    make(map[string]chan tunnelEnvelope),
	})

	state := server.workbenchStatePayload("user@example.com")
	if len(state.AllowPaths) != 1 || state.AllowPaths[0] != accountPath {
		t.Fatalf("AllowPaths = %v, want account path %s", state.AllowPaths, accountPath)
	}
	if len(state.EdgePaths) != 0 {
		t.Fatalf("EdgePaths = %v, want empty until local agent syncs account path", state.EdgePaths)
	}
	if state.WorkDir != "" {
		t.Fatalf("WorkDir = %q, want empty until local agent syncs account path", state.WorkDir)
	}
}

func TestTunnelWorkbenchStateShowsAccountPathsWhenAgentOffline(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	accountPath := filepath.Join(root, "account")
	if err := os.MkdirAll(accountPath, 0o755); err != nil {
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
				Commands:   map[string]policy.CommandPolicy{"codex": {Enabled: true, Interactive: true}},
			},
		}),
	})
	registerTestAccount(t, server, "user@example.com", "TestPassword1")
	if _, err := server.accountStore().SaveUserSettings("user@example.com", userSettingsUpdatePayload{
		CloudTunnelEnabled: true,
		AllowPaths:         []string{accountPath},
		Commands:           map[string]adminCommandPayload{"codex": {Enabled: true, Interactive: true}},
	}, server.config.Snapshot().Policy); err != nil {
		t.Fatalf("save user settings: %v", err)
	}

	state := server.workbenchStatePayload("user@example.com")
	if state.EdgeOnline {
		t.Fatal("EdgeOnline = true, want false")
	}
	if len(state.AllowPaths) != 1 || state.AllowPaths[0] != accountPath {
		t.Fatalf("AllowPaths = %v, want account path %s", state.AllowPaths, accountPath)
	}
	if len(state.EdgePaths) != 0 {
		t.Fatalf("EdgePaths = %v, want empty while agent is offline", state.EdgePaths)
	}
}

func TestTunnelHubDoesNotFallbackForSpecificAccount(t *testing.T) {
	t.Parallel()

	hub := newTunnelHub(slog.Default())
	hub.setDefaultAccount("admin")
	admin := &tunnelClient{hub: hub, account: "admin", edgeID: "edge-admin"}
	hub.set(admin)

	if got := hub.currentForAccount("admin"); got != admin {
		t.Fatalf("admin client = %v, want admin client", got)
	}
	if got := hub.currentForAccount(""); got != admin {
		t.Fatalf("default client = %v, want admin client", got)
	}
	if got := hub.currentForAccount("user@example.com"); got != nil {
		t.Fatalf("user client = %+v, want nil instead of default admin client", got.info())
	}
}

func TestTunnelWorkbenchFilesUsesAccountAllowPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	accountPath := filepath.Join(root, "account")
	agentPath := filepath.Join(root, "agent")
	if err := os.MkdirAll(filepath.Join(accountPath, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agentPath, 0o755); err != nil {
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
				Commands:   map[string]policy.CommandPolicy{"codex": {Enabled: true, Interactive: true}},
			},
		}),
	})
	cookies := registerTestAccount(t, server, "user@example.com", "TestPassword1")
	if _, err := server.accountStore().SaveUserSettings("user@example.com", userSettingsUpdatePayload{
		CloudTunnelEnabled: true,
		AllowPaths:         []string{accountPath},
		Commands:           map[string]adminCommandPayload{"codex": {Enabled: true, Interactive: true}},
	}, server.config.Snapshot().Policy); err != nil {
		t.Fatalf("save user settings: %v", err)
	}
	conn := newTunnelRoundTripConn(func(env tunnelEnvelope) tunnelEnvelope {
		if env.Type != "files" {
			return tunnelEnvelope{OK: false, Error: "unexpected request"}
		}
		var req tunnelFilesRequest
		if err := decodeTunnelPayload(env.Payload, &req); err != nil {
			return tunnelEnvelope{OK: false, Error: err.Error()}
		}
		if req.Path != accountPath {
			return tunnelEnvelope{OK: false, Error: "wrong path"}
		}
		if len(req.AllowPaths) != 1 || req.AllowPaths[0] != accountPath {
			return tunnelEnvelope{OK: false, Error: "missing account allow paths"}
		}
		payload, err := encodeTunnelPayload(workbenchFilesResponse{
			Path:       accountPath,
			AllowPaths: []string{accountPath},
			Entries: []workbenchFileEntry{
				{Name: "src", Path: filepath.Join(accountPath, "src"), IsDir: true},
				{Name: "agent", Path: agentPath, IsDir: true},
			},
		})
		if err != nil {
			return tunnelEnvelope{OK: false, Error: err.Error()}
		}
		return tunnelEnvelope{OK: true, Payload: payload}
	})
	client := &tunnelClient{
		hub:        server.tunnel,
		conn:       conn,
		logger:     slog.Default(),
		account:    "user@example.com",
		edgeID:     "edge-user",
		edgeName:   "User Edge",
		workDir:    agentPath,
		allowPaths: []string{agentPath},
		agents:     []workbenchAgentInfo{{ID: "codex", Label: "Codex", Command: "codex", Enabled: true}},
		pending:    make(map[string]chan tunnelEnvelope),
	}
	server.tunnel.set(client)
	go func() {
		for env := range conn.writes {
			client.handle(env)
		}
	}()

	req := httptest.NewRequest(http.MethodGet, "/cloud-terminal-api/workbench/files?path="+url.QueryEscape(accountPath), nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp := httptest.NewRecorder()
	server.Routes().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("files status = %d body = %s", resp.Code, resp.Body.String())
	}
	var out workbenchFilesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode files: %v", err)
	}
	if len(out.Entries) != 1 || out.Entries[0].Path != filepath.Join(accountPath, "src") {
		t.Fatalf("entries = %+v, want only account path child", out.Entries)
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
	registerTestAccount(t, server, "user@example.com", "TestPassword1")
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

func TestTunnelResolveStartKeepsAgentCommandLogical(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	server := NewServer(Options{
		Config: config.NewStore(filepath.Join(t.TempDir(), "policy.yaml"), &config.Config{
			Server: config.ServerConfig{
				DatabasePath:               filepath.Join(t.TempDir(), "xmux.db"),
				AccountRegistrationEnabled: testBoolPtr(true),
			},
			Policy: policy.Config{
				AllowPaths: []string{root},
				Commands: map[string]policy.CommandPolicy{
					"codex": {Enabled: true, Bin: "/usr/local/bin/codex", Interactive: true},
				},
			},
		}),
	})
	registerTestAccount(t, server, "user@example.com", "TestPassword1")
	if _, err := server.accountStore().SaveUserSettings("user@example.com", userSettingsUpdatePayload{
		CloudTunnelEnabled: true,
		AllowPaths:         []string{root},
		Commands: map[string]adminCommandPayload{
			"codex": {Enabled: true, Bin: "/usr/local/bin/codex", Interactive: true},
		},
	}, server.config.Snapshot().Policy); err != nil {
		t.Fatalf("save user settings: %v", err)
	}
	server.tunnel.set(&tunnelClient{
		hub:        server.tunnel,
		account:    "user@example.com",
		edgeID:     "edge-user",
		edgeName:   "User Edge",
		workDir:    root,
		allowPaths: []string{root},
		agents:     []workbenchAgentInfo{{ID: "codex", Label: "Codex", Command: "/usr/local/bin/codex", Enabled: true}},
		pending:    make(map[string]chan tunnelEnvelope),
	})

	resolved, err := server.runtime.(*tunnelRuntime).ResolveWorkbenchStart(workbenchStartOptions{
		Account: "user@example.com",
		Agent:   "codex",
	})
	if err != nil {
		t.Fatalf("ResolveWorkbenchStart: %v", err)
	}
	if resolved.Agent.Command != "codex" {
		t.Fatalf("resolved command = %q, want logical agent id", resolved.Agent.Command)
	}
}

func TestIsRawAbsPathDoesNotNormalizeRelativeValues(t *testing.T) {
	t.Parallel()

	if isRawAbsPath("codex") {
		t.Fatal("codex should not be treated as an absolute binary override")
	}
	if isRawAbsPath("bin/codex") {
		t.Fatal("relative binary path should not be treated as an absolute binary override")
	}
	if !isRawAbsPath("/opt/homebrew/bin/codex") {
		t.Fatal("absolute binary path should be treated as a binary override")
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

type historicalRuntime struct{}

func (historicalRuntime) ParseAndExec(context.Context, edge.ExecRequest, string) edge.ExecResult {
	return edge.ExecResult{}
}

func (historicalRuntime) Exec(context.Context, edge.ExecRequest) edge.ExecResult {
	return edge.ExecResult{}
}

func (historicalRuntime) ParseAndStartInteractive(context.Context, edge.ExecRequest, string, edge.InteractiveOptions) (InteractiveSession, error) {
	return nil, errors.New("should not start")
}

func (historicalRuntime) StartInteractive(context.Context, edge.ExecRequest, edge.InteractiveOptions) (InteractiveSession, error) {
	return nil, errors.New("should not start")
}

type resumeRuntime struct {
	session     *stubInteractiveSession
	resumeCalls int
}

func (r *resumeRuntime) ParseAndExec(context.Context, edge.ExecRequest, string) edge.ExecResult {
	return edge.ExecResult{}
}

func (r *resumeRuntime) Exec(context.Context, edge.ExecRequest) edge.ExecResult {
	return edge.ExecResult{}
}

func (r *resumeRuntime) ParseAndStartInteractive(context.Context, edge.ExecRequest, string, edge.InteractiveOptions) (InteractiveSession, error) {
	return nil, errors.New("should not start")
}

func (r *resumeRuntime) StartInteractive(context.Context, edge.ExecRequest, edge.InteractiveOptions) (InteractiveSession, error) {
	return nil, errors.New("should not start")
}

func (r *resumeRuntime) ResumeInteractive(context.Context, edge.ExecRequest, edge.InteractiveOptions) (InteractiveSession, error) {
	r.resumeCalls++
	return r.session, nil
}

type stubInteractiveSession struct {
	writes [][]byte
	done   chan edge.ExecResult
}

func (s *stubInteractiveSession) Write(data []byte) error {
	s.writes = append(s.writes, append([]byte(nil), data...))
	return nil
}

func (s *stubInteractiveSession) Resize(uint16, uint16) error {
	return nil
}

func (s *stubInteractiveSession) Close() {}

func (s *stubInteractiveSession) Done() <-chan edge.ExecResult {
	return s.done
}
