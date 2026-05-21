package gateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
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
	"cloud-terminal/internal/policy"

	"github.com/gorilla/websocket"
)

const (
	workbenchCookieName          = "cloud-terminal-workbench"
	workbenchReplayMax           = 4 << 20
	workbenchHistoricalReplayMax = 128 << 10
	workbenchFileMax             = 512 << 10
	workbenchDiffMax             = 256 << 10
	workbenchWarmupMax           = 2400
)

var errWorkbenchSessionNotRunning = errors.New("session is no longer running; start a new terminal from this folder")

const workbenchRestartUnavailableError = "session unavailable after service restart"

type workbenchManager struct {
	runtime        Runtime
	config         *config.Store
	policyResolver interface {
		UserPolicy(string, policy.Config) (policy.Config, error)
	}
	edgeID    string
	edgeName  string
	logger    *slog.Logger
	statePath string

	mu       sync.RWMutex
	sessions map[string]*workbenchSession
	lastSave time.Time
}

type workbenchStartOptions struct {
	SessionID string
	Account   string
	Agent     string
	WorkDir   string
	Target    string
	Rows      uint16
	Cols      uint16
}

type workbenchStartResolution struct {
	EdgeID   string
	EdgeName string
	Agent    workbenchAgent
	WorkDir  string
	Target   string
	Args     []string
}

type workbenchStartResolver interface {
	ResolveWorkbenchStart(workbenchStartOptions) (workbenchStartResolution, error)
}

type workbenchSessionResumer interface {
	ResumeInteractive(context.Context, edge.ExecRequest, edge.InteractiveOptions) (InteractiveSession, error)
}

type workbenchSession struct {
	id        string
	requestID string
	account   string
	edgeID    string
	edgeName  string
	agent     string
	workDir   string
	startedAt time.Time

	interactive InteractiveSession

	mu             sync.Mutex
	submitted      bool
	title          string
	inputBuffer    string
	lastActive     time.Time
	running        bool
	exitCode       int
	duration       string
	errText        string
	replay         []byte
	outputDecoder  utf8StreamDecoder
	attachmentSeq  uint64
	attachments    map[uint64]func(workbenchServerMessage)
	finishedOnce   sync.Once
	closedByClient bool
}

type workbenchSnapshot struct {
	ID         string
	RequestID  string
	Account    string
	EdgeID     string
	EdgeName   string
	Agent      string
	WorkDir    string
	StartedAt  time.Time
	LastActive time.Time
	Submitted  bool
	Title      string
	Running    bool
	ExitCode   int
	Duration   string
	Error      string
	Replay     string
}

type workbenchClientMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
}

type workbenchServerMessage struct {
	Type       string `json:"type"`
	SessionID  string `json:"session_id,omitempty"`
	EdgeID     string `json:"edge_id,omitempty"`
	EdgeName   string `json:"edge_name,omitempty"`
	Agent      string `json:"agent,omitempty"`
	AgentLabel string `json:"agent_label,omitempty"`
	Data       string `json:"data,omitempty"`
	Error      string `json:"error,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty"`
	Duration   string `json:"duration,omitempty"`
	WorkDir    string `json:"work_dir,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	LastActive string `json:"last_active,omitempty"`
	Running    bool   `json:"running"`
	Submitted  bool   `json:"submitted,omitempty"`
	Title      string `json:"title,omitempty"`
}

type workbenchAuthPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type accountAuthPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email,omitempty"`
	Code     string `json:"code,omitempty"`
}

type accountCreatePayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

type accountIdentity struct {
	Username string
	Role     string
}

type workbenchStatePayload struct {
	EdgeID               string                 `json:"edge_id"`
	EdgeName             string                 `json:"edge_name"`
	EdgeOnline           bool                   `json:"edge_online"`
	EdgeStatus           string                 `json:"edge_status,omitempty"`
	LastSeen             string                 `json:"last_seen,omitempty"`
	Tunnel               bool                   `json:"tunnel"`
	WorkDir              string                 `json:"work_dir"`
	AllowPaths           []string               `json:"allow_paths"`
	EdgePaths            []string               `json:"edge_allow_paths,omitempty"`
	PreviewPorts         []int                  `json:"preview_ports"`
	Agents               []workbenchAgentInfo   `json:"agents"`
	Sessions             []workbenchSessionInfo `json:"sessions"`
	ArchivedFolders      []string               `json:"archived_folders,omitempty"`
	ArchivedSessions     []string               `json:"archived_sessions,omitempty"`
	ForgottenFolders     []string               `json:"forgotten_folders,omitempty"`
	ForgottenSessions    []string               `json:"forgotten_sessions,omitempty"`
	ArchivedSessionItems []workbenchSessionInfo `json:"archived_session_items,omitempty"`
}

type workbenchSessionInfo struct {
	ID         string `json:"id"`
	Account    string `json:"account,omitempty"`
	Agent      string `json:"agent"`
	AgentLabel string `json:"agent_label"`
	WorkDir    string `json:"work_dir"`
	StartedAt  string `json:"started_at"`
	LastActive string `json:"last_active"`
	Submitted  bool   `json:"submitted"`
	Title      string `json:"title,omitempty"`
	Running    bool   `json:"running"`
	ExitCode   int    `json:"exit_code"`
	Duration   string `json:"duration,omitempty"`
	Error      string `json:"error,omitempty"`
}

type WorkbenchSessionInfo = workbenchSessionInfo

type workbenchPersistedState struct {
	Version  int                         `json:"version"`
	SavedAt  time.Time                   `json:"saved_at"`
	Sessions []workbenchPersistedSession `json:"sessions"`
}

type workbenchPersistedSession struct {
	ID         string    `json:"id"`
	RequestID  string    `json:"request_id,omitempty"`
	Account    string    `json:"account,omitempty"`
	EdgeID     string    `json:"edge_id"`
	EdgeName   string    `json:"edge_name"`
	Agent      string    `json:"agent"`
	WorkDir    string    `json:"work_dir"`
	StartedAt  time.Time `json:"started_at"`
	LastActive time.Time `json:"last_active"`
	Submitted  bool      `json:"submitted,omitempty"`
	Title      string    `json:"title,omitempty"`
	Running    bool      `json:"running"`
	ExitCode   int       `json:"exit_code"`
	Duration   string    `json:"duration,omitempty"`
	Error      string    `json:"error,omitempty"`
	Replay     string    `json:"replay,omitempty"`
}

type workbenchAgentInfo struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Command string `json:"command"`
	Enabled bool   `json:"enabled"`
}

type WorkbenchAgentInfo = workbenchAgentInfo

type workbenchFilesResponse struct {
	Path       string               `json:"path"`
	Parent     string               `json:"parent"`
	AllowPaths []string             `json:"allow_paths"`
	Entries    []workbenchFileEntry `json:"entries"`
}

type WorkbenchFilesResponse = workbenchFilesResponse

type workbenchFileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

type WorkbenchFileEntry = workbenchFileEntry

type workbenchFileResponse struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
}

type WorkbenchFileResponse = workbenchFileResponse

type workbenchWarmupResponse struct {
	Root       string `json:"root"`
	Dirs       int    `json:"dirs"`
	Files      int    `json:"files"`
	Skipped    int    `json:"skipped"`
	Truncated  bool   `json:"truncated"`
	DurationMS int64  `json:"duration_ms"`
}

type WorkbenchWarmupResponse = workbenchWarmupResponse

type workbenchDiffResponse struct {
	WorkDir string `json:"work_dir"`
	Status  string `json:"status"`
	Stat    string `json:"stat"`
	Diff    string `json:"diff"`
	Error   string `json:"error,omitempty"`
}

type WorkbenchDiffResponse = workbenchDiffResponse

func newWorkbenchManager(runtime Runtime, store *config.Store, edgeID, edgeName string, statePath string, logger *slog.Logger) *workbenchManager {
	if logger == nil {
		logger = slog.Default()
	}
	manager := &workbenchManager{
		runtime:   runtime,
		config:    store,
		edgeID:    edgeID,
		edgeName:  edgeName,
		logger:    logger,
		statePath: strings.TrimSpace(statePath),
		sessions:  make(map[string]*workbenchSession),
	}
	if err := manager.loadState(); err != nil {
		logger.Warn("load workbench sessions", "path", statePath, "error", err)
	}
	return manager
}

func (m *workbenchManager) getOrCreate(opts workbenchStartOptions) (*workbenchSession, bool, error) {
	if m.runtime == nil {
		return nil, false, errors.New("workbench runtime is not configured")
	}

	sessionID := cleanWorkbenchSessionID(opts.SessionID)
	m.mu.RLock()
	if sessionID != "" {
		if session := m.sessions[sessionID]; session != nil {
			m.mu.RUnlock()
			if normalizeTunnelAccount(session.account) != normalizeTunnelAccount(opts.Account) {
				return nil, false, errors.New("session does not belong to this account")
			}
			if session.isLive() {
				_ = session.Resize(opts.Rows, opts.Cols)
				session.touch()
				return session, false, nil
			}
			if m.resumeExistingSession(session, opts) {
				return session, false, nil
			}
			return session, false, nil
		}
	}
	m.mu.RUnlock()

	if sessionID == "" {
		sessionID = randomWorkbenchID()
	}

	resolved, err := m.resolveStart(opts)
	if err != nil {
		return nil, false, err
	}
	requestID := "wb-" + randomWorkbenchID()
	session := &workbenchSession{
		id:          sessionID,
		requestID:   requestID,
		account:     normalizeTunnelAccount(opts.Account),
		edgeID:      resolved.EdgeID,
		edgeName:    resolved.EdgeName,
		agent:       resolved.Agent.ID,
		workDir:     resolved.WorkDir,
		startedAt:   time.Now(),
		lastActive:  time.Now(),
		running:     true,
		attachments: make(map[uint64]func(workbenchServerMessage)),
	}

	interactive, err := m.runtime.StartInteractive(context.Background(), edge.ExecRequest{
		RequestID: requestID,
		SessionID: sessionID,
		User:      firstNonEmpty(normalizeTunnelAccount(opts.Account), "mobile"),
		EdgeID:    resolved.EdgeID,
		WorkDir:   resolved.WorkDir,
		Command:   resolved.Agent.Command,
		Args:      resolved.Args,
		Rows:      opts.Rows,
		Cols:      opts.Cols,
	}, edge.InteractiveOptions{
		Output: func(chunk []byte) {
			session.appendOutput(chunk)
			m.persistStateThrottled(2 * time.Second)
		},
	})
	if err != nil {
		return nil, false, err
	}
	session.interactive = interactive

	m.mu.Lock()
	if existing := m.sessions[sessionID]; existing != nil {
		m.mu.Unlock()
		session.Close()
		if existing.isLive() {
			_ = existing.Resize(opts.Rows, opts.Cols)
			existing.touch()
		}
		return existing, false, nil
	}
	m.sessions[sessionID] = session
	m.mu.Unlock()

	go func() {
		result := <-interactive.Done()
		session.finish(result)
		if session.isSubmitted() {
			m.persistState()
		}
	}()

	return session, true, nil
}

func (m *workbenchManager) resumeExistingSession(session *workbenchSession, opts workbenchStartOptions) bool {
	resumer, ok := m.runtime.(workbenchSessionResumer)
	if !ok {
		return false
	}
	snap := session.snapshot()
	if snap.Running {
		return false
	}
	workDir := firstNonEmpty(snap.WorkDir, opts.WorkDir)
	agent := firstNonEmpty(snap.Agent, normalizeWorkbenchAgentID(opts.Agent), "codex")
	requestID := firstNonEmpty(snap.RequestID, "wb-"+randomWorkbenchID())
	interactive, err := resumer.ResumeInteractive(context.Background(), edge.ExecRequest{
		RequestID: requestID,
		SessionID: snap.ID,
		User:      firstNonEmpty(normalizeTunnelAccount(opts.Account), normalizeTunnelAccount(snap.Account), "mobile"),
		EdgeID:    snap.EdgeID,
		WorkDir:   workDir,
		Command:   agent,
		Rows:      opts.Rows,
		Cols:      opts.Cols,
	}, edge.InteractiveOptions{
		Output: func(chunk []byte) {
			session.appendOutput(chunk)
			m.persistStateThrottled(2 * time.Second)
		},
	})
	if err != nil {
		return false
	}

	session.mu.Lock()
	if session.running && session.interactive != nil {
		session.mu.Unlock()
		interactive.Close()
		return true
	}
	session.requestID = requestID
	session.edgeID = firstNonEmpty(snap.EdgeID, m.edgeID)
	session.edgeName = firstNonEmpty(snap.EdgeName, m.edgeName, snap.EdgeID)
	session.agent = agent
	session.workDir = workDir
	session.lastActive = time.Now()
	session.running = true
	session.exitCode = 0
	session.duration = ""
	session.errText = ""
	session.closedByClient = false
	session.interactive = interactive
	session.mu.Unlock()

	go func() {
		result := <-interactive.Done()
		session.finish(result)
		if session.isSubmitted() {
			m.persistState()
		}
	}()
	return true
}

func (m *workbenchManager) resolveStart(opts workbenchStartOptions) (workbenchStartResolution, error) {
	if resolver, ok := m.runtime.(workbenchStartResolver); ok {
		return resolver.ResolveWorkbenchStart(opts)
	}

	cfg := m.config.Snapshot()
	policyCfg := cfg.Policy
	if m.policyResolver != nil {
		if resolved, err := m.policyResolver.UserPolicy(opts.Account, cfg.Policy); err == nil {
			policyCfg = resolved
		}
	}
	workDir, targetArgs, err := resolveWorkbenchTargetWithPolicy(cfg, policyCfg, opts.WorkDir, opts.Target)
	if err != nil {
		return workbenchStartResolution{}, err
	}
	agent, err := resolveWorkbenchAgentWithPolicy(policyCfg, opts.Agent)
	if err != nil {
		return workbenchStartResolution{}, err
	}
	return workbenchStartResolution{
		EdgeID:   m.edgeID,
		EdgeName: m.edgeName,
		Agent:    agent,
		WorkDir:  workDir,
		Target:   config.NormalizePath(opts.Target),
		Args:     targetArgs,
	}, nil
}

type workbenchAgent struct {
	ID      string
	Command string
}

func resolveWorkbenchAgent(cfg config.Config, requested string) (workbenchAgent, error) {
	return resolveWorkbenchAgentWithPolicy(cfg.Policy, requested)
}

func resolveWorkbenchAgentWithPolicy(policyCfg policy.Config, requested string) (workbenchAgent, error) {
	agentID := normalizeWorkbenchAgentID(requested)
	switch agentID {
	case "codex":
		return configuredWorkbenchAgent(policyCfg, "codex", "Codex")
	case "claude", "claude-code", "claude_code":
		return configuredWorkbenchAgent(policyCfg, "claude", "Claude Code")
	case "gemini":
		return configuredWorkbenchAgent(policyCfg, "gemini", "Gemini")
	default:
		return workbenchAgent{}, fmt.Errorf("unsupported agent %q", requested)
	}
}

func normalizeWorkbenchAgentID(requested string) string {
	agentID := strings.TrimSpace(strings.ToLower(requested))
	agentID = strings.ReplaceAll(agentID, "_", "-")
	if agentID == "" {
		return "codex"
	}
	if agentID == "claude-code" {
		return "claude"
	}
	return agentID
}

func configuredWorkbenchAgent(policyCfg policy.Config, id string, label string) (workbenchAgent, error) {
	rule, ok := policyCfg.Commands[id]
	if !ok || !rule.Enabled {
		return workbenchAgent{}, fmt.Errorf("%s is not enabled in command policy", label)
	}
	if !rule.Interactive {
		return workbenchAgent{}, fmt.Errorf("%s must enable interactive PTY in command policy", label)
	}
	return workbenchAgent{ID: id, Command: id}, nil
}

func listWorkbenchAgents(cfg config.Config) []workbenchAgentInfo {
	return listWorkbenchAgentsForPolicy(cfg.Policy)
}

func listWorkbenchAgentsForPolicy(policyCfg policy.Config) []workbenchAgentInfo {
	definitions := []workbenchAgentInfo{
		{ID: "codex", Label: "Codex", Command: "codex"},
		{ID: "claude", Label: "Claude Code", Command: "claude"},
		{ID: "gemini", Label: "Gemini", Command: "gemini"},
	}
	denied := make(map[string]struct{}, len(policyCfg.Deny))
	for _, command := range policyCfg.Deny {
		command = strings.TrimSpace(command)
		if command != "" {
			denied[command] = struct{}{}
		}
	}
	for index := range definitions {
		rule, ok := policyCfg.Commands[definitions[index].ID]
		if ok && strings.TrimSpace(rule.Bin) != "" {
			definitions[index].Command = strings.TrimSpace(rule.Bin)
		}
		_, isDenied := denied[definitions[index].ID]
		definitions[index].Enabled = ok && rule.Enabled && rule.Interactive && !isDenied
	}
	return definitions
}

func ListWorkbenchAgents(cfg config.Config) []workbenchAgentInfo {
	return listWorkbenchAgents(cfg)
}

func workbenchAgentLabel(agent string) string {
	switch strings.TrimSpace(strings.ToLower(agent)) {
	case "claude":
		return "Claude Code"
	case "gemini":
		return "Gemini"
	default:
		return "Codex"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func resolveWorkbenchTarget(cfg config.Config, requestedWorkDir string, requestedTarget string) (string, []string, error) {
	return resolveWorkbenchTargetWithPolicy(cfg, cfg.Policy, requestedWorkDir, requestedTarget)
}

func resolveWorkbenchTargetWithPolicy(cfg config.Config, policyCfg policy.Config, requestedWorkDir string, requestedTarget string) (string, []string, error) {
	workDir := config.NormalizePath(requestedWorkDir)
	if workDir == "" {
		workDir = defaultWorkbenchPath(cfg.Edge.WorkDir, policyCfg.AllowPaths, policyCfg.RequirePathMatch)
	}
	target := config.NormalizePath(requestedTarget)
	if target != "" {
		if !pathWithinAllowed(target, policyCfg.AllowPaths) {
			return "", nil, errors.New("target is outside allowed roots")
		}
		info, err := os.Stat(target)
		if err != nil {
			return "", nil, err
		}
		if info.IsDir() {
			workDir = target
			return workDir, nil, nil
		}
		workDir = filepath.Dir(target)
		return workDir, []string{filepath.Base(target)}, nil
	}
	if !pathWithinAllowed(workDir, policyCfg.AllowPaths) {
		return "", nil, errors.New("work_dir is outside allowed roots")
	}
	info, err := os.Stat(workDir)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		return "", nil, errors.New("work_dir is not a directory")
	}
	return workDir, nil, nil
}

func (m *workbenchManager) list(account string, allowPaths []string, requirePathMatch bool) []workbenchSessionInfo {
	account = normalizeTunnelAccount(account)
	m.mu.RLock()
	sessions := make([]*workbenchSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.RUnlock()

	items := make([]workbenchSessionInfo, 0, len(sessions))
	for _, session := range sessions {
		snap := session.snapshot()
		if account != "" && normalizeTunnelAccount(snap.Account) != account {
			continue
		}
		if !snap.Submitted {
			continue
		}
		if !workbenchSessionAllowedByPolicy(snap.WorkDir, allowPaths, requirePathMatch) {
			continue
		}
		items = append(items, workbenchSessionInfo{
			ID:         snap.ID,
			Account:    snap.Account,
			Agent:      snap.Agent,
			AgentLabel: workbenchAgentLabel(snap.Agent),
			WorkDir:    snap.WorkDir,
			StartedAt:  formatTime(snap.StartedAt),
			LastActive: formatTime(snap.LastActive),
			Submitted:  snap.Submitted,
			Title:      snap.Title,
			Running:    snap.Running,
			ExitCode:   snap.ExitCode,
			Duration:   snap.Duration,
			Error:      snap.Error,
		})
	}
	slices.SortFunc(items, func(a, b workbenchSessionInfo) int {
		return strings.Compare(b.LastActive, a.LastActive)
	})
	return items
}

func (m *workbenchManager) dispatchTunnelMessage(msg workbenchServerMessage) {
	sessionID := cleanWorkbenchSessionID(msg.SessionID)
	if sessionID == "" {
		return
	}
	m.mu.RLock()
	session := m.sessions[sessionID]
	m.mu.RUnlock()
	if session == nil {
		return
	}
	switch msg.Type {
	case "output":
		session.appendOutput([]byte(msg.Data))
		if session.isSubmitted() {
			m.persistStateThrottled(2 * time.Second)
		}
	case "submitted":
		title := msg.Title
		if title == "" {
			title = msg.Data
		}
		if session.markSubmitted(title) {
			snap := session.snapshot()
			msg.EdgeID = snap.EdgeID
			msg.EdgeName = snap.EdgeName
			msg.Agent = snap.Agent
			msg.AgentLabel = workbenchAgentLabel(snap.Agent)
			msg.WorkDir = snap.WorkDir
			msg.StartedAt = formatTime(snap.StartedAt)
			msg.LastActive = formatTime(snap.LastActive)
			msg.Running = snap.Running
			msg.Submitted = snap.Submitted
			msg.Title = snap.Title
			for _, callback := range session.callbacks() {
				callback(msg)
			}
			m.persistState()
		}
	case "exit":
		session.finish(edge.ExecResult{
			ExitCode: msg.ExitCode,
			Duration: msg.Duration,
			Error:    msg.Error,
			WorkDir:  msg.WorkDir,
		})
		if session.isSubmitted() {
			m.persistState()
		}
	}
}

func (m *workbenchManager) publishSession(snap workbenchSnapshot) {
	if !snap.Submitted {
		return
	}
	for _, callback := range m.sessionCallbacks(snap.ID) {
		callback(workbenchServerMessage{
			Type:       "submitted",
			SessionID:  snap.ID,
			EdgeID:     snap.EdgeID,
			EdgeName:   snap.EdgeName,
			Agent:      snap.Agent,
			AgentLabel: workbenchAgentLabel(snap.Agent),
			WorkDir:    snap.WorkDir,
			StartedAt:  formatTime(snap.StartedAt),
			LastActive: formatTime(snap.LastActive),
			Running:    snap.Running,
			Submitted:  snap.Submitted,
			Title:      snap.Title,
		})
	}
}

func (m *workbenchManager) sessionCallbacks(sessionID string) []func(workbenchServerMessage) {
	m.mu.RLock()
	session := m.sessions[sessionID]
	m.mu.RUnlock()
	if session == nil {
		return nil
	}
	return session.callbacks()
}

func (m *workbenchManager) loadState() error {
	if m.statePath == "" {
		return nil
	}
	content, err := os.ReadFile(m.statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var state workbenchPersistedState
	if err := json.Unmarshal(content, &state); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range state.Sessions {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		if m.sessions[item.ID] != nil {
			continue
		}
		startedAt := item.StartedAt
		if startedAt.IsZero() {
			startedAt = time.Now()
		}
		lastActive := item.LastActive
		if lastActive.IsZero() {
			lastActive = startedAt
		}
		errText := strings.TrimSpace(item.Error)
		if errText == workbenchRestartUnavailableError {
			errText = ""
		}
		session := &workbenchSession{
			id:          item.ID,
			requestID:   item.RequestID,
			account:     normalizeTunnelAccount(item.Account),
			edgeID:      firstNonEmpty(item.EdgeID, m.edgeID),
			edgeName:    firstNonEmpty(item.EdgeName, m.edgeName),
			agent:       firstNonEmpty(item.Agent, "codex"),
			workDir:     item.WorkDir,
			startedAt:   startedAt,
			lastActive:  lastActive,
			submitted:   item.Submitted || !item.StartedAt.IsZero() || item.Replay != "" || item.Duration != "" || item.Error != "",
			title:       strings.TrimSpace(item.Title),
			running:     false,
			exitCode:    item.ExitCode,
			duration:    item.Duration,
			errText:     errText,
			replay:      []byte(item.Replay),
			attachments: make(map[uint64]func(workbenchServerMessage)),
		}
		m.sessions[session.id] = session
	}
	return nil
}

func (m *workbenchManager) persistState() {
	if m.statePath == "" {
		return
	}
	if err := m.saveState(); err != nil {
		m.logger.Warn("save workbench sessions", "path", m.statePath, "error", err)
	}
}

func (m *workbenchManager) persistStateThrottled(interval time.Duration) {
	if m.statePath == "" {
		return
	}
	now := time.Now()
	m.mu.Lock()
	if !m.lastSave.IsZero() && now.Sub(m.lastSave) < interval {
		m.mu.Unlock()
		return
	}
	m.lastSave = now
	m.mu.Unlock()
	m.persistState()
}

func (m *workbenchManager) saveState() error {
	m.mu.Lock()
	m.lastSave = time.Now()
	m.mu.Unlock()
	state := workbenchPersistedState{
		Version:  1,
		SavedAt:  time.Now(),
		Sessions: m.persistedSessions(),
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(m.statePath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := m.statePath + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.statePath)
}

func (m *workbenchManager) persistedSessions() []workbenchPersistedSession {
	m.mu.RLock()
	sessions := make([]*workbenchSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.RUnlock()

	items := make([]workbenchPersistedSession, 0, len(sessions))
	for _, session := range sessions {
		snap := session.snapshot()
		if !snap.Submitted {
			continue
		}
		items = append(items, workbenchPersistedSession{
			ID:         snap.ID,
			RequestID:  snap.RequestID,
			Account:    snap.Account,
			EdgeID:     snap.EdgeID,
			EdgeName:   snap.EdgeName,
			Agent:      snap.Agent,
			WorkDir:    snap.WorkDir,
			StartedAt:  snap.StartedAt,
			LastActive: snap.LastActive,
			Submitted:  snap.Submitted,
			Title:      snap.Title,
			Running:    snap.Running,
			ExitCode:   snap.ExitCode,
			Duration:   snap.Duration,
			Error:      snap.Error,
			Replay:     snap.Replay,
		})
	}
	slices.SortFunc(items, func(a, b workbenchPersistedSession) int {
		return b.LastActive.Compare(a.LastActive)
	})
	return items
}

func (s *workbenchSession) attach(callback func(workbenchServerMessage)) (func(), workbenchSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.attachmentSeq++
	id := s.attachmentSeq
	s.lastActive = time.Now()
	s.attachments[id] = callback
	snapshot := s.snapshotLocked()

	return func() {
		s.mu.Lock()
		delete(s.attachments, id)
		s.lastActive = time.Now()
		s.mu.Unlock()
	}, snapshot
}

func (s *workbenchSession) snapshot() workbenchSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *workbenchSession) snapshotLocked() workbenchSnapshot {
	replay := make([]byte, len(s.replay))
	copy(replay, s.replay)
	replay, _ = splitCompleteUTF8(replay)
	return workbenchSnapshot{
		ID:         s.id,
		RequestID:  s.requestID,
		Account:    s.account,
		EdgeID:     s.edgeID,
		EdgeName:   s.edgeName,
		Agent:      s.agent,
		WorkDir:    s.workDir,
		StartedAt:  s.startedAt,
		LastActive: s.lastActive,
		Submitted:  s.submitted,
		Title:      s.title,
		Running:    s.running,
		ExitCode:   s.exitCode,
		Duration:   s.duration,
		Error:      s.errText,
		Replay:     validUTF8String(replay),
	}
}

func replayForAttach(snapshot workbenchSnapshot) string {
	if snapshot.Running || len(snapshot.Replay) <= workbenchHistoricalReplayMax {
		return snapshot.Replay
	}
	replay := trimUTF8Replay([]byte(snapshot.Replay), workbenchHistoricalReplayMax)
	return "\r\n\x1b[2m[showing last 128 KiB of saved session output]\x1b[0m\r\n" + validUTF8String(replay)
}

func (s *workbenchSession) touch() {
	s.mu.Lock()
	s.lastActive = time.Now()
	s.mu.Unlock()
}

func (s *workbenchSession) isLive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running && s.interactive != nil
}

func (s *workbenchSession) callbacks() []func(workbenchServerMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	callbacks := make([]func(workbenchServerMessage), 0, len(s.attachments))
	for _, callback := range s.attachments {
		callbacks = append(callbacks, callback)
	}
	return callbacks
}

func (s *workbenchSession) isSubmitted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submitted
}

func (s *workbenchSession) markSubmitted(title string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.submitted {
		return false
	}
	s.submitted = true
	s.title = cleanSessionTitle(title)
	s.lastActive = time.Now()
	return true
}

func (s *workbenchSession) captureSubmitTitle(data string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.submitted {
		return "", false
	}
	candidate, submitted, nextBuffer := updateSessionInputBuffer(s.inputBuffer, data)
	s.inputBuffer = nextBuffer
	if !submitted {
		return "", false
	}
	candidate = cleanSessionTitle(candidate)
	if candidate == "" {
		return "", false
	}
	s.submitted = true
	s.title = candidate
	s.lastActive = time.Now()
	return s.title, true
}

func (s *workbenchSession) Write(data []byte) error {
	return s.WriteWithTitle(data, "")
}

func (s *workbenchSession) WriteWithTitle(data []byte, title string) error {
	s.mu.Lock()
	running := s.running
	interactive := s.interactive
	s.lastActive = time.Now()
	s.mu.Unlock()
	if !running || interactive == nil {
		return errWorkbenchSessionNotRunning
	}
	if writer, ok := interactive.(interface {
		WriteWithTitle([]byte, string) error
	}); ok {
		return writer.WriteWithTitle(data, title)
	}
	return interactive.Write(data)
}

func (s *workbenchSession) Resize(rows, cols uint16) error {
	if rows == 0 || cols == 0 {
		return nil
	}
	s.mu.Lock()
	running := s.running
	interactive := s.interactive
	s.lastActive = time.Now()
	s.mu.Unlock()
	if !running || interactive == nil {
		return errWorkbenchSessionNotRunning
	}
	return interactive.Resize(rows, cols)
}

func (s *workbenchSession) Close() {
	s.mu.Lock()
	s.closedByClient = true
	s.lastActive = time.Now()
	s.mu.Unlock()
	if s.interactive != nil {
		s.interactive.Close()
	}
}

func (s *workbenchSession) appendOutput(chunk []byte) {
	if len(chunk) == 0 {
		return
	}

	callbacks, data := s.appendReplay(chunk)
	if data == "" {
		return
	}
	msg := workbenchServerMessage{Type: "output", SessionID: s.id, Data: data}
	for _, callback := range callbacks {
		callback(msg)
	}
}

func (s *workbenchSession) appendReplay(chunk []byte) ([]func(workbenchServerMessage), string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastActive = time.Now()
	s.replay = append(s.replay, chunk...)
	if len(s.replay) > workbenchReplayMax {
		s.replay = trimUTF8Replay(s.replay, workbenchReplayMax)
	}
	data := s.outputDecoder.Push(chunk)

	callbacks := make([]func(workbenchServerMessage), 0, len(s.attachments))
	for _, callback := range s.attachments {
		callbacks = append(callbacks, callback)
	}
	return callbacks, data
}

func (s *workbenchSession) finish(result edge.ExecResult) {
	s.finishedOnce.Do(func() {
		s.mu.Lock()
		finalOutput := s.outputDecoder.Flush()
		s.running = false
		s.exitCode = result.ExitCode
		s.duration = result.Duration
		s.errText = result.Error
		if s.closedByClient && s.errText == "" {
			s.errText = "session stopped"
		}
		s.lastActive = time.Now()
		callbacks := make([]func(workbenchServerMessage), 0, len(s.attachments))
		for _, callback := range s.attachments {
			callbacks = append(callbacks, callback)
		}
		s.mu.Unlock()

		if finalOutput != "" {
			outputMsg := workbenchServerMessage{Type: "output", SessionID: s.id, Data: finalOutput}
			for _, callback := range callbacks {
				callback(outputMsg)
			}
		}
		msg := workbenchServerMessage{
			Type:      "exit",
			SessionID: s.id,
			ExitCode:  result.ExitCode,
			Duration:  result.Duration,
			Error:     result.Error,
			Running:   false,
		}
		for _, callback := range callbacks {
			callback(msg)
		}
	})
}

func (s *Server) workbenchAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload workbenchAuthPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	session, err := s.accountStore().Login(payload.Username, payload.Password)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.setWorkbenchCookie(w, r, session.SessionID)
	s.setAccountCookie(w, r, session.SessionID)
	writeJSON(w, http.StatusOK, s.workbenchStatePayload(session.Username))
}

func (s *Server) accountRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.buckets != nil && !rateLimit(s.buckets.register, clientIP(r), w) {
		return
	}
	var payload accountAuthPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	authCfg := s.publicAuthSettingsConfig()
	email := strings.ToLower(strings.TrimSpace(payload.Email))
	code := strings.TrimSpace(payload.Code)

	// When the deployment requires email on registration, the user must also
	// have proven control of the address by supplying a verification code that
	// was just mailed to them via /accounts/register/send-code.
	if authCfg.RequireEmailOnRegister {
		if email == "" {
			http.Error(w, "email is required", http.StatusBadRequest)
			return
		}
		if !isPlausibleEmail(email) {
			http.Error(w, "email format is invalid", http.StatusBadRequest)
			return
		}
		if code == "" {
			http.Error(w, "code is required", http.StatusBadRequest)
			return
		}
		if s.accountStore().IsEmailUsed(email, "") {
			http.Error(w, "email is already used by another account", http.StatusConflict)
			return
		}
		if err := s.accountStore().ConsumeRegistrationCode(email, code); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	session, err := s.accountStore().Register(payload.Username, payload.Password, s.config.Snapshot().Policy)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "disabled") {
			status = http.StatusForbidden
		}
		if strings.Contains(err.Error(), "already exists") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}

	username := session.Username
	if email != "" {
		if err := s.accountStore().SetEmail(username, email); err != nil {
			s.logger.Warn("set email post-register", "username", username, "error", err)
		} else if err := s.accountStore().MarkEmailVerified(username, email); err != nil {
			s.logger.Warn("mark email verified post-register", "username", username, "error", err)
		}
	}

	s.setWorkbenchCookie(w, r, session.SessionID)
	s.setAccountCookie(w, r, session.SessionID)
	writeJSON(w, http.StatusOK, s.accountAuthResponse(username))
}

func (s *Server) accountLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload accountAuthPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	session, err := s.accountStore().Login(payload.Username, payload.Password)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	authCfg := s.publicAuthSettingsConfig()
	rec, ok := s.accountStore().AccountRecord(session.Username)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	role := s.accountRole(session.Username)
	if role != accountRoleAdmin {
		if rec.Email == "" && authCfg.RequireEmailOnRegister {
			// Old account without email — force back-fill on next step.
			s.accountStore().RevokeSession(session.SessionID)
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":             "email_required",
				"username":          session.Username,
				"requires_email":    true,
				"message":           "请补填邮箱以完成账户升级",
				"continue_password": payload.Password,
			})
			return
		}
		if rec.Email != "" && authCfg.RequireEmailVerifiedToLogin && !rec.EmailVerified {
			s.accountStore().RevokeSession(session.SessionID)
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error":           "email_not_verified",
				"username":        session.Username,
				"email":           rec.Email,
				"requires_verify": true,
				"message":         "请先完成邮箱验证",
			})
			return
		}
	}
	s.setWorkbenchCookie(w, r, session.SessionID)
	s.setAccountCookie(w, r, session.SessionID)
	writeJSON(w, http.StatusOK, s.accountAuthResponse(session.Username))
}

func (s *Server) accountLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if sessionID := accountRequestSessionID(r); sessionID != "" {
		s.accountStore().RevokeSession(sessionID)
	}
	s.clearWorkbenchCookie(w, r)
	s.clearAccountCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) accountMe(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accountIdentityFromRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.accountAuthResponse(identity.Username))
	case http.MethodPut:
		var payload accountProfileUpdatePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.accountStore().UpdatePassword(identity.Username, payload.CurrentPassword, payload.NewPassword); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, s.accountAuthResponse(identity.Username))
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// accountTunnelSession issues a fresh session for the logged-in account,
// returned to the caller so they can persist it on the agent's local
// config (cloud_tunnel.account / cloud_tunnel.session_id). The session is
// independent of the web-login cookie session and survives the user
// logging out of the admin UI.
func (s *Server) accountTunnelSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, ok := s.accountIdentityFromRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	session, err := s.accountStore().IssueSession(identity.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"username":   identity.Username,
		"session_id": session.SessionID,
	})
}

func (s *Server) accountAuthResponse(account string) map[string]any {
	rec, _ := s.accountStore().AccountRecord(account)
	return map[string]any{
		"username":             account,
		"role":                 s.accountRole(account),
		"registration_enabled": s.accountStore().RegistrationEnabled(),
		"state":                s.workbenchStatePayload(account),
		"email":                rec.Email,
		"email_verified":       rec.EmailVerified,
	}
}

func (s *Server) accountRole(account string) string {
	account = normalizeTunnelAccount(account)
	for _, item := range s.accountStore().List() {
		if item.Username == account {
			return item.Role
		}
	}
	return accountRoleUser
}

func (s *Server) setWorkbenchCookie(w http.ResponseWriter, r *http.Request, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     workbenchCookieName,
		Value:    strings.TrimSpace(value),
		Path:     "/",
		MaxAge:   30 * 24 * 60 * 60,
		HttpOnly: true,
		// Lax (not Strict) so the cookie survives OAuth's cross-site redirect
		// chain back to /user/. Strict drops cookies on the top-level GET that
		// follows a redirect from a different origin, which left users bouncing
		// back to /user/login.html after Google sign-in.
		SameSite: http.SameSiteLaxMode,
		Secure:   secureRequest(r),
	})
}

func (s *Server) setAccountCookie(w http.ResponseWriter, r *http.Request, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     accountCookieName,
		Value:    strings.TrimSpace(value),
		Path:     "/",
		MaxAge:   30 * 24 * 60 * 60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secureRequest(r),
	})
}

func (s *Server) workbenchLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if sessionID := accountRequestSessionID(r); sessionID != "" {
		s.accountStore().RevokeSession(sessionID)
	}
	s.clearWorkbenchCookie(w, r)
	s.clearAccountCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) clearWorkbenchCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     workbenchCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secureRequest(r),
	})
}

func (s *Server) clearAccountCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     accountCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secureRequest(r),
	})
}

func (s *Server) withWorkbench(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.workbenchAuthorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

type requestIdentity struct {
	Account string
}

func (s *Server) workbenchAuthorized(r *http.Request) bool {
	_, ok := s.workbenchIdentity(r)
	return ok
}

func (s *Server) workbenchIdentity(r *http.Request) (requestIdentity, bool) {
	sessionID := workbenchRequestSessionID(r)
	if account, ok := s.accountStore().ValidateSession(sessionID); ok {
		return requestIdentity{Account: account}, true
	}
	return requestIdentity{}, false
}

func (s *Server) accountFromRequest(r *http.Request) (string, bool) {
	identity, ok := s.accountIdentityFromRequest(r)
	return identity.Username, ok
}

func (s *Server) accountIdentityFromRequest(r *http.Request) (accountIdentity, bool) {
	sessionID := accountRequestSessionID(r)
	if sessionID == "" {
		if s != nil && s.logger != nil && r != nil && shouldDiagAuth(r.URL.Path) {
			s.logger.Warn("[OAUTH-DIAG] account auth: no session cookie",
				"path", r.URL.Path,
				"host", r.Host,
				"cookies", cookieNamesFromRequest(r))
		}
		return accountIdentity{}, false
	}
	account, ok := s.accountStore().ValidateSessionInfo(sessionID)
	if !ok {
		if s != nil && s.logger != nil && r != nil && shouldDiagAuth(r.URL.Path) {
			s.logger.Warn("[OAUTH-DIAG] account auth: cookie present but session invalid",
				"path", r.URL.Path,
				"session_id_prefix", safeSessionPrefix(sessionID))
		}
		return accountIdentity{}, false
	}
	return accountIdentity{Username: account.Username, Role: account.Role}, true
}

func shouldDiagAuth(path string) bool {
	switch {
	case path == "/user/", path == "/admin/", path == "/":
		return true
	case strings.HasPrefix(path, "/cloud-terminal-api/accounts/me"),
		strings.HasPrefix(path, "/cloud-terminal-api/user/"),
		strings.HasPrefix(path, "/cloud-terminal-api/workbench/"):
		return true
	}
	return false
}

func cookieNamesFromRequest(r *http.Request) []string {
	if r == nil {
		return nil
	}
	cookies := r.Cookies()
	names := make([]string, 0, len(cookies))
	for _, c := range cookies {
		names = append(names, c.Name)
	}
	return names
}

func workbenchRequestSessionID(r *http.Request) string {
	if cookie, err := r.Cookie(workbenchCookieName); err == nil {
		return cookie.Value
	}
	return ""
}

func accountRequestSessionID(r *http.Request) string {
	if cookie, err := r.Cookie(accountCookieName); err == nil {
		return cookie.Value
	}
	if cookie, err := r.Cookie(workbenchCookieName); err == nil {
		return cookie.Value
	}
	return ""
}

func (s *Server) workbenchState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _ := s.workbenchIdentity(r)
	writeJSON(w, http.StatusOK, s.workbenchStatePayload(identity.Account))
}

func (s *Server) workbenchStatePayload(account string) workbenchStatePayload {
	account = normalizeTunnelAccount(account)
	cfg := s.config.Snapshot()
	policyCfg := s.policyForAccount(account, cfg.Policy)
	archive := s.archiveStateForAccount(account, cfg.Policy)
	if s.runtimeIsTunnel() {
		edgeStatus, lastSeen := s.tunnel.statusForAccount(account)
		lastSeenStr := ""
		if !lastSeen.IsZero() {
			lastSeenStr = formatTime(lastSeen)
		}
		client := s.tunnelClientForAccount(account)
		if client == nil {
			payload := workbenchStatePayload{
				EdgeID:       s.edgeID,
				EdgeName:     s.edgeName,
				EdgeOnline:   false,
				EdgeStatus:   edgeStatus,
				LastSeen:     lastSeenStr,
				Tunnel:       true,
				WorkDir:      "",
				AllowPaths:   slices.Clone(policyCfg.AllowPaths),
				PreviewPorts: nil,
				Agents:       disabledWorkbenchAgents(),
				Sessions:     s.workbench.list(account, policyCfg.AllowPaths, policyCfg.RequirePathMatch),
			}
			return applyWorkbenchArchiveState(payload, archive)
		}
		info := client.info()
		edgePaths := filterAllowedPaths(policyCfg.AllowPaths, info.allowPaths)
		workDir := defaultWorkbenchPath(info.workDir, edgePaths, policyCfg.RequirePathMatch)
		payload := workbenchStatePayload{
			EdgeID:       info.edgeID,
			EdgeName:     info.edgeName,
			EdgeOnline:   true,
			EdgeStatus:   edgeStatus,
			LastSeen:     lastSeenStr,
			Tunnel:       true,
			WorkDir:      workDir,
			AllowPaths:   slices.Clone(policyCfg.AllowPaths),
			EdgePaths:    edgePaths,
			PreviewPorts: info.previewPorts,
			Agents:       filterWorkbenchAgentsForPolicy(info.agents, policyCfg),
			Sessions:     mergeWorkbenchSessions(s.workbench.list(account, edgePaths, policyCfg.RequirePathMatch), filterWorkbenchSessionsToPolicy(info.sessions, edgePaths, policyCfg.RequirePathMatch), account),
		}
		return applyWorkbenchArchiveState(payload, archive)
	}
	payload := workbenchStatePayload{
		EdgeID:       s.edgeID,
		EdgeName:     s.edgeName,
		EdgeOnline:   true,
		EdgeStatus:   "online",
		Tunnel:       false,
		WorkDir:      defaultWorkbenchPath(cfg.Edge.WorkDir, policyCfg.AllowPaths, policyCfg.RequirePathMatch),
		AllowPaths:   slices.Clone(policyCfg.AllowPaths),
		PreviewPorts: slices.Clone(cfg.Edge.PreviewPorts),
		Agents:       listWorkbenchAgentsForPolicy(policyCfg),
		Sessions:     s.workbench.list(account, policyCfg.AllowPaths, policyCfg.RequirePathMatch),
	}
	return applyWorkbenchArchiveState(payload, archive)
}

type workbenchArchiveState struct {
	ArchivedFolders   []string
	ArchivedSessions  []string
	ForgottenFolders  []string
	ForgottenSessions []string
}

func (s *Server) archiveStateForAccount(account string, global policy.Config) workbenchArchiveState {
	if s.accountStore() == nil || account == "" {
		return workbenchArchiveState{}
	}
	settings, err := s.accountStore().UserSettings(account, global)
	if err != nil {
		return workbenchArchiveState{}
	}
	settings = normalizeUserSettings(settings)
	return workbenchArchiveState{
		ArchivedFolders:   slices.Clone(settings.ArchivedFolders),
		ArchivedSessions:  slices.Clone(settings.ArchivedSessions),
		ForgottenFolders:  slices.Clone(settings.ForgottenFolders),
		ForgottenSessions: slices.Clone(settings.ForgottenSessions),
	}
}

func applyWorkbenchArchiveState(payload workbenchStatePayload, archive workbenchArchiveState) workbenchStatePayload {
	payload.ArchivedFolders = slices.Clone(archive.ArchivedFolders)
	payload.ArchivedSessions = slices.Clone(archive.ArchivedSessions)
	payload.ForgottenFolders = slices.Clone(archive.ForgottenFolders)
	payload.ForgottenSessions = slices.Clone(archive.ForgottenSessions)

	archivedFolderSet := stringSet(archive.ArchivedFolders)
	archivedSessionSet := stringSet(archive.ArchivedSessions)
	forgottenFolderSet := stringSet(archive.ForgottenFolders)
	forgottenSessionSet := stringSet(archive.ForgottenSessions)

	visible := make([]workbenchSessionInfo, 0, len(payload.Sessions))
	archivedItems := make([]workbenchSessionInfo, 0)
	for _, session := range payload.Sessions {
		if _, ok := forgottenSessionSet[session.ID]; ok {
			continue
		}
		if _, ok := forgottenFolderSet[session.WorkDir]; ok {
			continue
		}
		if _, ok := archivedSessionSet[session.ID]; ok {
			archivedItems = append(archivedItems, session)
			continue
		}
		if _, ok := archivedFolderSet[session.WorkDir]; ok {
			continue
		}
		visible = append(visible, session)
	}
	payload.Sessions = visible
	payload.ArchivedSessionItems = archivedItems
	return payload
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func (s *Server) policyForAccount(account string, global policy.Config) policy.Config {
	if s.accountStore() == nil {
		return global
	}
	resolved, err := s.accountStore().UserPolicy(account, global)
	if err != nil {
		return global
	}
	return resolved
}

func disabledWorkbenchAgents() []workbenchAgentInfo {
	return []workbenchAgentInfo{
		{ID: "codex", Label: "Codex", Command: "codex", Enabled: false},
		{ID: "claude", Label: "Claude Code", Command: "claude", Enabled: false},
		{ID: "gemini", Label: "Gemini", Command: "gemini", Enabled: false},
	}
}

func (s *Server) publishTunnelSession(snap workbenchSnapshot) {
	if !snap.Submitted {
		return
	}
	client := s.tunnelClientForAccount(snap.Account)
	if client == nil {
		return
	}
	client.updateSession(workbenchSessionInfo{
		ID:         snap.ID,
		Account:    snap.Account,
		Agent:      snap.Agent,
		AgentLabel: workbenchAgentLabel(snap.Agent),
		WorkDir:    snap.WorkDir,
		StartedAt:  formatTime(snap.StartedAt),
		LastActive: formatTime(snap.LastActive),
		Submitted:  snap.Submitted,
		Title:      snap.Title,
		Running:    snap.Running,
		ExitCode:   snap.ExitCode,
		Duration:   snap.Duration,
		Error:      snap.Error,
	})
}

func filterWorkbenchAgentsForPolicy(agents []workbenchAgentInfo, policyCfg policy.Config) []workbenchAgentInfo {
	allowed := listWorkbenchAgentsForPolicy(policyCfg)
	allowedByID := make(map[string]workbenchAgentInfo, len(allowed))
	for _, agent := range allowed {
		allowedByID[normalizeWorkbenchAgentID(agent.ID)] = agent
	}
	filtered := make([]workbenchAgentInfo, 0, len(agents))
	seen := make(map[string]struct{}, len(agents))
	for _, agent := range agents {
		id := normalizeWorkbenchAgentID(agent.ID)
		limit, ok := allowedByID[id]
		if !ok {
			continue
		}
		agent.ID = id
		agent.Enabled = agent.Enabled && limit.Enabled
		if strings.TrimSpace(agent.Label) == "" {
			agent.Label = limit.Label
		}
		filtered = append(filtered, agent)
		seen[id] = struct{}{}
	}
	for _, limit := range allowed {
		id := normalizeWorkbenchAgentID(limit.ID)
		if _, ok := seen[id]; ok {
			continue
		}
		filtered = append(filtered, workbenchAgentInfo{
			ID:      id,
			Label:   limit.Label,
			Command: limit.Command,
			Enabled: false,
		})
	}
	return filtered
}

func mergeWorkbenchSessions(primary []workbenchSessionInfo, secondary []workbenchSessionInfo, account string) []workbenchSessionInfo {
	if len(secondary) == 0 {
		return primary
	}
	account = normalizeTunnelAccount(account)
	seen := make(map[string]int, len(primary)+len(secondary))
	merged := make([]workbenchSessionInfo, 0, len(primary)+len(secondary))
	for _, item := range primary {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		seen[item.ID] = len(merged)
		merged = append(merged, item)
	}
	for _, item := range secondary {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		if !item.Submitted {
			continue
		}
		if account != "" && normalizeTunnelAccount(item.Account) != account {
			continue
		}
		if index, ok := seen[item.ID]; ok {
			if item.Running && !merged[index].Running {
				merged[index] = item
			}
			continue
		}
		seen[item.ID] = len(merged)
		merged = append(merged, item)
	}
	slices.SortFunc(merged, func(a, b workbenchSessionInfo) int {
		return strings.Compare(b.LastActive, a.LastActive)
	})
	return merged
}

func filterWorkbenchSessionsToPolicy(sessions []workbenchSessionInfo, allowPaths []string, requirePathMatch bool) []workbenchSessionInfo {
	filtered := make([]workbenchSessionInfo, 0, len(sessions))
	for _, item := range sessions {
		if !item.Submitted {
			continue
		}
		if workbenchSessionAllowedByPolicy(item.WorkDir, allowPaths, requirePathMatch) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func workbenchSessionAllowedByPolicy(workDir string, allowPaths []string, requirePathMatch bool) bool {
	if len(allowPaths) == 0 {
		return !requirePathMatch
	}
	return pathWithinAllowed(workDir, allowPaths)
}

func cleanSessionTitle(value string) string {
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

func updateSessionInputBuffer(buffer string, data string) (string, bool, string) {
	if data == "" {
		return "", false, trimSessionInputBuffer(buffer)
	}
	var submitted strings.Builder
	var next strings.Builder
	next.WriteString(buffer)
	for _, r := range data {
		switch r {
		case '\r', '\n':
			candidate := cleanSessionTitle(next.String())
			if candidate != "" {
				submitted.WriteString(candidate)
				return submitted.String(), true, ""
			}
			next.Reset()
		case '\b', 0x7f:
			current := []rune(next.String())
			if len(current) > 0 {
				next.Reset()
				next.WriteString(string(current[:len(current)-1]))
			}
		default:
			if r < 0x20 || r == 0x7f {
				continue
			}
			next.WriteRune(r)
		}
	}
	return "", false, trimSessionInputBuffer(next.String())
}

func trimSessionInputBuffer(value string) string {
	runes := []rune(value)
	if len(runes) <= 256 {
		return value
	}
	return string(runes[len(runes)-256:])
}

func filterWorkbenchFileEntriesToPolicy(entries []workbenchFileEntry, allowPaths []string) []workbenchFileEntry {
	out := entries[:0]
	for _, entry := range entries {
		if pathWithinAllowed(entry.Path, allowPaths) {
			out = append(out, entry)
		}
	}
	return out
}

func firstAllowedPath(preferred string, allowPaths []string) string {
	preferred = config.NormalizePath(preferred)
	if preferred != "" && pathWithinAllowed(preferred, allowPaths) {
		return preferred
	}
	for _, path := range allowPaths {
		path = config.NormalizePath(path)
		if path != "" {
			return path
		}
	}
	return ""
}

func defaultWorkbenchPath(preferred string, allowPaths []string, requirePathMatch bool) string {
	if requirePathMatch {
		for _, path := range allowPaths {
			path = config.NormalizePath(path)
			if path != "" {
				return path
			}
		}
		return ""
	}
	if path := firstAllowedPath(preferred, allowPaths); path != "" {
		return path
	}
	return config.NormalizePath(preferred)
}

func (s *Server) workbenchWS(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.workbenchIdentity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	upgrader := websocket.Upgrader{
		ReadBufferSize:  8192,
		WriteBufferSize: 8192,
		CheckOrigin:     s.checkOrigin,
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Warn("upgrade workbench websocket", "error", err)
		return
	}
	defer conn.Close()

	rows := queryUint16(r, "rows")
	cols := queryUint16(r, "cols")
	session, _, err := s.workbench.getOrCreate(workbenchStartOptions{
		SessionID: r.URL.Query().Get("session_id"),
		Account:   identity.Account,
		Agent:     r.URL.Query().Get("agent"),
		WorkDir:   r.URL.Query().Get("work_dir"),
		Target:    r.URL.Query().Get("target"),
		Rows:      rows,
		Cols:      cols,
	})
	if err != nil {
		_ = conn.WriteJSON(workbenchServerMessage{Type: "error", Error: err.Error()})
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()),
			time.Now().Add(time.Second),
		)
		return
	}

	var writeMu sync.Mutex
	send := func(msg workbenchServerMessage) {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := conn.WriteJSON(msg); err != nil {
			s.logger.Debug("write workbench websocket", "error", err)
		}
	}

	var pendingMu sync.Mutex
	ready := false
	var pending []workbenchServerMessage
	detach, snapshot := session.attach(func(msg workbenchServerMessage) {
		pendingMu.Lock()
		if !ready {
			pending = append(pending, msg)
			pendingMu.Unlock()
			return
		}
		pendingMu.Unlock()
		send(msg)
	})
	defer detach()

	send(workbenchServerMessage{
		Type:       "ready",
		SessionID:  snapshot.ID,
		EdgeID:     snapshot.EdgeID,
		EdgeName:   snapshot.EdgeName,
		Agent:      snapshot.Agent,
		AgentLabel: workbenchAgentLabel(snapshot.Agent),
		WorkDir:    snapshot.WorkDir,
		StartedAt:  formatTime(snapshot.StartedAt),
		LastActive: formatTime(snapshot.LastActive),
		Running:    snapshot.Running,
		Submitted:  snapshot.Submitted,
		Title:      snapshot.Title,
	})
	if replay := replayForAttach(snapshot); replay != "" {
		send(workbenchServerMessage{Type: "replay", SessionID: snapshot.ID, Data: replay})
	}
	if !snapshot.Running {
		send(workbenchServerMessage{
			Type:      "exit",
			SessionID: snapshot.ID,
			ExitCode:  snapshot.ExitCode,
			Duration:  snapshot.Duration,
			Error:     snapshot.Error,
			Running:   false,
		})
	}

	pendingMu.Lock()
	ready = true
	queued := append([]workbenchServerMessage(nil), pending...)
	pending = nil
	pendingMu.Unlock()
	for _, msg := range queued {
		send(msg)
	}

	for {
		var msg workbenchClientMessage
		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				s.logger.Debug("read workbench websocket", "error", err)
			}
			return
		}

		switch msg.Type {
		case "input":
			if !session.isLive() {
				send(workbenchServerMessage{Type: "error", SessionID: snapshot.ID, Error: errWorkbenchSessionNotRunning.Error()})
				continue
			}
			title := ""
			if nextTitle, ok := session.captureSubmitTitle(msg.Data); ok {
				title = nextTitle
				snapshot = session.snapshot()
				submitted := workbenchServerMessage{
					Type:       "submitted",
					SessionID:  snapshot.ID,
					EdgeID:     snapshot.EdgeID,
					EdgeName:   snapshot.EdgeName,
					Agent:      snapshot.Agent,
					AgentLabel: workbenchAgentLabel(snapshot.Agent),
					WorkDir:    snapshot.WorkDir,
					StartedAt:  formatTime(snapshot.StartedAt),
					LastActive: formatTime(snapshot.LastActive),
					Running:    snapshot.Running,
					Submitted:  snapshot.Submitted,
					Title:      title,
				}
				send(submitted)
				s.workbench.publishSession(snapshot)
				if s.runtimeIsTunnel() {
					s.publishTunnelSession(snapshot)
				}
			}
			if err := session.WriteWithTitle([]byte(msg.Data), title); err != nil {
				send(workbenchServerMessage{Type: "error", SessionID: snapshot.ID, Error: err.Error()})
			}
		case "resize":
			if !session.isLive() {
				continue
			}
			if err := session.Resize(msg.Rows, msg.Cols); err != nil {
				send(workbenchServerMessage{Type: "error", SessionID: snapshot.ID, Error: err.Error()})
			}
		case "stop":
			if session.isLive() {
				session.Close()
			}
		case "ping":
			send(workbenchServerMessage{Type: "pong", SessionID: snapshot.ID})
		default:
			send(workbenchServerMessage{Type: "error", SessionID: snapshot.ID, Error: "unsupported message type"})
		}
	}
}

func (s *Server) workbenchFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _ := s.workbenchIdentity(r)
	if s.runtimeIsTunnel() {
		client := s.tunnelClientForAccount(identity.Account)
		if client == nil {
			http.Error(w, tunnelUnavailable().Error(), http.StatusServiceUnavailable)
			return
		}
		cfg := s.config.Snapshot()
		policyCfg := s.policyForAccount(identity.Account, cfg.Policy)
		requestedPath := config.NormalizePath(r.URL.Query().Get("path"))
		info := client.info()
		if requestedPath == "" {
			requestedPath = defaultWorkbenchPath(info.workDir, policyCfg.AllowPaths, policyCfg.RequirePathMatch)
		}
		if !pathWithinAllowed(requestedPath, policyCfg.AllowPaths) {
			http.Error(w, "path is outside allowed roots", http.StatusForbidden)
			return
		}
		var response workbenchFilesResponse
		if err := client.request(r.Context(), "files", tunnelFilesRequest{
			Path:             requestedPath,
			AllowPaths:       slices.Clone(policyCfg.AllowPaths),
			RequirePathMatch: policyCfg.RequirePathMatch,
		}, &response); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		response.AllowPaths = slices.Clone(policyCfg.AllowPaths)
		response.Entries = filterWorkbenchFileEntriesToPolicy(response.Entries, policyCfg.AllowPaths)
		if response.Parent != "" && !pathWithinAllowed(response.Parent, policyCfg.AllowPaths) {
			response.Parent = ""
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	cfg := s.config.Snapshot()
	policyCfg := s.policyForAccount(identity.Account, cfg.Policy)
	path := config.NormalizePath(r.URL.Query().Get("path"))
	if path == "" {
		path = defaultWorkbenchPath(cfg.Edge.WorkDir, policyCfg.AllowPaths, policyCfg.RequirePathMatch)
	}
	if !pathWithinAllowed(path, policyCfg.AllowPaths) {
		http.Error(w, "path is outside allowed roots", http.StatusForbidden)
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	items := make([]workbenchFileEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		items = append(items, workbenchFileEntry{
			Name:    entry.Name(),
			Path:    filepath.Join(path, entry.Name()),
			IsDir:   entry.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}
	slices.SortFunc(items, func(a, b workbenchFileEntry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	parent := parentPath(path)
	if parent != "" && !pathWithinAllowed(parent, policyCfg.AllowPaths) {
		parent = ""
	}
	writeJSON(w, http.StatusOK, workbenchFilesResponse{
		Path:       path,
		Parent:     parent,
		AllowPaths: slices.Clone(policyCfg.AllowPaths),
		Entries:    items,
	})
}

func (s *Server) workbenchFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _ := s.workbenchIdentity(r)
	if s.runtimeIsTunnel() {
		client := s.tunnelClientForAccount(identity.Account)
		if client == nil {
			http.Error(w, tunnelUnavailable().Error(), http.StatusServiceUnavailable)
			return
		}
		cfg := s.config.Snapshot()
		policyCfg := s.policyForAccount(identity.Account, cfg.Policy)
		path := config.NormalizePath(r.URL.Query().Get("path"))
		if path == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		if !pathWithinAllowed(path, policyCfg.AllowPaths) {
			http.Error(w, "path is outside allowed roots", http.StatusForbidden)
			return
		}
		var response workbenchFileResponse
		if err := client.request(r.Context(), "file", tunnelFileRequest{
			Path:             path,
			AllowPaths:       slices.Clone(policyCfg.AllowPaths),
			RequirePathMatch: policyCfg.RequirePathMatch,
		}, &response); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	cfg := s.config.Snapshot()
	policyCfg := s.policyForAccount(identity.Account, cfg.Policy)
	path := config.NormalizePath(r.URL.Query().Get("path"))
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if !pathWithinAllowed(path, policyCfg.AllowPaths) {
		http.Error(w, "path is outside allowed roots", http.StatusForbidden)
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}

	content, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	truncated := false
	if len(content) > workbenchFileMax {
		content = content[:workbenchFileMax]
		truncated = true
	}
	writeJSON(w, http.StatusOK, workbenchFileResponse{
		Path:      path,
		Name:      filepath.Base(path),
		Content:   string(content),
		Size:      info.Size(),
		Truncated: truncated,
	})
}

func (s *Server) workbenchWarmup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _ := s.workbenchIdentity(r)
	if s.runtimeIsTunnel() {
		client := s.tunnelClientForAccount(identity.Account)
		if client == nil {
			http.Error(w, tunnelUnavailable().Error(), http.StatusServiceUnavailable)
			return
		}
		cfg := s.config.Snapshot()
		policyCfg := s.policyForAccount(identity.Account, cfg.Policy)
		path := config.NormalizePath(r.URL.Query().Get("path"))
		info := client.info()
		if path == "" {
			path = defaultWorkbenchPath(info.workDir, policyCfg.AllowPaths, policyCfg.RequirePathMatch)
		}
		if !pathWithinAllowed(path, policyCfg.AllowPaths) {
			http.Error(w, "path is outside allowed roots", http.StatusForbidden)
			return
		}
		var response workbenchWarmupResponse
		if err := client.request(r.Context(), "warmup", tunnelWarmupRequest{
			Path:             path,
			AllowPaths:       slices.Clone(policyCfg.AllowPaths),
			RequirePathMatch: policyCfg.RequirePathMatch,
		}, &response); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	cfg := s.config.Snapshot()
	policyCfg := s.policyForAccount(identity.Account, cfg.Policy)
	root := config.NormalizePath(r.URL.Query().Get("path"))
	if root == "" {
		root = defaultWorkbenchPath(cfg.Edge.WorkDir, policyCfg.AllowPaths, policyCfg.RequirePathMatch)
	}
	if !pathWithinAllowed(root, policyCfg.AllowPaths) {
		http.Error(w, "path is outside allowed roots", http.StatusForbidden)
		return
	}
	info, err := os.Stat(root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !info.IsDir() {
		root = filepath.Dir(root)
	}

	start := time.Now()
	response := warmupWorkbenchTree(r.Context(), root, policyCfg.AllowPaths)
	response.DurationMS = time.Since(start).Milliseconds()
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) workbenchDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _ := s.workbenchIdentity(r)
	if s.runtimeIsTunnel() {
		client := s.tunnelClientForAccount(identity.Account)
		if client == nil {
			http.Error(w, tunnelUnavailable().Error(), http.StatusServiceUnavailable)
			return
		}
		cfg := s.config.Snapshot()
		policyCfg := s.policyForAccount(identity.Account, cfg.Policy)
		workDir := config.NormalizePath(r.URL.Query().Get("work_dir"))
		path := config.NormalizePath(r.URL.Query().Get("path"))
		info := client.info()
		if workDir == "" {
			workDir = defaultWorkbenchPath(info.workDir, policyCfg.AllowPaths, policyCfg.RequirePathMatch)
		}
		if !pathWithinAllowed(workDir, policyCfg.AllowPaths) {
			http.Error(w, "work_dir is outside allowed roots", http.StatusForbidden)
			return
		}
		if path != "" {
			if !pathWithinAllowed(path, policyCfg.AllowPaths) {
				http.Error(w, "path is outside allowed roots", http.StatusForbidden)
				return
			}
			rel, err := filepath.Rel(workDir, path)
			if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
				http.Error(w, "path is outside work_dir", http.StatusForbidden)
				return
			}
		}
		var response workbenchDiffResponse
		if err := client.request(r.Context(), "diff", tunnelDiffRequest{
			WorkDir:          workDir,
			Path:             path,
			AllowPaths:       slices.Clone(policyCfg.AllowPaths),
			RequirePathMatch: policyCfg.RequirePathMatch,
		}, &response); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	cfg := s.config.Snapshot()
	policyCfg := s.policyForAccount(identity.Account, cfg.Policy)
	workDir := config.NormalizePath(r.URL.Query().Get("work_dir"))
	if workDir == "" {
		workDir = defaultWorkbenchPath(cfg.Edge.WorkDir, policyCfg.AllowPaths, policyCfg.RequirePathMatch)
	}
	if !pathWithinAllowed(workDir, policyCfg.AllowPaths) {
		http.Error(w, "work_dir is outside allowed roots", http.StatusForbidden)
		return
	}

	var relPath string
	if rawPath := strings.TrimSpace(r.URL.Query().Get("path")); rawPath != "" {
		path := config.NormalizePath(rawPath)
		if !pathWithinAllowed(path, policyCfg.AllowPaths) {
			http.Error(w, "path is outside allowed roots", http.StatusForbidden)
			return
		}
		rel, err := filepath.Rel(workDir, path)
		if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			http.Error(w, "path is outside work_dir", http.StatusForbidden)
			return
		}
		relPath = rel
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	status, statusErr := runGit(ctx, workDir, "status", "--short")
	statArgs := []string{"diff", "--stat"}
	diffArgs := []string{"diff", "--"}
	if relPath != "" {
		statArgs = append(statArgs, "--", relPath)
		diffArgs = []string{"diff", "--", relPath}
	}
	stat, _ := runGit(ctx, workDir, statArgs...)
	diff, _ := runGit(ctx, workDir, diffArgs...)

	response := workbenchDiffResponse{
		WorkDir: workDir,
		Status:  trimOutput(status, workbenchDiffMax/2),
		Stat:    trimOutput(stat, workbenchDiffMax/2),
		Diff:    trimOutput(diff, workbenchDiffMax),
	}
	if statusErr != nil {
		response.Error = statusErr.Error()
		if status != "" {
			response.Error += ": " + strings.TrimSpace(status)
		}
	}
	writeJSON(w, http.StatusOK, response)
}

const previewBodyMax = 20 << 20 // 20 MiB cap on both request and response bodies

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

func (s *Server) workbenchPreview(w http.ResponseWriter, r *http.Request) {
	portValue := strings.TrimSpace(r.URL.Query().Get("port"))
	if portValue == "" {
		http.Error(w, "port is required", http.StatusBadRequest)
		return
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port <= 0 || port > 65535 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	proxyPath := r.URL.Query().Get("path")
	if proxyPath == "" {
		proxyPath = "/"
	}
	if !strings.HasPrefix(proxyPath, "/") {
		proxyPath = "/" + proxyPath
	}
	s.dispatchPreview(w, r, port, proxyPath, r.URL.Query().Get("query"))
}

func (s *Server) workbenchPreviewPath(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/preview/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "port is required", http.StatusBadRequest)
		return
	}
	port, err := strconv.Atoi(parts[0])
	if err != nil || port <= 0 || port > 65535 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	proxyPath := "/"
	if len(parts) == 2 && parts[1] != "" {
		proxyPath += parts[1]
	}
	s.dispatchPreview(w, r, port, proxyPath, r.URL.RawQuery)
}

// dispatchPreview picks the right backend for the preview:
//   - tunnel mode: forward the request through the requesting user's tunnel to
//     the agent, which dials 127.0.0.1:port on the user's own machine.
//   - local mode: keep the original reverse-proxy to the gateway's own loopback
//     (single-machine deployments only).
func (s *Server) dispatchPreview(w http.ResponseWriter, r *http.Request, port int, proxyPath string, proxyQuery string) {
	if s.runtimeIsTunnel() {
		identity, _ := s.workbenchIdentity(r)
		client := s.tunnelClientForAccount(identity.Account)
		if client == nil {
			http.Error(w, tunnelUnavailable().Error(), http.StatusServiceUnavailable)
			return
		}
		if !previewPortAllowedForClient(client, port) {
			http.Error(w, "port is not allowlisted", http.StatusForbidden)
			return
		}
		s.servePreviewTunnel(w, r, client, port, proxyPath, proxyQuery)
		return
	}

	if !s.previewPortAllowed(port) {
		http.Error(w, "port is not allowlisted", http.StatusForbidden)
		return
	}
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	s.servePreviewProxy(w, r, target, proxyPath, proxyQuery)
}

// servePreviewTunnel forwards a preview HTTP request through the user's tunnel.
// Body is buffered both ways (capped at previewBodyMax) — streaming is out of
// scope for now and WebSocket upgrades are rejected.
func (s *Server) servePreviewTunnel(w http.ResponseWriter, r *http.Request, client *tunnelClient, port int, proxyPath string, proxyQuery string) {
	if isWebSocketUpgrade(r) {
		http.Error(w, "websocket preview is not supported over tunnel", http.StatusBadGateway)
		return
	}

	body, err := readLimitedBody(r.Body, previewBodyMax)
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	_ = r.Body.Close()

	fullPath := proxyPath
	if proxyQuery != "" {
		fullPath += "?" + proxyQuery
	}

	headers := cloneHeaderForProxy(r.Header)

	var resp tunnelPreviewResponse
	if err := client.request(r.Context(), "preview", tunnelPreviewRequest{
		Port:    port,
		Method:  r.Method,
		Path:    fullPath,
		Headers: headers,
		Body:    body,
	}, &resp); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	dst := w.Header()
	for key, values := range resp.Headers {
		lower := strings.ToLower(key)
		if _, hop := previewHopByHopHeaders[lower]; hop {
			continue
		}
		if lower == "x-frame-options" || lower == "content-security-policy" {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if len(resp.Body) > 0 {
		_, _ = w.Write(resp.Body)
	}
}

func readLimitedBody(r io.Reader, max int64) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	limited := io.LimitReader(r, max+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("preview body exceeds %d bytes", max)
	}
	return data, nil
}

func cloneHeaderForProxy(src http.Header) map[string][]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string][]string, len(src))
	for key, values := range src {
		lower := strings.ToLower(key)
		if _, hop := previewHopByHopHeaders[lower]; hop {
			continue
		}
		if lower == "cookie" || lower == "host" || lower == "content-length" {
			continue
		}
		out[key] = append([]string(nil), values...)
	}
	return out
}

func isWebSocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Connection"), "upgrade") &&
		!strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		return false
	}
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func (s *Server) servePreviewProxy(w http.ResponseWriter, r *http.Request, target *url.URL, proxyPath string, proxyQuery string) {
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
		req.URL.Path = proxyPath
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		req.URL.RawQuery = proxyQuery
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Del("X-Frame-Options")
		response.Header.Del("Content-Security-Policy")
		return nil
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) previewPortAllowed(port int) bool {
	cfg := s.config.Snapshot()
	for _, allowed := range cfg.Edge.PreviewPorts {
		if allowed == port {
			return true
		}
	}
	return false
}

// previewPortAllowedForClient checks against the ports the agent itself
// announced in its tunnel hello — i.e. the user's own configuration.
func previewPortAllowedForClient(client *tunnelClient, port int) bool {
	if client == nil {
		return false
	}
	info := client.info()
	for _, allowed := range info.previewPorts {
		if allowed == port {
			return true
		}
	}
	return false
}

func runGit(ctx context.Context, workDir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", workDir}, args...)...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func warmupWorkbenchTree(ctx context.Context, root string, allowPaths []string) workbenchWarmupResponse {
	response := workbenchWarmupResponse{Root: root}
	seen := make(map[string]struct{})
	queue := []string{root}
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	for len(queue) > 0 {
		if deadline.Err() != nil {
			response.Truncated = true
			break
		}
		if response.Dirs+response.Files >= workbenchWarmupMax {
			response.Truncated = true
			break
		}

		dir := queue[0]
		queue = queue[1:]
		realDir, err := filepath.EvalSymlinks(dir)
		if err == nil {
			if _, ok := seen[realDir]; ok {
				continue
			}
			seen[realDir] = struct{}{}
		}
		if !pathWithinAllowed(dir, allowPaths) {
			response.Skipped++
			continue
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			response.Skipped++
			continue
		}
		response.Dirs++
		for _, entry := range entries {
			name := entry.Name()
			if skipWarmupEntry(name) {
				response.Skipped++
				continue
			}
			fullPath := filepath.Join(dir, name)
			if entry.IsDir() {
				queue = append(queue, fullPath)
				continue
			}
			response.Files++
			if response.Dirs+response.Files >= workbenchWarmupMax {
				response.Truncated = true
				break
			}
		}
	}

	return response
}

func skipWarmupEntry(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", ".next", ".nuxt", ".turbo", ".cache", "dist", "build", "target", "coverage", ".idea", ".vscode":
		return true
	}
	return strings.HasPrefix(name, ".DS_Store")
}

func trimOutput(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n...[truncated]"
}

func queryUint16(r *http.Request, key string) uint16 {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseUint(value, 10, 16)
	if err != nil {
		return 0
	}
	return uint16(parsed)
}

func cleanWorkbenchSessionID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 96 {
		return ""
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return ""
	}
	return value
}

func randomWorkbenchID() string {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("sess-%d", time.Now().UnixNano())
	}
	return strings.TrimRight(base64.RawURLEncoding.EncodeToString(raw[:]), "=")
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
