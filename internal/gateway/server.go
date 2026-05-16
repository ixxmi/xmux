package gateway

import (
	"context"
	"crypto/subtle"
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
	tunnelHub := newTunnelHub(opts.Logger)
	runtime := opts.Runtime
	if runtime == nil {
		runtime = newTunnelRuntime(tunnelHub)
	}
	server := &Server{
		runtime:  runtime,
		staticFS: opts.StaticFS,
		config:   opts.Config,
		edgeID:   opts.EdgeID,
		edgeName: opts.EdgeName,
		logger:   opts.Logger,
		tunnel:   tunnelHub,
	}
	server.workbench = newWorkbenchManager(runtime, opts.Config, opts.EdgeID, opts.EdgeName, opts.WorkbenchStatePath, opts.Logger)
	tunnelHub.setSessionSink(server.handleTunnelSessionMessage)
	return server
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/cloud-terminal-api/health", s.health)
	mux.HandleFunc("/cloud-terminal-api/edge", s.withAuth(s.edgeInfo))
	mux.HandleFunc("/cloud-terminal-api/complete", s.withAuth(s.complete))
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
	mux.HandleFunc("/cloud-terminal-api/admin/fs", s.withAdmin(s.adminFS))
	mux.HandleFunc("/cloud-terminal-api/ws/terminal", s.terminalWS)
	mux.HandleFunc("/cloud-terminal-api/ws/workbench", s.workbenchWS)
	mux.HandleFunc("/cloud-terminal-api/tunnel/agent", s.tunnelAgentWS)
	if adminFS, err := fs.Sub(s.staticFS, "admin"); err == nil {
		adminFiles := http.StripPrefix("/admin/", http.FileServer(http.FS(adminFS)))
		mux.Handle("/admin/", s.withAdminStatic(adminFiles))
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

func (s *Server) handleTunnelSessionMessage(msg workbenchServerMessage) {
	if s.workbench != nil {
		s.workbench.dispatchTunnelMessage(msg)
	}
}

func (s *Server) runtimeIsTunnel() bool {
	_, ok := s.runtime.(*tunnelRuntime)
	return ok
}

func (s *Server) edgeInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"id":       s.edgeID,
		"name":     s.edgeName,
		"status":   "online",
		"commands": s.commandCompletions(),
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
		writeJSON(w, http.StatusOK, completionResponse{Matches: filterPrefix(s.commandCompletions(), prefix)})
	case "path":
		matches, err := s.pathCompletions(prefix, workDir)
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
	cfg := s.config.Snapshot()
	denied := make(map[string]struct{}, len(cfg.Policy.Deny))
	for _, command := range cfg.Policy.Deny {
		command = strings.TrimSpace(command)
		if command != "" {
			denied[command] = struct{}{}
		}
	}

	commands := make([]string, 0, len(cfg.Policy.Commands))
	for name, rule := range cfg.Policy.Commands {
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
	cfg := s.config.Snapshot()
	if workDir == "" {
		workDir = config.NormalizePath(cfg.Edge.WorkDir)
	}
	if workDir == "" {
		workDir = config.NormalizePath(".")
	}

	base, typed := splitCompletionPath(prefix, workDir)
	if !pathWithinAllowed(base, cfg.Policy.AllowPaths) {
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
		if !pathWithinAllowed(full, cfg.Policy.AllowPaths) {
			continue
		}
		value := completionValue(prefix, base, name, entry.IsDir())
		matches = append(matches, value)
	}
	slices.Sort(matches)
	return matches, nil
}

func (s *Server) terminalWS(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
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
	user := "browser"
	workDir := config.NormalizePath(s.config.Snapshot().Edge.WorkDir)

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
	token := requestToken(r)
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if !compareToken(token, s.config.TunnelToken()) {
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
	s.tunnel.set(client)
	_ = client.write(tunnelEnvelope{Type: "hello_ack", OK: true})
	s.logger.Info("edge agent tunnel connected", "edge_id", client.edgeID, "edge_name", client.edgeName)
	client.readLoop()
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
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
		if !s.adminAuthorized(r) {
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
			if !s.adminAuthorized(r) {
				http.Redirect(w, r, "/admin/login.html", http.StatusFound)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	return compareToken(requestToken(r), s.config.TerminalToken())
}

func (s *Server) adminAuthorized(r *http.Request) bool {
	return compareToken(requestToken(r), s.config.AdminToken())
}

func requestToken(r *http.Request) string {
	token := r.Header.Get("Authorization")
	token = strings.TrimPrefix(token, "Bearer ")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	return token
}

func compareToken(got string, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
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
		if err := s.config.Update(next); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, adminConfigFromConfig(next))
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
	return "\r\nCloud Terminal connected to " + edgeName + "\r\nOnly whitelisted structured commands are executed. Shell operators are disabled.\r\n\r\n"
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
	AuthToken        string                         `json:"auth_token"`
	AdminToken       string                         `json:"admin_token"`
	TunnelToken      string                         `json:"tunnel_token"`
	AllowHosts       []string                       `json:"allow_hosts"`
	AdminIPAllowlist []string                       `json:"admin_ip_allowlist"`
	Deny             []string                       `json:"deny"`
	AllowPaths       []string                       `json:"allow_paths"`
	Commands         map[string]adminCommandPayload `json:"commands"`
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
		AuthToken:        cfg.Server.AuthToken,
		AdminToken:       cfg.Server.AdminToken,
		TunnelToken:      cfg.Server.TunnelToken,
		AllowHosts:       slices.Clone(cfg.Server.AllowHosts),
		AdminIPAllowlist: slices.Clone(cfg.Server.AdminIPAllowlist),
		Deny:             slices.Clone(cfg.Policy.Deny),
		AllowPaths:       slices.Clone(cfg.Policy.AllowPaths),
		Commands:         commands,
	}
}

func (p adminConfigPayload) Apply(cfg config.Config) config.Config {
	cfg.Server.AuthToken = strings.TrimSpace(p.AuthToken)
	cfg.Server.AdminToken = strings.TrimSpace(p.AdminToken)
	cfg.Server.TunnelToken = strings.TrimSpace(p.TunnelToken)
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

func parentPath(path string) string {
	parent := filepath.Dir(path)
	if parent == path {
		return ""
	}
	return parent
}
