package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"

	"cloud-terminal/internal/policy"
)

// AgentRoutes returns an HTTP handler for the agent-mode local admin server.
// Auth and user/policy settings are proxied to the cloud gateway;
// tunnel config and the local file browser are handled locally.
// There is a single global admin account: the cloud one. The local /admin/
// path is redirected to /agent/ to make this unambiguous.
//
// initialCloudBaseURL is used as a fallback if the runtime config does not
// have a gateway_url / discovery_url; in normal operation the proxy reads
// the live value from the config store so admin changes take effect without
// restarting.
func (s *Server) AgentRoutes(initialCloudBaseURL string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/cloud-terminal-api/health", s.health)
	mux.HandleFunc("/cloud-terminal-api/discovery/gateway", s.discoveryGateway)

	// Local-only endpoints
	mux.HandleFunc("/cloud-terminal-api/agent/config", s.agentConfig)
	mux.HandleFunc("/cloud-terminal-api/agent/policy", s.agentPolicy)
	mux.HandleFunc("/cloud-terminal-api/agent/bind", s.agentBind)
	mux.HandleFunc("/cloud-terminal-api/admin/fs", s.agentLocalFS)

	// Proxy auth + user settings to cloud
	proxy := s.dynamicCloudProxy(initialCloudBaseURL)
	mux.Handle("/cloud-terminal-api/accounts/", proxy)
	mux.Handle("/cloud-terminal-api/user/", proxy)

	// Serve agent admin page; fall back to shared static files
	if agentSubFS, err := fs.Sub(s.staticFS, "agent"); err == nil {
		agentFiles := http.StripPrefix("/agent/", http.FileServer(http.FS(agentSubFS)))
		mux.Handle("/agent/", agentFiles)
	}

	// Serve /admin/ static files (only static assets — the legacy admin
	// HTML pages reference cloud-only endpoints that aren't registered in
	// agent mode, so they won't function, but the shared CSS used by the
	// /agent/ pages must still be reachable.
	if adminSubFS, err := fs.Sub(s.staticFS, "admin"); err == nil {
		adminFiles := http.StripPrefix("/admin/", http.FileServer(http.FS(adminSubFS)))
		mux.Handle("/admin/", adminFiles)
	}

	mux.HandleFunc("/", s.agentRoot)
	return s.securityHeaders(mux)
}

func (s *Server) agentRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		http.Redirect(w, r, "/agent/", http.StatusFound)
		return
	}
	http.FileServer(http.FS(s.staticFS)).ServeHTTP(w, r)
}

// dynamicCloudProxy creates a reverse proxy that reads the live cloud URL
// from the config store on every request, falling back to the initial value
// captured at startup. This lets the admin UI change discovery_url /
// gateway_url and have the next API call proxy to the new target.
func (s *Server) dynamicCloudProxy(initial string) http.Handler {
	initial = strings.TrimRight(strings.TrimSpace(initial), "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := s.resolveCloudBase(initial)
		if target == "" {
			http.Error(w, "cloud gateway not configured — set gateway_url or discovery_url in /agent/", http.StatusServiceUnavailable)
			return
		}
		parsed, err := url.Parse(target)
		if err != nil || parsed.Host == "" {
			http.Error(w, "invalid cloud gateway URL: "+target, http.StatusServiceUnavailable)
			return
		}
		proxy := httputil.NewSingleHostReverseProxy(parsed)
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			req.Host = parsed.Host
			req.Header.Del("X-Forwarded-For")
			req.Header.Del("X-Real-IP")
		}
		proxy.ServeHTTP(w, r)
	})
}

func (s *Server) resolveCloudBase(fallback string) string {
	cfg := s.config.Snapshot()
	if v := strings.TrimRight(strings.TrimSpace(cfg.CloudTunnel.GatewayURL), "/"); v != "" {
		return v
	}
	if v := strings.TrimRight(strings.TrimSpace(cfg.CloudTunnel.DiscoveryURL), "/"); v != "" {
		return v
	}
	return fallback
}

// buildCloudProxy is the simpler static-target variant; kept for tests
// or callers that want a single fixed target.
func buildCloudProxy(cloudBaseURL string) http.Handler {
	cloudBaseURL = strings.TrimRight(strings.TrimSpace(cloudBaseURL), "/")
	if cloudBaseURL == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "cloud gateway not configured — set discovery_url in local config", http.StatusServiceUnavailable)
		})
	}
	target, err := url.Parse(cloudBaseURL)
	if err != nil || target.Host == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "invalid cloud gateway URL in local config", http.StatusServiceUnavailable)
		})
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
		req.Header.Del("X-Forwarded-For")
		req.Header.Del("X-Real-IP")
	}
	return proxy
}

// agentConfigPayload is the GET/PUT body for /cloud-terminal-api/agent/config.
type agentConfigPayload struct {
	TunnelEnabled bool   `json:"tunnel_enabled"`
	DiscoveryURL  string `json:"discovery_url,omitempty"`
	GatewayURL    string `json:"gateway_url,omitempty"`
	EdgeID        string `json:"edge_id,omitempty"`
	EdgeName      string `json:"edge_name,omitempty"`
	WorkDir       string `json:"work_dir,omitempty"`
	BoundAccount  string `json:"bound_account,omitempty"`
	Bound         bool   `json:"bound"`
}

func (s *Server) agentConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.snapshotAgentConfig())
	case http.MethodPut:
		var payload agentConfigPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.config.UpdateTunnel(
			payload.TunnelEnabled,
			payload.DiscoveryURL,
			payload.GatewayURL,
			payload.EdgeName,
			payload.EdgeID,
		); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, s.snapshotAgentConfig())
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) snapshotAgentConfig() agentConfigPayload {
	cfg := s.config.Snapshot()
	return agentConfigPayload{
		TunnelEnabled: cfg.CloudTunnel.Enabled,
		DiscoveryURL:  cfg.CloudTunnel.DiscoveryURL,
		GatewayURL:    cfg.CloudTunnel.GatewayURL,
		EdgeID:        cfg.Edge.ID,
		EdgeName:      cfg.Edge.Name,
		WorkDir:       cfg.Edge.WorkDir,
		BoundAccount:  cfg.CloudTunnel.Account,
		Bound:         strings.TrimSpace(cfg.CloudTunnel.Account) != "" && strings.TrimSpace(cfg.CloudTunnel.SessionID) != "",
	}
}

type agentBindPayload struct {
	Account   string `json:"account"`
	SessionID string `json:"session_id"`
	Clear     bool   `json:"clear,omitempty"`
}

type agentPolicyPayload struct {
	Deny       []string                       `json:"deny"`
	AllowPaths []string                       `json:"allow_paths"`
	Commands   map[string]adminCommandPayload `json:"commands"`
}

func (s *Server) agentPolicy(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, agentPolicyFromConfig(s.config.Snapshot().Policy))
	case http.MethodPut:
		var payload agentPolicyPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		next := s.config.Snapshot().Policy
		if payload.Deny != nil {
			next.Deny = cleanList(payload.Deny)
		}
		next.AllowPaths = cleanPaths(payload.AllowPaths)
		next.Commands = policyCommandsFromAdminPayload(payload.Commands)
		if err := s.config.UpdatePolicy(next); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if s.agentPolicyUpdate != nil {
			if err := s.agentPolicyUpdate(); err != nil {
				s.logger.Warn("publish agent policy update", "error", err)
			}
		}
		writeJSON(w, http.StatusOK, agentPolicyFromConfig(s.config.Snapshot().Policy))
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func agentPolicyFromConfig(cfg policy.Config) agentPolicyPayload {
	return agentPolicyPayload{
		Deny:       slices.Clone(cfg.Deny),
		AllowPaths: slices.Clone(cfg.AllowPaths),
		Commands:   adminPayloadCommands(cfg.Commands),
	}
}

func policyCommandsFromAdminPayload(commands map[string]adminCommandPayload) map[string]policy.CommandPolicy {
	out := make(map[string]policy.CommandPolicy, len(commands))
	for rawName, payload := range commands {
		name := strings.TrimSpace(rawName)
		if name == "" {
			continue
		}
		out[name] = policy.CommandPolicy{
			Enabled:     payload.Enabled,
			Bin:         strings.TrimSpace(payload.Bin),
			Interactive: payload.Interactive,
			Subcommands: cleanList(payload.Subcommands),
			AllowPaths:  cleanPaths(payload.AllowPaths),
			MaxArgs:     payload.MaxArgs,
		}
	}
	return out
}

// agentBind persists the cloud tunnel credentials to the local agent.yaml.
// Typical flow from the browser:
//
//  1. POST /cloud-terminal-api/accounts/tunnel-session (proxied → cloud)
//     returns {username, session_id}
//  2. POST /cloud-terminal-api/agent/bind with that payload (handled here)
//     saves {Account, SessionID} into cloud_tunnel.* on local YAML.
//
// Set Clear=true to wipe the binding instead.
func (s *Server) agentBind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload agentBindPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if payload.Clear {
		cfg := s.config.Snapshot()
		if account := strings.TrimSpace(cfg.CloudTunnel.Account); account != "" {
			if err := s.clearBoundAccountPaths(r.Context(), account, cfg.CloudTunnel.SessionID); err != nil {
				s.logger.Warn("clear bound account allow paths", "account", account, "error", err)
			}
		}
		if err := s.config.BindTunnelAccount("", ""); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, s.snapshotAgentConfig())
		return
	}
	if strings.TrimSpace(payload.Account) == "" || strings.TrimSpace(payload.SessionID) == "" {
		http.Error(w, "account and session_id are required", http.StatusBadRequest)
		return
	}
	if err := s.clearBoundAccountPaths(r.Context(), payload.Account, payload.SessionID); err != nil {
		s.logger.Warn("clear newly bound account allow paths", "account", payload.Account, "error", err)
	}
	if err := s.config.BindTunnelAccount(payload.Account, payload.SessionID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, s.snapshotAgentConfig())
}

func (s *Server) clearBoundAccountPaths(ctx context.Context, account string, sessionID string) error {
	account = normalizeTunnelAccount(account)
	if account == "" {
		return nil
	}
	target := s.resolveCloudBase("")
	if target == "" {
		return nil
	}
	settingsURL := strings.TrimRight(target, "/") + "/cloud-terminal-api/user/settings"
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, settingsURL, nil)
	if err != nil {
		return err
	}
	for _, cookie := range agentCloudCookies(sessionID) {
		getReq.AddCookie(cookie)
	}
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		return err
	}
	defer getResp.Body.Close()
	if getResp.StatusCode == http.StatusUnauthorized || getResp.StatusCode == http.StatusForbidden || getResp.StatusCode == http.StatusNotFound {
		return nil
	}
	if getResp.StatusCode < 200 || getResp.StatusCode >= 300 {
		return nil
	}
	var settings userSettingsPayload
	if err := json.NewDecoder(getResp.Body).Decode(&settings); err != nil {
		return err
	}
	if normalizeTunnelAccount(settings.Account) != account {
		return nil
	}
	payload := userSettingsUpdatePayload{
		CloudTunnelEnabled: settings.CloudTunnelEnabled,
		Commands:           settings.Commands,
		AllowPaths:         nil,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, settingsURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	putReq.Header.Set("Content-Type", "application/json")
	for _, cookie := range agentCloudCookies(sessionID) {
		putReq.AddCookie(cookie)
	}
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		return err
	}
	defer putResp.Body.Close()
	return nil
}

func agentCloudCookies(sessionID string) []*http.Cookie {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	return []*http.Cookie{
		{Name: accountCookieName, Value: sessionID},
		{Name: workbenchCookieName, Value: sessionID},
	}
}

// agentLocalFS serves the local filesystem without cloud auth —
// the agent server only listens on localhost so access is inherently local.
func (s *Server) agentLocalFS(w http.ResponseWriter, r *http.Request) {
	s.adminFS(w, r)
}
