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
	Server ServerConfig  `yaml:"server"`
	Edge   EdgeConfig    `yaml:"edge"`
	Policy policy.Config `yaml:"policy"`
}

type ServerConfig struct {
	Addr               string   `yaml:"addr"`
	AuthToken          string   `yaml:"auth_token"`
	AdminToken         string   `yaml:"admin_token"`
	TunnelToken        string   `yaml:"tunnel_token"`
	AuditLogPath       string   `yaml:"audit_log_path"`
	WorkbenchStatePath string   `yaml:"workbench_state_path"`
	AllowHosts         []string `yaml:"allow_hosts"`
	AdminIPAllowlist   []string `yaml:"admin_ip_allowlist"`
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
	if cfg.Server.AuditLogPath == "" {
		cfg.Server.AuditLogPath = "data/audit.jsonl"
	}
	if cfg.Server.WorkbenchStatePath == "" {
		cfg.Server.WorkbenchStatePath = "data/workbench_sessions.json"
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
	if cfg.Server.AuthToken == "" {
		return nil, errors.New("server.auth_token is required")
	}
	if cfg.Server.AdminToken == "" {
		cfg.Server.AdminToken = cfg.Server.AuthToken
	}
	if cfg.Server.TunnelToken == "" {
		cfg.Server.TunnelToken = cfg.Server.AuthToken
	}
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
			Addr:               "127.0.0.1:18001",
			AuthToken:          "change-me-terminal-token",
			AdminToken:         "change-me-admin-token",
			TunnelToken:        "change-me-tunnel-token",
			AuditLogPath:       "data/audit.jsonl",
			WorkbenchStatePath: "data/workbench_sessions.json",
			AllowHosts: []string{
				"127.0.0.1:18001",
				"localhost:18001",
			},
			AdminIPAllowlist: []string{
				"127.0.0.1",
				"::1",
			},
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
	if next.Server.AuthToken == "" {
		return errors.New("server.auth_token is required")
	}
	if next.Server.AdminToken == "" {
		return errors.New("server.admin_token is required")
	}
	if len(next.Policy.Commands) == 0 {
		return errors.New("policy.commands must not be empty")
	}
	if _, err := policy.NewEngine(next.Policy); err != nil {
		return err
	}

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

func (s *Store) TerminalToken() string {
	cfg := s.Snapshot()
	return cfg.Server.AuthToken
}

func (s *Store) AdminToken() string {
	cfg := s.Snapshot()
	return cfg.Server.AdminToken
}

func (s *Store) TunnelToken() string {
	cfg := s.Snapshot()
	return cfg.Server.TunnelToken
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
	cfg.Edge.Env = cloneMap(cfg.Edge.Env)
	cfg.Edge.PreviewPorts = slices.Clone(cfg.Edge.PreviewPorts)
	cfg.Policy.Deny = slices.Clone(cfg.Policy.Deny)
	cfg.Policy.AllowPaths = slices.Clone(cfg.Policy.AllowPaths)
	cfg.Policy.Commands = cloneCommands(cfg.Policy.Commands)
	return cfg
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
