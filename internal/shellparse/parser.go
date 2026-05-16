package shellparse

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

type Parsed struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

var (
	ErrEmpty       = errors.New("empty input")
	ErrShellSyntax = errors.New("shell syntax is not supported")
)

func ParseLine(input string) (*Parsed, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, ErrEmpty
	}

	tokens, err := tokenize(input)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, ErrEmpty
	}

	return &Parsed{Command: tokens[0], Args: tokens[1:]}, nil
}

func tokenize(input string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	var quote rune
	escaped := false

	for _, r := range input {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\':
			if quote == '\'' {
				current.WriteRune(r)
				continue
			}
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			if r == '$' || r == '`' {
				return nil, fmt.Errorf("%w: expansion is disabled", ErrShellSyntax)
			}
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		case isForbiddenRune(r):
			return nil, fmt.Errorf("%w: forbidden token %q", ErrShellSyntax, string(r))
		default:
			current.WriteRune(r)
		}
	}

	if escaped {
		return nil, fmt.Errorf("%w: trailing escape", ErrShellSyntax)
	}
	if quote != 0 {
		return nil, fmt.Errorf("%w: unterminated quote", ErrShellSyntax)
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}

	return tokens, nil
}

func isForbiddenRune(r rune) bool {
	switch r {
	case ';', '&', '|', '$', '`', '(', ')', '>', '<':
		return true
	default:
		return false
	}
}
