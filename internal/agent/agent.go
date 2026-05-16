package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	GatewayURL string
	Token      string
	Runtime    *edge.Runtime
	Config     *config.Store
	EdgeID     string
	EdgeName   string
	Logger     *slog.Logger
}

type Agent struct {
	gatewayURL string
	token      string
	runtime    *edge.Runtime
	config     *config.Store
	edgeID     string
	edgeName   string
	logger     *slog.Logger

	mu       sync.Mutex
	sessions map[string]*edge.InteractiveSession
	decoders map[string]*gateway.UTF8StreamDecoder
	outputs  map[string]func(gateway.TunnelEnvelope) error
	meta     map[string]agentSessionMeta
}

type agentSessionMeta struct {
	ID         string
	RequestID  string
	Agent      string
	AgentLabel string
	WorkDir    string
	StartedAt  time.Time
	LastActive time.Time
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
		gatewayURL: strings.TrimSpace(opts.GatewayURL),
		token:      strings.TrimSpace(opts.Token),
		runtime:    opts.Runtime,
		config:     opts.Config,
		edgeID:     firstNonEmpty(opts.EdgeID, "local-edge"),
		edgeName:   firstNonEmpty(opts.EdgeName, "Local Edge"),
		logger:     opts.Logger,
		sessions:   make(map[string]*edge.InteractiveSession),
		decoders:   make(map[string]*gateway.UTF8StreamDecoder),
		outputs:    make(map[string]func(gateway.TunnelEnvelope) error),
		meta:       make(map[string]agentSessionMeta),
	}
}

func (a *Agent) Run(ctx context.Context) error {
	if a.gatewayURL == "" {
		return errors.New("gateway url is required")
	}
	if a.token == "" {
		return errors.New("tunnel token is required")
	}
	if a.runtime == nil || a.config == nil {
		return errors.New("runtime and config are required")
	}
	for {
		if err := a.runOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			a.logger.Warn("agent tunnel disconnected", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (a *Agent) runOnce(ctx context.Context) error {
	wsURL, err := tunnelURL(a.gatewayURL, a.token)
	if err != nil {
		return err
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, http.Header{})
	if err != nil {
		return err
	}
	defer conn.Close()

	client := &clientConn{conn: conn}
	if err := client.write(gateway.TunnelEnvelope{Type: "hello", Payload: mustRaw(a.hello())}); err != nil {
		return err
	}
	for {
		var env gateway.TunnelEnvelope
		if err := conn.ReadJSON(&env); err != nil {
			return err
		}
		if env.Type == "hello_ack" {
			a.logger.Info("agent tunnel connected", "gateway", a.gatewayURL, "edge_id", a.edgeID)
			break
		}
		if env.Type == "error" {
			return errors.New(env.Error)
		}
	}

	for {
		var env gateway.TunnelEnvelope
		if err := conn.ReadJSON(&env); err != nil {
			return err
		}
		go a.handle(ctx, client, env)
	}
}

func (a *Agent) hello() gateway.TunnelHello {
	cfg := a.config.Snapshot()
	return gateway.TunnelHello{
		EdgeID:       a.edgeID,
		EdgeName:     a.edgeName,
		WorkDir:      config.NormalizePath(cfg.Edge.WorkDir),
		AllowPaths:   slices.Clone(cfg.Policy.AllowPaths),
		PreviewPorts: slices.Clone(cfg.Edge.PreviewPorts),
		Agents:       gateway.ListWorkbenchAgents(cfg),
		Sessions:     a.sessionInfos(),
	}
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
		a.mu.Unlock()
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
		out, err := a.files(req.Path)
		respond(client, env.ID, err, out)
	case "file":
		var req gateway.TunnelFileRequest
		if !decodeAndRespond(client, env, &req) {
			return
		}
		out, err := a.file(req.Path)
		respond(client, env.ID, err, out)
	case "warmup":
		var req gateway.TunnelWarmupRequest
		if !decodeAndRespond(client, env, &req) {
			return
		}
		out, err := a.warmup(ctx, req.Path)
		respond(client, env.ID, err, out)
	case "diff":
		var req gateway.TunnelDiffRequest
		if !decodeAndRespond(client, env, &req) {
			return
		}
		out, err := a.diff(ctx, req.WorkDir, req.Path)
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
		})
		return
	}
	a.mu.Unlock()

	agentID := normalizeAgentID(req.Agent)
	command := strings.TrimSpace(req.Command)
	if command == "" || normalizeAgentID(command) == agentID {
		command = a.agentCommand(agentID)
	}
	if command == "" {
		command = agentID
	}
	workDir, args, err := a.resolveStartTarget(req.WorkDir, req.Target, req.Args)
	if err != nil {
		respond(client, env.ID, err, nil)
		return
	}
	execReq := edge.ExecRequest{
		RequestID: req.RequestID,
		SessionID: sessionID,
		User:      "cloud",
		EdgeID:    a.edgeID,
		WorkDir:   workDir,
		Command:   command,
		Args:      args,
		Rows:      req.Rows,
		Cols:      req.Cols,
	}
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
		Agent:      normalizeAgentID(command),
		AgentLabel: agentLabel(command),
		WorkDir:    workDir,
		StartedAt:  startedAt,
		LastActive: startedAt,
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

func (a *Agent) resolveStartTarget(workDir string, target string, args []string) (string, []string, error) {
	cfg := a.config.Snapshot()
	resolvedWorkDir := config.NormalizePath(workDir)
	if resolvedWorkDir == "" {
		resolvedWorkDir = config.NormalizePath(cfg.Edge.WorkDir)
	}
	resolvedTarget := config.NormalizePath(target)
	resolvedArgs := slices.Clone(args)
	if resolvedTarget == "" && len(resolvedArgs) > 0 {
		candidate := config.NormalizePath(resolvedArgs[0])
		if candidate != "" && filepath.IsAbs(candidate) {
			resolvedTarget = candidate
		}
	}
	if resolvedTarget != "" {
		if !pathAllowed(resolvedTarget, cfg.Policy.AllowPaths) {
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
	if !pathAllowed(resolvedWorkDir, cfg.Policy.AllowPaths) {
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
		items = append(items, gateway.WorkbenchSessionInfo{
			ID:         meta.ID,
			Agent:      meta.Agent,
			AgentLabel: meta.AgentLabel,
			WorkDir:    meta.WorkDir,
			StartedAt:  formatAgentTime(meta.StartedAt),
			LastActive: formatAgentTime(meta.LastActive),
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

func (a *Agent) files(path string) (gateway.WorkbenchFilesResponse, error) {
	cfg := a.config.Snapshot()
	path = config.NormalizePath(path)
	if path == "" {
		path = config.NormalizePath(cfg.Edge.WorkDir)
	}
	if !pathAllowed(path, cfg.Policy.AllowPaths) {
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
	if parent == path || !pathAllowed(parent, cfg.Policy.AllowPaths) {
		parent = ""
	}
	return gateway.WorkbenchFilesResponse{
		Path:       path,
		Parent:     parent,
		AllowPaths: slices.Clone(cfg.Policy.AllowPaths),
		Entries:    items,
	}, nil
}

func (a *Agent) file(path string) (gateway.WorkbenchFileResponse, error) {
	cfg := a.config.Snapshot()
	path = config.NormalizePath(path)
	if path == "" {
		return gateway.WorkbenchFileResponse{}, errors.New("path is required")
	}
	if !pathAllowed(path, cfg.Policy.AllowPaths) {
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

func (a *Agent) warmup(ctx context.Context, path string) (gateway.WorkbenchWarmupResponse, error) {
	cfg := a.config.Snapshot()
	root := config.NormalizePath(path)
	if root == "" {
		root = config.NormalizePath(cfg.Edge.WorkDir)
	}
	if !pathAllowed(root, cfg.Policy.AllowPaths) {
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

func (a *Agent) diff(ctx context.Context, workDir string, path string) (gateway.WorkbenchDiffResponse, error) {
	cfg := a.config.Snapshot()
	workDir = config.NormalizePath(workDir)
	if workDir == "" {
		workDir = config.NormalizePath(cfg.Edge.WorkDir)
	}
	if !pathAllowed(workDir, cfg.Policy.AllowPaths) {
		return gateway.WorkbenchDiffResponse{}, errors.New("work_dir is outside allowed roots")
	}
	args := []string{"diff", "--"}
	if strings.TrimSpace(path) != "" {
		path = config.NormalizePath(path)
		if !pathAllowed(path, cfg.Policy.AllowPaths) {
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

func (c *clientConn) write(env gateway.TunnelEnvelope) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
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

func tunnelURL(base string, token string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Scheme == "http" {
		u.Scheme = "ws"
	} else if u.Scheme == "https" {
		u.Scheme = "wss"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/cloud-terminal-api/tunnel/agent"
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String(), nil
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
