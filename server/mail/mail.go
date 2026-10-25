// Package mail sends the sign-in codes for email-code deployments over SMTP
// (Gmail by default) with an app password, the same way super-atlas does.
package mail

import (
	"errors"
	"fmt"
	"html"
	"log"

	"gopkg.in/gomail.v2"

	"main/config"
)

// ErrOff is returned when SMTP isn't configured.
var ErrOff = errors.New("email isn't set up on this server")

type Sender struct {
	host        string
	port        int
	email       string
	appPassword string
	// logCodes prints codes to the server log instead of failing when SMTP
	// isn't configured, so local dev (TLS off) works without a mail account.
	logCodes bool
}

func New(cfg config.Config) *Sender {
	return &Sender{
		host: cfg.SMTPHost, port: cfg.SMTPPort, email: cfg.SMTPEmail, appPassword: cfg.SMTPAppPassword,
		logCodes: !cfg.TLSOn(),
	}
}

// Ready reports whether SendCode can deliver (or, in dev, log) a code.
func (s *Sender) Ready() bool {
	return (s.email != "" && s.appPassword != "") || s.logCodes
}

// SendCode emails code, the sign-in code for host, to to.
func (s *Sender) SendCode(to, host, code string) error {
	if s.email == "" || s.appPassword == "" {
		if s.logCodes {
			log.Printf("sign-in code for %s on %s: %s (SMTP not configured)", to, host, code)
			return nil
		}
		return ErrOff
	}

	m := gomail.NewMessage()
	m.SetHeader("From", m.FormatAddress(s.email, "Super Matrix"))
	m.SetHeader("To", to)
	m.SetHeader("Subject", fmt.Sprintf("%s is your code for %s", code, host))
	m.SetBody("text/html", fmt.Sprintf(`
        <p>Your code to sign in to <b>%s</b> is:</p>
        <h2 style="letter-spacing: 4px;">%s</h2>
        <p>It's valid for 10 minutes. If you didn't ask for it, you can ignore this email.</p>
    `, html.EscapeString(host), code))

	d := gomail.NewDialer(s.host, s.port, s.email, s.appPassword)
	if err := d.DialAndSend(m); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}
