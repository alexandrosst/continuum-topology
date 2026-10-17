package install

import (
	"errors"
	"strings"
)

// OperatorTelemetry is the command that points an installed agent's telemetry at regional operators: the Secrets that hold the client
// certificates, the credential Secrets, and the upgrade. The operator's side of it is the server's alone to say (it issues the
// certificate, and knows the address the operator is reached at from another cluster); the rest is the telemetry the person drafted.
type OperatorTelemetry struct {
	// AgentUpgrade says which chart and release; its Telemetry is the person's draft, with Operator set on each destination that is one.
	AgentUpgrade
	// Certificates are the commands that create the Secrets holding the client certificates, run first.
	Certificates []string
	// Restated are the values the server states itself: whom the telemetry belongs to, and each operator destination in full (its
	// endpoint, mutual TLS and the Secret that holds the certificate). They come last, on one line.
	Restated []Value
	// ReceiverAuth is how each operator's receiver authenticates this agent, by operator id: "mtls" when the client certificate alone does.
	ReceiverAuth map[string]string
}

// TowardOperators is the telemetry as a command should state it when some destinations are regional operators. The receiver of one is
// OTLP over gRPC with its certificate verified, so whatever an earlier destination left behind (HTTP, skip-verify) must not reach the
// command: it would be kept under --reset-then-reuse-values and break the connection. A receiver the client certificate alone gates has no
// token to present, so a credential left in the draft (from before the operator was chosen, say) is dropped.
func (t Telemetry) TowardOperators(auth map[string]string) Telemetry {
	toward := func(d Destination) Destination {
		if d.Operator == "" {
			return d
		}
		d.Protocol, d.Insecure = "grpc", false
		if auth[d.Operator] == "mtls" {
			d.AuthHeader, d.AuthSecret, d.AuthKey = "", "", ""
		}
		return d
	}
	if !t.Routes.Split {
		t.Destination = toward(t.Destination)
		return t
	}
	for _, m := range t.ActiveLanes() {
		switch m {
		case Metrics:
			t.Routes.Metrics = toward(t.Routes.Metrics)
		case Logs:
			t.Routes.Logs = toward(t.Routes.Logs)
		case Traces:
			t.Routes.Traces = toward(t.Routes.Traces)
		}
	}
	return t
}

// Render is the block to paste, or the reason there is none. The draft's own endpoint for a destination the server restates is left out:
// it is only a placeholder that says which operator this is, and Helm applies --set-string after --set whatever their order on the line,
// so a placeholder written with --set-string would beat the real address the server states with --set.
func (o OperatorTelemetry) Render() (string, error) {
	if o.Telemetry == nil {
		return "", errors.New("install: an operator destination has no telemetry to point at it")
	}
	t := o.Telemetry.TowardOperators(o.ReceiverAuth)
	head, err := o.helm().Render()
	if err != nil {
		return "", err
	}
	values, err := t.Values()
	if err != nil {
		return "", err
	}
	restated := map[string]bool{}
	for _, v := range o.Restated {
		if strings.HasSuffix(v.Key, ".endpoint") {
			restated[v.Key] = true
		}
	}
	kept := values[:0:0]
	for _, v := range values {
		if !restated[v.Key] {
			kept = append(kept, v)
		}
	}
	upgrade := head + Continued(kept)
	if len(o.Restated) > 0 {
		upgrade += " \\\n  " + Flags(o.Restated)
	}
	commands := append([]string(nil), o.Certificates...)
	for _, s := range t.CredentialSecrets(o.Namespace) {
		commands = append(commands, s.Command)
	}
	return Chain(append(commands, upgrade)), nil
}
