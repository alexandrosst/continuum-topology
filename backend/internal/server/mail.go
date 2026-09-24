package server

import (
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// MailConfig is how this server sends the handful of messages it ever sends: a login or email-verification
// code, nothing else - there is no newsletter, no digest, nothing to unsubscribe from. Built against the
// standard library alone, the same choice totp.go made for the same reason: smtp.SendMail already speaks
// STARTTLS to a server that offers it (net/smtp negotiates it before authenticating, so there is no TLS
// handshake to hand-roll here), and PLAIN/LOGIN auth against a submission port is a well-specified handful of
// lines, not something worth a dependency for.
type MailConfig struct {
	Host, Port, Username, Password, From string
}

// Enabled reports whether an operator has configured outgoing mail at all. Nothing that needs to send a
// message should be reachable unless this is true - see RequestEmailVerification and RequestLoginEmailCode.
func (m MailConfig) Enabled() bool { return strings.TrimSpace(m.Host) != "" }

// smtpSendMail is net/smtp.SendMail by default; tests replace it with a fake that records the message
// instead of opening a real connection, the same seam Core.Now gives tests over the clock.
var smtpSendMail = smtp.SendMail

// send delivers a short plain-text message. Never called unless Enabled().
func (m MailConfig) send(to, subject, body string) error {
	if !m.Enabled() {
		return fmt.Errorf("this server has no outgoing mail configured")
	}
	addr := net.JoinHostPort(m.Host, m.Port)
	var auth smtp.Auth
	if m.Username != "" {
		auth = smtp.PlainAuth("", m.Username, m.Password, m.Host)
	}
	msg := mimeMessage(m.From, to, subject, body)
	return smtpSendMail(addr, auth, m.From, []string{to}, msg)
}

// mimeMessage builds the minimal RFC 5322 message smtp.SendMail wants as its raw DATA: headers, a blank
// line, then the body. Plain text only - a code to type back has no reason to be HTML.
func mimeMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	b.WriteString("\r\n")
	return []byte(b.String())
}
