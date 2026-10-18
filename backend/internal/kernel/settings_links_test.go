package kernel

import (
	"fmt"
	"strings"
	"testing"
)

func settingsOfLinks(ls ...LinkSetting) Settings {
	s := DefaultSettings()
	s.Links = ls
	return s
}

func TestSettingsDiffOfPairsIsBoundedAndHoldsIdsOnly(t *testing.T) {
	var many []LinkSetting
	for i := 0; i < 40; i++ {
		many = append(many, LinkSetting{A: "cl-a", B: fmt.Sprintf("cl-%02d", i), Throughput: &LinkThroughput{}})
	}
	got := SettingsDiff(settingsOfLinks(), settingsOfLinks(many...))
	if n := strings.Count(got, "pair cl-"); n != maxLinksDiffEntries || !strings.Contains(got, "and 30 more linked pair changes") {
		t.Errorf("%d entries: %q", n, got)
	}
	if len(got) > 1200 {
		t.Errorf("the row is %d bytes", len(got))
	}
	long := strings.Repeat("x", 500)
	got = SettingsDiff(settingsOfLinks(), settingsOfLinks(LinkSetting{A: long, B: "y"}))
	if len(got) > 300 {
		t.Errorf("an id is not bounded: %d bytes", len(got))
	}
}
