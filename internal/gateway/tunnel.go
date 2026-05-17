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

const tunnelRequestTimeout = 30 * time.Second

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
}

type TunnelStartSessionResponse = tunnelStartSessionResponse

type tunnelInputRequest struct {
	SessionID string `json:"session_id"`
	Data      string `json:"data"`
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

type tunnelConn interface {
	ReadJSON(any) error
	WriteJSON(any) error
	Close() error
}

type tunnelClient struct {
	hub    *tunnelHub
	conn   tunnelConn
	logger *slog.Logger

	mu           sync.Mutex
	closed       bool
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
	config         interface {
		Snapshot() config.Config
	}
}

func newTunnelHub(logger *slog.Logger) *tunnelHub {
	if logger == nil {
		logger = slog.Default()
	}
	return &tunnelHub{logger: logger, clients: make(map[string]*tunnelClient)}
}

func (h *tunnelHub) set(client *tunnelClient) {
	account := normalizeTunnelAccount(client.account)
	h.mu.Lock()
	old := h.clients[account]
	h.clients[account] = client
	h.mu.Unlock()
	if old != nil && old != client {
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

func (c *tunnelClient) readLoop() {
	defer func() {
		c.hub.clear(c)
		c.closePending(errors.New("tunnel disconnected"))
		c.close()
	}()
	for {
		var env tunnelEnvelope
		if err := c.conn.ReadJSON(&env); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				c.logger.Debug("read tunnel", "error", err)
			}
			return
		}
		c.handle(env)
	}
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
