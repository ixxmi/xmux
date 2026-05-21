// Package mailtemplates renders the HTML/text emails sent by the gateway.
// Templates are embedded so deployments don't need to ship loose files.
package mailtemplates

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"strings"
	texttemplate "text/template"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

var (
	htmlTpl = template.Must(template.ParseFS(templatesFS, "templates/*.html.tmpl"))
	textTpl = texttemplate.Must(texttemplate.ParseFS(templatesFS, "templates/*.txt.tmpl"))
)

// VerifyData populates the verification email template.
type VerifyData struct {
	Username  string
	VerifyURL string
	ExpiresIn string
	RequestIP string
	UserAgent string
}

// ResetData populates the password reset email template.
type ResetData struct {
	Username  string
	ResetURL  string
	ExpiresIn string
	RequestIP string
	UserAgent string
}

func RenderVerifyEmail(data VerifyData) (htmlBody, textBody string, err error) {
	htmlBody, err = renderHTML("verify_email.html.tmpl", data)
	if err != nil {
		return "", "", err
	}
	textBody, err = renderText("verify_email.txt.tmpl", data)
	if err != nil {
		return "", "", err
	}
	return htmlBody, textBody, nil
}

func RenderResetEmail(data ResetData) (htmlBody, textBody string, err error) {
	htmlBody, err = renderHTML("password_reset.html.tmpl", data)
	if err != nil {
		return "", "", err
	}
	textBody, err = renderText("password_reset.txt.tmpl", data)
	if err != nil {
		return "", "", err
	}
	return htmlBody, textBody, nil
}

func renderHTML(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := htmlTpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("render html %s: %w", name, err)
	}
	return buf.String(), nil
}

func renderText(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := textTpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("render text %s: %w", name, err)
	}
	return strings.TrimSpace(buf.String()) + "\n", nil
}
