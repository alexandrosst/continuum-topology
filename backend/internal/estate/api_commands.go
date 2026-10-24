package estate

import (
	"net/http"

	"ikhnos/internal/agents"
	"ikhnos/internal/httpkit"
	"ikhnos/internal/install"
	"ikhnos/internal/kernel"
)

// agentUpgradeRequest is what the page asks for a command from: where the agent's release lives, the access ceiling to set, and the
// telemetry (in the words of install.Telemetry, which the page says its draft in). The chart is not in it: this server knows which chart
// it serves and from where, and a page that is not told cannot name another.
type agentUpgradeRequest struct {
	Namespace string             `json:"namespace"`
	Release   string             `json:"release"`
	Tier      *int               `json:"tier"`
	Telemetry *install.Telemetry `json:"telemetry"`
}

// credentialSecretDoc is a Secret the command's credential flags point at, and the command that creates it.
type credentialSecretDoc struct {
	Name     string   `json:"name"`
	Variable string   `json:"variable"`
	Lanes    []string `json:"lanes"`
	Command  string   `json:"command"`
}

// agentUpgradeDoc is the answer: the block to paste, its parts, and what is wrong with the request when there is no block.
type agentUpgradeDoc struct {
	// Command is the whole block: the Secrets' commands first, each running only if the one before it worked, then the upgrade.
	Command string `json:"command"`
	// Upgrade is the helm command alone, and SecretCommand the commands that create the credential Secrets (empty when there are none).
	Upgrade       string                `json:"upgrade"`
	SecretCommand string                `json:"secretCommand"`
	Secrets       []credentialSecretDoc `json:"secrets"`
	// Problems say what makes the telemetry unfit to write a command from. With any, Command is empty.
	Problems []string `json:"problems"`
	// Controller is the same change for an agent that a controller keeps (an IkhnosAgent resource), which a helm upgrade would not
	// last on: the controller installs the chart again from the resource.
	Controller controllerUpgradeDoc `json:"controller"`
}

// controllerUpgradeDoc is the change written as a patch of the IkhnosAgent resource.
type controllerUpgradeDoc struct {
	// Command is the whole block: the Secrets' commands first (the same as the helm block's), then the patch. Patch is the patch alone.
	Command string `json:"command"`
	Patch   string `json:"patch"`
	// NeedsTelemetryPermission is whether the controller must have been installed with allowTelemetry for the resource to be accepted.
	NeedsTelemetryPermission bool `json:"needsTelemetryPermission"`
	// Problems say why there is no patch for these settings (and say nothing when there is no change to make).
	Problems []string `json:"problems"`
}

// controllerUpgrade writes the resource patch for a request: the tier, and the telemetry as the resource states it. Telemetry that
// the resource cannot state (extra processors, a regional operator, a part left as installed) is told as problems, and the helm
// block remains.
func controllerUpgrade(req agentUpgradeRequest, namespace string, secretCommands []string) controllerUpgradeDoc {
	doc := controllerUpgradeDoc{Problems: []string{}}
	if req.Tier == nil && req.Telemetry == nil {
		return doc
	}
	patch := install.ResourcePatch{Namespace: namespace, Tier: req.Tier}
	if t := req.Telemetry; t != nil {
		rt, err := install.ResourceOf(*t)
		if err != nil {
			if pe, ok := err.(*install.ProblemsError); ok {
				doc.Problems = append(doc.Problems, pe.Problems...)
			} else {
				doc.Problems = append(doc.Problems, err.Error())
			}
			return doc
		}
		tier := 2
		if req.Tier != nil {
			tier = *req.Tier
		}
		if rt != nil {
			// The controller applies the same rules when it reads the resource; saying them here keeps a patch the controller would
			// refuse from being offered.
			if p := rt.Problems(tier, t.MeasurementsOn == nil || *t.MeasurementsOn); len(p) > 0 {
				doc.Problems = append(doc.Problems, p...)
				return doc
			}
			doc.NeedsTelemetryPermission = true
		}
		patch.SetTelemetry, patch.Telemetry = true, rt
	}
	cmd, err := patch.Render()
	if err != nil {
		doc.Problems = append(doc.Problems, err.Error())
		return doc
	}
	doc.Patch = cmd
	doc.Command = install.Chain(append(append([]string{}, secretCommands...), cmd))
	return doc
}

// agentUpgradeCommand writes the command that changes an agent that is already installed. It reads and computes only: nothing is stored,
// no certificate is issued, and the credential a Secret holds never reaches here (the command reads it from the person's shell).
func (h *API) agentUpgradeCommand(w http.ResponseWriter, r *http.Request) {
	var req agentUpgradeRequest
	if err := httpkit.Decode(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if req.Tier != nil && !agents.GrantableTier(*req.Tier) {
		h.fail(w, r, kernel.Errf(kernel.KindInvalid, "the access tier must be between 0 and %d", kernel.ImplementedTier))
		return
	}
	img := h.Settings.Images(h.For(r).C.Core)
	up := install.AgentUpgrade{
		ChartRef: h.Settings.AgentRef(img), ChartVersion: h.Settings.AgentVersion(), ChartFile: h.Settings.AgentChartFile(),
		Namespace: req.Namespace, Release: req.Release, Tier: req.Tier, Telemetry: req.Telemetry,
	}
	doc := agentUpgradeDoc{Secrets: []credentialSecretDoc{}, Problems: []string{}}
	upgrade, err := up.Render()
	if err != nil {
		// What is wrong with the telemetry is the person's to fix and is said; any other reason the command cannot be written is how
		// this server was set up, which is logged and not told.
		if pe, ok := err.(*install.ProblemsError); ok {
			doc.Problems = append(doc.Problems, pe.Problems...)
			httpkit.WriteJSON(w, http.StatusOK, doc)
			return
		}
		h.fail(w, r, h.installConfigError(r, err))
		return
	}
	doc.Upgrade = upgrade
	commands := []string{}
	if req.Telemetry != nil {
		ns := up.Namespace
		for _, s := range req.Telemetry.CredentialSecrets(ns) {
			lanes := make([]string, len(s.Lanes))
			for i, l := range s.Lanes {
				lanes[i] = string(l)
			}
			doc.Secrets = append(doc.Secrets, credentialSecretDoc{Name: s.Name, Variable: s.Variable, Lanes: lanes, Command: s.Command})
			commands = append(commands, s.Command)
		}
		doc.SecretCommand = install.Chain(commands)
	}
	doc.Command = install.Chain(append(commands, upgrade))
	doc.Controller = controllerUpgrade(req, up.TargetNamespace(), commands)
	httpkit.WriteJSON(w, http.StatusOK, doc)
}

// installChoices are the Helm values for what a new install is asked to switch on or narrow (install.AgentChoices, which the controller
// uses too), with a choice that cannot be honoured told as a refusal for the caller to correct, where the page used to leave it out of the
// command without a word.
func installChoices(tier int, c install.Collectors, s install.Scope, purgeIdentity bool) ([]install.Value, error) {
	v, err := install.AgentChoices(tier, c, s, purgeIdentity)
	if pe, ok := err.(*install.ProblemsError); ok {
		return nil, kernel.Errf(kernel.KindInvalid, "%s", pe.Problems[0])
	}
	return v, err
}
