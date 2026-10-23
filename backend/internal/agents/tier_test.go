package agents

import (
	"testing"

	"ikhnos/internal/kernel"
)

func TestCheckTierNamesTheFirstLimitThatStopsATier(t *testing.T) {
	for _, c := range []struct {
		tier, installed, cap int
		want                 TierVerdict
	}{
		{0, 0, 0, TierAllowed},
		{2, 2, 2, TierAllowed},
		{1, 2, 1, TierAllowed},
		{-1, 2, 2, TierBelowZero},
		{-1, -5, -5, TierBelowZero}, // the range is tried first
		{3, 4, 4, TierNotImplemented},
		{9, 0, 0, TierNotImplemented}, // past the release beats past the install
		{2, 1, 2, TierAboveInstall},
		{2, 1, 0, TierAboveInstall}, // the install beats the token
		{2, 2, 1, TierAboveToken},
	} {
		if got := CheckTier(c.tier, c.installed, c.cap); got != c.want {
			t.Errorf("CheckTier(%d, %d, %d) = %d, want %d", c.tier, c.installed, c.cap, got, c.want)
		}
	}
}

func TestGrantableTierIsTheReleasesOwnRange(t *testing.T) {
	for n := -2; n <= kernel.MaxTier+2; n++ {
		if got, want := GrantableTier(n), n >= 0 && n <= kernel.ImplementedTier; got != want {
			t.Errorf("GrantableTier(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestReportedTierIsHeldInsideTheTypesBoundNotTheReleases(t *testing.T) {
	for _, c := range []struct {
		in, out int
		ok      bool
	}{{-3, 0, false}, {0, 0, true}, {2, 2, true}, {3, 3, true}, {kernel.MaxTier, kernel.MaxTier, true}, {kernel.MaxTier + 1, kernel.MaxTier, false}, {1 << 20, kernel.MaxTier, false}} {
		if out, ok := ReportedTier(c.in); out != c.out || ok != c.ok {
			t.Errorf("ReportedTier(%d) = %d, %v; want %d, %v", c.in, out, ok, c.out, c.ok)
		}
	}
}
