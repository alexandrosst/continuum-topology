package server

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// MailConfig is how this server sends the handful of messages it ever sends: a login or email-verification
// code, nothing else - there is no newsletter, no digest, nothing to unsubscribe from. Built against the
// standard library alone, the same choice totp.go made for the same reason: PLAIN/LOGIN auth against a
// submission port is a well-specified handful of lines, not something worth a dependency for. Sending itself
// goes through sendMail below rather than smtp.SendMail directly - see its doc comment for why.
type MailConfig struct {
	Host, Port, Username, Password, From string
}

// Enabled reports whether an operator has configured outgoing mail at all. Nothing that needs to send a
// message should be reachable unless this is true - see RequestEmailVerification and RequestLoginEmailCode.
func (m MailConfig) Enabled() bool { return strings.TrimSpace(m.Host) != "" }

// smtpSendMail is sendMail by default; tests replace it with a fake that records the message instead of
// opening a real connection, the same seam Core.Now gives tests over the clock.
var smtpSendMail = sendMail

// mailLoopback reports whether host can only ever be reached without crossing a network - "localhost" or a
// loopback literal - the one case a STARTTLS-stripping attacker has no position to sit in. This is the same
// trust boundary net/smtp's own PlainAuth already assumes: it refuses to send credentials in the clear to
// anything else.
func mailLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// requireSTARTTLS is sendMail's actual downgrade defense, pulled out on its own so it can be tested without a
// real connection: nil means it's fine to proceed (either offered is true, or host cannot be reached without
// crossing a network in the first place), and an error means sendMail must stop rather than send a login or
// verification code - or, worse, this account's SMTP credentials - to a relay that dropped STARTTLS.
func requireSTARTTLS(host string, offered bool) error {
	if offered || mailLoopback(host) {
		return nil
	}
	return fmt.Errorf("refusing to send mail to %s in clear text: it did not offer STARTTLS", host)
}

// sendMail is net/smtp.SendMail with one change: STARTTLS is required, not merely attempted, for any host
// that isn't loopback. SendMail negotiates STARTTLS when the server's EHLO reply advertises it but has no way
// to insist on it, so a network attacker between this server and a real mail relay can strip that one line
// from the plaintext EHLO reply and SendMail sends the message anyway, in the clear, without any error - for
// this server that means a login or email-verification code, and for a relay with no authentication guard of
// its own, the SMTP credentials too. Loopback is exempted because there is no network for that attacker to
// sit on, matching the trust boundary smtp.PlainAuth already assumes for credentials.
func sendMail(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	c, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Hello("localhost"); err != nil {
		return err
	}
	offered, _ := c.Extension("STARTTLS")
	if err := requireSTARTTLS(host, offered); err != nil {
		return err
	}
	if offered {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("starting TLS with %s: %w", host, err)
		}
	}
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); !ok {
			return fmt.Errorf("%s does not support authentication", host)
		}
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

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
