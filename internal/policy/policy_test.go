package policy

import "testing"

func TestDecideAllowsWhitelistedCommand(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(Config{Commands: map[string]CommandPolicy{
		"kubectl": {Enabled: true, Subcommands: []string{"get"}},
	}})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	decision, err := engine.Decide("kubectl", []string{"get", "pods"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Bin != "kubectl" {
		t.Fatalf("bin = %q", decision.Bin)
	}
}

func TestDecideCarriesInteractiveFlag(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(Config{Commands: map[string]CommandPolicy{
		"codex": {Enabled: true, Interactive: true},
		"ls":    {Enabled: true},
	}})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	codex, err := engine.Decide("codex", nil)
	if err != nil {
		t.Fatalf("Decide codex: %v", err)
	}
	if !codex.Interactive {
		t.Fatal("expected codex to be interactive")
	}

	ls, err := engine.Decide("ls", nil)
	if err != nil {
		t.Fatalf("Decide ls: %v", err)
	}
	if ls.Interactive {
		t.Fatal("expected ls to remain non-interactive")
	}
}

func TestDecideAllowsInteractiveBinaryOverride(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(Config{Commands: map[string]CommandPolicy{
		"codex": {Enabled: true, Interactive: true, MaxArgs: 4},
	}})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	decision, err := engine.Decide("codex", []string{"/opt/homebrew/bin/codex", "--model", "gpt-5"})
	if err != nil {
		t.Fatalf("Decide codex: %v", err)
	}
	if decision.Bin != "/opt/homebrew/bin/codex" {
		t.Fatalf("bin = %q, want override", decision.Bin)
	}
	if len(decision.Args) != 2 || decision.Args[0] != "--model" || decision.Args[1] != "gpt-5" {
		t.Fatalf("args = %v, want override stripped", decision.Args)
	}
}

func TestDecideRejectsDeniedCommand(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(Config{
		Deny: []string{"bash"},
		Commands: map[string]CommandPolicy{
			"bash": {Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	if _, err := engine.Decide("bash", nil); err == nil {
		t.Fatal("expected deny")
	}
}

func TestDecideRejectsSubcommand(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(Config{Commands: map[string]CommandPolicy{
		"docker": {Enabled: true, Subcommands: []string{"ps"}},
	}})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	if _, err := engine.Decide("docker", []string{"run", "nginx"}); err == nil {
		t.Fatal("expected subcommand deny")
	}
}

func TestDecideRestrictsAbsolutePaths(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(Config{Commands: map[string]CommandPolicy{
		"cat": {Enabled: true, AllowPaths: []string{"/tmp"}},
	}})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	if _, err := engine.Decide("cat", []string{"/etc/passwd"}); err == nil {
		t.Fatal("expected path deny")
	}
	if _, err := engine.Decide("cat", []string{"/tmp/app.log"}); err != nil {
		t.Fatalf("expected allow: %v", err)
	}
}

func TestDecideUsesGlobalAllowedPaths(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(Config{
		AllowPaths: []string{"/tmp"},
		Commands: map[string]CommandPolicy{
			"cat": {Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	if _, err := engine.Decide("cat", []string{"/etc/passwd"}); err == nil {
		t.Fatal("expected path deny")
	}
	if _, err := engine.Decide("cat", []string{"/tmp/app.log"}); err != nil {
		t.Fatalf("expected allow: %v", err)
	}
}
