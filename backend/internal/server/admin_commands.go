package server

import (
	"net/http"

	"continuum/internal/install"
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
}

// agentUpgradeCommand writes the command that changes an agent that is already installed. It reads and computes only: nothing is stored,
// no certificate is issued, and the credential a Secret holds never reaches here (the command reads it from the person's shell).
func (a *Admin) agentUpgradeCommand(w http.ResponseWriter, r *http.Request) {
	var req agentUpgradeRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if req.Tier != nil && !GrantableTier(*req.Tier) {
		a.fail(w, errf(KindInvalid, "the access tier must be between 0 and %d", ImplementedTier))
		return
	}
	img := a.images(a.core(r))
	up := install.AgentUpgrade{
		ChartRef: a.chartRef(img), ChartVersion: a.agentChartVersion(), ChartFile: a.chartFile(),
		Namespace: req.Namespace, Release: req.Release, Tier: req.Tier, Telemetry: req.Telemetry,
	}
	doc := agentUpgradeDoc{Secrets: []credentialSecretDoc{}, Problems: []string{}}
	upgrade, err := up.Render()
	if err != nil {
		// What is wrong with the telemetry is the person's to fix and is said; any other reason the command cannot be written is how
		// this server was set up, which is logged and not told.
		if pe, ok := err.(*install.ProblemsError); ok {
			doc.Problems = append(doc.Problems, pe.Problems...)
			writeJSON(w, http.StatusOK, doc)
			return
		}
		a.fail(w, a.installConfigError(err))
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
	writeJSON(w, http.StatusOK, doc)
}
