package operatorsapi

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"continuum/internal/httpkit"
	"continuum/internal/install"
	"continuum/internal/kernel"
	"continuum/internal/operators"
	"continuum/internal/store"
)

// enableOperatorHeartbeat mints (or rotates) an existing operator's heartbeat secret and returns the
// one-time commands that put it to use, in the same style createOperator's reminders use.
func (h *API) enableOperatorHeartbeat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	core := h.For(r)
	img := h.Settings.Images(core.Core)
	url := h.HeartbeatURL(r)
	// The secret is shown once, with the commands that use it: a command that cannot be written is found before the secret is minted.
	before, err := core.GetOperator(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if _, err := h.HeartbeatUpgradeCommand(img, before, url); err != nil {
		h.fail(w, r, h.cfgErr(r, err))
		return
	}
	if _, err := HeartbeatSecretCommand(before, "check"); err != nil {
		h.fail(w, r, h.cfgErr(r, err))
		return
	}
	if _, err := h.HeartbeatCASecretCommand(before); err != nil {
		h.fail(w, r, h.cfgErr(r, err))
		return
	}
	secret, rotated, err := core.EnableOperatorHeartbeat(r.Context(), httpkit.Actor(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	op, err := core.GetOperator(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	upgrade, err := h.HeartbeatUpgradeCommand(img, op, url)
	if err != nil {
		h.fail(w, r, h.cfgErr(r, err))
		return
	}
	secretCmd, err := HeartbeatSecretCommand(op, secret)
	if err != nil {
		h.fail(w, r, h.cfgErr(r, err))
		return
	}
	resp := heartbeatEnabledDoc{
		Operator:                 h.opDoc(r, op),
		Rotated:                  rotated,
		HeartbeatToken:           secret,
		HeartbeatSecretCommand:   secretCmd,
		HeartbeatUpgradeCommand:  upgrade,
		HeartbeatURL:             url,
		HeartbeatIntervalSeconds: int(operators.OperatorHeartbeatInterval / time.Second),
	}
	if rotated {
		// A Secret's new value is not seen by a running pod's environment: the collector has to restart
		// to present it.
		restart, err := RestartCommand(op)
		if err != nil {
			h.fail(w, r, h.cfgErr(r, err))
			return
		}
		resp.HeartbeatRestartCommand = restart
	}
	resp.HeartbeatWarning = HeartbeatURLWarning(url)
	caCmd, err := h.HeartbeatCASecretCommand(op)
	if err != nil {
		h.fail(w, r, h.cfgErr(r, err))
		return
	}
	resp.HeartbeatCaSecretCommand = caCmd
	httpkit.WriteJSON(w, 200, resp)
}

// HeartbeatURL is the address an operator is told to report to: the server's configured public URL (PublicURL)
// when there is one, and otherwise this server's admin address as the administrator reached it just now (scheme
// from the connection or the trusted proxy, host from the request) plus HeartbeatPath - the same source of
// truth relyingParty uses. The request's own host is a guess that is wrong exactly when it matters (an administrator
// on a port-forward, or through an internal name, is not how an operator in another cluster reaches the server), so
// a configured URL wins, and HeartbeatURLWarning says when the address it is left with cannot be right. Whichever it
// is, the person can still edit heartbeat.url; it is only a --set flag.
func (h *API) HeartbeatURL(r *http.Request) string {
	if base := h.Settings.PublicBase(); base != "" {
		return base + HeartbeatPath
	}
	scheme := "http"
	if h.Secure(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + HeartbeatPath
}

// HeartbeatURLWarning explains an address that cannot work as it stands: plain HTTP is refused by the chart by
// default (the heartbeat secret would cross the network in clear text), and a host that only exists inside one
// machine, cluster or network is not one an operator elsewhere can report to.
func HeartbeatURLWarning(heartbeatURL string) string {
	var warn []string
	if !strings.HasPrefix(heartbeatURL, "https://") {
		warn = append(warn, "this address is plain HTTP, and the operator chart refuses a plain-HTTP heartbeat.url unless heartbeat.allowPlainHTTP=true, since the heartbeat secret would travel unencrypted. Open the server over HTTPS (or set heartbeat.url to its HTTPS address) before applying this")
	}
	if u, err := url.Parse(heartbeatURL); err == nil {
		if why := UnreachableHostWhy(u.Hostname()); why != "" {
			warn = append(warn, fmt.Sprintf("%s is %s, which an operator in another cluster or network cannot reach. Give the server a public URL (admin.publicURL in the chart, or CONTINUUM_PUBLIC_URL), or set heartbeat.url to the address the operator can use", u.Hostname(), why))
		}
	}
	return strings.Join(warn, ". ")
}

// UnreachableHostWhy says why host cannot be reached from another network ("" when it may be): this machine, a Service
// name that exists inside one cluster, or a private address.
func UnreachableHostWhy(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if ip := net.ParseIP(host); ip != nil {
		switch {
		case ip.IsLoopback() || ip.IsUnspecified():
			return "this machine"
		case ip.IsPrivate() || ip.IsLinkLocalUnicast():
			return "a private address"
		}
		return ""
	}
	switch {
	case host == "localhost" || strings.HasSuffix(host, ".localhost"):
		return "this machine"
	case operators.IsClusterLocalName(host):
		return "a name that only exists inside one Kubernetes cluster"
	}
	return ""
}

func operatorHeartbeatSecretName(op store.Operator) string   { return op.ID + "-heartbeat-auth" }
func operatorHeartbeatCASecretName(op store.Operator) string { return op.ID + "-heartbeat-ca" }

// HeartbeatUpgradeCommand is the upgrade that turns the heartbeat on for a release that already runs.
func (h *API) HeartbeatUpgradeCommand(img kernel.ImageConfig, op store.Operator, heartbeatURL string) (string, error) {
	ref, version := h.Settings.OperatorChart(img)
	return install.Helm{Verb: install.Upgrade, Release: op.ID, Chart: ref, Version: version, Namespace: "continuum-system", ResetThenReuse: true,
		Values: h.HeartbeatValues(op, heartbeatURL)}.Render()
}

// HeartbeatValues is the Helm values that turn the heartbeat on. intervalSeconds is left at the
// chart's default, which equals OperatorHeartbeatInterval. When the server's certificate is signed by a private CA
// (HeartbeatCAPEM) the chart is told which Secret holds that CA, or the operator could never verify the server.
func (h *API) HeartbeatValues(op store.Operator, heartbeatURL string) []install.Value {
	values := []install.Value{
		install.Set("heartbeat.enabled", "true"),
		install.Set("heartbeat.url", heartbeatURL),
		install.Set("heartbeat.auth.secretName", operatorHeartbeatSecretName(op)),
	}
	if len(h.Settings.HeartbeatCAPEM) > 0 {
		values = append(values, install.Set("heartbeat.tls.caSecretName", operatorHeartbeatCASecretName(op)))
	}
	return values
}

// HeartbeatCASecretCommand creates the Secret holding the private CA the server's certificate is signed by, in the
// operator's namespace; "" when the server's certificate chains to a public root and there is nothing to add.
func (h *API) HeartbeatCASecretCommand(op store.Operator) (string, error) {
	if len(h.Settings.HeartbeatCAPEM) == 0 {
		return "", nil
	}
	return install.Secret{Name: operatorHeartbeatCASecretName(op), Namespace: "continuum-system", EnsureNamespace: true,
		Entries: []install.Entry{{Key: "ca.crt", Value: string(h.Settings.HeartbeatCAPEM)}}}.Render()
}

// HeartbeatSecretCommand creates the Secret the chart's heartbeat.auth.secretName points at. A first install and
// a rotation are the same create-or-update command, and it makes the namespace first, so it can be the first thing run.
func HeartbeatSecretCommand(op store.Operator, secret string) (string, error) {
	return install.Secret{Name: operatorHeartbeatSecretName(op), Namespace: "continuum-system", EnsureNamespace: true,
		Entries: []install.Entry{{Key: "token", Value: secret}}}.Render()
}
