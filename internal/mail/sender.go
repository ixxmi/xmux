// Package mail provides outbound email delivery for transactional flows
// (account verification, password reset, etc.). The active Sender is held
// behind a Reloader so admins can edit SMTP settings at runtime without
// restarting the gateway.
package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// Message is the payload handed to a Sender. Either HTMLBody, TextBody, or
// both must be non-empty. Subject and To are required.
type Message struct {
	To       string
	Subject  string
	HTMLBody string
	TextBody string
}

func (m Message) validate() error {
	if strings.TrimSpace(m.To) == "" {
		return errors.New("mail: To is required")
	}
	if strings.TrimSpace(m.Subject) == "" {
		return errors.New("mail: Subject is required")
	}
	if strings.TrimSpace(m.HTMLBody) == "" && strings.TrimSpace(m.TextBody) == "" {
		return errors.New("mail: HTMLBody or TextBody is required")
	}
	return nil
}

// Sender delivers a Message. Implementations must be safe for concurrent use.
type Sender interface {
	Send(ctx context.Context, msg Message) error
	Kind() string
}

// LogSender is the no-real-delivery fallback. It logs every message at info
// level so devs can copy the verification/reset link out of the gateway log
// when SMTP isn't configured. NEVER use in production.
type LogSender struct {
	Logger *slog.Logger
}

func (l *LogSender) Send(_ context.Context, msg Message) error {
	if err := msg.validate(); err != nil {
		return err
	}
	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}
	body := msg.TextBody
	if body == "" {
		body = msg.HTMLBody
	}
	logger.Info("mail.log_sender",
		"to", msg.To,
		"subject", msg.Subject,
		"body_preview", trimForLog(body, 1024),
	)
	return nil
}

func (l *LogSender) Kind() string { return "log" }

func trimForLog(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "...(truncated)"
}

// Reloader holds the currently-active Sender and lets it be swapped at
// runtime (e.g. after admins save new SMTP settings). Zero value is unusable;
// always use NewReloader.
type Reloader struct {
	mu     sync.RWMutex
	sender Sender
}

func NewReloader(initial Sender) *Reloader {
	if initial == nil {
		initial = &LogSender{}
	}
	return &Reloader{sender: initial}
}

func (r *Reloader) Set(sender Sender) {
	if r == nil {
		return
	}
	if sender == nil {
		sender = &LogSender{}
	}
	r.mu.Lock()
	r.sender = sender
	r.mu.Unlock()
}

func (r *Reloader) Send(ctx context.Context, msg Message) error {
	if r == nil {
		return errors.New("mail: reloader is nil")
	}
	r.mu.RLock()
	sender := r.sender
	r.mu.RUnlock()
	if sender == nil {
		return errors.New("mail: no sender configured")
	}
	return sender.Send(ctx, msg)
}

func (r *Reloader) Kind() string {
	if r == nil {
		return "none"
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.sender == nil {
		return "none"
	}
	return r.sender.Kind()
}

// FormatAddress combines name and email into a single "Name <addr>" header
// value. Used by SMTP sender for both From and To.
func FormatAddress(name, addr string) string {
	addr = strings.TrimSpace(addr)
	name = strings.TrimSpace(name)
	if addr == "" {
		return ""
	}
	if name == "" {
		return addr
	}
	return fmt.Sprintf("%s <%s>", name, addr)
}
