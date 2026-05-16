package shellparse

import "testing"

func TestParseLine(t *testing.T) {
	t.Parallel()

	parsed, err := ParseLine(`kubectl get pods -A`)
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if parsed.Command != "kubectl" {
		t.Fatalf("command = %q", parsed.Command)
	}
	if len(parsed.Args) != 3 || parsed.Args[0] != "get" || parsed.Args[2] != "-A" {
		t.Fatalf("args = %#v", parsed.Args)
	}
}

func TestParseLineRejectsShellOperators(t *testing.T) {
	t.Parallel()

	bad := []string{
		"ls && rm -rf /",
		"cat /tmp/a | sh",
		"echo $(whoami)",
		"echo `whoami`",
		"date > /tmp/out",
	}

	for _, input := range bad {
		if _, err := ParseLine(input); err == nil {
			t.Fatalf("expected %q to fail", input)
		}
	}
}

func TestParseLineAllowsQuotedArgs(t *testing.T) {
	t.Parallel()

	parsed, err := ParseLine(`ls "/tmp/example dir"`)
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if got := parsed.Args[0]; got != "/tmp/example dir" {
		t.Fatalf("arg = %q", got)
	}
}
