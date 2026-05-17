package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloud-terminal/internal/config"
	"cloud-terminal/internal/edge"
	"cloud-terminal/internal/policy"

	"github.com/gorilla/websocket"
)

type Runtime interface {
	ParseAndExec(context.Context, edge.ExecRequest, string) edge.ExecResult
	Exec(context.Context, edge.ExecRequest) edge.ExecResult
	ParseAndStartInteractive(context.Context, edge.ExecRequest, string, edge.InteractiveOptions) (InteractiveSession, error)
	StartInteractive(context.Context, edge.ExecRequest, edge.InteractiveOptions) (InteractiveSession, error)
}

type userPolicyRuntime interface {
	SetUserPolicyResolver(interface {
		UserPolicy(string, policy.Config) (policy.Config, error)
	})
}

type InteractiveSession interface {
	Write([]byte) error
	Resize(uint16, uint16) error
	Close()
	Done() <-chan edge.ExecResult
}

type Options struct {
	Runtime            Runtime
	StaticFS           fs.FS
	Config             *config.Store
	EdgeID             string
	EdgeName           string
	WorkbenchStatePath string
	Logger             *slog.Logger
}

type Server struct {
	runtime   Runtime
	staticFS  fs.FS
	config    *config.Store
	accountMu sync.RWMutex
	accounts  *accountStore
	edgeID    string
	edgeName  string
	logger    *slog.Logger
	workbench *workbenchManager
	tunnel    *tunnelHub
	counter   atomic.Uint64
}

func NewServer(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Config == nil {
		defaultConfig := config.Default()
		opts.Config = config.NewStore("", &defaultConfig)
	}
	cfg := opts.Config.Snapshot()
	if cfg.Server.AdminUsername == "" {
		cfg.Server.AdminUsername = "admin"
	}
	if cfg.Server.AdminPassword == "" {
		cfg.Server.AdminPassword = "admin123456"
	}
	accounts, err := newAccountStore(opts.Config.DatabasePath(), opts.Config.AccountStorePath(), opts.Config.AccountRegistrationEnabled(), cfg.Server.AdminUsername, cfg.Server.AdminPassword, cfg.Policy)
	if err != nil {
		opts.Logger.Warn("load account store", "path", opts.Config.DatabasePath(), "error", err)
		accounts = newFallbackAccountStore(opts.Config.DatabasePath(), opts.Config.AccountStorePath(), opts.Config.AccountRegistrationEnabled(), cfg.Server.AdminUsername)
		if err := accounts.ensureAdmin(cfg.Server.AdminUsername, cfg.Server.AdminPassword, cfg.Policy); err != nil {
			opts.Logger.Warn("ensure default admin", "error", err)
		}
	}
	tunnelHub := newTunnelHub(opts.Logger)
	tunnelHub.setConfigStore(opts.Config)
	tunnelHub.setDefaultAccount(cfg.CloudTunnel.Account)
	runtime := opts.Runtime
	if runtime == nil {
		runtime = newTunnelRuntime(tunnelHub)
	}
	if configurable, ok := runtime.(userPolicyRuntime); ok {
		configurable.SetUserPolicyResolver(accounts)
	}
	server := &Server{
		runtime:  runtime,
		staticFS: opts.StaticFS,
		config:   opts.Config,
		accounts: accounts,
		edgeID:   opts.EdgeID,
		edgeName: opts.EdgeName,
		logger:   opts.Logger,
		tunnel:   tunnelHub,
	}
	server.workbench = newWorkbenchManager(runtime, opts.Config, opts.EdgeID, opts.EdgeName, opts.WorkbenchStatePath, opts.Logger)
	server.workbench.policyResolver = accounts
	tunnelHub.setSessionSink(server.handleTunnelSessionMessage)
	return server
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/cloud-terminal-api/health", s.health)
	mux.HandleFunc("/cloud-terminal-api/discovery/gateway", s.discoveryGateway)
	mux.HandleFunc("/cloud-terminal-api/edge", s.withAuth(s.edgeInfo))
	mux.HandleFunc("/cloud-terminal-api/complete", s.withAuth(s.complete))
	mux.HandleFunc("/cloud-terminal-api/accounts/register", s.accountRegister)
	mux.HandleFunc("/cloud-terminal-api/accounts/login", s.accountLogin)
	mux.HandleFunc("/cloud-terminal-api/accounts/logout", s.accountLogout)
	mux.HandleFunc("/cloud-terminal-api/accounts/me", s.accountMe)
	mux.HandleFunc("/cloud-terminal-api/user/settings", s.withAccount(s.userSettings))
	mux.HandleFunc("/cloud-terminal-api/user/fs", s.withAccount(s.userFS))
	mux.HandleFunc("/cloud-terminal-api/workbench/auth", s.workbenchAuth)
	mux.HandleFunc("/cloud-terminal-api/workbench/logout", s.workbenchLogout)
	mux.HandleFunc("/cloud-terminal-api/workbench/state", s.withWorkbench(s.workbenchState))
	mux.HandleFunc("/cloud-terminal-api/workbench/files", s.withWorkbench(s.workbenchFiles))
	mux.HandleFunc("/cloud-terminal-api/workbench/warmup", s.withWorkbench(s.workbenchWarmup))
	mux.HandleFunc("/cloud-terminal-api/workbench/file", s.withWorkbench(s.workbenchFile))
	mux.HandleFunc("/cloud-terminal-api/workbench/diff", s.withWorkbench(s.workbenchDiff))
	mux.HandleFunc("/cloud-terminal-api/workbench/preview", s.withWorkbench(s.workbenchPreview))
	mux.HandleFunc("/preview/", s.withWorkbench(s.workbenchPreviewPath))
	mux.HandleFunc("/cloud-terminal-api/admin/config", s.withAdmin(s.adminConfig))
	mux.HandleFunc("/cloud-terminal-api/admin/accounts", s.withAdmin(s.adminAccounts))
	mux.HandleFunc("/cloud-terminal-api/admin/fs", s.withAdmin(s.adminFS))
	mux.HandleFunc("/cloud-terminal-api/ws/terminal", s.terminalWS)
	mux.HandleFunc("/cloud-terminal-api/ws/workbench", s.workbenchWS)
	mux.HandleFunc("/cloud-terminal-api/tunnel/agent", s.tunnelAgentWS)
	if adminFS, err := fs.Sub(s.staticFS, "admin"); err == nil {
		adminFiles := http.StripPrefix("/admin/", http.FileServer(http.FS(adminFS)))
		mux.Handle("/admin/", s.withAdminStatic(adminFiles))
	}
	if userFS, err := fs.Sub(s.staticFS, "user"); err == nil {
		userFiles := http.StripPrefix("/user/", http.FileServer(http.FS(userFS)))
		mux.Handle("/user/", s.withUserStatic(userFiles))
	}
	mux.HandleFunc("/", s.serveRoot)
	return s.securityHeaders(mux)
}

func (s *Server) serveRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		http.Redirect(w, r, "/mobile/", http.StatusFound)
		return
	}
	http.FileServer(http.FS(s.staticFS)).ServeHTTP(w, r)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) discoveryGateway(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := s.config.Snapshot()
	gatewayURL := strings.TrimSpace(cfg.CloudTunnel.GatewayURL)
	if gatewayURL == "" {
		gatewayURL = inferGatewayURL(r)
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"gateway_url": gatewayURL,
		"tunnel_path": "/cloud-terminal-api/tunnel/agent",
	})
}

func inferGatewayURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		if idx := strings.Index(forwarded, ","); idx >= 0 {
			forwarded = forwarded[:idx]
		}
		scheme = strings.TrimSpace(forwarded)
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func (s *Server) handleTunnelSessionMessage(msg workbenchServerMessage) {
	if s.workbench != nil {
		s.workbench.dispatchTunnelMessage(msg)
	}
}

func (s *Server) runtimeIsTunnel() bool {
	_, ok := s.runtime.(*tunnelRuntime)
	return ok
}

func (s *Server) tunnelClientForAccount(account string) *tunnelClient {
	s.tunnel.setDefaultAccount(s.config.Snapshot().CloudTunnel.Account)
	if accounts := s.accountStore(); accounts != nil && !accounts.TunnelAllowed(account) {
		return nil
	}
	return s.tunnel.currentForAccount(account)
}

func (s *Server) edgeInfo(w http.ResponseWriter, r *http.Request) {
	account, _ := s.accountFromRequest(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":       s.edgeID,
		"name":     s.edgeName,
		"status":   "online",
		"commands": s.commandCompletionsForAccount(account),
	})
}

func (s *Server) complete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	prefix := r.URL.Query().Get("prefix")
	workDir := config.NormalizePath(r.URL.Query().Get("work_dir"))

	switch kind {
	case "command":
		account, _ := s.accountFromRequest(r)
		writeJSON(w, http.StatusOK, completionResponse{Matches: filterPrefix(s.commandCompletionsForAccount(account), prefix)})
	case "path":
		account, _ := s.accountFromRequest(r)
		matches, err := s.pathCompletionsForAccount(account, prefix, workDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, completionResponse{Matches: matches})
	default:
		http.Error(w, "unsupported completion kind", http.StatusBadRequest)
	}
}

func (s *Server) commandCompletions() []string {
	return s.commandCompletionsForAccount("")
}

func (s *Server) commandCompletionsForAccount(account string) []string {
	cfg := s.config.Snapshot()
	policyCfg := cfg.Policy
	if s.accountStore() != nil {
		if resolved, err := s.accountStore().UserPolicy(account, cfg.Policy); err == nil {
			policyCfg = resolved
		}
	}
	denied := make(map[string]struct{}, len(policyCfg.Deny))
	for _, command := range policyCfg.Deny {
		command = strings.TrimSpace(command)
		if command != "" {
			denied[command] = struct{}{}
		}
	}

	commands := make([]string, 0, len(policyCfg.Commands))
	for name, rule := range policyCfg.Commands {
		name = strings.TrimSpace(name)
		if name == "" || !rule.Enabled {
			continue
		}
		if _, ok := denied[name]; ok {
			continue
		}
		commands = append(commands, name)
	}
	slices.Sort(commands)
	return commands
}

func (s *Server) pathCompletions(prefix string, workDir string) ([]string, error) {
	return s.pathCompletionsForAccount("", prefix, workDir)
}

func (s *Server) pathCompletionsForAccount(account string, prefix string, workDir string) ([]string, error) {
	cfg := s.config.Snapshot()
	policyCfg := cfg.Policy
	if s.accountStore() != nil {
		if resolved, err := s.accountStore().UserPolicy(account, cfg.Policy); err == nil {
			policyCfg = resolved
		}
	}
	if workDir == "" {
		workDir = config.NormalizePath(cfg.Edge.WorkDir)
	}
	if workDir == "" {
		workDir = config.NormalizePath(".")
	}

	base, typed := splitCompletionPath(prefix, workDir)
	if !pathWithinAllowed(base, policyCfg.AllowPaths) {
		return nil, nil
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}

	matches := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, typed) {
			continue
		}
		full := filepath.Join(base, name)
		if !pathWithinAllowed(full, policyCfg.AllowPaths) {
			continue
		}
		value := completionValue(prefix, base, name, entry.IsDir())
		matches = append(matches, value)
	}
	slices.Sort(matches)
	return matches, nil
}

func (s *Server) terminalWS(w http.ResponseWriter, r *http.Request) {
	account, ok := s.accountFromRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     s.checkOrigin,
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Warn("upgrade websocket", "error", err)
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex
	var closing atomic.Bool
	send := func(msg serverMessage) {
		if closing.Load() {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		s.send(conn, msg)
	}

	sessionID := fmt.Sprintf("sess-%d-%d", time.Now().UnixMilli(), s.counter.Add(1))
	user := normalizeTunnelAccount(account)
	cfg := s.config.Snapshot()
	policyCfg := s.policyForAccount(user, cfg.Policy)
	workDir := config.NormalizePath(cfg.Edge.WorkDir)
	if !pathWithinAllowed(workDir, policyCfg.AllowPaths) && len(policyCfg.AllowPaths) > 0 {
		workDir = config.NormalizePath(policyCfg.AllowPaths[0])
	}

	send(serverMessage{Type: "ready", SessionID: sessionID, EdgeID: s.edgeID, Data: welcome(s.edgeName), WorkDir: workDir})

	var interactiveMu sync.Mutex
	var interactive InteractiveSession
	getInteractive := func() InteractiveSession {
		interactiveMu.Lock()
		defer interactiveMu.Unlock()
		return interactive
	}
	setInteractive := func(session InteractiveSession) {
		interactiveMu.Lock()
		defer interactiveMu.Unlock()
		interactive = session
	}
	clearInteractive := func(session InteractiveSession) {
		interactiveMu.Lock()
		defer interactiveMu.Unlock()
		if interactive == session {
			interactive = nil
		}
	}
	defer func() {
		closing.Store(true)
		if session := getInteractive(); session != nil {
			session.Close()
		}
	}()

	for {
		var msg clientMessage
		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				s.logger.Warn("read websocket", "error", err)
			}
			return
		}

		switch msg.Type {
		case "exec":
			if getInteractive() != nil {
				send(serverMessage{Type: "error", Error: "interactive session is already running"})
				continue
			}
			line := strings.TrimSpace(msg.Line)
			if line == "" {
				send(serverMessage{Type: "prompt"})
				continue
			}
			requestID := fmt.Sprintf("req-%d-%d", time.Now().UnixMilli(), s.counter.Add(1))
			send(serverMessage{Type: "start", RequestID: requestID})
			result := s.runtime.ParseAndExec(r.Context(), edge.ExecRequest{
				RequestID: requestID,
				SessionID: sessionID,
				User:      user,
				EdgeID:    s.edgeID,
				WorkDir:   workDir,
				Rows:      msg.Rows,
				Cols:      msg.Cols,
			}, line)
			if result.WorkDir != "" {
				workDir = result.WorkDir
			}
			send(serverMessage{
				Type:      "result",
				RequestID: requestID,
				Stdout:    result.Stdout,
				Stderr:    result.Stderr,
				ExitCode:  result.ExitCode,
				Denied:    result.Denied,
				Error:     result.Error,
				Duration:  result.Duration,
				WorkDir:   workDir,
			})
			send(serverMessage{Type: "prompt", WorkDir: workDir})
		case "tool":
			if getInteractive() != nil {
				send(serverMessage{Type: "error", Error: "interactive session is already running"})
				continue
			}
			requestID := fmt.Sprintf("req-%d-%d", time.Now().UnixMilli(), s.counter.Add(1))
			send(serverMessage{Type: "start", RequestID: requestID})
			result := s.runtime.Exec(r.Context(), edge.ExecRequest{
				RequestID: requestID,
				SessionID: sessionID,
				User:      user,
				EdgeID:    s.edgeID,
				WorkDir:   workDir,
				Command:   msg.Command,
				Args:      msg.Args,
				Rows:      msg.Rows,
				Cols:      msg.Cols,
			})
			if result.WorkDir != "" {
				workDir = result.WorkDir
			}
			send(serverMessage{
				Type:      "result",
				RequestID: requestID,
				Stdout:    result.Stdout,
				Stderr:    result.Stderr,
				ExitCode:  result.ExitCode,
				Denied:    result.Denied,
				Error:     result.Error,
				Duration:  result.Duration,
				WorkDir:   workDir,
			})
			send(serverMessage{Type: "prompt", WorkDir: workDir})
		case "interactive_start":
			if getInteractive() != nil {
				send(serverMessage{Type: "error", Error: "interactive session is already running"})
				continue
			}
			requestID := fmt.Sprintf("req-%d-%d", time.Now().UnixMilli(), s.counter.Add(1))
			req := edge.ExecRequest{
				RequestID: requestID,
				SessionID: sessionID,
				User:      user,
				EdgeID:    s.edgeID,
				WorkDir:   workDir,
				Command:   msg.Command,
				Args:      msg.Args,
				Rows:      msg.Rows,
				Cols:      msg.Cols,
			}
			var outputDecoder utf8StreamDecoder
			opts := edge.InteractiveOptions{
				Output: func(chunk []byte) {
					data := outputDecoder.Push(chunk)
					if data == "" {
						return
					}
					send(serverMessage{
						Type:      "interactive_output",
						RequestID: requestID,
						Data:      data,
					})
				},
			}
			var session InteractiveSession
			var err error
			if strings.TrimSpace(msg.Line) != "" {
				session, err = s.runtime.ParseAndStartInteractive(r.Context(), req, msg.Line, opts)
			} else {
				session, err = s.runtime.StartInteractive(r.Context(), req, opts)
			}
			if err != nil {
				send(serverMessage{Type: "error", RequestID: requestID, Error: err.Error()})
				send(serverMessage{Type: "prompt", WorkDir: workDir})
				continue
			}
			setInteractive(session)
			send(serverMessage{Type: "interactive_ready", RequestID: requestID})
			go func(session InteractiveSession) {
				result := <-session.Done()
				clearInteractive(session)
				if data := outputDecoder.Flush(); data != "" {
					send(serverMessage{
						Type:      "interactive_output",
						RequestID: requestID,
						Data:      data,
					})
				}
				send(serverMessage{
					Type:      "interactive_exit",
					RequestID: result.RequestID,
					ExitCode:  result.ExitCode,
					Error:     result.Error,
					Duration:  result.Duration,
					WorkDir:   workDir,
				})
				send(serverMessage{Type: "prompt", WorkDir: workDir})
			}(session)
		case "interactive_input":
			session := getInteractive()
			if session == nil {
				send(serverMessage{Type: "error", Error: "no interactive session is running"})
				continue
			}
			if err := session.Write([]byte(msg.Data)); err != nil {
				send(serverMessage{Type: "error", Error: err.Error()})
			}
		case "resize":
			session := getInteractive()
			if session == nil {
				continue
			}
			if err := session.Resize(msg.Rows, msg.Cols); err != nil {
				send(serverMessage{Type: "error", Error: err.Error()})
			}
		case "interactive_stop":
			session := getInteractive()
			if session == nil {
				send(serverMessage{Type: "prompt", WorkDir: workDir})
				continue
			}
			session.Close()
			clearInteractive(session)
		case "ping":
			send(serverMessage{Type: "pong"})
		default:
			send(serverMessage{Type: "error", Error: "unsupported message type"})
			send(serverMessage{Type: "prompt"})
		}
	}
}

func (s *Server) tunnelAgentWS(w http.ResponseWriter, r *http.Request) {
	username, sessionID, ok := r.BasicAuth()
	accounts := s.accountStore()
	if !ok || !accounts.VerifySession(sessionID, username) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	account, err := normalizeAccountUsername(username)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !accounts.TunnelAllowed(account) {
		http.Error(w, "cloud tunnel is disabled for this account", http.StatusForbidden)
		return
	}

	upgrader := websocket.Upgrader{
		ReadBufferSize:  8192,
		WriteBufferSize: 8192,
		CheckOrigin:     s.checkOrigin,
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Warn("upgrade tunnel websocket", "error", err)
		return
	}

	var helloEnv tunnelEnvelope
	if err := conn.ReadJSON(&helloEnv); err != nil {
		_ = conn.Close()
		s.logger.Warn("read tunnel hello", "error", err)
		return
	}
	if helloEnv.Type != "hello" {
		_ = conn.WriteJSON(tunnelEnvelope{Type: "error", Error: "first tunnel message must be hello"})
		_ = conn.Close()
		return
	}
	var hello tunnelHello
	if err := decodeTunnelPayload(helloEnv.Payload, &hello); err != nil {
		_ = conn.WriteJSON(tunnelEnvelope{Type: "error", Error: err.Error()})
		_ = conn.Close()
		return
	}
	if strings.TrimSpace(hello.EdgeID) == "" {
		hello.EdgeID = s.edgeID
	}
	if strings.TrimSpace(hello.EdgeName) == "" {
		hello.EdgeName = hello.EdgeID
	}

	client := &tunnelClient{
		hub:          s.tunnel,
		conn:         conn,
		logger:       s.logger,
		account:      account,
		edgeID:       hello.EdgeID,
		edgeName:     hello.EdgeName,
		workDir:      hello.WorkDir,
		allowPaths:   slices.Clone(hello.AllowPaths),
		previewPorts: slices.Clone(hello.PreviewPorts),
		agents:       slices.Clone(hello.Agents),
		sessions:     slices.Clone(hello.Sessions),
		pending:      make(map[string]chan tunnelEnvelope),
		exitWaiters:  make(map[string]chan workbenchServerMessage),
	}
	client.sessionSink = s.handleTunnelSessionMessage
	if existing := s.tunnel.currentForAccount(account); existing != nil && existing != client {
		s.logger.Warn("reject duplicate agent tunnel", "account", account, "edge_id", hello.EdgeID, "existing_edge_id", existing.edgeID)
		_ = conn.WriteJSON(tunnelEnvelope{
			Type:  "error",
			Code:  "already_connected",
			Error: "another agent for this account is already connected to the gateway",
		})
		_ = conn.Close()
		return
	}
	s.tunnel.set(client)
	_ = client.write(tunnelEnvelope{Type: "hello_ack", OK: true})
	s.logger.Info("edge agent tunnel connected", "edge_id", client.edgeID, "edge_name", client.edgeName)
	client.readLoop()
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.accountFromRequest(r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) withAccount(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.accountIdentityFromRequest(r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) withAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.adminIPAllowed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if identity, ok := s.accountIdentityFromRequest(r); !ok || identity.Role != accountRoleAdmin {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) withAdminStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.adminIPAllowed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/admin/login.html" || r.URL.Path == "/admin/login.js" || r.URL.Path == "/admin/styles.css" || r.URL.Path == "/admin/app.js" {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/admin/" || r.URL.Path == "/admin/index.html" {
			if identity, ok := s.accountIdentityFromRequest(r); !ok || identity.Role != accountRoleAdmin {
				http.Redirect(w, r, "/admin/login.html", http.StatusFound)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withUserStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/login.html", "/user/login.js", "/user/styles.css", "/user/app.js":
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := s.accountIdentityFromRequest(r); !ok {
			http.Redirect(w, r, "/user/login.html", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	_, ok := s.accountFromRequest(r)
	return ok
}

func (s *Server) adminAuthorized(r *http.Request) bool {
	identity, ok := s.accountIdentityFromRequest(r)
	return ok && identity.Role == accountRoleAdmin
}

func (s *Server) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	originHost, err := originHost(origin)
	if err != nil {
		s.logger.Warn("reject websocket origin", "origin", origin, "error", err)
		return false
	}

	for _, requestHost := range requestHosts(r) {
		if sameHost(originHost, requestHost) {
			return true
		}
	}

	if hostAllowed(originHost, s.config.AllowHosts()) {
		return true
	}

	s.logger.Warn("reject websocket origin", "origin", originHost, "host", normalizeHost(r.Host), "forwarded_host", r.Header.Get("X-Forwarded-Host"))
	return false
}

func requestHosts(r *http.Request) []string {
	var hosts []string
	addHost := func(value string) {
		value = normalizeHost(value)
		if value != "" && !slices.Contains(hosts, value) {
			hosts = append(hosts, value)
		}
	}
	addHost(r.Host)
	for _, header := range []string{"X-Forwarded-Host", "X-Original-Host"} {
		for _, value := range r.Header.Values(header) {
			for _, part := range strings.Split(value, ",") {
				addHost(part)
			}
		}
	}
	return hosts
}

func originHost(origin string) (string, error) {
	parsed, err := url.Parse(origin)
	if err != nil {
		return "", err
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("missing origin host")
	}
	return normalizeHost(parsed.Host), nil
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

func sameHost(originHost string, requestHost string) bool {
	if originHost == "" || requestHost == "" {
		return false
	}
	if originHost == requestHost {
		return true
	}

	originName, _, originErr := net.SplitHostPort(originHost)
	requestName, _, requestErr := net.SplitHostPort(requestHost)
	return originErr == nil && requestErr == nil && strings.EqualFold(originName, requestName)
}

func hostAllowed(host string, hosts []string) bool {
	allowed := make(map[string]struct{}, len(hosts))
	for _, value := range hosts {
		value = normalizeHost(value)
		if value != "" {
			allowed[value] = struct{}{}
		}
	}
	if _, ok := allowed[host]; ok {
		return true
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		_, ok := allowed[normalizeHost(h)]
		return ok
	}
	return false
}

func filterPrefix(values []string, prefix string) []string {
	matches := make([]string, 0, len(values))
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			matches = append(matches, value)
		}
	}
	return matches
}

func splitCompletionPath(prefix string, workDir string) (string, string) {
	if prefix == "" {
		return workDir, ""
	}
	cleanPrefix := prefix
	if !filepath.IsAbs(cleanPrefix) {
		cleanPrefix = filepath.Join(workDir, cleanPrefix)
	}
	if strings.HasSuffix(prefix, string(filepath.Separator)) {
		return filepath.Clean(cleanPrefix), ""
	}
	return filepath.Clean(filepath.Dir(cleanPrefix)), filepath.Base(cleanPrefix)
}

func completionValue(prefix string, base string, name string, isDir bool) string {
	suffix := ""
	if isDir {
		suffix = string(filepath.Separator)
	}
	if filepath.IsAbs(prefix) {
		return filepath.Join(base, name) + suffix
	}
	dir := filepath.Dir(prefix)
	if dir == "." {
		return name + suffix
	}
	return filepath.Join(dir, name) + suffix
}

func pathWithinAllowed(path string, roots []string) bool {
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

func (s *Server) adminIPAllowed(r *http.Request) bool {
	allowed := s.config.AdminIPAllowlist()
	if len(allowed) == 0 {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	for _, rule := range allowed {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		if _, network, err := net.ParseCIDR(rule); err == nil && ip != nil && network.Contains(ip) {
			return true
		}
		if parsed := net.ParseIP(rule); parsed != nil && ip != nil && parsed.Equal(ip) {
			return true
		}
		if strings.EqualFold(rule, host) {
			return true
		}
	}
	return false
}

func (s *Server) adminConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, adminConfigFromConfig(s.config.Snapshot()))
	case http.MethodPut:
		var payload adminConfigPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		current := s.config.Snapshot()
		next := payload.Apply(current)
		if payload.CloudTunnel.UseCurrentAccount {
			identity, ok := s.accountIdentityFromRequest(r)
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			tunnelSession, err := s.accountStore().IssueSession(identity.Username)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			next.CloudTunnel.Account = identity.Username
			next.CloudTunnel.SessionID = tunnelSession.SessionID
		}
		if err := s.config.Update(next); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if accounts := s.accountStore(); accounts != nil {
			accounts.SetRegistrationEnabled(next.Server.RegistrationEnabled())
		}
		s.tunnel.setDefaultAccount(next.CloudTunnel.Account)
		writeJSON(w, http.StatusOK, adminConfigFromConfig(next))
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) adminAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"registration_enabled": s.accountStore().RegistrationEnabled(),
			"accounts":             s.accountStore().List(),
		})
	case http.MethodPost:
		var payload accountCreatePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.accountStore().CreateAccount(payload.Username, payload.Password, payload.Role, s.config.Snapshot().Policy); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"accounts": s.accountStore().List(),
		})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) userSettings(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accountIdentityFromRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	cfg := s.config.Snapshot()
	switch r.Method {
	case http.MethodGet:
		settings, err := s.accountStore().UserSettings(identity.Username, cfg.Policy)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, userSettingsToPayload(settings, accountPublicInfo{Username: identity.Username, Role: identity.Role}, cfg.Policy, cfg.CloudTunnel.GatewayURL))
	case http.MethodPut:
		var payload userSettingsUpdatePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		settings, err := s.accountStore().SaveUserSettings(identity.Username, payload, cfg.Policy)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, userSettingsToPayload(settings, accountPublicInfo{Username: identity.Username, Role: identity.Role}, cfg.Policy, cfg.CloudTunnel.GatewayURL))
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) userFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, ok := s.accountIdentityFromRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	cfg := s.config.Snapshot()
	roots := cleanPaths(cfg.Policy.AllowPaths)
	path := config.NormalizePath(r.URL.Query().Get("path"))
	if path == "" {
		writeJSON(w, http.StatusOK, fsListResponse{
			Path:    "",
			Parent:  "",
			Roots:   slices.Clone(roots),
			Entries: rootEntries(roots),
		})
		return
	}
	if !pathWithinAllowed(path, roots) {
		http.Error(w, "path is outside global allowed roots", http.StatusForbidden)
		return
	}
	if s.runtimeIsTunnel() {
		client := s.tunnelClientForAccount(identity.Username)
		if client == nil {
			http.Error(w, tunnelUnavailable().Error(), http.StatusServiceUnavailable)
			return
		}
		var response workbenchFilesResponse
		if err := client.request(r.Context(), "files", tunnelFilesRequest{Path: path}, &response); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		response.AllowPaths = slices.Clone(roots)
		if response.Parent != "" && !pathWithinAllowed(response.Parent, roots) {
			response.Parent = ""
		}
		response.Entries = filterWorkbenchFileEntriesToPolicy(response.Entries, roots)
		writeJSON(w, http.StatusOK, fsListResponse{
			Path:    response.Path,
			Parent:  response.Parent,
			Roots:   slices.Clone(roots),
			Entries: workbenchEntriesToFSEntries(response.Entries),
		})
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items := make([]fsEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		entryPath := filepath.Join(path, entry.Name())
		if !pathWithinAllowed(entryPath, roots) {
			continue
		}
		items = append(items, fsEntry{
			Name:  entry.Name(),
			Path:  entryPath,
			IsDir: entry.IsDir(),
			Size:  info.Size(),
		})
	}
	slices.SortFunc(items, func(a, b fsEntry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	parent := parentPath(path)
	if parent != "" && !pathWithinAllowed(parent, roots) {
		parent = ""
	}
	writeJSON(w, http.StatusOK, fsListResponse{
		Path:    path,
		Parent:  parent,
		Roots:   slices.Clone(roots),
		Entries: items,
	})
}

func (s *Server) accountStore() *accountStore {
	s.accountMu.RLock()
	defer s.accountMu.RUnlock()
	return s.accounts
}

func (s *Server) setAccountStore(accounts *accountStore) {
	s.accountMu.Lock()
	s.accounts = accounts
	s.accountMu.Unlock()
}

func (s *Server) adminFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := config.NormalizePath(r.URL.Query().Get("path"))
	if path == "" {
		path = string(filepath.Separator)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	items := make([]fsEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		items = append(items, fsEntry{
			Name:  entry.Name(),
			Path:  filepath.Join(path, entry.Name()),
			IsDir: entry.IsDir(),
			Size:  info.Size(),
		})
	}
	slices.SortFunc(items, func(a, b fsEntry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	writeJSON(w, http.StatusOK, fsListResponse{
		Path:    path,
		Parent:  parentPath(path),
		Roots:   []string{string(filepath.Separator)},
		Entries: items,
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/preview/") {
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
			w.Header().Set("Content-Security-Policy", "default-src 'self' data: blob:; script-src 'self' 'unsafe-inline' 'unsafe-eval' blob:; style-src 'self' 'unsafe-inline'; font-src 'self' data:; connect-src 'self' ws: wss: http: https:; img-src 'self' data: blob:; frame-ancestors 'self'")
		} else {
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self' data:; connect-src 'self' ws: wss:; img-src 'self' data:; frame-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) send(conn *websocket.Conn, msg serverMessage) {
	if err := conn.WriteJSON(msg); err != nil {
		s.logger.Warn("write websocket", "error", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func welcome(edgeName string) string {
	return "\r\nxmux connected to " + edgeName + "\r\nOnly whitelisted structured commands are executed. Shell operators are disabled.\r\n\r\n"
}

type clientMessage struct {
	Type    string   `json:"type"`
	Line    string   `json:"line,omitempty"`
	Data    string   `json:"data,omitempty"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Rows    uint16   `json:"rows,omitempty"`
	Cols    uint16   `json:"cols,omitempty"`
}

type serverMessage struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	EdgeID    string `json:"edge_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	Data      string `json:"data,omitempty"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	Denied    bool   `json:"denied,omitempty"`
	Error     string `json:"error,omitempty"`
	Duration  string `json:"duration,omitempty"`
	WorkDir   string `json:"work_dir,omitempty"`
}

type completionResponse struct {
	Matches []string `json:"matches"`
}

type adminConfigPayload struct {
	DatabasePath               string                         `json:"database_path"`
	AccountStorePath           string                         `json:"account_store_path"`
	AccountRegistrationEnabled bool                           `json:"account_registration_enabled"`
	CloudTunnel                adminCloudTunnelPayload        `json:"cloud_tunnel"`
	AllowHosts                 []string                       `json:"allow_hosts"`
	AdminIPAllowlist           []string                       `json:"admin_ip_allowlist"`
	Deny                       []string                       `json:"deny"`
	AllowPaths                 []string                       `json:"allow_paths"`
	Commands                   map[string]adminCommandPayload `json:"commands"`
}

type adminCloudTunnelPayload struct {
	Enabled           bool   `json:"enabled"`
	DiscoveryURL      string `json:"discovery_url"`
	GatewayURL        string `json:"gateway_url"`
	Account           string `json:"account"`
	Bound             bool   `json:"bound"`
	UseCurrentAccount bool   `json:"use_current_account,omitempty"`
}

type adminCommandPayload struct {
	Enabled     bool     `json:"enabled"`
	Bin         string   `json:"bin"`
	Interactive bool     `json:"interactive"`
	Subcommands []string `json:"subcommands"`
	AllowPaths  []string `json:"allow_paths,omitempty"`
	MaxArgs     int      `json:"max_args"`
}

func adminConfigFromConfig(cfg config.Config) adminConfigPayload {
	commands := make(map[string]adminCommandPayload, len(cfg.Policy.Commands))
	for name, rule := range cfg.Policy.Commands {
		commands[name] = adminCommandPayload{
			Enabled:     rule.Enabled,
			Bin:         rule.Bin,
			Interactive: rule.Interactive,
			Subcommands: slices.Clone(rule.Subcommands),
			AllowPaths:  slices.Clone(rule.AllowPaths),
			MaxArgs:     rule.MaxArgs,
		}
	}
	return adminConfigPayload{
		DatabasePath:               cfg.Server.DatabasePath,
		AccountStorePath:           cfg.Server.AccountStorePath,
		AccountRegistrationEnabled: cfg.Server.RegistrationEnabled(),
		CloudTunnel: adminCloudTunnelPayload{
			Enabled:      cfg.CloudTunnel.Enabled,
			DiscoveryURL: cfg.CloudTunnel.DiscoveryURL,
			GatewayURL:   cfg.CloudTunnel.GatewayURL,
			Account:      cfg.CloudTunnel.Account,
			Bound:        strings.TrimSpace(cfg.CloudTunnel.Account) != "" && strings.TrimSpace(cfg.CloudTunnel.SessionID) != "",
		},
		AllowHosts:       slices.Clone(cfg.Server.AllowHosts),
		AdminIPAllowlist: slices.Clone(cfg.Server.AdminIPAllowlist),
		Deny:             slices.Clone(cfg.Policy.Deny),
		AllowPaths:       slices.Clone(cfg.Policy.AllowPaths),
		Commands:         commands,
	}
}

func (p adminConfigPayload) Apply(cfg config.Config) config.Config {
	if strings.TrimSpace(p.DatabasePath) != "" {
		cfg.Server.DatabasePath = strings.TrimSpace(p.DatabasePath)
	}
	registrationEnabled := p.AccountRegistrationEnabled
	cfg.Server.AccountRegistrationEnabled = &registrationEnabled
	cfg.CloudTunnel.Enabled = p.CloudTunnel.Enabled
	cfg.CloudTunnel.DiscoveryURL = strings.TrimSpace(p.CloudTunnel.DiscoveryURL)
	cfg.CloudTunnel.GatewayURL = strings.TrimSpace(p.CloudTunnel.GatewayURL)
	cfg.Server.AllowHosts = cleanList(p.AllowHosts)
	cfg.Server.AdminIPAllowlist = cleanList(p.AdminIPAllowlist)
	cfg.Policy.Deny = cleanList(p.Deny)
	cfg.Policy.AllowPaths = cleanPaths(p.AllowPaths)
	cfg.Policy.Commands = toPolicyCommands(p.Commands)
	return cfg
}

func toPolicyCommands(commands map[string]adminCommandPayload) map[string]policy.CommandPolicy {
	next := make(map[string]policy.CommandPolicy, len(commands))
	for rawName, rule := range commands {
		name := strings.TrimSpace(rawName)
		if name == "" {
			continue
		}
		next[name] = policy.CommandPolicy{
			Enabled:     rule.Enabled,
			Bin:         strings.TrimSpace(rule.Bin),
			Interactive: rule.Interactive,
			Subcommands: cleanList(rule.Subcommands),
			AllowPaths:  cleanPaths(rule.AllowPaths),
			MaxArgs:     rule.MaxArgs,
		}
	}
	return next
}

func cleanList(values []string) []string {
	var cleaned []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}

func cleanPaths(values []string) []string {
	var cleaned []string
	for _, value := range values {
		value = config.NormalizePath(value)
		if value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}

type fsListResponse struct {
	Path    string    `json:"path"`
	Parent  string    `json:"parent"`
	Roots   []string  `json:"roots"`
	Entries []fsEntry `json:"entries"`
}

type fsEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

func rootEntries(roots []string) []fsEntry {
	items := make([]fsEntry, 0, len(roots))
	for _, root := range roots {
		root = config.NormalizePath(root)
		if root == "" {
			continue
		}
		name := filepath.Base(root)
		if name == "." || name == string(filepath.Separator) {
			name = root
		}
		items = append(items, fsEntry{
			Name:  name,
			Path:  root,
			IsDir: true,
		})
	}
	return items
}

func workbenchEntriesToFSEntries(entries []workbenchFileEntry) []fsEntry {
	out := make([]fsEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, fsEntry{
			Name:  entry.Name,
			Path:  entry.Path,
			IsDir: entry.IsDir,
			Size:  entry.Size,
		})
	}
	return out
}

func parentPath(path string) string {
	parent := filepath.Dir(path)
	if parent == path {
		return ""
	}
	return parent
}
