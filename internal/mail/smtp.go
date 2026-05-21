package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wneessen/go-mail"
)

// SMTPConfig is the runtime input for building an SMTPSender. Password is the
// plaintext (the caller decrypts ciphertext from app_settings before handing
// it in).
type SMTPConfig struct {
	Host          string
	Port          int
	Username      string
	Password      string
	FromAddress   string
	FromName      string
	UseStartTLS   bool
	UseSMTPS      bool
	SkipTLSVerify bool
	Timeout       time.Duration
}

func (c SMTPConfig) validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("smtp: host is required")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return errors.New("smtp: port is out of range")
	}
	if strings.TrimSpace(c.FromAddress) == "" {
		return errors.New("smtp: from_address is required")
	}
	return nil
}

// SMTPSender delivers messages via SMTP using github.com/wneessen/go-mail.
type SMTPSender struct {
	cfg SMTPConfig
}

func NewSMTPSender(cfg SMTPConfig) (*SMTPSender, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	return &SMTPSender{cfg: cfg}, nil
}

func (s *SMTPSender) Kind() string { return "smtp" }

func (s *SMTPSender) Send(ctx context.Context, msg Message) error {
	if err := msg.validate(); err != nil {
		return err
	}

	m := mail.NewMsg()
	from := FormatAddress(s.cfg.FromName, s.cfg.FromAddress)
	if err := m.FromFormat(s.cfg.FromName, s.cfg.FromAddress); err != nil {
		return fmt.Errorf("smtp: invalid from %q: %w", from, err)
	}
	if err := m.To(msg.To); err != nil {
		return fmt.Errorf("smtp: invalid to %q: %w", msg.To, err)
	}
	m.Subject(msg.Subject)
	if msg.TextBody != "" {
		m.SetBodyString(mail.TypeTextPlain, msg.TextBody)
	}
	if msg.HTMLBody != "" {
		if msg.TextBody != "" {
			m.AddAlternativeString(mail.TypeTextHTML, msg.HTMLBody)
		} else {
			m.SetBodyString(mail.TypeTextHTML, msg.HTMLBody)
		}
	}

	options := []mail.Option{
		mail.WithPort(s.cfg.Port),
		mail.WithTimeout(s.cfg.Timeout),
	}
	switch {
	case s.cfg.UseSMTPS:
		options = append(options, mail.WithSSLPort(false))
	case s.cfg.UseStartTLS:
		options = append(options, mail.WithTLSPolicy(mail.TLSMandatory))
	default:
		options = append(options, mail.WithTLSPolicy(mail.TLSOpportunistic))
	}
	if s.cfg.SkipTLSVerify {
		options = append(options, mail.WithTLSConfig(&tls.Config{InsecureSkipVerify: true}))
	}
	if s.cfg.Username != "" || s.cfg.Password != "" {
		options = append(options,
			mail.WithSMTPAuth(mail.SMTPAuthPlain),
			mail.WithUsername(s.cfg.Username),
			mail.WithPassword(s.cfg.Password),
		)
	}

	client, err := mail.NewClient(s.cfg.Host, options...)
	if err != nil {
		return fmt.Errorf("smtp: new client: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := client.DialAndSendWithContext(ctx, m); err != nil {
		return fmt.Errorf("smtp: send: %w", err)
	}
	return nil
}
