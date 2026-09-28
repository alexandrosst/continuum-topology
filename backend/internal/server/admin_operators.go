package server

import (
	"fmt"
	"net/http"
	"strings"

	"continuum/internal/chart"
	"continuum/internal/store"
)

type destinationDoc struct {
	Kind           string `json:"kind"`
	Endpoint       string `json:"endpoint"`
	Insecure       bool   `json:"insecure,omitempty"`
	CAFile         string `json:"caFile,omitempty"`
	AuthHeaderName string `json:"authHeaderName,omitempty"`
	AuthSecretName string `json:"authSecretName,omitempty"`
	AuthSecretKey  string `json:"authSecretKey,omitempty"`
}

func toDestinationDoc(d store.Destination) destinationDoc {
	return destinationDoc{Kind: string(d.Kind), Endpoint: d.Endpoint, Insecure: d.Insecure, CAFile: d.CAFile, AuthHeaderName: d.AuthHeaderName, AuthSecretName: d.AuthSecretName, AuthSecretKey: d.AuthSecretKey}
}

func (d destinationDoc) toStore() store.Destination {
	return store.Destination{Kind: store.DestinationKind(d.Kind), Endpoint: d.Endpoint, Insecure: d.Insecure, CAFile: d.CAFile, AuthHeaderName: d.AuthHeaderName, AuthSecretName: d.AuthSecretName, AuthSecretKey: d.AuthSecretKey}
}

type operatorDoc struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	SiteID           string         `json:"siteId,omitempty"`
	Status           string         `json:"status"`
	SourceClusterIDs []string       `json:"sourceClusterIds"`
	Destination      destinationDoc `json:"destination"`
	CreatedAt        string         `json:"createdAt"`
	CreatedBy        string         `json:"createdBy"`
	RevokedAt        string         `json:"revokedAt,omitempty"`
	Reason           string         `json:"reason,omitempty"`
}

func toOperatorDoc(op store.Operator) operatorDoc {
	d := operatorDoc{
		ID: op.ID, Name: op.Name, SiteID: op.SiteID, Status: string(op.Status),
		SourceClusterIDs: op.SourceClusterIDs, Destination: toDestinationDoc(op.Destination),
		CreatedAt: rfc(op.CreatedAt), CreatedBy: op.CreatedBy, Reason: op.Reason,
	}
	if op.SourceClusterIDs == nil {
		d.SourceClusterIDs = []string{}
	}
	if op.RevokedAt != nil {
		d.RevokedAt = rfc(*op.RevokedAt)
	}
	return d
}

func (a *Admin) listOperators(w http.ResponseWriter, r *http.Request) {
	ops, err := a.core(r).ListOperators(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	out := []operatorDoc{}
	for _, op := range ops {
		out = append(out, toOperatorDoc(op))
	}
	writeJSON(w, 200, out)
}

func (a *Admin) getOperator(w http.ResponseWriter, r *http.Request) {
	op, err := a.core(r).GetOperator(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, toOperatorDoc(op))
}

func (a *Admin) createOperator(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string         `json:"name"`
		SourceClusterIDs []string       `json:"sourceClusterIds"`
		Destination      destinationDoc `json:"destination"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	op, secret, err := a.core(r).CreateOperator(r.Context(), actor(r), req.Name, req.SourceClusterIDs, req.Destination.toStore())
	if err != nil {
		a.fail(w, err)
		return
	}
	img := a.images(a.core(r))
	install, secretCmd := a.operatorInstallCommand(img, secret, op)
	writeJSON(w, 201, map[string]any{
		"operator":      toOperatorDoc(op),
		"token":         secret,
		"install":       install,
		"secretCommand": secretCmd,
		"reminders":     a.operatorSourceReminders(r, op),
	})
}

func (a *Admin) updateOperatorScope(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceClusterIDs []string       `json:"sourceClusterIds"`
		Destination      destinationDoc `json:"destination"`
	}
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	id := r.PathValue("id")
	if err := a.core(r).UpdateOperatorScope(r.Context(), actor(r), id, req.SourceClusterIDs, req.Destination.toStore()); err != nil {
		a.fail(w, err)
		return
	}
	op, err := a.core(r).GetOperator(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"operator": toOperatorDoc(op), "reminders": a.operatorSourceReminders(r, op)})
}

func (a *Admin) revokeOperator(w http.ResponseWriter, r *http.Request) {
	a.reasoned(w, r, func(reason string) error {
		return a.core(r).RevokeOperator(r.Context(), actor(r), r.PathValue("id"), reason)
	})
}

func (a *Admin) deleteOperator(w http.ResponseWriter, r *http.Request) {
	if err := a.core(r).DeleteOperator(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// operatorInstallCommand is installCommand's own twin for the regional-operator chart: simpler, since
// this chart never dials the Continuum server at all (see store.Operator's own comment) - there is no
// server.address or enrollment.key here, only where the operator exports to and the receiver token it
// checks incoming OTLP against. Returns the `helm install` command and a companion `kubectl create
// secret` line for the receiver token, shown once - the same convention as an enrollment token.
func (a *Admin) operatorInstallCommand(img ImageConfig, secret string, op store.Operator) (install, secretCmd string) {
	ref, version := a.operatorChartRef(img), ""
	if ref == "" {
		ref = "./" + chart.RegionalOperator.Filename()
	} else if !strings.HasSuffix(ref, ".tgz") {
		version = " --version " + a.operatorChartVersion()
	}
	secretName := op.ID + "-receiver-auth"
	var b strings.Builder
	fmt.Fprintf(&b, "helm install %s %s%s \\\n  --namespace continuum-system --create-namespace \\\n  --set export.otlp.endpoint=%s",
		op.ID, ref, version, op.Destination.Endpoint)
	if op.Destination.Insecure {
		fmt.Fprintf(&b, " \\\n  --set export.otlp.tls.insecure=true")
	}
	if op.Destination.CAFile != "" {
		fmt.Fprintf(&b, " \\\n  --set export.otlp.tls.caFile=%s", op.Destination.CAFile)
	}
	if op.Destination.AuthSecretName != "" {
		fmt.Fprintf(&b, " \\\n  --set export.otlp.auth.headerName=%s \\\n  --set export.otlp.auth.secretName=%s \\\n  --set export.otlp.auth.secretKey=%s",
			op.Destination.AuthHeaderName, op.Destination.AuthSecretName, op.Destination.AuthSecretKey)
	}
	fmt.Fprintf(&b, " \\\n  --set receiver.auth.enabled=true \\\n  --set receiver.auth.secretName=%s", secretName)
	if img.Configured() {
		fmt.Fprintf(&b, " \\\n  --set image.repository=%s/continuum-regional-operator", img.Registry)
		if img.Tag != "" {
			fmt.Fprintf(&b, " \\\n  --set image.tag=%s", img.Tag)
		}
		if img.Digest != "" {
			fmt.Fprintf(&b, " \\\n  --set image.digest=%s", img.Digest)
		}
	}
	secretCmd = fmt.Sprintf("kubectl create secret generic %s --namespace continuum-system --from-literal=%s=%s", secretName, "token", secret)
	return b.String(), secretCmd
}

// operatorSourceReminders is informational only, not applied: for each source cluster, the exact `helm
// upgrade` a person would run against that cluster's own continuum-agent release to actually point its
// telemetry.export.otlp.endpoint at this operator - there is no live reparenting in this release (see the
// plan), so nothing here is executed on the caller's behalf. Uses the same release-name/namespace guess
// upgradeCommand/teardownCommands already fall back to when an agent has never reported its own.
func (a *Admin) operatorSourceReminders(r *http.Request, op store.Operator) []string {
	hub := a.tn(r).Hub
	img := a.images(a.core(r))
	ref, version := a.chartRef(img), ""
	if ref == "" {
		ref = "./" + chart.Agent.Filename()
	} else if !strings.HasSuffix(ref, ".tgz") {
		version = " --version " + a.agentChartVersion()
	}
	agents, err := a.core(r).Store.ListAgents(r.Context(), a.core(r).OrgID)
	if err != nil {
		return nil
	}
	byCluster := make(map[string]store.Agent, len(agents))
	for _, ag := range agents {
		if ag.Status == store.StatusApproved && ag.ClusterID != "" {
			byCluster[ag.ClusterID] = ag
		}
	}
	endpoint := fmt.Sprintf("%s.continuum-system.svc:4317", op.ID)
	out := make([]string, 0, len(op.SourceClusterIDs))
	for _, cl := range op.SourceClusterIDs {
		ag, ok := byCluster[cl]
		ns, name := "", ""
		if ok {
			ns, name = hub.NamespaceOf(ag.ID), hub.ReleaseNameOf(ag.ID)
		}
		rns, rname, _ := releaseTarget(ns, name)
		out = append(out, fmt.Sprintf("helm upgrade %s %s%s --namespace %s --reuse-values --set telemetry.export.otlp.endpoint=%s  # cluster %s", rname, ref, version, rns, endpoint, cl))
	}
	return out
}
