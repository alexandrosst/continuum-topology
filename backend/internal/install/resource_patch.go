package install

import (
	"encoding/json"
	"fmt"
)

// ResourcePatch is the `kubectl patch` that changes an IkhnosAgent that is already there: its access tier, its telemetry, or both. It is
// what the Telemetry panel and the access-tier change print for an agent the controller keeps, in the place of a `helm upgrade` the
// controller would undo on its next look (the resource is what the release is kept to).
//
// Telemetry is replaced whole: a resource states all of the agent's telemetry, so the patch for a new setting must not leave a field of the
// old one behind. A JSON patch `add` replaces what is at the path; a merge patch would merge into it. Telemetry turned off is the other
// way round: a merge patch that deletes the key, which a JSON patch `remove` cannot do when it is not there.
type ResourcePatch struct {
	Namespace string
	// Tier is the new access tier, or nil to leave it.
	Tier *int
	// SetTelemetry says that the telemetry is part of the change; Telemetry is then what it becomes, nil for none.
	SetTelemetry bool
	Telemetry    *ResourceTelemetry
}

type jsonOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// Render is the command.
func (p ResourcePatch) Render() (string, error) {
	if err := checkNames([2]string{"namespace", p.Namespace}); err != nil {
		return "", err
	}
	if p.Tier == nil && !p.SetTelemetry {
		return "", fmt.Errorf("install: there is nothing to change")
	}
	if p.Tier != nil && (*p.Tier < 0 || *p.Tier > 2) {
		return "", fmt.Errorf("install: the access tier %d is not 0, 1 or 2", *p.Tier)
	}
	var kind string
	var body any
	if p.SetTelemetry && p.Telemetry != nil {
		var ops []jsonOp
		if p.Tier != nil {
			ops = append(ops, jsonOp{"add", "/spec/accessTier", *p.Tier})
		}
		kind, body = "json", append(ops, jsonOp{"add", "/spec/telemetry", p.Telemetry})
	} else {
		spec := map[string]any{}
		if p.Tier != nil {
			spec["accessTier"] = *p.Tier
		}
		if p.SetTelemetry {
			spec["telemetry"] = nil
		}
		kind, body = "merge", map[string]any{"spec": spec}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("kubectl --namespace %s patch ikhnosagents.ikhnos.io %s --type %s -p %s", p.Namespace, ControllerResourceName, kind, Quote(string(b))), nil
}
