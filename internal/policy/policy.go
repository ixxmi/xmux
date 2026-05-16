package policy

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

type Config struct {
	Deny       []string                 `yaml:"deny"`
	AllowPaths []string                 `yaml:"allow_paths"`
	Commands   map[string]CommandPolicy `yaml:"commands"`
}

type CommandPolicy struct {
	Enabled     bool     `yaml:"enabled"`
	Bin         string   `yaml:"bin"`
	Interactive bool     `yaml:"interactive"`
	Subcommands []string `yaml:"subcommands"`
	AllowPaths  []string `yaml:"allow_paths,omitempty"`
	MaxArgs     int      `yaml:"max_args"`
}

type Engine struct {
	cfg  Config
	deny map[string]struct{}
}

type Decision struct {
	Command     string
	Args        []string
	Bin         string
	Interactive bool
}

var ErrDenied = errors.New("command denied by policy")

func NewEngine(cfg Config) (*Engine, error) {
	if len(cfg.Commands) == 0 {
		return nil, errors.New("policy.commands must not be empty")
	}

	deny := make(map[string]struct{}, len(cfg.Deny))
	for _, cmd := range cfg.Deny {
		cmd = strings.TrimSpace(cmd)
		if cmd != "" {
			deny[cmd] = struct{}{}
		}
	}

	return &Engine{cfg: cfg, deny: deny}, nil
}

func (e *Engine) Decide(command string, args []string) (*Decision, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("%w: empty command", ErrDenied)
	}

	if _, denied := e.deny[command]; denied {
		return nil, fmt.Errorf("%w: %s is globally denied", ErrDenied, command)
	}

	rule, ok := e.cfg.Commands[command]
	if !ok || !rule.Enabled {
		return nil, fmt.Errorf("%w: %s is not whitelisted", ErrDenied, command)
	}

	if rule.MaxArgs > 0 && len(args) > rule.MaxArgs {
		return nil, fmt.Errorf("%w: %s accepts at most %d arguments", ErrDenied, command, rule.MaxArgs)
	}

	bin := rule.Bin
	if bin == "" {
		bin = command
	}
	decisionArgs := slices.Clone(args)
	if rule.Interactive && len(decisionArgs) > 0 && filepath.IsAbs(filepath.Clean(decisionArgs[0])) {
		bin = decisionArgs[0]
		decisionArgs = decisionArgs[1:]
	}

	if len(rule.Subcommands) > 0 {
		if len(decisionArgs) == 0 {
			return nil, fmt.Errorf("%w: %s requires a subcommand", ErrDenied, command)
		}
		subcommand := decisionArgs[0]
		if !slices.Contains(rule.Subcommands, subcommand) {
			return nil, fmt.Errorf("%w: %s %s is not allowed", ErrDenied, command, subcommand)
		}
	}

	allowedPaths := append(slices.Clone(e.cfg.AllowPaths), rule.AllowPaths...)
	if len(allowedPaths) > 0 {
		if err := validatePaths(decisionArgs, allowedPaths); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDenied, err)
		}
	}

	return &Decision{Command: command, Args: decisionArgs, Bin: bin, Interactive: rule.Interactive}, nil
}

func validatePaths(args []string, allowedRoots []string) error {
	for _, arg := range args {
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}

		cleaned := filepath.Clean(arg)
		if !filepath.IsAbs(cleaned) {
			continue
		}

		if !pathAllowed(cleaned, allowedRoots) {
			return fmt.Errorf("path %s is outside allowed roots", arg)
		}
	}
	return nil
}

func pathAllowed(path string, roots []string) bool {
	for _, root := range roots {
		cleanRoot := filepath.Clean(root)
		rel, err := filepath.Rel(cleanRoot, path)
		if err == nil && (rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")) {
			return true
		}
	}
	return false
}
