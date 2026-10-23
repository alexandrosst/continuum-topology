package operatorsapi

import (
	"errors"
	"net/http"

	"ikhnos/internal/httpkit"
	"ikhnos/internal/install"
	"ikhnos/internal/kernel"
	"ikhnos/internal/operators"
	"ikhnos/internal/store"
)

func (h *API) listOperators(w http.ResponseWriter, r *http.Request) {
	ops, err := h.For(r).ListOperators(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	usages, _ := h.For(r).OperatorUsages(r.Context())
	managed := h.ManagedState(r)
	out := []Doc{}
	for _, op := range ops {
		out = append(out, h.opDocWith(r, op, usages, managed))
	}
	httpkit.WriteJSON(w, 200, out)
}

func (h *API) getOperator(w http.ResponseWriter, r *http.Request) {
	op, err := h.For(r).GetOperator(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, h.opDoc(r, op))
}

func (h *API) createOperator(w http.ResponseWriter, r *http.Request) {
	var req createOperatorRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	processors, err := install.OperatorProcessors(req.ExtraProcessors)
	if err != nil {
		var pe *install.ProblemsError
		if errors.As(err, &pe) {
			h.fail(w, r, kernel.Errf(kernel.KindInvalid, "%s", pe.Problems[0]))
			return
		}
		h.fail(w, r, err)
		return
	}
	labels := make([]store.OperatorLabel, 0, len(req.Labels))
	for _, l := range req.Labels {
		labels = append(labels, store.OperatorLabel{Key: l.Key, Value: l.Value})
	}
	// The receiver token and the certificates are shown once, with the command that uses them: a command that cannot be written is
	// found before they exist.
	if err := h.CheckInstall(h.images(r)); err != nil {
		h.fail(w, r, h.cfgErr(r, err))
		return
	}
	// req.Exposure "" stays "": a client that was not asked has not said "this cluster only". The core validates it.
	op, secret, tlsBundle, hbSecret, err := h.For(r).CreateOperatorWithOptions(r.Context(), httpkit.Actor(r), req.Name, req.SourceClusterIDs, req.Destination.ToStore(), modalitiesFromDoc(req.AcceptedModalities), operators.OperatorOptions{Heartbeat: req.Heartbeat, Labels: labels, Exposure: req.Exposure})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	doc, err := h.operatorInstallDoc(r, op, installMaterial{Secret: secret, TLS: tlsBundle, HeartbeatSecret: hbSecret, Processors: processors})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 201, doc)
}

// reissueOperatorInstall is "install this operator again / renew its certificates", available at any time: a fresh
// receiver and client certificate from the operator's stored CA, with the commands to apply them, and a new
// receiver token or heartbeat secret where the operator has one (they are hashed and cannot be shown again).
func (h *API) reissueOperatorInstall(w http.ResponseWriter, r *http.Request) {
	if err := h.CheckInstall(h.images(r)); err != nil {
		h.fail(w, r, h.cfgErr(r, err))
		return
	}
	op, mat, err := h.For(r).ReissueOperatorInstall(r.Context(), httpkit.Actor(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	doc, err := h.operatorInstallDoc(r, op, installMaterial{Secret: mat.ReceiverToken, TLS: mat.TLS, HeartbeatSecret: mat.HeartbeatSecret, Reissue: true})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, doc)
}

// listOperatorCertificates answers "which certificates exist for this operator, who holds them, and when do they
// stop": the ledger newest first. It never returns a certificate or key.
func (h *API) listOperatorCertificates(w http.ResponseWriter, r *http.Request) {
	core := h.For(r)
	certs, err := core.OperatorCertificates(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	now := core.Now()
	out := make([]issuedCertDoc, 0, len(certs))
	for _, c := range certs {
		end := c.NotAfter
		state := CertValid
		if !end.After(now) {
			state = CertExpired
		}
		out = append(out, issuedCertDoc{
			Serial: c.Serial, Kind: CertKind(c.Kind), Subject: c.Subject, Sender: c.Sender, IssuedBy: c.IssuedBy,
			IssuedAt: kernel.RFC(c.IssuedAt), NotBefore: kernel.RFC(c.NotBefore), NotAfter: kernel.RFC(c.NotAfter), State: state,
		})
	}
	httpkit.WriteJSON(w, 200, operatorCertificatesDoc{Certificates: out})
}

// setOperatorAddress records the host:port other clusters reach the operator at ({"address": ""} clears it).
func (h *API) setOperatorAddress(w http.ResponseWriter, r *http.Request) {
	var req setOperatorAddressRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	if c := h.central(); id == operators.CentralOperatorID && c != nil {
		req.Address = c.WithGatewayPort(r.Context(), req.Address) // a NodePort is not on 4317
	}
	addr, err := h.For(r).SetOperatorAddress(r.Context(), httpkit.Actor(r), id, req.Address)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if c := h.central(); id == operators.CentralOperatorID && c != nil {
		c.SetPublicAddress(addr) // from now on every command and the screens use it; no certificate changes
	}
	op, err := h.For(r).GetOperator(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, h.opDoc(r, op))
}

func (h *API) updateOperatorScope(w http.ResponseWriter, r *http.Request) {
	var req updateOperatorScopeRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	before, err := h.For(r).GetOperator(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.For(r).UpdateOperatorScope(r.Context(), httpkit.Actor(r), id, req.SourceClusterIDs, req.Destination.ToStore(), modalitiesFromDoc(req.AcceptedModalities)); err != nil {
		h.fail(w, r, err)
		return
	}
	op, err := h.For(r).GetOperator(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// A client certificate is minted only for a cluster the scope gained: it has none yet. The ones already
	// configured keep the certificate they have (it stays valid until it expires; renewing is the install route's
	// job), so a scope change that adds nobody costs no certificate and no key derivation. Nothing about the
	// previously issued ones stops working either: there is no per-certificate revocation here. Each gained
	// cluster gets its own, naming it, signed by the operator's own CA, whose certificate the receiver trusts.
	var tlsBundle operators.OperatorTLSBundle
	if gained := clustersGained(before.SourceClusterIDs, op.SourceClusterIDs); len(gained) > 0 {
		tlsBundle.Senders, tlsBundle.CACertPEM = h.For(r).MintSenderCerts(r.Context(), httpkit.Actor(r), op, gained)
	}
	resp := operatorScopeChangedDoc{Operator: h.opDoc(r, op), Reminders: h.SourceReminders(r, op, tlsBundle)}
	resp.ExportSecretCommand, resp.ExportTarget = h.operatorTargetExport(r, op)
	httpkit.WriteJSON(w, 200, resp)
}

// clustersGained are the clusters in now that were not in before.
func clustersGained(before, now []string) []string {
	had := make(map[string]bool, len(before))
	for _, c := range before {
		had[c] = true
	}
	var out []string
	for _, c := range now {
		if !had[c] {
			out = append(out, c)
		}
	}
	return out
}

// uninstallDoc is the response of revoking or deleting an operator, which has been done by now: the command that removes the running
// receiver, or no command (and a line in the log) when it cannot be written, which the operator's id, a name this server made, makes
// unlikely.
func (h *API) uninstallDoc(r *http.Request, id string) operatorUninstallDoc {
	cmd, err := UninstallCommand(id)
	if err != nil {
		h.cfgErr(r, err)
		return operatorUninstallDoc{}
	}
	return operatorUninstallDoc{Uninstall: cmd}
}

func (h *API) revokeOperator(w http.ResponseWriter, r *http.Request) {
	var req revokeOperatorRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	if err := h.For(r).RevokeOperatorForce(r.Context(), httpkit.Actor(r), id, req.Reason, req.Force); err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, h.uninstallDoc(r, id))
}

func (h *API) deleteOperator(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.For(r).DeleteOperatorForce(r.Context(), httpkit.Actor(r), id, r.URL.Query().Get("force") == "true"); err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, h.uninstallDoc(r, id))
}
