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
	if cfg.Server.Addr != "127.0.0.1:18001" {
		t.Fatalf("Server.Addr = %q", cfg.Server.Addr)
	}
	if cfg.Server.AdminUsername != "admin" {
		t.Fatalf("Server.AdminUsername = %q", cfg.Server.AdminUsername)
	}
	if cfg.Server.AdminPassword != "admin123456" {
		t.Fatalf("Server.AdminPassword = %q", cfg.Server.AdminPassword)
	}
	if cfg.Server.DatabasePath != "data/xmux.db" {
		t.Fatalf("Server.DatabasePath = %q", cfg.Server.DatabasePath)
	}
	if cfg.Server.AccountStorePath != "data/accounts.json" {
		t.Fatalf("Server.AccountStorePath = %q", cfg.Server.AccountStorePath)
	}
	if !cfg.Server.RegistrationEnabled() {
		t.Fatal("Server.AccountRegistrationEnabled = false, want true")
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
