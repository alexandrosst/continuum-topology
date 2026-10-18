package server

import (
	"continuum/internal/kernel"
)

// Four numbers bound the tier an agent may have: the type's outer bound (MaxTier), what this release honours
// (ImplementedTier), what the agent's install allows (Agent.InstalledTier, the Helm value access.tier) and what its enrollment
// token allowed (Agent.TierCap). Before this file each place that asked the question - token creation, approval, a tier change,
// a proposal, the ceiling an agent reports - did its own comparisons, in its own order, and they had already drifted once. They
// now ask here. Each place still words its own refusal (a person reads a different sentence at an approval than at a proposal),
// but whether a tier is allowed, and which limit stopped it, is decided in one place.

// TierVerdict says which limit, if any, a tier is past. The limits are tried in a fixed order, so a tier past two of them gets
// the first one: the range, then the release, then the install, then the token.
type TierVerdict int

const (
	TierAllowed        TierVerdict = iota
	TierBelowZero                  // not a tier at all
	TierNotImplemented             // above what this release honours
	TierAboveInstall               // above what the agent's install allows
	TierAboveToken                 // above what the agent's enrollment token allowed
)

// CheckTier says whether an agent whose install allows `installed` and whose token allowed `tokenCap` may have `tier`.
func CheckTier(tier, installed, tokenCap int) TierVerdict {
	switch {
	case tier < 0:
		return TierBelowZero
	case tier > kernel.ImplementedTier:
		return TierNotImplemented
	case tier > installed:
		return TierAboveInstall
	case tier > tokenCap:
		return TierAboveToken
	}
	return TierAllowed
}

// GrantableTier is whether a token may be created for this tier: only the release's own range applies, because there is no
// install or token yet.
func GrantableTier(tier int) bool {
	return CheckTier(tier, kernel.ImplementedTier, kernel.ImplementedTier) == TierAllowed
}

// ReportedTier is the ceiling an install reports, held inside the type's bound (which is wider than the release's, so a newer
// install than this server is recorded as it says; the release ceiling applies when something is granted, not when it is
// reported). ok is false when it was outside the bound and had to be held.
func ReportedTier(reported int) (tier int, ok bool) {
	tier = min(max(reported, 0), kernel.MaxTier)
	return tier, tier == reported
}
