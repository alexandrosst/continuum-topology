package agents

import (
	"slices"
	"strings"

	"ikhnos/internal/kernel"
	"ikhnos/internal/store"
)

// What a change to an agent's access does to the agent is the agents' to say: which tiers its install and its token allow, which
// overrides are well formed, and whether the result shares more than before. The decision loop proposes a change and asks this; it
// does not know what a tier or an override is.

// AccessChange is a change to one agent's access as the agents understand it: exactly one of a new approved tier, or additions and
// removals on the agent's overrides (so they apply to what the agent has when the change is made, not to what it had when it was asked
// for). The decision loop has its own record of what it proposed and the estate converts it; this package does not know the loop.
type AccessChange struct {
	Tier    *int
	Pause   []string
	Resume  []string
	Exclude []string
	Include []string
}

func minusStrings(list, drop []string) []string {
	var out []string
	for _, x := range list {
		if !slices.Contains(drop, x) {
			out = append(out, x)
		}
	}
	return out
}

// ResolveChange checks a change against the agent as it is now and returns the access it would leave the agent with and whether
// that is wider than the agent has: a higher tier, or a pause or exclusion lifted (even when the change also adds one). It is
// used when a change is proposed and again when it is accepted.
func ResolveChange(a store.Agent, cur Consent, ch AccessChange) (nextTier int, next Consent, widens bool, err error) {
	next = cur
	nextTier = a.AccessTier
	consentFields := len(ch.Pause)+len(ch.Resume)+len(ch.Exclude)+len(ch.Include) > 0
	switch {
	case ch.Tier != nil && consentFields:
		return 0, cur, false, kernel.Errf(kernel.KindInvalid, "a proposal changes either the approved tier or the overrides, not both")
	case ch.Tier == nil && !consentFields:
		return 0, cur, false, kernel.Errf(kernel.KindInvalid, "the proposal changes nothing")
	}
	if ch.Tier != nil {
		t := *ch.Tier
		verdict := CheckTier(t, a.InstalledTier, a.TierCap)
		switch {
		case verdict == TierBelowZero || verdict == TierNotImplemented:
			return 0, cur, false, kernel.Errf(kernel.KindInvalid, "access tier must be 0-%d in this release", kernel.ImplementedTier)
		case t == a.AccessTier:
			return 0, cur, false, kernel.Errf(kernel.KindConflict, "the agent already has access tier %d", t)
		case verdict == TierAboveInstall:
			return 0, cur, false, kernel.Errf(kernel.KindInvalid, "this agent's install allows at most tier %d", a.InstalledTier)
		case verdict == TierAboveToken:
			return 0, cur, false, kernel.Errf(kernel.KindInvalid, "access tier %d is above what the enrollment token allowed for this agent (%d)", t, a.TierCap)
		}
		return t, cur, t > a.AccessTier, nil
	}
	pause, err := CleanConsent(Consent{Paused: ch.Pause, Excluded: ch.Exclude})
	if err != nil {
		return 0, cur, false, err
	}
	resume, err := CleanConsent(Consent{Paused: ch.Resume, Excluded: ch.Include})
	if err != nil {
		return 0, cur, false, err
	}
	for _, p := range pause.Paused {
		if slices.Contains(resume.Paused, p) {
			return 0, cur, false, kernel.Errf(kernel.KindInvalid, "%s is both paused and resumed in the same proposal", p)
		}
	}
	for _, n := range pause.Excluded {
		if slices.Contains(resume.Excluded, n) {
			return 0, cur, false, kernel.Errf(kernel.KindInvalid, "namespace %s is both excluded and included in the same proposal", n)
		}
	}
	next.Paused = addNew(minusStrings(cur.Paused, resume.Paused), pause.Paused)
	next.Excluded = addNew(minusStrings(cur.Excluded, resume.Excluded), pause.Excluded)
	next, err = CleanConsent(next)
	if err != nil {
		return 0, cur, false, err
	}
	if strings.Join(next.Paused, ",") == strings.Join(cur.Paused, ",") && strings.Join(next.Excluded, ",") == strings.Join(cur.Excluded, ",") {
		return 0, cur, false, kernel.Errf(kernel.KindConflict, "the agent's overrides are already as the proposal would leave them")
	}
	widens = len(minusStrings(cur.Paused, next.Paused)) > 0 || len(minusStrings(cur.Excluded, next.Excluded)) > 0
	return nextTier, next, widens, nil
}

// addNew is list followed by what more has that list does not, in order.
func addNew(list, more []string) []string {
	out := append([]string(nil), list...)
	for _, x := range more {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}
