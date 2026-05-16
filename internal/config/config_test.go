package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureCreatesDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")

	created, err := Ensure(path)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if !created {
		t.Fatal("Ensure() created = false, want true")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat generated config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("generated config mode = %v, want 0600", info.Mode().Perm())
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() generated config error = %v", err)
	}
	if cfg.Server.AuthToken != "change-me-terminal-token" {
		t.Fatalf("Server.AuthToken = %q", cfg.Server.AuthToken)
	}
	if cfg.Server.AdminToken != "change-me-admin-token" {
		t.Fatalf("Server.AdminToken = %q", cfg.Server.AdminToken)
	}
	if cfg.Server.Addr != "127.0.0.1:18001" {
		t.Fatalf("Server.Addr = %q", cfg.Server.Addr)
	}
	for _, command := range []string{"codex", "claude", "gemini"} {
		rule, ok := cfg.Policy.Commands[command]
		if !ok {
			t.Fatalf("default command %q missing", command)
		}
		if !rule.Enabled || !rule.Interactive {
			t.Fatalf("default command %q = %+v, want enabled interactive", command, rule)
		}
	}

	created, err = Ensure(path)
	if err != nil {
		t.Fatalf("Ensure() existing config error = %v", err)
	}
	if created {
		t.Fatal("Ensure() existing config created = true, want false")
	}
}
