package server

import (
	"continuum/internal/estate"
	"continuum/internal/kernel"
)

// upgradeCommand is the command that changes the ceiling of an agent's install to a tier (estate.UpgradeCommand), for the namespace and
// release the agent reported.
func (a *Admin) upgradeCommand(img kernel.ImageConfig, tier int, namespace, release string) (string, error) {
	return estate.UpgradeCommand(&a.Settings, img, tier, namespace, release)
}

// withoutDiagnostics removes what only people who can change an agent's access may see.
func withoutDiagnostics(doc *estate.StateDoc) {
	for i := range doc.Agents {
		doc.Agents[i].Diagnostics, doc.Agents[i].Consent, doc.Agents[i].Teardown = nil, nil, nil
	}
}
