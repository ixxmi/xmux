package agent

import (
	"path/filepath"
	"testing"
)

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
