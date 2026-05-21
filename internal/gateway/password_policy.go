package gateway

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// passwordPolicy is the runtime-tunable password complexity config.
// Loaded from app_settings, with sensible defaults if unset.
type passwordPolicy struct {
	MinLength     int  `json:"min_length"`
	RequireUpper  bool `json:"require_upper"`
	RequireLower  bool `json:"require_lower"`
	RequireDigit  bool `json:"require_digit"`
	RequireSymbol bool `json:"require_symbol"`
	DenyCommon    bool `json:"deny_common"`
	MaxLength     int  `json:"max_length"`
}

func defaultPasswordPolicy() passwordPolicy {
	return passwordPolicy{
		MinLength:     10,
		RequireUpper:  true,
		RequireLower:  true,
		RequireDigit:  true,
		RequireSymbol: false,
		DenyCommon:    true,
		MaxLength:     128,
	}
}

// commonPasswords is a small embedded list of widely-leaked passwords. Kept
// short on purpose — admins concerned about credential stuffing should layer
// rate limiting or external breach-list checks on top.
var commonPasswords = map[string]struct{}{
	"123456": {}, "12345678": {}, "123456789": {}, "1234567890": {},
	"qwerty": {}, "qwertyuiop": {}, "abc123": {}, "password": {},
	"password1": {}, "password123": {}, "admin": {}, "admin123": {},
	"admin123456": {}, "letmein": {}, "welcome": {}, "welcome1": {},
	"iloveyou": {}, "monkey": {}, "dragon": {}, "master": {},
	"superman": {}, "hello": {}, "hello123": {}, "login": {},
	"passw0rd": {}, "p@ssword": {}, "p@ssw0rd": {}, "1q2w3e4r": {},
	"1q2w3e4r5t": {}, "qazwsx": {}, "trustno1": {}, "test": {},
	"test123": {}, "user": {}, "user123": {}, "guest": {},
	"changeme": {}, "default": {}, "root": {}, "root123": {},
	"toor": {}, "11111111": {}, "00000000": {}, "asdfghjkl": {},
	"zxcvbnm": {}, "qwerty123": {}, "abcd1234": {}, "1234qwer": {},
}

func isCommonPassword(pw string) bool {
	_, ok := commonPasswords[strings.ToLower(pw)]
	return ok
}

// validatePassword reports the first complexity violation. Returns nil when
// the password satisfies every enabled rule.
func validatePassword(pw string, policy passwordPolicy) error {
	if pw == "" {
		return errors.New("password is required")
	}
	if policy.MinLength <= 0 {
		policy.MinLength = 8
	}
	if policy.MaxLength <= 0 {
		policy.MaxLength = 128
	}
	if len(pw) < policy.MinLength {
		return fmt.Errorf("password must be at least %d characters", policy.MinLength)
	}
	if len(pw) > policy.MaxLength {
		return fmt.Errorf("password must be at most %d characters", policy.MaxLength)
	}
	var hasUpper, hasLower, hasDigit, hasSymbol bool
	for _, r := range pw {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			hasSymbol = true
		}
	}
	if policy.RequireUpper && !hasUpper {
		return errors.New("password must contain at least one uppercase letter")
	}
	if policy.RequireLower && !hasLower {
		return errors.New("password must contain at least one lowercase letter")
	}
	if policy.RequireDigit && !hasDigit {
		return errors.New("password must contain at least one digit")
	}
	if policy.RequireSymbol && !hasSymbol {
		return errors.New("password must contain at least one symbol")
	}
	if policy.DenyCommon && isCommonPassword(pw) {
		return errors.New("password is too common; please choose a stronger one")
	}
	return nil
}
