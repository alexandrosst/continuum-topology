package server

import (
	"context"
	"net/http"

	"continuum/internal/accounts"
	"continuum/internal/httpkit"
	"continuum/internal/kernel"
)

// The HTTP side of the server's mail settings. What they are, how they are checked and sent is mail.go's (accounts); who may change
// them and how the request is read is the shell's.

// requireDefaultOwner reports whether p is specifically an owner of the default organisation - regardless
// of which organisation, if any, their current request happens to be scoped to (mail configuration is not
// a /api/v1/orgs/{org} route, so guard never places them in one). This is the same GetMembership lookup
// Member itself makes, just against the fixed DefaultOrg instead of whatever org a path names. SMTP
// credentials can relay mail as this server and are worth restricting more tightly than an "admin" of some
// org a stranger could just create for themselves (see Settings.ImageRegistry's comment for that same
// reasoning) - an owner of the one organisation that exists from first boot is the closest thing this
// server has to "the operator".
func (a *Admin) requireDefaultOwner(ctx context.Context, p accounts.Principal) error {
	m, err := a.C.Accounts.Member(ctx, p, a.C.DefaultOrg)
	if err != nil || !accounts.Allows(accounts.Standing(m.Role), accounts.ActOwn) {
		return kernel.Errf(kernel.KindForbidden, "only an owner of the default organisation may do this")
	}
	return nil
}

func (a *Admin) getMailConfig(w http.ResponseWriter, r *http.Request) {
	if err := a.requireDefaultOwner(r.Context(), httpkit.Principal(r)); err != nil {
		a.fail(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, accounts.NewMailConfigDoc(a.C.Accounts.Mailer()))
}

// putMailConfig never lets a client blank the password by accident: a GET never carries it (see
// NewMailConfigDoc), so a client that reads its config and PUTs most of it back unchanged - which is exactly
// what the UI does - naturally sends no `password` at all, or "". Both are treated as "leave it alone". A
// client sets a new password by sending a non-empty `password`, and removes it, explicitly, with
// `clearPassword: true` - the same three-way convention putSettings uses for the decider secret.
func (a *Admin) putMailConfig(w http.ResponseWriter, r *http.Request) {
	p := httpkit.Principal(r)
	if err := a.requireDefaultOwner(r.Context(), p); err != nil {
		a.fail(w, err)
		return
	}
	var body struct {
		accounts.MailConfig
		ClearPassword bool `json:"clearPassword"`
	}
	if err := httpkit.Decode(r, &body); err != nil {
		httpkit.WriteErr(w, 400, err.Error())
		return
	}
	m := body.MailConfig
	switch {
	case body.ClearPassword:
		m.Password = ""
	case m.Password == "":
		m.Password = a.C.Accounts.Mailer().Password
	}
	n, err := a.C.Accounts.SaveMailConfig(r.Context(), p.User.Username, m)
	if err != nil {
		a.fail(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, accounts.NewMailConfigDoc(n))
}
