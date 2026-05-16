package edge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cloud-terminal/internal/policy"
)

type staticPolicyProvider struct {
	cfg policy.Config
}

func (p staticPolicyProvider) PolicyEngine() (*policy.Engine, error) {
	return policy.NewEngine(p.cfg)
}

func TestExecCDReturnsNewWorkDirAndCommandUsesIt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	subdir := filepath.Join(root, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(Options{
		DefaultDir: root,
		PolicyProvider: staticPolicyProvider{cfg: policy.Config{
			AllowPaths: []string{root},
			Commands: map[string]policy.CommandPolicy{
				"cd":  {Enabled: true, MaxArgs: 1},
				"pwd": {Enabled: true},
			},
		}},
	})

	cd := runtime.ParseAndExec(context.Background(), ExecRequest{RequestID: "cd"}, "cd subdir")
	if cd.ExitCode != 0 {
		t.Fatalf("cd failed: %+v", cd)
	}
	if cd.WorkDir != subdir {
		t.Fatalf("WorkDir = %q, want %q", cd.WorkDir, subdir)
	}

	pwd := runtime.ParseAndExec(context.Background(), ExecRequest{RequestID: "pwd", WorkDir: cd.WorkDir}, "pwd")
	if pwd.ExitCode != 0 {
		t.Fatalf("pwd failed: %+v", pwd)
	}
	if !strings.Contains(pwd.Stdout, subdir) {
		t.Fatalf("pwd stdout = %q, want it to contain %q", pwd.Stdout, subdir)
	}
}

func TestExecCDRejectsPathOutsideAllowPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	other := t.TempDir()
	runtime := NewRuntime(Options{
		DefaultDir: root,
		PolicyProvider: staticPolicyProvider{cfg: policy.Config{
			AllowPaths: []string{root},
			Commands: map[string]policy.CommandPolicy{
				"cd": {Enabled: true, MaxArgs: 1},
			},
		}},
	})

	result := runtime.ParseAndExec(context.Background(), ExecRequest{RequestID: "cd"}, "cd "+other)
	if !result.Denied {
		t.Fatalf("expected cd outside allow paths to be denied: %+v", result)
	}
}
