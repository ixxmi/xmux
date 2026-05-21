package gateway

import (
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// stubTunnelConn is a tunnelConn that never returns from ReadJSON so a client
// constructed with it stays "alive" until close() is called explicitly.
type stubTunnelConn struct {
	mu     sync.Mutex
	closed chan struct{}
	once   sync.Once
}

func newStubTunnelConn() *stubTunnelConn {
	return &stubTunnelConn{closed: make(chan struct{})}
}

func (c *stubTunnelConn) ReadJSON(any) error {
	<-c.closed
	return errors.New("closed")
}

func (c *stubTunnelConn) WriteJSON(any) error { return nil }

func (c *stubTunnelConn) SetWriteDeadline(time.Time) error { return nil }

func (c *stubTunnelConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func newTestTunnelClient(hub *tunnelHub, account string, sessions []workbenchSessionInfo, sink func(workbenchServerMessage)) *tunnelClient {
	conn := newStubTunnelConn()
	c := &tunnelClient{
		hub:         hub,
		conn:        conn,
		logger:      slog.Default(),
		account:     account,
		edgeID:      "edge-" + account,
		edgeName:    "Edge " + account,
		sessions:    append([]workbenchSessionInfo(nil), sessions...),
		pending:     make(map[string]chan tunnelEnvelope),
		exitWaiters: make(map[string]chan workbenchServerMessage),
		lastSeen:    time.Now(),
		pinger:      make(chan struct{}),
		sessionSink: sink,
	}
	return c
}

func TestTunnelHubStatusOnlineAndOffline(t *testing.T) {
	t.Parallel()
	hub := newTunnelHub(slog.Default())
	hub.setGraceDuration(50 * time.Millisecond)

	if got, _ := hub.statusForAccount("alice"); got != "offline" {
		t.Fatalf("status before connect = %q, want offline", got)
	}

	client := newTestTunnelClient(hub, "alice", nil, func(workbenchServerMessage) {})
	hub.set(client)
	if got, _ := hub.statusForAccount("alice"); got != "online" {
		t.Fatalf("status after connect = %q, want online", got)
	}
}

func TestTunnelHubGraceExpiresAndFailsSessions(t *testing.T) {
	t.Parallel()
	hub := newTunnelHub(slog.Default())
	hub.setGraceDuration(40 * time.Millisecond)

	var mu sync.Mutex
	var emitted []workbenchServerMessage
	sink := func(msg workbenchServerMessage) {
		mu.Lock()
		emitted = append(emitted, msg)
		mu.Unlock()
	}

	sessions := []workbenchSessionInfo{{ID: "s1", Running: true}, {ID: "s2", Running: true}}
	client := newTestTunnelClient(hub, "alice", sessions, sink)
	hub.set(client)

	hub.markDisconnected(client)
	if got, _ := hub.statusForAccount("alice"); got != "reconnecting" {
		t.Fatalf("status during grace = %q, want reconnecting", got)
	}

	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case <-deadline:
			t.Fatalf("grace timer did not fire; status = %q", mustStatus(hub, "alice"))
		default:
		}
		if mustStatus(hub, "alice") == "offline" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	count := len(emitted)
	mu.Unlock()
	if count < 2 {
		t.Fatalf("expected at least 2 exit messages after grace, got %d", count)
	}
}

func TestTunnelHubCancelGraceReturnsPrior(t *testing.T) {
	t.Parallel()
	hub := newTunnelHub(slog.Default())
	hub.setGraceDuration(200 * time.Millisecond)

	var mu sync.Mutex
	var emitted []workbenchServerMessage
	sink := func(msg workbenchServerMessage) {
		mu.Lock()
		emitted = append(emitted, msg)
		mu.Unlock()
	}

	sessions := []workbenchSessionInfo{{ID: "s1", Running: true}}
	client := newTestTunnelClient(hub, "alice", sessions, sink)
	hub.set(client)
	hub.markDisconnected(client)

	prior := hub.cancelGrace("alice")
	if prior != client {
		t.Fatalf("cancelGrace = %v, want the prior client", prior)
	}

	// Sleep past the would-be expiry to make sure the timer doesn't still fire.
	time.Sleep(250 * time.Millisecond)
	mu.Lock()
	count := len(emitted)
	mu.Unlock()
	if count != 0 {
		t.Fatalf("expected no exit messages after cancelGrace, got %d", count)
	}
}

func TestTunnelClientRestoreFromPrevious(t *testing.T) {
	t.Parallel()
	hub := newTunnelHub(slog.Default())

	priorSessions := []workbenchSessionInfo{
		{ID: "kept", Running: true, WorkDir: "/w"},
		{ID: "gone", Running: true, WorkDir: "/w"},
		{ID: "alreadyDone", Running: false, WorkDir: "/w"},
	}
	prior := newTestTunnelClient(hub, "alice", priorSessions, nil)

	newSessions := []workbenchSessionInfo{{ID: "kept", Running: true, WorkDir: "/w"}}
	next := newTestTunnelClient(hub, "alice", newSessions, nil)

	failed := next.restoreFromPrevious(prior)
	if len(failed) != 1 || failed[0].ID != "gone" {
		t.Fatalf("restoreFromPrevious failed list = %+v, want [gone]", failed)
	}

	// Sessions on next should be unchanged (agent's hello is authoritative).
	info := next.info()
	if len(info.sessions) != 1 || info.sessions[0].ID != "kept" {
		t.Fatalf("next sessions = %+v, want [kept]", info.sessions)
	}
}

func TestTunnelHubTakeoverMarksOldSuperseded(t *testing.T) {
	t.Parallel()
	hub := newTunnelHub(slog.Default())

	old := newTestTunnelClient(hub, "alice", []workbenchSessionInfo{{ID: "s1", Running: true}}, nil)
	hub.set(old)

	next := newTestTunnelClient(hub, "alice", []workbenchSessionInfo{{ID: "s1", Running: true}}, nil)
	hub.set(next)

	old.mu.Lock()
	superseded := old.superseded
	closed := old.closed
	old.mu.Unlock()
	if !superseded {
		t.Fatal("old client was not marked superseded")
	}
	if !closed {
		t.Fatal("old client conn was not closed")
	}

	// handleDisconnect on the superseded client must not push it into the grace
	// map nor fail its sessions.
	old.handleDisconnect()

	if got, _ := hub.statusForAccount("alice"); got != "online" {
		t.Fatalf("status after takeover = %q, want online", got)
	}
}

func mustStatus(hub *tunnelHub, account string) string {
	status, _ := hub.statusForAccount(account)
	return status
}
