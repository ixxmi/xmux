package agent

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestInteractiveStartArgsOnlyPrependsAbsoluteBin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		bin  string
		args []string
		want []string
	}{
		{name: "empty bin", args: []string{"--resume"}, want: []string{"--resume"}},
		{name: "command name bin", bin: "codex", want: nil},
		{name: "relative bin", bin: "bin/codex", args: []string{"--resume"}, want: []string{"--resume"}},
		{name: "absolute bin", bin: "/opt/homebrew/bin/codex", args: []string{"--resume"}, want: []string{"/opt/homebrew/bin/codex", "--resume"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := interactiveStartArgs(tt.bin, tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("interactiveStartArgs(%q, %v) = %v, want %v", tt.bin, tt.args, got, tt.want)
			}
		})
	}
}

func TestIsAgentCommandAliasDoesNotCollapseBinaryPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		agentID string
		want    bool
	}{
		{name: "matching codex command", value: "codex", agentID: "codex", want: true},
		{name: "matching claude alias", value: "claude-code", agentID: "claude", want: true},
		{name: "absolute codex binary", value: "/usr/local/bin/codex", agentID: "codex", want: false},
		{name: "relative codex binary path", value: "bin/codex", agentID: "codex", want: false},
		{name: "different command", value: "claude", agentID: "codex", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isAgentCommandAlias(tt.value, tt.agentID); got != tt.want {
				t.Fatalf("isAgentCommandAlias(%q, %q) = %v, want %v", tt.value, tt.agentID, got, tt.want)
			}
		})
	}
}

func TestEffectiveAllowedPathsRequiresAccountPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")

	if got := effectiveAllowedPaths([]string{root}, nil, false); len(got) != 1 || got[0] != root {
		t.Fatalf("without account requirement = %v, want local root", got)
	}
	if got := effectiveAllowedPaths([]string{root}, nil, true); len(got) != 0 {
		t.Fatalf("with account requirement = %v, want empty", got)
	}
	if got := effectiveAllowedPaths([]string{root}, []string{allowed}, true); len(got) != 1 || got[0] != allowed {
		t.Fatalf("account path = %v, want %s", got, allowed)
	}
	remoteOnly := filepath.Join(t.TempDir(), "remote")
	if got := effectiveAllowedPaths([]string{root}, []string{remoteOnly}, true); len(got) != 1 || got[0] != remoteOnly {
		t.Fatalf("remote account path = %v, want %s", got, remoteOnly)
	}
}
