package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// MailConfig is how this server sends the handful of messages it ever sends: a login or email-verification
// code, nothing else - there is no newsletter, no digest, nothing to unsubscribe from. Built against the
// standard library alone, the same choice totp.go made for the same reason: PLAIN/LOGIN auth against a
// submission port is a well-specified handful of lines, not something worth a dependency for. Sending itself
// goes through sendMail below rather than smtp.SendMail directly - see its doc comment for why.
type MailConfig struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username"`
	// Password is never sent back to a client once saved, even to the owner who set it - see MailConfigDoc
	// and putMailConfig, the same convention Settings.DeciderSecret uses.
	Password string `json:"password,omitempty"`
	From     string `json:"from"`
}

// maxMailField bounds each MailConfig string so a client cannot store something absurd in a single-row
// table meant for a hostname, a port, a username, an address - the same kind of bound NormalizeFor
// applies to Settings.DeciderURL.
const maxMailField = 320

// Normalize trims whitespace and rejects a field that is unreasonably long. It does not require Host to
// be set - a blank one is exactly how mail stays disabled (see Enabled) - and does not attempt to
// resolve or dial anything: the only way to really know an SMTP config works is to use it, which sending
// the next real code already exercises.
func (m MailConfig) Normalize() (MailConfig, error) {
	m.Host, m.Port, m.Username, m.From = strings.TrimSpace(m.Host), strings.TrimSpace(m.Port), strings.TrimSpace(m.Username), strings.TrimSpace(m.From)
	for name, v := range map[string]string{"the SMTP host": m.Host, "the SMTP port": m.Port, "the SMTP username": m.Username, "the from address": m.From, "the SMTP password": m.Password} {
		if len(v) > maxMailField {
			return m, fmt.Errorf("%s is at most %d characters", name, maxMailField)
		}
	}
	return m, nil
}

// Enabled reports whether an operator has configured outgoing mail at all. Nothing that needs to send a
// message should be reachable unless this is true - see RequestEmailVerification and RequestLoginEmailCode.
func (m MailConfig) Enabled() bool { return strings.TrimSpace(m.Host) != "" }

// mailHolder is Mailer's storage: a pointer so every organisation's Core (see Core.ForOrg, whose shallow
// copy shares this same pointer rather than resetting it the way it resets settings) sees one live,
// server-wide configuration, mutex-guarded the same way settingsHolder guards Settings.
type mailHolder struct {
	mu sync.RWMutex
	m  MailConfig
}

func (h *mailHolder) get() MailConfig {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.m
}

func (h *mailHolder) set(m MailConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.m = m
}

// Mailer returns the current mail configuration. It is the same for every organisation's Core - SMTP is
// one setting for the whole server, not per organisation the way Settings is, the same way DefaultOrg
// and RegMode are not per organisation either - so which Core this is called through does not matter.
func (c *Core) Mailer() MailConfig { return c.mailer.get() }

// SetMailerDefault seeds the in-memory mail configuration without persisting it. cmd/server calls this
// once at boot with whatever the --smtp-* flags say, before LoadMailConfig may overwrite it with
// whatever an owner of DefaultOrg last saved through Settings - the same relationship a CLI flag has to
// a value stored in the database everywhere else in this file's package.
func (c *Core) SetMailerDefault(m MailConfig) { c.mailer.set(m) }

// LoadMailConfig reads the persisted mail configuration at startup, the same way LoadSettings does for
// per-organisation Settings. Unreadable JSON or nothing ever saved leaves whatever SetMailerDefault (or
// the zero value) already set - it never disables mail as a side effect of a storage hiccup.
func (c *Core) LoadMailConfig(ctx context.Context) {
	data, err := c.Store.GetMailConfig(ctx)
	if err != nil || data == nil {
		return
	}
	var m MailConfig
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	c.mailer.set(m)
}

// SaveMailConfig validates, persists and makes a new mail configuration visible to every organisation's
// Core at once (see ForOrg), returning it normalized the way SaveSettings returns Settings. Callers are
// responsible for authorization - see requireDefaultOwner. It always writes to the server's own audit
// trail ("" - see auditOrg), not this Core's OrgID, because SMTP is one setting for the whole server and
// this may be called through any organisation's Core.
func (c *Core) SaveMailConfig(ctx context.Context, by string, m MailConfig) (MailConfig, error) {
	m, err := m.Normalize()
	if err != nil {
		return MailConfig{}, errf(KindInvalid, "%v", err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		return MailConfig{}, err
	}
	if err := c.Store.AddAudit(ctx, c.auditRow("", by, "mail-config-changed", "server", "", "")); err != nil {
		c.Log.Error("audit write failed; the action was not carried out", "action", "mail-config-changed", "err", err)
		return MailConfig{}, errf(KindInternal, "the audit trail could not be written, so nothing was changed. Check the server's database and try again")
	}
	if err := c.Store.PutMailConfig(ctx, data, c.Now()); err != nil {
		c.auditOrg(ctx, "", by, "mail-config-changed-failed", "server", "", err.Error())
		return MailConfig{}, err
	}
	c.mailer.set(m)
	return m, nil
}

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

// ---- HTTP: server-wide, not one organisation's - see requireDefaultOwner ----

// MailConfigDoc is MailConfig as the UI sees it: the password is never sent back, only whether one is
// set, the same way SettingsDoc hides the decider secret.
type MailConfigDoc struct {
	Host        string `json:"host"`
	Port        string `json:"port"`
	Username    string `json:"username"`
	From        string `json:"from"`
	PasswordSet bool   `json:"passwordSet"`
	Enabled     bool   `json:"enabled"`
}

func mailConfigDoc(m MailConfig) MailConfigDoc {
	return MailConfigDoc{Host: m.Host, Port: m.Port, Username: m.Username, From: m.From, PasswordSet: m.Password != "", Enabled: m.Enabled()}
}

// requireDefaultOwner reports whether p is specifically an owner of the default organisation - regardless
// of which organisation, if any, their current request happens to be scoped to (mail configuration is not
// a /api/v1/orgs/{org} route, so guard never places them in one). This is the same GetMembership lookup
// Member itself makes, just against the fixed DefaultOrg instead of whatever org a path names. SMTP
// credentials can relay mail as this server and are worth restricting more tightly than an "admin" of some
// org a stranger could just create for themselves (see Settings.ImageRegistry's comment for that same
// reasoning) - an owner of the one organisation that exists from first boot is the closest thing this
// server has to "the operator".
func (a *Admin) requireDefaultOwner(ctx context.Context, p Principal) error {
	m, err := a.C.Member(ctx, p, a.C.DefaultOrg)
	if err != nil || roleRank[m.Role] < roleRank[RoleOwner] {
		return errf(KindForbidden, "only an owner of the default organisation may do this")
	}
	return nil
}

func (a *Admin) getMailConfig(w http.ResponseWriter, r *http.Request) {
	if err := a.requireDefaultOwner(r.Context(), principal(r)); err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, mailConfigDoc(a.C.Mailer()))
}

// putMailConfig never lets a client blank the password by accident: a GET never carries it (see
// mailConfigDoc), so a client that reads its config and PUTs most of it back unchanged - which is exactly
// what the UI does - naturally sends no `password` at all, or "". Both are treated as "leave it alone". A
// client sets a new password by sending a non-empty `password`, and removes it, explicitly, with
// `clearPassword: true` - the same three-way convention putSettings uses for the decider secret.
func (a *Admin) putMailConfig(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if err := a.requireDefaultOwner(r.Context(), p); err != nil {
		a.fail(w, err)
		return
	}
	var body struct {
		MailConfig
		ClearPassword bool `json:"clearPassword"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	m := body.MailConfig
	switch {
	case body.ClearPassword:
		m.Password = ""
	case m.Password == "":
		m.Password = a.C.Mailer().Password
	}
	n, err := a.C.SaveMailConfig(r.Context(), p.User.Username, m)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, mailConfigDoc(n))
}
