package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"cloud-terminal/internal/policy"

	"gopkg.in/yaml.v3"
)

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.MappingNode {
		var raw struct {
			Duration string `yaml:"duration"`
		}
		if err := value.Decode(&raw); err != nil {
			return err
		}
		parsed, err := time.ParseDuration(raw.Duration)
		if err != nil {
			return fmt.Errorf("parse duration %q: %w", raw.Duration, err)
		}
		d.Duration = parsed
		return nil
	}

	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", raw, err)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalYAML() (any, error) {
	return d.Duration.String(), nil
}

type Config struct {
	Server      ServerConfig      `yaml:"server"`
	CloudTunnel CloudTunnelConfig `yaml:"cloud_tunnel"`
	Edge        EdgeConfig        `yaml:"edge"`
	Policy      policy.Config     `yaml:"policy"`
}

type ServerConfig struct {
	Addr                       string   `yaml:"addr"`
	AdminUsername              string   `yaml:"admin_username"`
	AdminPassword              string   `yaml:"admin_password"`
	DatabasePath               string   `yaml:"database_path"`
	AccountStorePath           string   `yaml:"account_store_path"`
	AccountRegistrationEnabled *bool    `yaml:"account_registration_enabled"`
	AuditLogPath               string   `yaml:"audit_log_path"`
	WorkbenchStatePath         string   `yaml:"workbench_state_path"`
	AllowHosts                 []string `yaml:"allow_hosts"`
	AdminIPAllowlist           []string `yaml:"admin_ip_allowlist"`
}

type CloudTunnelConfig struct {
	Enabled      bool   `yaml:"enabled"`
	DiscoveryURL string `yaml:"discovery_url,omitempty"`
	GatewayURL   string `yaml:"gateway_url,omitempty"`
	Account      string `yaml:"account,omitempty"`
	SessionID    string `yaml:"session_id,omitempty"`
}

type EdgeConfig struct {
	ID             string            `yaml:"id"`
	Name           string            `yaml:"name"`
	WorkDir        string            `yaml:"work_dir"`
	Env            map[string]string `yaml:"env"`
	CommandTimeout Duration          `yaml:"command_timeout"`
	MaxOutputBytes int64             `yaml:"max_output_bytes"`
	PreviewPorts   []int             `yaml:"preview_ports"`
}

func Load(path string) (*Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return nil, err
	}

	if cfg.Server.Addr == "" {
		cfg.Server.Addr = "127.0.0.1:8080"
	}
	if cfg.Server.AdminUsername == "" {
		cfg.Server.AdminUsername = "admin"
	}
	if cfg.Server.AdminPassword == "" {
		cfg.Server.AdminPassword = "admin123456"
	}
	if cfg.Server.DatabasePath == "" {
		cfg.Server.DatabasePath = "data/xmux.db"
	}
	if cfg.Server.AuditLogPath == "" {
		cfg.Server.AuditLogPath = "data/audit.jsonl"
	}
	if cfg.Server.WorkbenchStatePath == "" {
		cfg.Server.WorkbenchStatePath = "data/workbench_sessions.json"
	}
	if cfg.Server.AccountStorePath == "" {
		cfg.Server.AccountStorePath = "data/accounts.json"
	}
	if cfg.Server.AccountRegistrationEnabled == nil {
		cfg.Server.AccountRegistrationEnabled = boolPtr(true)
	}
	if cfg.Edge.ID == "" {
		cfg.Edge.ID = "local-edge"
	}
	if cfg.Edge.Name == "" {
		cfg.Edge.Name = "Local Edge"
	}
	if cfg.Edge.WorkDir == "" {
		cfg.Edge.WorkDir = "."
	}
	if cfg.Edge.CommandTimeout.Duration == 0 {
		cfg.Edge.CommandTimeout.Duration = 30 * time.Second
	}
	if cfg.Edge.MaxOutputBytes == 0 {
		cfg.Edge.MaxOutputBytes = 1 << 20
	}
	if len(cfg.Edge.PreviewPorts) == 0 {
		cfg.Edge.PreviewPorts = []int{3000, 5173, 8080}
	}
	cfg.Policy.AllowPaths = normalizePaths(cfg.Policy.AllowPaths)
	return &cfg, nil
}

func Ensure(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	cfg := Default()
	content, err := yaml.Marshal(cfg)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func Default() Config {
	return Config{
		Server: ServerConfig{
			Addr:                       "127.0.0.1:18001",
			AdminUsername:              "admin",
			AdminPassword:              "admin123456",
			DatabasePath:               "data/xmux.db",
			AccountStorePath:           "data/accounts.json",
			AccountRegistrationEnabled: boolPtr(true),
			AuditLogPath:               "data/audit.jsonl",
			WorkbenchStatePath:         "data/workbench_sessions.json",
			AllowHosts: []string{
				"127.0.0.1:18001",
				"localhost:18001",
			},
			AdminIPAllowlist: []string{
				"127.0.0.1",
				"::1",
			},
		},
		CloudTunnel: CloudTunnelConfig{
			Enabled: false,
		},
		Edge: EdgeConfig{
			ID:      "local-edge",
			Name:    "Local Developer Edge",
			WorkDir: ".",
			Env: map[string]string{
				"LANG": "C.UTF-8",
				"TERM": "xterm-256color",
			},
			CommandTimeout: Duration{Duration: 20 * time.Second},
			MaxOutputBytes: 1 << 20,
			PreviewPorts:   []int{3000, 5173, 8080},
		},
		Policy: policy.Config{
			Deny: []string{
				"rm",
				"reboot",
				"shutdown",
				"mkfs",
				"dd",
				"chmod",
				"chown",
				"sudo",
				"su",
				"bash",
				"sh",
				"zsh",
				"fish",
				"python",
				"python3",
				"perl",
				"ruby",
				"node",
			},
			AllowPaths: []string{
				".",
				os.TempDir(),
			},
			Commands: map[string]policy.CommandPolicy{
				"cat":    {Enabled: true, MaxArgs: 8},
				"cd":     {Enabled: true, MaxArgs: 8},
				"claude": {Enabled: true, Bin: "claude", Interactive: true, MaxArgs: 12},
				"clear":  {Enabled: true, MaxArgs: 0},
				"codex":  {Enabled: true, Bin: "codex", Interactive: true, MaxArgs: 12},
				"date":   {Enabled: true, MaxArgs: 4},
				"gemini": {Enabled: true, Bin: "gemini", Interactive: true, MaxArgs: 12},
				"ls":     {Enabled: true, MaxArgs: 8},
				"pwd":    {Enabled: true, MaxArgs: 0},
				"uname":  {Enabled: true, MaxArgs: 4},
				"whoami": {Enabled: true, MaxArgs: 0},
			},
		},
	}
}

type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func NewStore(path string, cfg *Config) *Store {
	return &Store{path: path, cfg: cloneConfig(*cfg)}
}

func (s *Store) Snapshot() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneConfig(s.cfg)
}

func (s *Store) Update(next Config) error {
	next = cloneConfig(next)
	if len(next.Policy.Commands) == 0 {
		return errors.New("policy.commands must not be empty")
	}
	if _, err := policy.NewEngine(next.Policy); err != nil {
		return err
	}
	return s.writeConfig(next)
}

// UpdateTunnel persists tunnel/edge connection fields only,
// leaving policy unchanged. Used by agent-mode admin.
func (s *Store) UpdateTunnel(tunnelEnabled bool, discoveryURL, gatewayURL, edgeName, edgeID string) error {
	s.mu.Lock()
	next := cloneConfig(s.cfg)
	s.mu.Unlock()
	next.CloudTunnel.Enabled = tunnelEnabled
	if strings.TrimSpace(discoveryURL) != "" {
		next.CloudTunnel.DiscoveryURL = strings.TrimSpace(discoveryURL)
	}
	if strings.TrimSpace(gatewayURL) != "" {
		next.CloudTunnel.GatewayURL = strings.TrimSpace(gatewayURL)
	}
	if strings.TrimSpace(edgeName) != "" {
		next.Edge.Name = strings.TrimSpace(edgeName)
	}
	if strings.TrimSpace(edgeID) != "" {
		next.Edge.ID = strings.TrimSpace(edgeID)
	}
	return s.writeConfig(next)
}

func (s *Store) writeConfig(next Config) error {
	content, err := yaml.Marshal(next)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.path, content, 0o600); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = next
	s.mu.Unlock()
	return nil
}

func (s *Store) PolicyEngine() (*policy.Engine, error) {
	cfg := s.Snapshot()
	return policy.NewEngine(cfg.Policy)
}

func (s *Store) UserPolicyEngine(account string, resolver interface {
	UserPolicy(string, policy.Config) (policy.Config, error)
}) (*policy.Engine, error) {
	cfg := s.Snapshot()
	if resolver == nil || strings.TrimSpace(account) == "" {
		return policy.NewEngine(cfg.Policy)
	}
	userPolicy, err := resolver.UserPolicy(account, cfg.Policy)
	if err != nil {
		return nil, err
	}
	return policy.NewEngine(userPolicy)
}

func (s *Store) DatabasePath() string {
	cfg := s.Snapshot()
	return cfg.Server.DatabasePath
}

func (s *Store) AccountStorePath() string {
	cfg := s.Snapshot()
	return cfg.Server.AccountStorePath
}

func (s *Store) AccountRegistrationEnabled() bool {
	cfg := s.Snapshot()
	return cfg.Server.RegistrationEnabled()
}

func (s *Store) CloudTunnel() CloudTunnelConfig {
	cfg := s.Snapshot()
	return cfg.CloudTunnel
}

func (s *Store) AllowHosts() []string {
	cfg := s.Snapshot()
	return cfg.Server.AllowHosts
}

func (s *Store) AdminIPAllowlist() []string {
	cfg := s.Snapshot()
	return cfg.Server.AdminIPAllowlist
}

func cloneConfig(cfg Config) Config {
	cfg.Server.AllowHosts = slices.Clone(cfg.Server.AllowHosts)
	cfg.Server.AdminIPAllowlist = slices.Clone(cfg.Server.AdminIPAllowlist)
	if cfg.Server.AccountRegistrationEnabled != nil {
		value := *cfg.Server.AccountRegistrationEnabled
		cfg.Server.AccountRegistrationEnabled = &value
	}
	cfg.Edge.Env = cloneMap(cfg.Edge.Env)
	cfg.Edge.PreviewPorts = slices.Clone(cfg.Edge.PreviewPorts)
	cfg.Policy.Deny = slices.Clone(cfg.Policy.Deny)
	cfg.Policy.AllowPaths = slices.Clone(cfg.Policy.AllowPaths)
	cfg.Policy.Commands = cloneCommands(cfg.Policy.Commands)
	return cfg
}

func (c ServerConfig) RegistrationEnabled() bool {
	if c.AccountRegistrationEnabled == nil {
		return true
	}
	return *c.AccountRegistrationEnabled
}

func boolPtr(value bool) *bool {
	return &value
}

func cloneCommands(commands map[string]policy.CommandPolicy) map[string]policy.CommandPolicy {
	if commands == nil {
		return nil
	}
	cloned := make(map[string]policy.CommandPolicy, len(commands))
	for name, rule := range commands {
		rule.Subcommands = slices.Clone(rule.Subcommands)
		rule.AllowPaths = slices.Clone(rule.AllowPaths)
		cloned[name] = rule
	}
	return cloned
}

func cloneMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func NormalizePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if absolute, err := filepath.Abs(value); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(value)
}

func normalizePaths(values []string) []string {
	if values == nil {
		return nil
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = NormalizePath(value)
		if value != "" {
			normalized = append(normalized, value)
		}
	}
	return normalized
}
