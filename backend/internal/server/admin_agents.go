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
