package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud-terminal/internal/config"
	"cloud-terminal/internal/edge"
	"cloud-terminal/internal/gateway"

	"github.com/gorilla/websocket"
)

type Options struct {
	DiscoveryURL string
	GatewayURL   string
	Username     string
	SessionID    string
	Runtime      *edge.Runtime
	Config       *config.Store
	EdgeID       string
	EdgeName     string
	Logger       *slog.Logger
}

type Agent struct {
	discoveryURL string
	gatewayURL   string
	username     string
	sessionID    string
	runtime      *edge.Runtime
	config       *config.Store
	edgeID       string
	edgeName     string
	logger       *slog.Logger

	mu           sync.Mutex
	sessions     map[string]*edge.InteractiveSession
	decoders     map[string]*gateway.UTF8StreamDecoder
	outputs      map[string]func(gateway.TunnelEnvelope) error
	control      *clientConn
	meta         map[string]agentSessionMeta
	connectCount int
}

type agentSessionMeta struct {
	ID         string
	RequestID  string
	Account    string
	Agent      string
	AgentLabel string
	WorkDir    string
	StartedAt  time.Time
	LastActive time.Time
	Submitted  bool
	Title      string
	Running    bool
	ExitCode   int
	Duration   string
	Error      string
}

type clientConn struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

func New(opts Options) *Agent {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Agent{
		discoveryURL: strings.TrimSpace(opts.DiscoveryURL),
		gatewayURL:   strings.TrimSpace(opts.GatewayURL),
		username:     strings.TrimSpace(opts.Username),
		sessionID:    strings.TrimSpace(opts.SessionID),
		runtime:      opts.Runtime,
		config:       opts.Config,
		edgeID:       firstNonEmpty(opts.EdgeID, "local-edge"),
		edgeName:     firstNonEmpty(opts.EdgeName, "Local Edge"),
		logger:       opts.Logger,
		sessions:     make(map[string]*edge.InteractiveSession),
		decoders:     make(map[string]*gateway.UTF8StreamDecoder),
		outputs:      make(map[string]func(gateway.TunnelEnvelope) error),
		meta:         make(map[string]agentSessionMeta),
	}
}

const (
	agentBackoffMin      = 2 * time.Second
	agentBackoffMax      = 30 * time.Second
	agentPingInterval    = 20 * time.Second
	agentReadTimeout     = 60 * time.Second
	agentPingWriteLimit  = 10 * time.Second
	agentWriteTimeout    = 15 * time.Second
	agentHealthyDuration = 30 * time.Second
)

func (a *Agent) Run(ctx context.Context) error {
	if a.runtime == nil || a.config == nil {
		return errors.New("runtime and config are required")
	}
	backoff := agentBackoffMin
	for {
		start := time.Now()
		err := a.runOnce(ctx)
		if time.Since(start) >= agentHealthyDuration {
			backoff = agentBackoffMin
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, errAlreadyConnected) {
				a.logger.Warn("agent tunnel rejected as duplicate; will retry", "error", err)
			} else {
				a.logger.Warn("agent tunnel disconnected", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if err != nil {
			backoff *= 2
			if backoff > agentBackoffMax {
				backoff = agentBackoffMax
			}
		}
	}
}

var errAlreadyConnected = errors.New("agent already connected for this account")

func (a *Agent) runOnce(ctx context.Context) error {
	username, sessionID, discoveryURL, gatewayURL := a.tunnelInputs()
	if discoveryURL == "" && gatewayURL == "" {
		return errors.New("discovery_url or gateway_url is not configured")
	}
	if username == "" || sessionID == "" {
		return errors.New("cloud account is not bound; open the local admin and click 使用当前用户")
	}
	resolvedURL, tunnelPath, err := a.resolveGatewayURL(ctx, discoveryURL, gatewayURL)
	if err != nil {
		return err
	}
	wsURL, err := tunnelURL(resolvedURL, tunnelPath)
	if err != nil {
		return err
	}
	header := http.Header{}
	header.Set("Authorization", basicAuth(username, sessionID))
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return describeDialError(wsURL, resp, err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(agentReadTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(agentReadTimeout))
	})

	client := &clientConn{conn: conn}
	a.mu.Lock()
	a.control = client
	a.connectCount++
	resume := a.connectCount > 1
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.control == client {
			a.control = nil
		}
		a.mu.Unlock()
	}()

	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		ticker := time.NewTicker(agentPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-ticker.C:
				client.writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(agentPingWriteLimit))
				err := conn.WriteMessage(websocket.PingMessage, nil)
				client.writeMu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()

	if err := client.write(gateway.TunnelEnvelope{Type: "hello", Payload: mustRaw(a.hello(resume))}); err != nil {
		return err
	}
	for {
		var env gateway.TunnelEnvelope
		if err := conn.ReadJSON(&env); err != nil {
			return err
		}
		if env.Type == "hello_ack" {
			a.logger.Info("agent tunnel connected", "gateway", resolvedURL, "edge_id", a.edgeID)
			break
		}
		if env.Type == "error" {
			if env.Code == "already_connected" {
				return fmt.Errorf("%w: %s", errAlreadyConnected, env.Error)
			}
			return errors.New(env.Error)
		}
	}

	for {
		var env gateway.TunnelEnvelope
		if err := conn.ReadJSON(&env); err != nil {
			return err
		}
		switch env.Type {
		case "input", "resize", "stop":
			a.handle(ctx, client, env)
		default:
			go a.handle(ctx, client, env)
		}
	}
}

func (a *Agent) tunnelInputs() (string, string, string, string) {
	cfg := a.config.Snapshot()
	username := strings.TrimSpace(cfg.CloudTunnel.Account)
	sessionID := strings.TrimSpace(cfg.CloudTunnel.SessionID)
	if username == "" {
		username = a.username
	}
	if sessionID == "" {
		sessionID = a.sessionID
	}
	discovery := strings.TrimSpace(cfg.CloudTunnel.DiscoveryURL)
	if discovery == "" {
		discovery = a.discoveryURL
	}
	gateway := strings.TrimSpace(cfg.CloudTunnel.GatewayURL)
	if gateway == "" {
		gateway = a.gatewayURL
	}
	return username, sessionID, discovery, gateway
}

func (a *Agent) resolveGatewayURL(ctx context.Context, discoveryURL string, gatewayURL string) (string, string, error) {
	discovery := strings.TrimSpace(discoveryURL)
	if discovery == "" {
		return gatewayURL, "", nil
	}
	resolved, tunnelPath, err := fetchDiscovery(ctx, discovery)
	if err != nil {
		if gatewayURL != "" {
			a.logger.Warn("discovery failed, falling back to gateway_url", "error", err)
			return gatewayURL, "", nil
		}
		return "", "", fmt.Errorf("discovery %s failed: %w", discovery, err)
	}
	if strings.TrimSpace(resolved) == "" {
		if gatewayURL != "" {
			return gatewayURL, tunnelPath, nil
		}
		return "", "", fmt.Errorf("discovery %s returned empty gateway_url", discovery)
	}
	return resolved, tunnelPath, nil
}

func fetchDiscovery(ctx context.Context, discoveryURL string) (string, string, error) {
	endpoint, err := discoveryEndpoint(discoveryURL)
	if err != nil {
		return "", "", err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", "", fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		GatewayURL string `json:"gateway_url"`
		TunnelPath string `json:"tunnel_path"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&payload); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(payload.GatewayURL), strings.TrimSpace(payload.TunnelPath), nil
}

func discoveryEndpoint(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid discovery url %q", base)
	}
	if !strings.HasSuffix(u.Path, "/cloud-terminal-api/discovery/gateway") {
		u.Path = strings.TrimRight(u.Path, "/") + "/cloud-terminal-api/discovery/gateway"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func (a *Agent) hello(resume bool) gateway.TunnelHello {
	cfg := a.config.Snapshot()
	return gateway.TunnelHello{
		EdgeID:       a.edgeID,
		EdgeName:     a.edgeName,
		WorkDir:      config.NormalizePath(cfg.Edge.WorkDir),
		AllowPaths:   slices.Clone(cfg.Policy.AllowPaths),
		PreviewPorts: slices.Clone(cfg.Edge.PreviewPorts),
		Agents:       gateway.ListWorkbenchAgents(cfg),
		Sessions:     a.sessionInfos(),
		Resume:       resume,
	}
}

func (a *Agent) PublishPolicyUpdate() error {
	if a == nil {
		return errors.New("agent is not configured")
	}
	cfg := a.config.Snapshot()
	update := gateway.TunnelPolicyUpdate{
		WorkDir:    config.NormalizePath(cfg.Edge.WorkDir),
		AllowPaths: slices.Clone(cfg.Policy.AllowPaths),
		Agents:     gateway.ListWorkbenchAgents(cfg),
	}
	a.mu.Lock()
	client := a.control
	a.mu.Unlock()
	if client == nil {
		return nil
	}
	return client.write(gateway.TunnelEnvelope{Type: "policy_update", Payload: mustRaw(update)})
}

func (a *Agent) handle(ctx context.Context, client *clientConn, env gateway.TunnelEnvelope) {
	switch env.Type {
	case "start_session":
		a.handleStart(ctx, client, env)
	case "input":
		var req gateway.TunnelInputRequest
		if !decodeAndRespond(client, env, &req) {
			return
		}
		a.mu.Lock()
		session := a.sessions[req.SessionID]
		submitted := a.markSessionSubmittedLocked(req.SessionID, req.Title)
		a.mu.Unlock()
		if submitted {
			_ = client.write(gateway.TunnelEnvelope{
				Type:      "session_submitted",
				SessionID: req.SessionID,
				Payload:   mustRaw(gateway.TunnelSessionSubmitted{SessionID: req.SessionID, Title: req.Title}),
			})
		}
		err := errors.New("session not found")
		if session != nil {
			err = session.Write([]byte(req.Data))
		}
		respond(client, env.ID, err, nil)
	case "resize":
		var req gateway.TunnelResizeRequest
		if !decodeAndRespond(client, env, &req) {
			return
		}
		a.mu.Lock()
		session := a.sessions[req.SessionID]
		a.mu.Unlock()
		err := errors.New("session not found")
		if session != nil {
			err = session.Resize(req.Rows, req.Cols)
		}
		respond(client, env.ID, err, nil)
	case "stop":
		var req gateway.TunnelStopRequest
		if !decodeAndRespond(client, env, &req) {
			return
		}
		a.mu.Lock()
		session := a.sessions[req.SessionID]
		a.mu.Unlock()
		if session != nil {
			session.Close()
		}
		respond(client, env.ID, nil, nil)
	case "files":
		var req gateway.TunnelFilesRequest
		if !decodeAndRespond(client, env, &req) {
			return
		}
		out, err := a.files(req.Path, req.AllowPaths, req.RequirePathMatch)
		respond(client, env.ID, err, out)
	case "file":
		var req gateway.TunnelFileRequest
		if !decodeAndRespond(client, env, &req) {
			return
		}
		out, err := a.file(req.Path, req.AllowPaths, req.RequirePathMatch)
		respond(client, env.ID, err, out)
	case "warmup":
		var req gateway.TunnelWarmupRequest
		if !decodeAndRespond(client, env, &req) {
			return
		}
		out, err := a.warmup(ctx, req.Path, req.AllowPaths, req.RequirePathMatch)
		respond(client, env.ID, err, out)
	case "diff":
		var req gateway.TunnelDiffRequest
		if !decodeAndRespond(client, env, &req) {
			return
		}
		out, err := a.diff(ctx, req.WorkDir, req.Path, req.AllowPaths, req.RequirePathMatch)
		respond(client, env.ID, err, out)
	case "preview":
		var req gateway.TunnelPreviewRequest
		if !decodeAndRespond(client, env, &req) {
			return
		}
		out, err := a.preview(ctx, req)
		respond(client, env.ID, err, out)
	default:
		respond(client, env.ID, fmt.Errorf("unsupported tunnel request %q", env.Type), nil)
	}
}

func (a *Agent) handleStart(ctx context.Context, client *clientConn, env gateway.TunnelEnvelope) {
	var req gateway.TunnelStartSessionRequest
	if !decodeAndRespond(client, env, &req) {
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = "sess-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	a.mu.Lock()
	if existing := a.sessions[sessionID]; existing != nil {
		meta := a.meta[sessionID]
		a.outputs[sessionID] = client.write
		if req.Rows > 0 || req.Cols > 0 {
			_ = existing.Resize(req.Rows, req.Cols)
		}
		a.mu.Unlock()
		respond(client, env.ID, nil, gateway.TunnelStartSessionResponse{
			SessionID:  sessionID,
			RequestID:  firstNonEmpty(meta.RequestID, req.RequestID),
			WorkDir:    firstNonEmpty(meta.WorkDir, req.WorkDir),
			StartedAt:  formatAgentTime(meta.StartedAt),
			LastActive: formatAgentTime(meta.LastActive),
			Running:    true,
			Submitted:  meta.Submitted,
			Title:      meta.Title,
		})
		return
	}
	if req.ResumeOnly {
		a.mu.Unlock()
		respond(client, env.ID, errors.New("session not found"), nil)
		return
	}
	a.mu.Unlock()

	agentID := normalizeAgentID(req.Agent)
	bin := strings.TrimSpace(req.Bin)
	if bin == "" {
		bin = strings.TrimSpace(req.Command)
	}
	if bin == "" || isAgentCommandAlias(bin, agentID) {
		bin = a.agentCommand(agentID)
	}
	if bin == "" {
		bin = agentID
	}
	workDir, args, err := a.resolveStartTarget(req.WorkDir, req.Target, req.Args, req.AllowPaths, req.RequirePathMatch)
	if err != nil {
		respond(client, env.ID, err, nil)
		return
	}
	execReq := edge.ExecRequest{
		RequestID: req.RequestID,
		SessionID: sessionID,
		User:      firstNonEmpty(normalizeTunnelAccount(req.Account), "cloud"),
		EdgeID:    a.edgeID,
		WorkDir:   workDir,
		Command:   agentID,
		Args:      args,
		Rows:      req.Rows,
		Cols:      req.Cols,
	}
	execReq.Args = interactiveStartArgs(bin, execReq.Args)
	decoder := &gateway.UTF8StreamDecoder{}
	startedAt := time.Now()
	session, err := a.runtime.StartInteractive(ctx, execReq, edge.InteractiveOptions{
		Output: func(chunk []byte) {
			data := decoder.Push(chunk)
			if data == "" {
				return
			}
			a.mu.Lock()
			output := a.outputs[sessionID]
			meta := a.meta[sessionID]
			meta.LastActive = time.Now()
			a.meta[sessionID] = meta
			a.mu.Unlock()
			if output == nil {
				output = client.write
			}
			_ = output(gateway.TunnelEnvelope{
				Type:      "session_output",
				SessionID: sessionID,
				Payload:   mustRaw(gateway.TunnelSessionOutput{SessionID: sessionID, Data: data}),
			})
		},
	})
	if err != nil {
		respond(client, env.ID, err, nil)
		return
	}

	a.mu.Lock()
	a.sessions[sessionID] = session
	a.decoders[sessionID] = decoder
	a.outputs[sessionID] = client.write
	a.meta[sessionID] = agentSessionMeta{
		ID:         sessionID,
		RequestID:  req.RequestID,
		Account:    normalizeTunnelAccount(req.Account),
		Agent:      agentID,
		AgentLabel: agentLabel(agentID),
		WorkDir:    workDir,
		StartedAt:  startedAt,
		LastActive: startedAt,
		Submitted:  false,
		Running:    true,
	}
	a.mu.Unlock()
	respond(client, env.ID, nil, gateway.TunnelStartSessionResponse{
		SessionID:  sessionID,
		RequestID:  req.RequestID,
		WorkDir:    workDir,
		StartedAt:  startedAt.Format(time.RFC3339),
		LastActive: startedAt.Format(time.RFC3339),
		Running:    true,
		Submitted:  false,
	})

	go func() {
		result := <-session.Done()
		if data := decoder.Flush(); data != "" {
			a.mu.Lock()
			output := a.outputs[sessionID]
			a.mu.Unlock()
			if output == nil {
				output = client.write
			}
			_ = output(gateway.TunnelEnvelope{
				Type:      "session_output",
				SessionID: sessionID,
				Payload:   mustRaw(gateway.TunnelSessionOutput{SessionID: sessionID, Data: data}),
			})
		}
		a.mu.Lock()
		delete(a.sessions, sessionID)
		delete(a.decoders, sessionID)
		delete(a.outputs, sessionID)
		meta := a.meta[sessionID]
		meta.Running = false
		meta.ExitCode = result.ExitCode
		meta.Duration = result.Duration
		meta.Error = result.Error
		meta.LastActive = time.Now()
		a.meta[sessionID] = meta
		a.mu.Unlock()
		_ = client.write(gateway.TunnelEnvelope{
			Type:      "session_exit",
			SessionID: sessionID,
			Payload: mustRaw(gateway.TunnelSessionExit{
				SessionID: sessionID,
				ExitCode:  result.ExitCode,
				Duration:  result.Duration,
				Error:     result.Error,
				WorkDir:   firstNonEmpty(result.WorkDir, workDir),
			}),
		})
	}()
}

func (a *Agent) resolveStartTarget(workDir string, target string, args []string, allowPaths []string, requirePathMatch bool) (string, []string, error) {
	cfg := a.config.Snapshot()
	allowed := effectiveAllowedPaths(cfg.Policy.AllowPaths, allowPaths, requirePathMatch)
	resolvedWorkDir := config.NormalizePath(workDir)
	if resolvedWorkDir == "" {
		resolvedWorkDir = config.NormalizePath(cfg.Edge.WorkDir)
	}
	resolvedTarget := config.NormalizePath(target)
	resolvedArgs := slices.Clone(args)
	if resolvedTarget == "" && len(resolvedArgs) > 0 && !strings.HasPrefix(resolvedArgs[0], "-") {
		candidate := config.NormalizePath(resolvedArgs[0])
		if candidate != "" && filepath.IsAbs(candidate) {
			resolvedTarget = candidate
		}
	}
	if resolvedTarget != "" {
		if !pathAllowed(resolvedTarget, allowed) {
			return "", nil, errors.New("target is outside allowed roots")
		}
		info, err := os.Stat(resolvedTarget)
		if err != nil {
			return "", nil, err
		}
		if info.IsDir() {
			resolvedWorkDir = resolvedTarget
			resolvedArgs = nil
		} else {
			resolvedWorkDir = filepath.Dir(resolvedTarget)
			resolvedArgs = []string{filepath.Base(resolvedTarget)}
		}
	}
	if !pathAllowed(resolvedWorkDir, allowed) {
		return "", nil, errors.New("work_dir is outside allowed roots")
	}
	info, err := os.Stat(resolvedWorkDir)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		return "", nil, errors.New("work_dir is not a directory")
	}
	return resolvedWorkDir, resolvedArgs, nil
}

func effectiveAllowedPaths(local []string, account []string, requirePathMatch bool) []string {
	local = cleanAgentPaths(local)
	account = cleanAgentPaths(account)
	if len(account) > 0 {
		return account
	}
	if !requirePathMatch {
		return local
	}
	return nil
}

func interactiveStartArgs(bin string, args []string) []string {
	out := slices.Clone(args)
	bin = strings.TrimSpace(bin)
	if bin == "" || !filepath.IsAbs(filepath.Clean(bin)) {
		return out
	}
	return append([]string{bin}, out...)
}

func isAgentCommandAlias(value string, agentID string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || filepath.IsAbs(filepath.Clean(value)) || strings.Contains(value, string(filepath.Separator)) {
		return false
	}
	value = strings.ReplaceAll(value, "_", "-")
	switch normalizeAgentID(agentID) {
	case "claude":
		return value == "claude" || value == "claude-code"
	case "gemini":
		return value == "gemini"
	default:
		return value == "codex"
	}
}

func cleanAgentPaths(values []string) []string {
	var out []string
	for _, value := range values {
		value = config.NormalizePath(value)
		if value != "" && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

func (a *Agent) agentCommand(agentID string) string {
	cfg := a.config.Snapshot()
	id := normalizeAgentID(agentID)
	for _, agent := range gateway.ListWorkbenchAgents(cfg) {
		if normalizeAgentID(agent.ID) == id && strings.TrimSpace(agent.Command) != "" {
			return strings.TrimSpace(agent.Command)
		}
	}
	return id
}

func (a *Agent) sessionInfos() []gateway.WorkbenchSessionInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	items := make([]gateway.WorkbenchSessionInfo, 0, len(a.meta))
	for _, meta := range a.meta {
		if !meta.Submitted {
			continue
		}
		items = append(items, gateway.WorkbenchSessionInfo{
			ID:         meta.ID,
			Account:    normalizeTunnelAccount(meta.Account),
			Agent:      meta.Agent,
			AgentLabel: meta.AgentLabel,
			WorkDir:    meta.WorkDir,
			StartedAt:  formatAgentTime(meta.StartedAt),
			LastActive: formatAgentTime(meta.LastActive),
			Submitted:  meta.Submitted,
			Title:      meta.Title,
			Running:    meta.Running,
			ExitCode:   meta.ExitCode,
			Duration:   meta.Duration,
			Error:      meta.Error,
		})
	}
	slices.SortFunc(items, func(a, b gateway.WorkbenchSessionInfo) int {
		return strings.Compare(b.LastActive, a.LastActive)
	})
	return items
}

func (a *Agent) markSessionSubmittedLocked(sessionID string, title string) bool {
	meta := a.meta[sessionID]
	if meta.ID == "" || meta.Submitted {
		return false
	}
	title = cleanAgentSessionTitle(title)
	if title == "" {
		return false
	}
	meta.Submitted = true
	meta.Title = title
	meta.LastActive = time.Now()
	a.meta[sessionID] = meta
	return true
}

func cleanAgentSessionTitle(value string) string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == '\r' || r == '\n'
	})
	if len(fields) == 0 {
		return ""
	}
	title := strings.TrimSpace(fields[0])
	title = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, title)
	title = strings.Join(strings.Fields(title), " ")
	if len([]rune(title)) > 60 {
		return string([]rune(title)[:60])
	}
	return title
}

func (a *Agent) files(path string, allowPaths []string, requirePathMatch bool) (gateway.WorkbenchFilesResponse, error) {
	cfg := a.config.Snapshot()
	allowed := effectiveAllowedPaths(cfg.Policy.AllowPaths, allowPaths, requirePathMatch)
	path = config.NormalizePath(path)
	if path == "" {
		path = config.NormalizePath(cfg.Edge.WorkDir)
	}
	if !pathAllowed(path, allowed) {
		return gateway.WorkbenchFilesResponse{}, errors.New("path is outside allowed roots")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return gateway.WorkbenchFilesResponse{}, err
	}
	items := make([]gateway.WorkbenchFileEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		items = append(items, gateway.WorkbenchFileEntry{
			Name:    entry.Name(),
			Path:    filepath.Join(path, entry.Name()),
			IsDir:   entry.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}
	slices.SortFunc(items, func(a, b gateway.WorkbenchFileEntry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	parent := filepath.Dir(path)
	if parent == path || !pathAllowed(parent, allowed) {
		parent = ""
	}
	return gateway.WorkbenchFilesResponse{
		Path:       path,
		Parent:     parent,
		AllowPaths: slices.Clone(allowed),
		Entries:    items,
	}, nil
}

func (a *Agent) file(path string, allowPaths []string, requirePathMatch bool) (gateway.WorkbenchFileResponse, error) {
	cfg := a.config.Snapshot()
	allowed := effectiveAllowedPaths(cfg.Policy.AllowPaths, allowPaths, requirePathMatch)
	path = config.NormalizePath(path)
	if path == "" {
		return gateway.WorkbenchFileResponse{}, errors.New("path is required")
	}
	if !pathAllowed(path, allowed) {
		return gateway.WorkbenchFileResponse{}, errors.New("path is outside allowed roots")
	}
	info, err := os.Stat(path)
	if err != nil {
		return gateway.WorkbenchFileResponse{}, err
	}
	if info.IsDir() {
		return gateway.WorkbenchFileResponse{}, errors.New("path is a directory")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return gateway.WorkbenchFileResponse{}, err
	}
	truncated := false
	if len(content) > 512<<10 {
		content = content[:512<<10]
		truncated = true
	}
	return gateway.WorkbenchFileResponse{Path: path, Name: filepath.Base(path), Content: string(content), Size: info.Size(), Truncated: truncated}, nil
}

func (a *Agent) warmup(ctx context.Context, path string, allowPaths []string, requirePathMatch bool) (gateway.WorkbenchWarmupResponse, error) {
	cfg := a.config.Snapshot()
	allowed := effectiveAllowedPaths(cfg.Policy.AllowPaths, allowPaths, requirePathMatch)
	root := config.NormalizePath(path)
	if root == "" {
		root = config.NormalizePath(cfg.Edge.WorkDir)
	}
	if !pathAllowed(root, allowed) {
		return gateway.WorkbenchWarmupResponse{}, errors.New("path is outside allowed roots")
	}
	info, err := os.Stat(root)
	if err != nil {
		return gateway.WorkbenchWarmupResponse{}, err
	}
	if !info.IsDir() {
		root = filepath.Dir(root)
	}
	start := time.Now()
	var response gateway.WorkbenchWarmupResponse
	response.Root = root
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil || response.Dirs+response.Files >= 2400 {
			if response.Dirs+response.Files >= 2400 {
				response.Truncated = true
			}
			return filepath.SkipDir
		}
		name := d.Name()
		if d.IsDir() && (name == ".git" || name == "node_modules" || name == "vendor" || name == "dist" || name == "build") && path != root {
			response.Skipped++
			return filepath.SkipDir
		}
		if d.IsDir() {
			response.Dirs++
		} else {
			response.Files++
		}
		return nil
	})
	response.DurationMS = time.Since(start).Milliseconds()
	return response, nil
}

func (a *Agent) diff(ctx context.Context, workDir string, path string, allowPaths []string, requirePathMatch bool) (gateway.WorkbenchDiffResponse, error) {
	cfg := a.config.Snapshot()
	allowed := effectiveAllowedPaths(cfg.Policy.AllowPaths, allowPaths, requirePathMatch)
	workDir = config.NormalizePath(workDir)
	if workDir == "" {
		workDir = config.NormalizePath(cfg.Edge.WorkDir)
	}
	if !pathAllowed(workDir, allowed) {
		return gateway.WorkbenchDiffResponse{}, errors.New("work_dir is outside allowed roots")
	}
	args := []string{"diff", "--"}
	if strings.TrimSpace(path) != "" {
		path = config.NormalizePath(path)
		if !pathAllowed(path, allowed) {
			return gateway.WorkbenchDiffResponse{}, errors.New("path is outside allowed roots")
		}
		rel, err := filepath.Rel(workDir, path)
		if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			return gateway.WorkbenchDiffResponse{}, errors.New("path is outside work_dir")
		}
		args = append(args, rel)
	}
	status, statusErr := runGit(ctx, workDir, "status", "--short")
	stat, _ := runGit(ctx, workDir, "diff", "--stat")
	diff, _ := runGit(ctx, workDir, args...)
	out := gateway.WorkbenchDiffResponse{WorkDir: workDir, Status: trim(status, 128<<10), Stat: trim(stat, 128<<10), Diff: trim(diff, 256<<10)}
	if statusErr != nil {
		out.Error = statusErr.Error()
	}
	return out, nil
}

const previewBodyMax = 20 << 20

var previewHopByHopHeaders = map[string]struct{}{
	"connection":          {},
	"proxy-connection":    {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

// preview dials 127.0.0.1:port on this agent's host and forwards the request
// the gateway captured from the user's browser. The port must be in the
// agent's configured preview_ports list (i.e. announced via hello) — the
// gateway also enforces this, this is a defense-in-depth re-check.
func (a *Agent) preview(ctx context.Context, req gateway.TunnelPreviewRequest) (gateway.TunnelPreviewResponse, error) {
	cfg := a.config.Snapshot()
	allowed := false
	for _, port := range cfg.Edge.PreviewPorts {
		if port == req.Port {
			allowed = true
			break
		}
	}
	if !allowed {
		return gateway.TunnelPreviewResponse{}, fmt.Errorf("preview port %d not allowlisted", req.Port)
	}

	method := strings.TrimSpace(req.Method)
	if method == "" {
		method = http.MethodGet
	}
	rawPath := req.Path
	if rawPath == "" {
		rawPath = "/"
	}
	if !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}

	target := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("127.0.0.1", strconv.Itoa(req.Port)),
	}
	if idx := strings.IndexByte(rawPath, '?'); idx >= 0 {
		target.Path = rawPath[:idx]
		target.RawQuery = rawPath[idx+1:]
	} else {
		target.Path = rawPath
	}

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return gateway.TunnelPreviewResponse{}, err
	}
	for key, values := range req.Headers {
		lower := strings.ToLower(key)
		if _, hop := previewHopByHopHeaders[lower]; hop {
			continue
		}
		if lower == "host" || lower == "content-length" {
			continue
		}
		httpReq.Header[key] = append([]string(nil), values...)
	}
	httpReq.Host = target.Host

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return gateway.TunnelPreviewResponse{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, previewBodyMax+1))
	if err != nil {
		return gateway.TunnelPreviewResponse{}, err
	}
	if int64(len(respBody)) > previewBodyMax {
		return gateway.TunnelPreviewResponse{}, fmt.Errorf("preview response exceeds %d bytes", previewBodyMax)
	}

	headers := make(map[string][]string, len(resp.Header))
	for key, values := range resp.Header {
		lower := strings.ToLower(key)
		if _, hop := previewHopByHopHeaders[lower]; hop {
			continue
		}
		headers[key] = append([]string(nil), values...)
	}
	return gateway.TunnelPreviewResponse{
		Status:  resp.StatusCode,
		Headers: headers,
		Body:    respBody,
	}, nil
}

func (c *clientConn) write(env gateway.TunnelEnvelope) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(agentWriteTimeout))
	return c.conn.WriteJSON(env)
}

func respond(client *clientConn, id string, err error, payload any) {
	env := gateway.TunnelEnvelope{Type: "response", ID: id, OK: err == nil}
	if err != nil {
		env.Error = err.Error()
	} else if payload != nil {
		env.Payload = mustRaw(payload)
	}
	_ = client.write(env)
}

func decodeAndRespond(client *clientConn, env gateway.TunnelEnvelope, out any) bool {
	if err := gateway.DecodeTunnelPayload(env.Payload, out); err != nil {
		respond(client, env.ID, err, nil)
		return false
	}
	return true
}

func mustRaw(value any) gateway.JSONRawEnvelope {
	raw, err := gateway.EncodeTunnelPayload(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func tunnelURL(base string, tunnelPath string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Scheme == "http" {
		u.Scheme = "ws"
	} else if u.Scheme == "https" {
		u.Scheme = "wss"
	}
	path := strings.TrimSpace(tunnelPath)
	if path == "" {
		path = "/cloud-terminal-api/tunnel/agent"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u.String(), nil
}

func basicAuth(username string, sessionID string) string {
	value := base64.StdEncoding.EncodeToString([]byte(username + ":" + sessionID))
	return "Basic " + value
}

func describeDialError(wsURL string, resp *http.Response, err error) error {
	if resp == nil {
		return fmt.Errorf("dial %s: %w", wsURL, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	resp.Body.Close()
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 240 {
		snippet = snippet[:240] + "..."
	}
	hint := dialErrorHint(resp.StatusCode)
	if snippet == "" {
		return fmt.Errorf("dial %s: HTTP %d (%s)%s: %w", wsURL, resp.StatusCode, http.StatusText(resp.StatusCode), hint, err)
	}
	return fmt.Errorf("dial %s: HTTP %d (%s)%s body=%q: %w", wsURL, resp.StatusCode, http.StatusText(resp.StatusCode), hint, snippet, err)
}

func dialErrorHint(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return " — session 失效或账号未绑定，请到管理后台重新点击「使用当前用户」保存"
	case http.StatusForbidden:
		return " — 当前账号未开启本地穿透，或 Origin 被云端 allow_hosts 拒绝"
	case http.StatusNotFound:
		return " — gateway 路径不对，确认 discovery_url/gateway_url 指向真正的云端入口"
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return " — 反向代理（Nginx）与上游连接异常，检查 Upgrade 头转发"
	}
	if status >= 500 {
		return " — 云端 5xx，检查云端日志"
	}
	return ""
}

func pathAllowed(path string, roots []string) bool {
	path = config.NormalizePath(path)
	for _, root := range roots {
		root = config.NormalizePath(root)
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err == nil && (rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")) {
			return true
		}
	}
	return false
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func trim(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}

func normalizeTunnelAccount(account string) string {
	return strings.TrimSpace(strings.ToLower(account))
}

func normalizeAgentID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, "_", "-")
	if value == "claude-code" {
		return "claude"
	}
	if value == "claude" || value == "gemini" {
		return value
	}
	return "codex"
}

func agentLabel(value string) string {
	switch normalizeAgentID(value) {
	case "claude":
		return "Claude Code"
	case "gemini":
		return "Gemini"
	default:
		return "Codex"
	}
}

func formatAgentTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
