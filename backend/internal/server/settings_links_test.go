package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"ikhnos/internal/kernel"
)

func settingsWithLinks(ls ...kernel.LinkSetting) kernel.Settings {
	s := kernel.DefaultSettings()
	s.Links = ls
	return s
}

// Every choice of a pair that changes must show in the audit row, though the number of pairs stays the same.
func TestSettingsDiffShowsEveryChangeToAPair(t *testing.T) {
	base := kernel.LinkSetting{A: "cl-a", B: "cl-b"}
	with := func(f func(*kernel.LinkSetting)) kernel.LinkSetting { l := base; f(&l); return l }
	th := func(i, b uint32) *kernel.LinkThroughput {
		return &kernel.LinkThroughput{IntervalMinutes: i, BudgetMB: b}
	}
	for name, c := range map[string]struct {
		from, to kernel.LinkSetting
		want     string
	}{
		"via":              {base, with(func(l *kernel.LinkSetting) { l.Via = "mesh" }), "via follow traffic → mesh"},
		"via back":         {with(func(l *kernel.LinkSetting) { l.Via = "public" }), base, "via public → follow traffic"},
		"noProbe":          {base, with(func(l *kernel.LinkSetting) { l.NoProbe = true }), "noProbe false → true"},
		"throughput on":    {base, with(func(l *kernel.LinkSetting) { l.Throughput = th(60, 1024) }), "throughput off → on (every 60 min, budget 1024 MB)"},
		"throughput off":   {with(func(l *kernel.LinkSetting) { l.Throughput = th(0, 0) }), base, "throughput on → off"},
		"interval":         {with(func(l *kernel.LinkSetting) { l.Throughput = th(60, 5) }), with(func(l *kernel.LinkSetting) { l.Throughput = th(30, 5) }), "throughput interval (min) 60 → 30"},
		"budget":           {with(func(l *kernel.LinkSetting) { l.Throughput = th(60, 5) }), with(func(l *kernel.LinkSetting) { l.Throughput = th(60, 1<<20) }), "throughput budget (MB) 5 → 1048576"},
		"pair replaced":    {base, kernel.LinkSetting{A: "cl-a", B: "cl-c"}, "pair cl-a/cl-c added"},
		"pair replaced 2":  {base, kernel.LinkSetting{A: "cl-a", B: "cl-c"}, "pair cl-a/cl-b removed"},
		"added with load":  {kernel.LinkSetting{A: "cl-x", B: "cl-y"}, with(func(l *kernel.LinkSetting) { l.Throughput = th(10, 99) }), "pair cl-a/cl-b added (throughput on (every 10 min, budget 99 MB))"},
		"pair order alike": {kernel.LinkSetting{A: "cl-b", B: "cl-a", Via: "mesh"}, with(func(l *kernel.LinkSetting) { l.Via = "public" }), "pair cl-a/cl-b: via mesh → public"},
	} {
		got := kernel.SettingsDiff(settingsWithLinks(c.from), settingsWithLinks(c.to))
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %q does not say %q", name, got, c.want)
		}
	}
	// Nothing changed, however the list is ordered: no row.
	a := settingsWithLinks(kernel.LinkSetting{A: "cl-a", B: "cl-b", Via: "mesh"}, kernel.LinkSetting{A: "cl-a", B: "cl-c"})
	b := settingsWithLinks(kernel.LinkSetting{A: "cl-a", B: "cl-c"}, kernel.LinkSetting{A: "cl-b", B: "cl-a", Via: "mesh"})
	if got := kernel.SettingsDiff(a, b); got != "" {
		t.Errorf("an unchanged list is audited: %q", got)
	}
}

func TestPairChangesAreAuditedWhenSaved(t *testing.T) {
	e := newEnv(t)
	save := func(ls ...kernel.LinkSetting) {
		t.Helper()
		if _, err := e.core.SaveSettings(e.ctx, "alex", settingsWithLinks(ls...)); err != nil {
			t.Fatal(err)
		}
	}
	save(kernel.LinkSetting{A: "cl-a", B: "cl-b"})
	before := auditCount(t, e)
	save(kernel.LinkSetting{A: "cl-a", B: "cl-b", Throughput: &kernel.LinkThroughput{BudgetMB: 1 << 20}}) // same number of pairs
	if after := auditCount(t, e); after != before+1 {
		t.Fatalf("a throughput budget was switched on with %d new audit rows", after-before)
	}
	before = auditCount(t, e)
	save(kernel.LinkSetting{A: "cl-a", B: "cl-b", Throughput: &kernel.LinkThroughput{BudgetMB: 1 << 20}}) // nothing changed
	if after := auditCount(t, e); after != before {
		t.Errorf("an unchanged save was audited")
	}
}

func auditCount(t *testing.T, e *env) int {
	t.Helper()
	ev, err := e.st.ListAudit(e.ctx, e.core.OrgID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return len(ev)
}

func TestASavedPairNeedsWellFormedIdsAndRoomInThePeerList(t *testing.T) {
	e := newEnv(t)
	for name, l := range map[string]kernel.LinkSetting{
		"separator":    {A: "a\x1fb", B: "c"},
		"control":      {A: "cl-a\n", B: "c\td"},
		"unicode":      {A: "cl-ä", B: "cl-b"},
		"leading dash": {A: "-cl", B: "cl-b"},
		"space":        {A: "cl a", B: "cl-b"},
	} {
		if _, err := e.core.SaveSettings(e.ctx, "alex", settingsWithLinks(l)); err == nil {
			t.Errorf("%s: a bad id was saved", name)
		}
	}
	if _, err := e.core.SaveSettings(e.ctx, "alex", settingsWithLinks(kernel.LinkSetting{A: "a", B: "cl-3f2b6c1e9a"}, kernel.LinkSetting{A: "x.y_z-1", B: "b"})); err != nil {
		t.Errorf("ordinary ids refused: %v", err)
	}
	// 65 pairs of one cluster is a valid list for NormalizeLinks (the stored settings must still load) but not for a save.
	var hub []kernel.LinkSetting
	for i := 0; i <= 64; i++ {
		hub = append(hub, kernel.LinkSetting{A: "cl-hub", B: fmt.Sprintf("cl-s%02d", i)})
	}
	if _, err := kernel.NormalizeLinks(hub); err != nil {
		t.Fatalf("normalizeLinks refuses what stored settings may hold: %v", err)
	}
	_, err := e.core.SaveSettings(e.ctx, "alex", settingsWithLinks(hub...))
	if err == nil || !strings.Contains(err.Error(), "cl-hub") {
		t.Fatalf("a cluster in 65 pairs was saved: %v", err)
	}
	if _, err := e.core.SaveSettings(e.ctx, "alex", settingsWithLinks(hub[:64]...)); err != nil {
		t.Errorf("a cluster in exactly 64 pairs refused: %v", err)
	}
}

// A stored pair with a bad id is dropped when the settings load; every other setting is kept (an error there would put the whole
// document back to its defaults).
func TestABadStoredPairIsDroppedWithoutResettingTheOtherSettings(t *testing.T) {
	e := newEnv(t)
	s := kernel.DefaultSettings()
	s.SnapshotMinutes = 7
	s.Links = []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "bad\x1fid", B: "cl-c"}, {A: "cl-d", B: "cl-d"}, {A: "cl-e", B: "cl-f", Via: "wifi"}}
	data, _ := json.Marshal(kernel.StoredSettings{Settings: s, Rev: 3})
	if err := e.st.PutSettings(e.ctx, e.core.OrgID, data, e.core.Now()); err != nil {
		t.Fatal(err)
	}
	e.core.LoadSettings(e.ctx)
	got := e.core.Settings()
	if got.SnapshotMinutes != 7 {
		t.Fatalf("the other settings were reset: snapshot every %d", got.SnapshotMinutes)
	}
	if len(got.Links) != 1 || got.Links[0].A != "cl-a" || got.Links[0].B != "cl-b" {
		t.Fatalf("links = %+v", got.Links)
	}
	if _, rev := e.core.SettingsAt(); rev != 3 {
		t.Errorf("rev %d", rev)
	}
}
