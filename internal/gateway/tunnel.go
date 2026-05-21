package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"cloud-terminal/internal/config"

	"github.com/gorilla/websocket"
)

const (
	tunnelRequestTimeout = 30 * time.Second
	tunnelWriteTimeout   = 10 * time.Second
)

type tunnelEnvelope struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	OK        bool            `json:"ok,omitempty"`
	Code      string          `json:"code,omitempty"`
	Error     string          `json:"error,omitempty"`
	Payload   jsonRawEnvelope `json:"payload,omitempty"`
}

type TunnelEnvelope = tunnelEnvelope

type jsonRawEnvelope []byte

type JSONRawEnvelope = jsonRawEnvelope

func (r jsonRawEnvelope) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}

func (r *jsonRawEnvelope) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*r = nil
		return nil
	}
	*r = append((*r)[:0], data...)
	return nil
}

type tunnelHello struct {
	EdgeID       string                 `json:"edge_id"`
	EdgeName     string                 `json:"edge_name"`
	WorkDir      string                 `json:"work_dir"`
	AllowPaths   []string               `json:"allow_paths"`
	PreviewPorts []int                  `json:"preview_ports"`
	Agents       []workbenchAgentInfo   `json:"agents"`
	Sessions     []workbenchSessionInfo `json:"sessions"`
	Resume       bool                   `json:"resume,omitempty"`
}

type TunnelHello = tunnelHello

type tunnelPolicyUpdate struct {
	WorkDir    string               `json:"work_dir"`
	AllowPaths []string             `json:"allow_paths"`
	Agents     []workbenchAgentInfo `json:"agents"`
}

type TunnelPolicyUpdate = tunnelPolicyUpdate

type tunnelStartSessionRequest struct {
	SessionID        string   `json:"session_id"`
	RequestID        string   `json:"request_id"`
	Account          string   `json:"account,omitempty"`
	Agent            string   `json:"agent"`
	Command          string   `json:"command"`
	Bin              string   `json:"bin,omitempty"`
	WorkDir          string   `json:"work_dir"`
	Target           string   `json:"target,omitempty"`
	Args             []string `json:"args,omitempty"`
	AllowPaths       []string `json:"allow_paths"`
	RequirePathMatch bool     `json:"require_path_match,omitempty"`
	ResumeOnly       bool     `json:"resume_only,omitempty"`
	Rows             uint16   `json:"rows"`
	Cols             uint16   `json:"cols"`
}

type TunnelStartSessionRequest = tunnelStartSessionRequest

type tunnelStartSessionResponse struct {
	SessionID  string `json:"session_id"`
	RequestID  string `json:"request_id"`
	WorkDir    string `json:"work_dir"`
	StartedAt  string `json:"started_at"`
	LastActive string `json:"last_active"`
	Running    bool   `json:"running"`
	Submitted  bool   `json:"submitted,omitempty"`
	Title      string `json:"title,omitempty"`
}

type TunnelStartSessionResponse = tunnelStartSessionResponse

type tunnelInputRequest struct {
	SessionID string `json:"session_id"`
	Data      string `json:"data"`
	Title     string `json:"title,omitempty"`
}

type TunnelInputRequest = tunnelInputRequest

type tunnelResizeRequest struct {
	SessionID string `json:"session_id"`
	Rows      uint16 `json:"rows"`
	Cols      uint16 `json:"cols"`
}

type TunnelResizeRequest = tunnelResizeRequest

type tunnelStopRequest struct {
	SessionID string `json:"session_id"`
}

type TunnelStopRequest = tunnelStopRequest

type tunnelSessionOutput struct {
	SessionID string `json:"session_id"`
	Data      string `json:"data"`
}

type TunnelSessionOutput = tunnelSessionOutput

type tunnelSessionSubmitted struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title,omitempty"`
}

type TunnelSessionSubmitted = tunnelSessionSubmitted

type tunnelSessionExit struct {
	SessionID string `json:"session_id"`
	ExitCode  int    `json:"exit_code"`
	Duration  string `json:"duration"`
	Error     string `json:"error,omitempty"`
	WorkDir   string `json:"work_dir,omitempty"`
}

type TunnelSessionExit = tunnelSessionExit

type tunnelFilesRequest struct {
	Path             string   `json:"path"`
	AllowPaths       []string `json:"allow_paths,omitempty"`
	RequirePathMatch bool     `json:"require_path_match,omitempty"`
}

type TunnelFilesRequest = tunnelFilesRequest

type tunnelWarmupRequest struct {
	Path             string   `json:"path"`
	AllowPaths       []string `json:"allow_paths,omitempty"`
	RequirePathMatch bool     `json:"require_path_match,omitempty"`
}

type TunnelWarmupRequest = tunnelWarmupRequest

type tunnelFileRequest struct {
	Path             string   `json:"path"`
	AllowPaths       []string `json:"allow_paths,omitempty"`
	RequirePathMatch bool     `json:"require_path_match,omitempty"`
}

type TunnelFileRequest = tunnelFileRequest

type tunnelDiffRequest struct {
	WorkDir          string   `json:"work_dir"`
	Path             string   `json:"path,omitempty"`
	AllowPaths       []string `json:"allow_paths,omitempty"`
	RequirePathMatch bool     `json:"require_path_match,omitempty"`
}

type TunnelDiffRequest = tunnelDiffRequest

type tunnelPreviewRequest struct {
	Port    int                 `json:"port"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
}

type TunnelPreviewRequest = tunnelPreviewRequest

type tunnelPreviewResponse struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
}

type TunnelPreviewResponse = tunnelPreviewResponse

type tunnelConn interface {
	ReadJSON(any) error
	WriteJSON(any) error
	SetWriteDeadline(time.Time) error
	Close() error
}

type tunnelClient struct {
	hub    *tunnelHub
	conn   tunnelConn
	logger *slog.Logger

	mu           sync.Mutex
	closed       bool
	superseded   bool
	disconnected bool
	lastSeen     time.Time
	pinger       chan struct{}
	account      string
	edgeID       string
	edgeName     string
	workDir      string
	allowPaths   []string
	previewPorts []int
	agents       []workbenchAgentInfo
	sessions     []workbenchSessionInfo
	writeMu      sync.Mutex
	pending      map[string]chan tunnelEnvelope
	sessionSink  func(workbenchServerMessage)
	exitWaiters  map[string]chan workbenchServerMessage
}

type tunnelClientInfo struct {
	account      string
	edgeID       string
	edgeName     string
	workDir      string
	allowPaths   []string
	previewPorts []int
	agents       []workbenchAgentInfo
	sessions     []workbenchSessionInfo
}

type tunnelHub struct {
	logger *slog.Logger

	mu             sync.RWMutex
	defaultAccount string
	clients        map[string]*tunnelClient
	disconnected   map[string]*disconnectedRecord
	graceDuration  time.Duration
	config         interface {
		Snapshot() config.Config
	}
}

type disconnectedRecord struct {
	client    *tunnelClient
	timer     *time.Timer
	expiresAt time.Time
}

const defaultReconnectGrace = 30 * time.Second

func newTunnelHub(logger *slog.Logger) *tunnelHub {
	if logger == nil {
		logger = slog.Default()
	}
	return &tunnelHub{
		logger:        logger,
		clients:       make(map[string]*tunnelClient),
		disconnected:  make(map[string]*disconnectedRecord),
		graceDuration: defaultReconnectGrace,
	}
}

func (h *tunnelHub) setGraceDuration(d time.Duration) {
	if d <= 0 {
		d = defaultReconnectGrace
	}
	h.mu.Lock()
	h.graceDuration = d
	h.mu.Unlock()
}

func (h *tunnelHub) set(client *tunnelClient) {
	account := normalizeTunnelAccount(client.account)
	h.mu.Lock()
	old := h.clients[account]
	h.clients[account] = client
	h.mu.Unlock()
	if old != nil && old != client {
		old.mu.Lock()
		old.superseded = true
		old.mu.Unlock()
		old.close()
	}
}

func (h *tunnelHub) clear(client *tunnelClient) {
	account := normalizeTunnelAccount(client.account)
	h.mu.Lock()
	if h.clients[account] == client {
		delete(h.clients, account)
	}
	h.mu.Unlock()
}

// markDisconnected moves the client into the grace map and schedules a timer that
// fails its sessions when the grace window expires. Callers must NOT also call
// failActiveSessions directly — the timer (or cancelGrace) owns that decision.
func (h *tunnelHub) markDisconnected(client *tunnelClient) {
	if client == nil {
		return
	}
	account := normalizeTunnelAccount(client.account)
	h.mu.Lock()
	if h.clients[account] == client {
		delete(h.clients, account)
	}
	grace := h.graceDuration
	if grace <= 0 {
		grace = defaultReconnectGrace
	}
	var stale *tunnelClient
	if prev := h.disconnected[account]; prev != nil {
		if prev.timer != nil {
			prev.timer.Stop()
		}
		if prev.client != nil && prev.client != client {
			stale = prev.client
		}
		delete(h.disconnected, account)
	}
	record := &disconnectedRecord{client: client, expiresAt: time.Now().Add(grace)}
	h.disconnected[account] = record
	h.mu.Unlock()

	client.mu.Lock()
	client.disconnected = true
	client.mu.Unlock()
	if stale != nil {
		stale.failActiveSessions(errors.New("tunnel disconnected"))
	}
	record.timer = time.AfterFunc(grace, func() {
		h.mu.Lock()
		current, ok := h.disconnected[account]
		if !ok || current != record {
			h.mu.Unlock()
			return
		}
		delete(h.disconnected, account)
		h.mu.Unlock()
		client.failActiveSessions(errors.New("tunnel disconnected"))
	})
}

// cancelGrace removes and returns the disconnected record for the account, if any.
// If the grace timer already fired (and ran the cleanup), this returns nil.
func (h *tunnelHub) cancelGrace(account string) *tunnelClient {
	account = normalizeTunnelAccount(account)
	h.mu.Lock()
	record, ok := h.disconnected[account]
	if !ok {
		h.mu.Unlock()
		return nil
	}
	delete(h.disconnected, account)
	h.mu.Unlock()
	if record.timer != nil && !record.timer.Stop() {
		// timer already fired; cleanup happened — caller should not rely on the
		// client's sessions being intact.
		return nil
	}
	return record.client
}

func (h *tunnelHub) current() *tunnelClient {
	return h.currentForAccount("")
}

func (h *tunnelHub) currentForAccount(account string) *tunnelClient {
	account = normalizeTunnelAccount(account)
	h.mu.RLock()
	defer h.mu.RUnlock()
	if client := h.clients[account]; client != nil {
		return client
	}
	if account == "" && h.defaultAccount != "" {
		return h.clients[h.defaultAccount]
	}
	return nil
}

func (h *tunnelHub) online() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients) > 0
}

func (h *tunnelHub) onlineForAccount(account string) bool {
	return h.currentForAccount(account) != nil
}

// lastKnownForAccount returns the live client if connected, otherwise falls back
// to the disconnected (grace-period) snapshot. Intended for read-only enumeration
// (e.g. workbench state listing); never returned to callers that issue requests.
func (h *tunnelHub) lastKnownForAccount(account string) *tunnelClient {
	if live := h.currentForAccount(account); live != nil {
		return live
	}
	account = normalizeTunnelAccount(account)
	h.mu.RLock()
	defer h.mu.RUnlock()
	if record := h.disconnected[account]; record != nil {
		return record.client
	}
	return nil
}

// statusForAccount returns the tri-state agent status and the last-seen timestamp.
// Values: "online", "reconnecting", "offline".
func (h *tunnelHub) statusForAccount(account string) (string, time.Time) {
	account = normalizeTunnelAccount(account)
	h.mu.RLock()
	defer h.mu.RUnlock()
	if client := h.clients[account]; client != nil {
		client.mu.Lock()
		ts := client.lastSeen
		client.mu.Unlock()
		return "online", ts
	}
	if account == "" && h.defaultAccount != "" {
		if client := h.clients[h.defaultAccount]; client != nil {
			client.mu.Lock()
			ts := client.lastSeen
			client.mu.Unlock()
			return "online", ts
		}
	}
	if record := h.disconnected[account]; record != nil && record.client != nil {
		record.client.mu.Lock()
		ts := record.client.lastSeen
		record.client.mu.Unlock()
		return "reconnecting", ts
	}
	return "offline", time.Time{}
}

func (h *tunnelHub) setSessionSink(sink func(workbenchServerMessage)) {
	h.mu.RLock()
	clients := make([]*tunnelClient, 0, len(h.clients))
	for _, client := range h.clients {
		clients = append(clients, client)
	}
	h.mu.RUnlock()
	for _, client := range clients {
		client.mu.Lock()
		client.sessionSink = sink
		client.mu.Unlock()
	}
}

func (h *tunnelHub) setDefaultAccount(account string) {
	h.mu.Lock()
	h.defaultAccount = normalizeTunnelAccount(account)
	h.mu.Unlock()
}

func (h *tunnelHub) setConfigStore(store interface {
	Snapshot() config.Config
}) {
	h.mu.Lock()
	h.config = store
	h.mu.Unlock()
}

func (h *tunnelHub) configSnapshot() config.Config {
	h.mu.RLock()
	store := h.config
	h.mu.RUnlock()
	if store == nil {
		return config.Config{}
	}
	return store.Snapshot()
}

func (c *tunnelClient) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	_ = c.conn.Close()
}

func (c *tunnelClient) info() tunnelClientInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return tunnelClientInfo{
		account:      c.account,
		edgeID:       c.edgeID,
		edgeName:     c.edgeName,
		workDir:      c.workDir,
		allowPaths:   append([]string(nil), c.allowPaths...),
		previewPorts: append([]int(nil), c.previewPorts...),
		agents:       append([]workbenchAgentInfo(nil), c.agents...),
		sessions:     append([]workbenchSessionInfo(nil), c.sessions...),
	}
}

func (c *tunnelClient) updateSession(next workbenchSessionInfo) {
	if strings.TrimSpace(next.ID) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := range c.sessions {
		if c.sessions[index].ID == next.ID {
			c.sessions[index] = next
			return
		}
	}
	c.sessions = append([]workbenchSessionInfo{next}, c.sessions...)
}

func (c *tunnelClient) markSessionExited(msg workbenchServerMessage) {
	if strings.TrimSpace(msg.SessionID) == "" {
		return
	}
	lastActive := formatTime(time.Now())
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := range c.sessions {
		if c.sessions[index].ID != msg.SessionID {
			continue
		}
		c.sessions[index].Running = false
		c.sessions[index].ExitCode = msg.ExitCode
		c.sessions[index].Duration = msg.Duration
		c.sessions[index].Error = msg.Error
		c.sessions[index].LastActive = lastActive
		if msg.WorkDir != "" {
			c.sessions[index].WorkDir = msg.WorkDir
		}
		return
	}
}

func (c *tunnelClient) readLoop() {
	defer c.handleDisconnect()
	for {
		var env tunnelEnvelope
		if err := c.conn.ReadJSON(&env); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				c.logger.Debug("read tunnel", "error", err)
			}
			return
		}
		c.touch()
		c.handle(env)
	}
}

func (c *tunnelClient) touch() {
	now := time.Now()
	c.mu.Lock()
	c.lastSeen = now
	c.mu.Unlock()
}

// handleDisconnect runs when readLoop exits. If this client was superseded by a
// newer takeover, it skips session-failing entirely (the new client now owns
// those sessions). Otherwise it hands the client to the hub's grace machinery,
// which will fail sessions only if no reconnect happens within the grace window.
func (c *tunnelClient) handleDisconnect() {
	c.mu.Lock()
	superseded := c.superseded
	pinger := c.pinger
	c.mu.Unlock()
	if pinger != nil {
		select {
		case <-pinger:
		default:
			close(pinger)
		}
	}
	c.closePending(errors.New("tunnel disconnected"))
	c.close()
	if superseded {
		return
	}
	c.hub.markDisconnected(c)
}

// restoreFromPrevious compares the new client's hello-announced sessions against
// the prior client's sessions. The new client's session list is authoritative.
// Returns sessions that were running on prior but absent from the new hello —
// the caller should emit exit messages for these so workbench observers learn
// they are gone.
func (c *tunnelClient) restoreFromPrevious(prior *tunnelClient) []workbenchSessionInfo {
	if prior == nil || prior == c {
		return nil
	}
	priorInfo := prior.info()
	c.mu.Lock()
	announced := make(map[string]struct{}, len(c.sessions))
	for _, s := range c.sessions {
		if id := strings.TrimSpace(s.ID); id != "" {
			announced[id] = struct{}{}
		}
	}
	c.mu.Unlock()

	var failed []workbenchSessionInfo
	for _, s := range priorInfo.sessions {
		id := strings.TrimSpace(s.ID)
		if id == "" || !s.Running {
			continue
		}
		if _, ok := announced[id]; ok {
			continue
		}
		failed = append(failed, s)
	}
	return failed
}

func (c *tunnelClient) handle(env tunnelEnvelope) {
	switch env.Type {
	case "response":
		c.mu.Lock()
		ch := c.pending[env.ID]
		delete(c.pending, env.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- env
		}
	case "session_output":
		var payload tunnelSessionOutput
		if decodeTunnelPayload(env.Payload, &payload) != nil {
			return
		}
		c.emit(workbenchServerMessage{Type: "output", SessionID: payload.SessionID, Data: payload.Data})
	case "session_submitted":
		var payload tunnelSessionSubmitted
		if decodeTunnelPayload(env.Payload, &payload) != nil {
			return
		}
		c.emit(workbenchServerMessage{Type: "submitted", SessionID: payload.SessionID, Title: payload.Title})
	case "session_exit":
		var payload tunnelSessionExit
		if decodeTunnelPayload(env.Payload, &payload) != nil {
			return
		}
		msg := workbenchServerMessage{
			Type:      "exit",
			SessionID: payload.SessionID,
			ExitCode:  payload.ExitCode,
			Duration:  payload.Duration,
			Error:     payload.Error,
			WorkDir:   payload.WorkDir,
			Running:   false,
		}
		c.markSessionExited(msg)
		c.emit(msg)
		c.notifyExit(msg)
	case "policy_update":
		var payload tunnelPolicyUpdate
		if decodeTunnelPayload(env.Payload, &payload) != nil {
			return
		}
		c.updatePolicy(payload)
	}
}

func (c *tunnelClient) updatePolicy(next tunnelPolicyUpdate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(next.WorkDir) != "" {
		c.workDir = next.WorkDir
	}
	c.allowPaths = slices.Clone(next.AllowPaths)
	c.agents = slices.Clone(next.Agents)
}

func (c *tunnelClient) emit(msg workbenchServerMessage) {
	c.mu.Lock()
	sink := c.sessionSink
	c.mu.Unlock()
	if sink != nil {
		sink(msg)
	}
}

func (c *tunnelClient) waitExit(sessionID string) <-chan workbenchServerMessage {
	ch := make(chan workbenchServerMessage, 1)
	c.mu.Lock()
	if c.exitWaiters == nil {
		c.exitWaiters = make(map[string]chan workbenchServerMessage)
	}
	c.exitWaiters[sessionID] = ch
	c.mu.Unlock()
	return ch
}

func (c *tunnelClient) notifyExit(msg workbenchServerMessage) {
	c.mu.Lock()
	ch := c.exitWaiters[msg.SessionID]
	delete(c.exitWaiters, msg.SessionID)
	c.mu.Unlock()
	if ch != nil {
		ch <- msg
		close(ch)
	}
}

func (c *tunnelClient) request(ctx context.Context, typ string, payload any, out any) error {
	id := "tun-" + randomWorkbenchID()
	raw, err := encodeTunnelPayload(payload)
	if err != nil {
		return err
	}
	ch := make(chan tunnelEnvelope, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("tunnel disconnected")
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.write(tunnelEnvelope{Type: typ, ID: id, Payload: raw}); err != nil {
		return err
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), tunnelRequestTimeout)
		defer cancel()
	}
	select {
	case env := <-ch:
		if !env.OK {
			if env.Error == "" {
				env.Error = "tunnel request failed"
			}
			return errors.New(env.Error)
		}
		if out != nil {
			return decodeTunnelPayload(env.Payload, out)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *tunnelClient) write(env tunnelEnvelope) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(tunnelWriteTimeout))
	return c.conn.WriteJSON(env)
}

func (c *tunnelClient) closePending(err error) {
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[string]chan tunnelEnvelope)
	c.mu.Unlock()
	for id, ch := range pending {
		ch <- tunnelEnvelope{Type: "response", ID: id, OK: false, Error: err.Error()}
	}
}

func (c *tunnelClient) failActiveSessions(err error) {
	if err == nil {
		err = errors.New("tunnel disconnected")
	}
	reason := err.Error()
	c.mu.Lock()
	sessions := append([]workbenchSessionInfo(nil), c.sessions...)
	waiterIDs := make([]string, 0, len(c.exitWaiters))
	for sessionID := range c.exitWaiters {
		waiterIDs = append(waiterIDs, sessionID)
	}
	c.mu.Unlock()

	seen := make(map[string]struct{}, len(sessions)+len(waiterIDs))
	for _, item := range sessions {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		seen[item.ID] = struct{}{}
		if !item.Running {
			continue
		}
		msg := workbenchServerMessage{
			Type:      "exit",
			SessionID: item.ID,
			ExitCode:  1,
			Error:     reason,
			WorkDir:   item.WorkDir,
			Running:   false,
		}
		c.markSessionExited(msg)
		c.emit(msg)
		c.notifyExit(msg)
	}
	for _, sessionID := range waiterIDs {
		if strings.TrimSpace(sessionID) == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		msg := workbenchServerMessage{
			Type:      "exit",
			SessionID: sessionID,
			ExitCode:  1,
			Error:     reason,
			Running:   false,
		}
		c.markSessionExited(msg)
		c.emit(msg)
		c.notifyExit(msg)
	}
}

func encodeTunnelPayload(value any) (jsonRawEnvelope, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	return jsonRawEnvelope(raw), err
}

func EncodeTunnelPayload(value any) (JSONRawEnvelope, error) {
	return encodeTunnelPayload(value)
}

func decodeTunnelPayload(raw jsonRawEnvelope, out any) error {
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func DecodeTunnelPayload(raw JSONRawEnvelope, out any) error {
	return decodeTunnelPayload(raw, out)
}

func tunnelUnavailable() error {
	return fmt.Errorf("no local edge agent is connected")
}

func normalizeTunnelAccount(account string) string {
	return strings.TrimSpace(strings.ToLower(account))
}
