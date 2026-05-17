package gateway

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// AgentRoutes returns an HTTP handler for the agent-mode local admin server.
// Auth and user/policy settings are proxied to the cloud gateway;
// tunnel config and the local file browser are handled locally.
func (s *Server) AgentRoutes(cloudBaseURL string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/cloud-terminal-api/health", s.health)
	mux.HandleFunc("/cloud-terminal-api/discovery/gateway", s.discoveryGateway)

	// Local-only endpoints
	mux.HandleFunc("/cloud-terminal-api/agent/config", s.agentConfig)
	mux.HandleFunc("/cloud-terminal-api/admin/fs", s.agentLocalFS)

	// Proxy auth + user settings to cloud
	proxy := buildCloudProxy(cloudBaseURL)
	mux.Handle("/cloud-terminal-api/accounts/", proxy)
	mux.Handle("/cloud-terminal-api/user/", proxy)

	// Serve agent admin page; fall back to shared static files
	if agentSubFS, err := fs.Sub(s.staticFS, "agent"); err == nil {
		agentFiles := http.StripPrefix("/agent/", http.FileServer(http.FS(agentSubFS)))
		mux.Handle("/agent/", agentFiles)
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

// buildCloudProxy creates a reverse proxy that forwards requests to the cloud gateway.
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
}

func (s *Server) agentConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.config.Snapshot()
		writeJSON(w, http.StatusOK, agentConfigPayload{
			TunnelEnabled: cfg.CloudTunnel.Enabled,
			DiscoveryURL:  cfg.CloudTunnel.DiscoveryURL,
			GatewayURL:    cfg.CloudTunnel.GatewayURL,
			EdgeID:        cfg.Edge.ID,
			EdgeName:      cfg.Edge.Name,
			WorkDir:       cfg.Edge.WorkDir,
		})
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
		cfg := s.config.Snapshot()
		writeJSON(w, http.StatusOK, agentConfigPayload{
			TunnelEnabled: cfg.CloudTunnel.Enabled,
			DiscoveryURL:  cfg.CloudTunnel.DiscoveryURL,
			GatewayURL:    cfg.CloudTunnel.GatewayURL,
			EdgeID:        cfg.Edge.ID,
			EdgeName:      cfg.Edge.Name,
			WorkDir:       cfg.Edge.WorkDir,
		})
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// agentLocalFS serves the local filesystem without cloud auth —
// the agent server only listens on localhost so access is inherently local.
func (s *Server) agentLocalFS(w http.ResponseWriter, r *http.Request) {
	s.adminFS(w, r)
}
