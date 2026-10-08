package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/history"
	"continuum/internal/measure"
	"continuum/internal/model"
	"continuum/internal/store"
)

// ---- settings ----

func TestSettingsDefaultsAndBounds(t *testing.T) {
	n, err := Settings{}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	d := DefaultSettings()
	if n.SnapshotMinutes != d.SnapshotMinutes || n.ConsistencyMinutes != d.ConsistencyMinutes || n.RetentionDays != d.RetentionDays || n.DeciderTimeoutSec != d.DeciderTimeoutSec || n.FlowStaleSeconds != d.FlowStaleSeconds {
		t.Fatalf("zero settings did not fall back to defaults: %+v", n)
	}
	if d.FlowStaleSeconds != 300 {
		t.Fatalf("the quiet-link default changed without updating this test: %d", d.FlowStaleSeconds)
	}
	for name, s := range map[string]Settings{
		"snapshot too long":     {SnapshotMinutes: 100000},
		"negative retention":    {RetentionDays: -1},
		"retention beyond year": {RetentionDays: 400},
		"size too small":        {MaxHistoryMB: 1},
		"consistency too long":  {ConsistencyMinutes: 1000},
		"one missed beat":       {StaleAfterBeats: 1},
		"measure too often":     {MeasureSeconds: 1},
		"flow stale negative":   {FlowStaleSeconds: -1},
		"flow stale too long":   {FlowStaleSeconds: 30*24*3600 + 1},
		"decider timeout":       {DeciderTimeoutSec: 26},
		"decider secret short":  {DeciderSecret: "too-short"},
		"decider secret long":   {DeciderSecret: strings.Repeat("x", 201)},
	} {
		if _, err := s.Normalize(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if n, err := (Settings{DeciderSecret: strings.Repeat("x", 32)}).Normalize(); err != nil || n.DeciderSecret != strings.Repeat("x", 32) {
		t.Fatalf("a decider secret of reasonable length was rejected: %v", err)
	}
}

func TestTombstoneAndEventRetentionSettingsBoundsAndDefaults(t *testing.T) {
	d := DefaultSettings()
	if d.TombstoneRetentionDays != 7 || d.EventRetentionDays != 0 {
		t.Fatalf("defaults = %+v", d)
	}
	n, err := Settings{}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if n.TombstoneRetentionDays != 7 || n.EventRetentionDays != 0 {
		t.Fatalf("zero settings did not fall back to defaults: %+v", n)
	}
	for name, s := range map[string]Settings{
		"negative tombstone retention": {TombstoneRetentionDays: -1},
		"tombstone retention too long": {TombstoneRetentionDays: 91},
		"negative event retention":     {EventRetentionDays: -1},
		"event retention too short":    {EventRetentionDays: 6},
		"event retention too long":     {EventRetentionDays: 3651},
	} {
		if _, err := s.Normalize(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	ok, err := (Settings{TombstoneRetentionDays: 90, EventRetentionDays: 3650}).Normalize()
	if err != nil {
		t.Fatalf("boundary values were refused: %v", err)
	}
	if ok.TombstoneRetentionDays != 90 || ok.EventRetentionDays != 3650 {
		t.Fatalf("boundary values changed: %+v", ok)
	}
	// Event retention is opt-in: setting something else must not turn it on by itself.
	off, err := (Settings{TombstoneRetentionDays: 14}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if off.EventRetentionDays != 0 {
		t.Fatalf("event retention was turned on by itself: %+v", off)
	}
}

func TestSettingsDiffMentionsTombstoneAndEventRetention(t *testing.T) {
	a := DefaultSettings()
	b := a
	b.TombstoneRetentionDays = 14
	b.EventRetentionDays = 90
	d := settingsDiff(a, b)
	if !strings.Contains(d, "tombstone retention (days) 7 → 14") || !strings.Contains(d, "event retention (days) 0 → 90") {
		t.Fatalf("diff = %q", d)
	}
	if settingsDiff(a, a) != "" {
		t.Fatal("no change produced a diff")
	}
}

func TestSettingsProbeTargetsAndDeciderURLAreChecked(t *testing.T) {
	bad := map[string]Settings{
		"loopback target":      {ProbeTargets: []ProbeTarget{{ClusterID: "c", Host: "127.0.0.1", Port: 80}}},
		"metadata target":      {ProbeTargets: []ProbeTarget{{ClusterID: "c", Host: "169.254.169.254", Port: 80}}},
		"url as host":          {ProbeTargets: []ProbeTarget{{ClusterID: "c", Host: "http://example.com", Port: 80}}},
		"no port":              {ProbeTargets: []ProbeTarget{{ClusterID: "c", Host: "example.com"}}},
		"no cluster":           {ProbeTargets: []ProbeTarget{{Host: "example.com", Port: 80}}},
		"duplicate ids":        {ProbeTargets: []ProbeTarget{{ID: "a", ClusterID: "c", Host: "example.com", Port: 80}, {ID: "a", ClusterID: "c", Host: "example.org", Port: 80}}},
		"ftp decider":          {DeciderURL: "ftp://x/y"},
		"decider credentials":  {DeciderURL: "http://u:p@decider.local/x"},
		"decider metadata":     {DeciderURL: "http://169.254.169.254/latest"},
		"decider unspecified":  {DeciderURL: "http://0.0.0.0:8080/"},
		"decider loopback":     {DeciderURL: "http://127.0.0.1:9000/decide"},
		"decider private":      {DeciderURL: "http://10.1.2.3/decide"},
		"decider no host":      {DeciderURL: "http:///x"},
		"decider name too big": {DeciderName: strings.Repeat("n", 61)},
	}
	for name, s := range bad {
		if _, err := s.Normalize(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	many := Settings{}
	for i := 0; i < 51; i++ {
		many.ProbeTargets = append(many.ProbeTargets, ProbeTarget{ClusterID: "c", Host: "example.com", Port: 80 + i})
	}
	if _, err := many.Normalize(); err == nil {
		t.Error("51 targets were accepted")
	}
	ok, err := Settings{DeciderURL: "https://decider.example.com/decide", ProbeTargets: []ProbeTarget{{ClusterID: "c", Host: "example.com", Port: 443}}}.Normalize()
	if err != nil {
		t.Fatalf("a public decider and a normal target were refused: %v", err)
	}
	local, _ := NewDeciderPolicy("127.0.0.0/8")
	if _, err := (Settings{DeciderURL: "http://127.0.0.1:9000/decide"}).NormalizeFor(context.Background(), local); err != nil {
		t.Fatalf("a decider next to the server was refused although the operator allowed it: %v", err)
	}
	if ok.ProbeTargets[0].ID == "" {
		t.Fatal("a target without an id was not given one")
	}
}

func TestSettingsQuickStartBackendsAreChecked(t *testing.T) {
	bad := map[string]Settings{
		"unknown kind":           {QuickStartBackends: []QuickStartBackend{{Kind: "tempo", Modality: "traces", Namespace: "observability", Retention: "72h"}}},
		"zipkin is traces only":  {QuickStartBackends: []QuickStartBackend{{Kind: "zipkin", Modality: "logs", Namespace: "observability", Retention: "500000"}}},
		"modality mismatch":      {QuickStartBackends: []QuickStartBackend{{Kind: "jaeger", Modality: "metrics", Namespace: "observability", Retention: "72h"}}},
		"no namespace":           {QuickStartBackends: []QuickStartBackend{{Kind: "jaeger", Modality: "traces", Retention: "72h"}}},
		"bad namespace":          {QuickStartBackends: []QuickStartBackend{{Kind: "jaeger", Modality: "traces", Namespace: "Not_Valid!", Retention: "72h"}}},
		"no retention":           {QuickStartBackends: []QuickStartBackend{{Kind: "prometheus", Modality: "metrics", Namespace: "observability"}}},
		"bad tool url":           {QuickStartBackends: []QuickStartBackend{{Kind: "jaeger", Modality: "traces", Namespace: "observability", Retention: "72h", ToolURL: "not a url"}}},
		"ftp tool url":           {QuickStartBackends: []QuickStartBackend{{Kind: "jaeger", Modality: "traces", Namespace: "observability", Retention: "72h", ToolURL: "ftp://x/y"}}},
		"duplicate ids":          {QuickStartBackends: []QuickStartBackend{{ID: "a", Kind: "jaeger", Modality: "traces", Namespace: "observability", Retention: "72h"}, {ID: "a", Kind: "prometheus", Modality: "metrics", Namespace: "observability", Retention: "15d"}}},
		"loki modality mismatch": {QuickStartBackends: []QuickStartBackend{{Kind: "loki", Modality: "traces", Namespace: "observability", Retention: "168h"}}},
	}
	for name, s := range bad {
		if _, err := s.Normalize(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	many := Settings{}
	for i := 0; i < 21; i++ {
		many.QuickStartBackends = append(many.QuickStartBackends, QuickStartBackend{Kind: "jaeger", Modality: "traces", Namespace: "observability", Retention: "72h"})
	}
	if _, err := many.Normalize(); err == nil {
		t.Error("21 quick-start backends were accepted")
	}

	ok, err := (Settings{QuickStartBackends: []QuickStartBackend{
		{Kind: "jaeger", Modality: "traces", Namespace: "observability", Retention: "72h"},
		{Kind: "prometheus", Modality: "metrics", Namespace: "observability", Retention: "15d", ToolURL: "http://localhost:9090"},
		{Kind: "loki", Modality: "logs", Namespace: "observability", Retention: "168h"},
		{Kind: "zipkin", Modality: "traces", Namespace: "observability", Retention: "500000"},
	}}).Normalize()
	if err != nil {
		t.Fatalf("four valid quick-start backends were refused: %v", err)
	}
	if len(ok.QuickStartBackends) != 4 {
		t.Fatalf("quick-start backends = %+v", ok.QuickStartBackends)
	}
	for _, b := range ok.QuickStartBackends {
		if b.ID == "" {
			t.Error("a quick-start backend without an id was not given one")
		}
	}
	if ok.QuickStartBackends[1].ToolURL != "http://localhost:9090" {
		t.Errorf("tool url was not kept: %+v", ok.QuickStartBackends[1])
	}
}

func TestSettingsAllowedBackendKindsGateQuickStartBackends(t *testing.T) {
	// A custom backend is refused by default: "custom" is not in defaultAllowedBackendKinds.
	if _, err := (Settings{QuickStartBackends: []QuickStartBackend{
		{Kind: "custom", Modality: "traces", Namespace: "observability", Retention: "n/a", Label: "My APM", ToolURL: "https://apm.example.com"},
	}}).Normalize(); err == nil {
		t.Error("a custom backend was accepted although \"custom\" is not allowed by default")
	}
	// Once "custom" is allowed, a custom backend needs a label and a tool URL (there being no catalog
	// entry to fall back to), but any of the three modalities is fine.
	if _, err := (Settings{
		AllowedBackendKinds: []string{"custom"},
		QuickStartBackends:  []QuickStartBackend{{Kind: "custom", Modality: "traces", Namespace: "observability", Retention: "n/a"}},
	}).Normalize(); err == nil {
		t.Error("a custom backend without a label was accepted")
	}
	if _, err := (Settings{
		AllowedBackendKinds: []string{"custom"},
		QuickStartBackends:  []QuickStartBackend{{Kind: "custom", Modality: "traces", Namespace: "observability", Retention: "n/a", Label: "My APM"}},
	}).Normalize(); err == nil {
		t.Error("a custom backend without a tool URL was accepted")
	}
	ok, err := (Settings{
		AllowedBackendKinds: []string{"custom"},
		QuickStartBackends:  []QuickStartBackend{{Kind: "custom", Modality: "logs", Namespace: "observability", Retention: "n/a", Label: "My APM", ToolURL: "https://apm.example.com"}},
	}).Normalize()
	if err != nil {
		t.Fatalf("a well-formed custom backend was refused: %v", err)
	}
	if len(ok.QuickStartBackends) != 1 || ok.QuickStartBackends[0].Kind != "custom" {
		t.Fatalf("custom backend not kept: %+v", ok.QuickStartBackends)
	}
	// Narrowing the allow-list away from a built-in kind refuses a backend of that kind, even though it
	// would be fine on its own (the modality mismatch tests above already cover the unrestricted default).
	if _, err := (Settings{
		AllowedBackendKinds: []string{"prometheus", "loki"},
		QuickStartBackends:  []QuickStartBackend{{Kind: "jaeger", Modality: "traces", Namespace: "observability", Retention: "72h"}},
	}).Normalize(); err == nil {
		t.Error("a jaeger backend was accepted although jaeger was removed from the allow-list")
	}
	// An unknown kind in the allow-list itself is refused.
	if _, err := (Settings{AllowedBackendKinds: []string{"tempo"}}).Normalize(); err == nil {
		t.Error("an unknown allowed backend kind was accepted")
	}
	// The allow-list is de-duplicated rather than rejected for repeats.
	allow, err := (Settings{AllowedBackendKinds: []string{"jaeger", "jaeger", "loki"}}).Normalize()
	if err != nil {
		t.Fatalf("a repeated allowed kind was refused: %v", err)
	}
	if len(allow.AllowedBackendKinds) != 2 {
		t.Fatalf("allowed backend kinds not de-duplicated: %+v", allow.AllowedBackendKinds)
	}
}

func TestSettingsAreAuditedAndSurviveARestart(t *testing.T) {
	e := newEnv(t)
	e.core.Decider, _ = NewDeciderPolicy("127.0.0.0/8")
	var pushed atomic.Int32
	e.core.OnSettings = func(Settings) { pushed.Add(1) }
	if _, err := e.core.SaveSettings(e.ctx, "alex", Settings{SnapshotMinutes: 2, DeciderURL: "http://127.0.0.1:1/x"}); err != nil {
		t.Fatal(err)
	}
	if pushed.Load() != 1 {
		t.Fatal("agents were not told about new settings")
	}
	if _, err := e.core.SaveSettings(e.ctx, "alex", Settings{SnapshotMinutes: 3}); err != nil {
		t.Fatal(err)
	}
	c2 := NewCore(e.st, e.core.CA, "org-1", nil)
	c2.Decider = e.core.Decider
	if c2.Settings().SnapshotMinutes != DefaultSettings().SnapshotMinutes {
		t.Fatal("settings should be defaults before loading")
	}
	c2.LoadSettings(e.ctx)
	if c2.Settings().SnapshotMinutes != 3 {
		t.Fatalf("after restart: %+v", c2.Settings())
	}
	evs, _ := e.st.ListAudit(e.ctx, "org-1", 20)
	var found bool
	for _, ev := range evs {
		if ev.Action == "settings-changed" {
			found = true
			if strings.Contains(ev.Detail, "127.0.0.1:1") {
				t.Fatalf("the decider address leaked into the audit log: %s", ev.Detail)
			}
		}
	}
	if !found {
		t.Fatal("no settings-changed audit entry")
	}
}

// ---- consistency ----

func TestApplySyncReportsDriftOnlyForLaterFullPictures(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	a, _ := r.st.GetAgent(r.ctx, id)
	a.AccessTier = 2
	r.hub.mu.Lock()
	r.hub.views[id] = &view{state: facts.New()}
	r.hub.mu.Unlock()

	n1 := &continuumv1.NodeFacts{Key: "n1", Name: "n1"}
	w1 := &continuumv1.WorkloadFacts{Key: "ns/Deployment/a", Namespace: "ns", Kind: "Deployment", Name: "a", Replicas: 1}
	full := func(nodes []*continuumv1.NodeFacts, ws ...*continuumv1.WorkloadFacts) *continuumv1.Sync {
		return &continuumv1.Sync{Full: true, Cluster: &continuumv1.ClusterFacts{Uid: fp}, Nodes: nodes, Workloads: ws}
	}
	if _, checked, _ := r.hub.applySync(a, full([]*continuumv1.NodeFacts{n1}, w1), false); checked {
		t.Fatal("the first full picture was treated as a check")
	}
	d, checked, _ := r.hub.applySync(a, full([]*continuumv1.NodeFacts{n1}, w1), true)
	if !checked || d.Total() != 0 {
		t.Fatalf("identical picture: checked=%v drift=%+v", checked, d)
	}
	// The cluster changed behind the server's back: a workload scaled and a node appeared.
	w1b := &continuumv1.WorkloadFacts{Key: "ns/Deployment/a", Namespace: "ns", Kind: "Deployment", Name: "a", Replicas: 5}
	n2 := &continuumv1.NodeFacts{Key: "n2", Name: "n2"}
	d, checked, _ = r.hub.applySync(a, full([]*continuumv1.NodeFacts{n1, n2}, w1b), true)
	if !checked || d.Changed != 1 || d.Missing != 1 || d.Extra != 0 {
		t.Fatalf("drift = %+v", d)
	}
	if got := r.hub.views[id].state.Workloads["ns/Deployment/a"].Replicas; got != 5 {
		t.Fatalf("the server was not corrected: %d replicas", got)
	}
	d, _, _ = r.hub.applySync(a, full([]*continuumv1.NodeFacts{n1}), true) // n2 and the workload are gone
	if d.Extra != 2 || d.Missing != 0 || d.Changed != 0 {
		t.Fatalf("removals: %+v", d)
	}
	if !strings.Contains(d.String(), "no longer has") {
		t.Fatalf("description: %q", d.String())
	}
	if _, checked, _ := r.hub.applySync(a, &continuumv1.Sync{Nodes: []*continuumv1.NodeFacts{n2}}, true); checked {
		t.Fatal("a delta was treated as a check")
	}
}

func TestReportConsistencyStoresAWarningOnlyWhenSomethingDiffers(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	a, _ := r.st.GetAgent(r.ctx, id)
	r.hub.reportConsistency(r.ctx, a, facts.Drift{})
	evs, _ := r.st.ListEvents(r.ctx, "org-1", store.EventQuery{Since: r.now.Add(-time.Hour)})
	if len(evs) != 0 {
		t.Fatalf("a clean check left %d events", len(evs))
	}
	r.hub.reportConsistency(r.ctx, a, facts.Drift{Missing: 1, Changed: 2, Samples: []string{"node n2"}})
	evs, _ = r.st.ListEvents(r.ctx, "org-1", store.EventQuery{Since: r.now.Add(-time.Hour), Kind: "drift"})
	if len(evs) != 1 || evs[0].Severity != "warning" || !strings.Contains(evs[0].Detail, "node n2") || evs[0].TargetID != id {
		t.Fatalf("events = %+v", evs)
	}
}

// ---- measurements ----

func extEdge(ft *flowTable, src, dstIP string, port uint32, proto string, conns uint64, noise string) {
	f := flowOf(wep(src), xep(dstIP), port, conns)
	f.Protocol = proto
	f.Noise = noise
	ft.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f}}, time.Now())
}

func TestDeriveTargetsPicksBusiestSafeTCPDestinations(t *testing.T) {
	ft := newFlowTable()
	w := "ns/Deployment/a"
	extEdge(ft, w, "198.51.100.10", 443, "tcp", 50, "")
	extEdge(ft, w, "198.51.100.11", 5432, "tcp", 5, "")
	extEdge(ft, w, "198.51.100.12", 53, "udp", 500, "")   // UDP is not timed
	extEdge(ft, w, "169.254.169.254", 80, "tcp", 900, "") // forbidden
	extEdge(ft, w, "127.0.0.1", 80, "tcp", 900, "")       // forbidden
	extEdge(ft, w, "198.51.100.13", 80, "tcp", 900, "dns")
	got := deriveTargets(ft)
	if len(got) != 2 || got[0].Host != "198.51.100.10" || got[0].Port != 443 || got[1].Host != "198.51.100.11" {
		t.Fatalf("targets = %+v", got)
	}
	if got[0].Source != "observed" || got[0].ID != observedTargetID("198.51.100.10") {
		t.Fatalf("target = %+v", got[0])
	}
	if deriveTargets(nil) != nil {
		t.Fatal("nil table")
	}
	big := newFlowTable()
	for i := 0; i < 40; i++ {
		extEdge(big, w, "198.51.100."+itoa(int64(i+1)), 443, "tcp", uint64(i+1), "")
	}
	if n := len(deriveTargets(big)); n != maxObservedTargets {
		t.Fatalf("%d targets, want %d", n, maxObservedTargets)
	}
}

func TestPathSummaryMergesRounds(t *testing.T) {
	p := &pathTrack{}
	at := time.Now()
	p.add(measure.Result{Samples: 5, Failed: 0, Min: 10, P50: 12, P95: 20}, at)
	p.add(measure.Result{Samples: 5, Failed: 5}, at) // an outage round must not pull times to zero
	p.add(measure.Result{Samples: 5, Failed: 1, Min: 9, P50: 14, P95: 30}, at)
	min, p50, p95, loss, n := p.summary()
	if min != 9 || p50 != 14 || p95 != 30 || n != 15 {
		t.Fatalf("summary = %v %v %v %v %v", min, p50, p95, loss, n)
	}
	if loss < 39 || loss > 41 { // 6 of 15
		t.Fatalf("loss = %v", loss)
	}
	for i := 0; i < 10; i++ {
		p.add(measure.Result{Samples: 5, Min: 1, P50: 2, P95: 3}, at)
	}
	if len(p.rounds) != roundsKept {
		t.Fatalf("kept %d rounds", len(p.rounds))
	}
	if _, _, _, loss, _ := p.summary(); loss != 0 {
		t.Fatal("old failures were not forgotten")
	}
}

func TestNoteMeasurementsAcceptsOnlyIssuedTargetsAndSaneNumbers(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	r.hub.mu.Lock()
	r.hub.views[id] = &view{state: facts.New(), paths: map[string]*pathTrack{},
		targets: map[string]issuedTarget{"t1": {ID: "t1", Host: "203.0.113.10", Port: 443, Source: "manual"}}}
	r.hub.mu.Unlock()
	r.hub.noteMeasurements(id, &continuumv1.Measurements{Results: []*continuumv1.PathResult{
		{TargetId: "t1", Samples: 5, Failed: 1, RttMinMs: 10, RttP50Ms: 12, RttP95Ms: 1e12},
		{TargetId: "made-up", Samples: 5, RttP50Ms: 1}, // never issued
		{TargetId: "t1", Samples: 0},                   // nothing measured
		{TargetId: "t1", Samples: 101},                 // absurd
		{TargetId: "t1", Samples: 5, Failed: 6},        // more failures than attempts
	}})
	v := r.hub.views[id]
	if len(v.paths) != 1 || v.paths["t1"] == nil || len(v.paths["t1"].rounds) != 1 {
		t.Fatalf("paths = %+v", v.paths)
	}
	if got := v.paths["t1"].rounds[0].P95; got != 60000 {
		t.Fatalf("an absurd time was not clamped: %v", got)
	}
	r.hub.noteMeasurements("unknown-agent", &continuumv1.Measurements{}) // must not panic
}

func TestConfigForIssuesManualAndObservedTargetsByTier(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	a, _ := r.st.GetAgent(r.ctx, id)
	if _, err := r.core.SaveSettings(r.ctx, "alex", Settings{MeasureSeconds: 30, ProbeTargets: []ProbeTarget{
		{ID: "mine", ClusterID: a.ClusterID, Host: "203.0.113.20", Port: 8443},
		{ID: "theirs", ClusterID: "another-cluster", Host: "203.0.113.21", Port: 8443},
	}}); err != nil {
		t.Fatal(err)
	}
	v := &view{state: facts.New(), flows: newFlowTable(), paths: map[string]*pathTrack{"stale": {}}, targets: map[string]issuedTarget{}}
	extEdge(v.flows, "ns/Deployment/a", "198.51.100.10", 443, "tcp", 20, "")

	a.AccessTier = 1 // flows come from pods; tier 1 does not cover them
	cfg := r.hub.configFor(a, v)
	if len(cfg.ProbeTargets) != 1 || cfg.ProbeTargets[0].Id != "mine" || cfg.MeasureSeconds != 30 {
		t.Fatalf("tier 1 config = %+v", cfg)
	}
	a.AccessTier = 2
	cfg = r.hub.configFor(a, v)
	if len(cfg.ProbeTargets) != 2 || cfg.ResyncSeconds != int32(15*60) {
		t.Fatalf("tier 2 config = %+v", cfg)
	}
	if _, ok := v.paths["stale"]; ok {
		t.Fatal("a path for a target no longer issued was kept")
	}
	h1 := v.cfgHash
	r.hub.ConsistencyEvery = 2 * time.Second
	if cfg = r.hub.configFor(a, v); cfg.ResyncSeconds != 2 || v.cfgHash == h1 {
		t.Fatalf("the test override was ignored: %+v", cfg)
	}
	if _, err := r.core.SaveSettings(r.ctx, "alex", Settings{}); err != nil {
		t.Fatal(err)
	}
	bare := &view{state: facts.New(), flows: newFlowTable(), paths: map[string]*pathTrack{}, targets: map[string]issuedTarget{}}
	if cfg = r.hub.configFor(a, bare); cfg.MeasureSeconds != 0 || len(cfg.ProbeTargets) != 0 {
		t.Fatalf("empty config = %+v", cfg)
	}
}

// ---- suspicions ----

func TestSuspicionsFromKubernetesPortsAndSharedNetworks(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	w := "app/Deployment/api"
	home := cluster("home", "203.0.113.1", []*continuumv1.NodeFacts{{Key: "n1", InternalIps: []string{"10.0.0.5"}}}, wk("app", "Deployment", "api"))
	known := cluster("known", "", []*continuumv1.NodeFacts{{Key: "k1", InternalIps: []string{"192.0.2.10"}}})
	feed(&home, now, 60,
		flowOf(wep(w), xep("198.51.100.50"), 6443, 8),    // an API server nobody onboarded
		flowOf(wep(w), xep("198.51.100.51"), 10250, 4),   // and a kubelet next to it (same /24)
		flowOf(wep(w), xep("192.0.2.10"), 6443, 30),      // an onboarded cluster's node: not a suspicion
		flowOf(wep(w), xep("198.51.100.90"), 31000, 100), // one NodePort alone is too weak
		flowOf(wep(w), xep("198.51.100.91"), 443, 100),   // ordinary HTTPS
		flowOf(wep(w), xep("10.0.0.77"), 5432, 40),       // a machine in the nodes' network
		flowOf(wep(w), xep("10.0.0.78"), 5432, 3),        // too little traffic to mention
	)
	all := []observedCluster{home, known}
	got := suspicions("org-1", []observedCluster{home}, all, now)
	var cluster198, machine10 bool
	for _, s := range got {
		if s.Kind != "infrastructure" || !strings.HasPrefix(s.ID, "sg-unk-") || s.Status != "open" {
			t.Fatalf("suggestion = %+v", s)
		}
		switch {
		case strings.Contains(s.Title, "198.51.100.0/24"):
			cluster198 = true
			if ap, _ := s.Apply.(map[string]any); ap["type"] != "connect-cluster" || !strings.Contains(s.Detail, "API server") || !strings.Contains(s.Detail, "kubelet") {
				t.Fatalf("cluster suspicion = %+v", s)
			}
		case strings.Contains(s.Title, "10.0.0.77"):
			machine10 = true
		default:
			t.Errorf("unexpected suspicion: %s", s.Title)
		}
	}
	if !cluster198 || !machine10 || len(got) != 2 {
		t.Fatalf("got %d suspicions: cluster=%v machine=%v", len(got), cluster198, machine10)
	}
	again := suspicions("org-1", []observedCluster{home}, all, now.Add(time.Hour))
	if len(again) != len(got) || again[0].ID != got[0].ID {
		t.Fatal("suspicion ids are not stable")
	}
	if suspicions("org-1", nil, nil, now) != nil {
		t.Fatal("no clusters, no suspicions")
	}
}

func TestOnboardedClusterAddressesAreNeverSuspicious(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	w := "app/Deployment/api"
	home := cluster("home", "", nil, wk("app", "Deployment", "api"))
	peer := cluster("peer", "198.51.100.200", []*continuumv1.NodeFacts{{Key: "p", InternalIps: []string{"198.51.100.60"}}})
	feed(&home, now, 60, flowOf(wep(w), xep("198.51.100.60"), 6443, 50), flowOf(wep(w), xep("198.51.100.200"), 6443, 50))
	if got := suspicions("org-1", []observedCluster{home}, []observedCluster{home, peer}, now); len(got) != 0 {
		t.Fatalf("known clusters were suspected: %+v", got)
	}
}

// ---- recording ----

func liveView(now time.Time, nodes ...string) *view {
	st := facts.New()
	st.Cluster = &continuumv1.ClusterFacts{Uid: fp}
	for _, n := range nodes {
		st.Nodes[n] = &continuumv1.NodeFacts{Key: n, Name: n, Ready: true}
	}
	return &view{state: st, lastSync: now, lastBeat: now, flows: newFlowTable(), paths: map[string]*pathTrack{}, targets: map[string]issuedTarget{}}
}

func TestRecorderStoresSnapshotsAndEventsOnlyForRealChanges(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	r.hub.mu.Lock()
	r.hub.views[id] = liveView(*r.now, "n1", "n2")
	r.hub.mu.Unlock()

	r.hub.ScanNow(r.ctx)
	pts, _ := r.st.ListHistory(r.ctx, "org-1", time.Time{}, time.Time{})
	if len(pts) != 1 {
		t.Fatalf("%d snapshots after the first scan", len(pts))
	}
	*r.now = r.now.Add(time.Minute)
	r.hub.mu.Lock()
	r.hub.views[id].lastBeat = *r.now
	r.hub.mu.Unlock()
	r.hub.ScanNow(r.ctx)
	pts, _ = r.st.ListHistory(r.ctx, "org-1", time.Time{}, time.Time{})
	evs, _ := r.st.ListEvents(r.ctx, "org-1", store.EventQuery{Since: r.now.Add(-time.Hour)})
	if len(pts) != 1 || len(evs) != 0 {
		t.Fatalf("an unchanged estate produced %d snapshots and %d events", len(pts), len(evs))
	}
	r.hub.mu.Lock()
	delete(r.hub.views[id].state.Nodes, "n2")
	r.hub.mu.Unlock()
	*r.now = r.now.Add(time.Minute)
	r.hub.mu.Lock()
	r.hub.views[id].lastBeat = *r.now
	r.hub.mu.Unlock()
	r.hub.ScanNow(r.ctx)
	pts, _ = r.st.ListHistory(r.ctx, "org-1", time.Time{}, time.Time{})
	evs, _ = r.st.ListEvents(r.ctx, "org-1", store.EventQuery{Since: r.now.Add(-time.Hour)})
	if len(pts) != 2 || len(evs) == 0 {
		t.Fatalf("a removed node produced %d snapshots and %d events", len(pts), len(evs))
	}
	var sawNode bool
	for _, e := range evs {
		if e.TargetKind == "node" {
			sawNode = true
		}
	}
	if !sawNode {
		t.Fatalf("no node event among %+v", evs)
	}
}

func TestEventRetentionSettingPrunesByAgeOnlyForItsOwnOrgWhenOn(t *testing.T) {
	r := newHubRig(t)
	now := *r.now
	old := store.Event{At: now.Add(-10 * 24 * time.Hour), Kind: "test", TargetKind: "node", TargetID: "n1", Name: "old"}
	recent := store.Event{At: now.Add(-time.Hour), Kind: "test", TargetKind: "node", TargetID: "n2", Name: "recent"}
	if err := r.st.AddEvents(r.ctx, "org-1", []store.Event{old, recent}); err != nil {
		t.Fatal(err)
	}
	// A second organisation's own old event must survive pruning done for org-1 (tenancy isolation).
	if err := r.st.CreateOrg(r.ctx, store.Org{ID: "org-2", Name: "Org Two", CreatedAt: now, CreatedBy: "u-owner"}, "u-owner"); err != nil {
		t.Fatal(err)
	}
	other := store.Event{At: now.Add(-10 * 24 * time.Hour), Kind: "test", TargetKind: "node", TargetID: "n3", Name: "other-org"}
	if err := r.st.AddEvents(r.ctx, "org-2", []store.Event{other}); err != nil {
		t.Fatal(err)
	}

	// Off (the default): the opt-in setting is inert, nothing of ours is pruned by it.
	r.hub.rec.prune(r.ctx, now, Settings{})
	evs, _ := r.st.ListEvents(r.ctx, "org-1", store.EventQuery{Limit: 100})
	if len(evs) != 2 {
		t.Fatalf("with EventRetentionDays=0, %d events remain, want 2 (the opt-in setting must stay inert)", len(evs))
	}

	// On: only the old event in org-1 is pruned; the recent one and org-2's event are untouched.
	r.hub.rec.prune(r.ctx, now, Settings{EventRetentionDays: 7})
	evs, _ = r.st.ListEvents(r.ctx, "org-1", store.EventQuery{Limit: 100})
	if len(evs) != 1 || evs[0].Name != "recent" {
		t.Fatalf("org-1 events after pruning = %+v", evs)
	}
	evs2, _ := r.st.ListEvents(r.ctx, "org-2", store.EventQuery{Limit: 100})
	if len(evs2) != 1 || evs2[0].Name != "other-org" {
		t.Fatalf("another organisation's events were touched: %+v", evs2)
	}
}

// ---- admin endpoints ----

func TestSettingsEndpointsRolesAndHiddenDeciderURL(t *testing.T) {
	a := newAdminRig(t)
	a.base.Decider, _ = NewDeciderPolicy("127.0.0.0/8")
	_, admin := a.user(t, "root", RoleAdmin)
	_, viewer := a.user(t, "eve", RoleViewer)

	put := a.do("PUT", "/api/v1/settings", Settings{SnapshotMinutes: 7, DeciderURL: "http://127.0.0.1:9/decide?key=secret", DeciderName: "Mine"}, withCookie(admin))
	if put.Code != 200 || put.json(t)["deciderUrl"] != "http://127.0.0.1:9/decide?key=secret" {
		t.Fatalf("admin put: %d %s", put.Code, put.Body.String())
	}
	if r := a.do("PUT", "/api/v1/settings", Settings{SnapshotMinutes: 9}, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("a viewer changed settings: %d", r.Code)
	}
	if r := a.do("PUT", "/api/v1/settings", Settings{SnapshotMinutes: 99999}, withCookie(admin)); r.Code != 400 {
		t.Fatalf("an out of range value: %d", r.Code)
	}
	if r := a.do("PUT", "/api/v1/settings", Settings{DeciderURL: "http://169.254.169.254/"}, withCookie(admin)); r.Code != 400 {
		t.Fatalf("a metadata address as decider: %d", r.Code)
	}
	adm := a.do("GET", "/api/v1/settings", nil, withCookie(admin)).json(t)
	vw := a.do("GET", "/api/v1/settings", nil, withCookie(viewer)).json(t)
	if adm["snapshotMinutes"] != float64(7) || adm["deciderUrl"] == "" {
		t.Fatalf("admin view: %v", adm)
	}
	if vw["deciderUrl"] != "" || vw["deciderConfigured"] != true || vw["deciderName"] != "Mine" {
		t.Fatalf("viewer view: %v", vw)
	}
	if r := a.do("GET", "/api/v1/settings", nil); r.Code != 401 {
		t.Fatalf("anonymous: %d", r.Code)
	}
	if r := a.do("PUT", "/api/v1/settings", Settings{}, withCookie(admin), withoutXRW()); r.Code == 200 {
		t.Fatal("a settings change without the CSRF header was accepted")
	}
}

// TestHistoryDependencySeries checks the per-dependency RTT/loss/throughput trend endpoint: a
// sparkline's data source. It seeds recorded snapshots directly via history.Encode + Store.AddHistory
// (the way TestRecorderStoresSnapshotsAndEventsOnlyForRealChanges exercises the recorder itself, not
// this endpoint) so the dependency's exact RttMs/Stats.LossPct/Bytes at each point are pinned, rather
// than depending on the flow-aggregation pipeline that normally produces them.
func TestHistoryDependencySeries(t *testing.T) {
	a := newAdminRig(t)
	_, viewer := a.user(t, "eve", RoleViewer)

	snap := func(rttMs float64, lossPct *float64, bytes uint64) model.Topology {
		d := model.Dependency{ID: "dep-1", OrgID: "org-1", From: "a", To: "b", Protocol: "TCP", RttMs: rttMs, Bytes: bytes}
		if lossPct != nil {
			d.Stats = &model.DependencyStats{LossPct: lossPct}
		}
		return model.Topology{Dependencies: []model.Dependency{d}}
	}
	put := func(at time.Time, top model.Topology) {
		data, _, err := history.Encode(top)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.st.AddHistory(a.ctx, "org-1", at, data); err != nil {
			t.Fatal(err)
		}
	}
	loss1, loss2 := 0.5, 2.0
	base := *a.now
	put(base, snap(10, &loss1, 1000))
	put(base.Add(10*time.Second), snap(12, &loss2, 3000))
	put(base.Add(20*time.Second), snap(0, nil, 3000)) // gone quiet this round: no rtt/loss sample, bytes unchanged

	r := a.do("GET", "/api/v1/history/dependency/dep-1/series?hours=1", nil, withCookie(viewer))
	if r.Code != 200 {
		t.Fatalf("series: %d %s", r.Code, r.Body.String())
	}
	body := r.json(t)
	pts, _ := body["points"].([]any)
	if len(pts) != 3 {
		t.Fatalf("got %d points, want 3: %v", len(pts), pts)
	}
	p0 := pts[0].(map[string]any)
	if p0["rttMs"] != float64(10) || p0["lossPct"] != 0.5 {
		t.Fatalf("point 0 = %v", p0)
	}
	if _, has := p0["bytesPerSec"]; has {
		t.Fatalf("the first point has no earlier sample to derive a rate from: %v", p0)
	}
	p1 := pts[1].(map[string]any)
	if p1["rttMs"] != float64(12) || p1["lossPct"] != 2.0 || p1["bytesPerSec"] != float64(200) {
		t.Fatalf("point 1 = %v", p1)
	}
	p2 := pts[2].(map[string]any)
	if _, has := p2["rttMs"]; has {
		t.Fatalf("a 0 rtt sample should be omitted, not fabricated: %v", p2)
	}
	if _, has := p2["lossPct"]; has {
		t.Fatalf("an unmeasured loss should be omitted, not fabricated as 0: %v", p2)
	}
	if p2["bytesPerSec"] != float64(0) {
		t.Fatalf("unchanged bytes over a real gap is a genuine 0 rate, not omitted: %v", p2["bytesPerSec"])
	}

	none := a.do("GET", "/api/v1/history/dependency/does-not-exist/series", nil, withCookie(viewer))
	if none.Code != 200 {
		t.Fatalf("unknown dependency id: %d", none.Code)
	}
	if pts, _ := none.json(t)["points"].([]any); len(pts) != 0 {
		t.Fatalf("unknown dependency id returned points: %v", pts)
	}
	if r := a.do("GET", "/api/v1/history/dependency/dep-1/series?hours=0", nil, withCookie(viewer)); r.Code != 400 {
		t.Fatalf("hours=0: %d", r.Code)
	}
	if r := a.do("GET", "/api/v1/history/dependency/dep-1/series?hours=9999", nil, withCookie(viewer)); r.Code != 400 {
		t.Fatalf("hours=9999: %d", r.Code)
	}
	if r := a.do("GET", "/api/v1/history/dependency/dep-1/series", nil); r.Code != 401 {
		t.Fatalf("anonymous: %d", r.Code)
	}
}

func TestHistoryEndpoints(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "root", RoleAdmin)
	_, viewer := a.user(t, "eve", RoleViewer)
	id, _, _ := a.approvedAgent(t, fp)
	a.hub().mu.Lock()
	a.hub().views[id] = liveView(*a.now, "n1")
	a.hub().mu.Unlock()

	if r := a.do("POST", "/api/v1/history/record", nil, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("a viewer forced a recording: %d", r.Code)
	}
	if r := a.do("POST", "/api/v1/history/record", nil, withCookie(admin)); r.Code != 204 {
		t.Fatalf("record: %d %s", r.Code, r.Body.String())
	}
	*a.now = a.now.Add(time.Hour)
	a.hub().mu.Lock()
	a.hub().views[id].state.Nodes["n2"] = &continuumv1.NodeFacts{Key: "n2", Name: "n2", Ready: true}
	a.hub().views[id].lastBeat = *a.now
	a.hub().mu.Unlock()
	a.do("POST", "/api/v1/history/record", nil, withCookie(admin))

	idx := a.do("GET", "/api/v1/history", nil, withCookie(viewer)).json(t)
	pts := idx["points"].([]any)
	if len(pts) != 2 || idx["snapshotMinutes"] != float64(5) {
		t.Fatalf("index = %v", idx)
	}
	first := pts[0].(map[string]any)["at"].(string)
	snap := a.do("GET", "/api/v1/history/snapshot?at="+first, nil, withCookie(viewer))
	if snap.Code != 200 {
		t.Fatalf("snapshot: %d %s", snap.Code, snap.Body.String())
	}
	if nodes := snap.json(t)["topology"].(map[string]any)["nodes"].([]any); len(nodes) != 1 {
		t.Fatalf("the earlier snapshot has %d nodes, want 1", len(nodes))
	}
	if r := a.do("GET", "/api/v1/history/snapshot?at=2000-01-01T00:00:00Z", nil, withCookie(viewer)); r.Code != 404 {
		t.Fatalf("before any record: %d", r.Code)
	}
	for _, bad := range []string{"/api/v1/history/snapshot", "/api/v1/history/snapshot?at=yesterday", "/api/v1/history?since=x", "/api/v1/events?until=x", "/api/v1/history/traffic?hours=0", "/api/v1/history/traffic?hours=9999"} {
		if r := a.do("GET", bad, nil, withCookie(viewer)); r.Code != 400 {
			t.Errorf("%s: %d", bad, r.Code)
		}
	}
	if r := a.do("GET", "/api/v1/history/traffic?hours=48", nil, withCookie(viewer)); r.Code != 200 {
		t.Fatalf("traffic: %d %s", r.Code, r.Body.String())
	}
	all := a.do("GET", "/api/v1/events?since="+a.now.Add(-48*time.Hour).UTC().Format(time.RFC3339), nil, withCookie(viewer)).json(t)["events"].([]any)
	if len(all) == 0 {
		t.Fatal("adding a node produced no event")
	}
	kind := all[0].(map[string]any)["kind"].(string)
	filtered := a.do("GET", "/api/v1/events?limit=10&kind="+kind, nil, withCookie(viewer)).json(t)["events"].([]any)
	if len(filtered) == 0 || len(filtered) > len(all) {
		t.Fatalf("events: %d of %d", len(filtered), len(all))
	}
	for _, e := range filtered {
		if e.(map[string]any)["kind"] != kind {
			t.Fatalf("the kind filter leaked: %v", e)
		}
	}
	if r := a.do("GET", "/api/v1/events", nil); r.Code != 401 {
		t.Fatalf("anonymous events: %d", r.Code)
	}
}

func TestDecideProxyForwardsOnlyToTheConfiguredDecider(t *testing.T) {
	a := newAdminRig(t)
	a.base.Decider, _ = NewDeciderPolicy("127.0.0.0/8") // the test decider listens on loopback
	_, admin := a.user(t, "root", RoleAdmin)
	_, viewer := a.user(t, "eve", RoleEditor) // deciding needs an editor; see TestDecideNeedsAnEditor

	if r := a.do("POST", "/api/v1/decide", map[string]any{"schema": 1}, withCookie(viewer)); r.Code != 404 {
		t.Fatalf("no decider configured: %d", r.Code)
	}

	var gotBody, gotPath atomic.Value
	var healthy atomic.Bool
	healthy.Store(true)
	dec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(string(b))
		gotPath.Store(r.URL.RequestURI())
		if !healthy.Load() {
			http.Error(w, "boom", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"recommendations":[]}`))
	}))
	defer dec.Close()
	if r := a.do("PUT", "/api/v1/settings", Settings{DeciderURL: dec.URL + "/decide?k=1", DeciderName: "Test decider", DeciderTimeoutSec: 3}, withCookie(admin)); r.Code != 200 {
		t.Fatalf("settings: %d %s", r.Code, r.Body.String())
	}
	// the request may not choose where it goes, whatever it contains
	r := a.do("POST", "/api/v1/decide", map[string]any{"schema": 1, "url": "http://169.254.169.254/"}, withCookie(viewer))
	if r.Code != 200 {
		t.Fatalf("decide: %d %s", r.Code, r.Body.String())
	}
	out := r.json(t)
	if out["decider"] != "Test decider" || gotPath.Load() != "/decide?k=1" || !strings.Contains(gotBody.Load().(string), `"schema":1`) {
		t.Fatalf("out=%v path=%v body=%v", out, gotPath.Load(), gotBody.Load())
	}
	if r := a.do("POST", "/api/v1/decide", []byte("not json"), withCookie(viewer)); r.Code != 400 {
		t.Fatalf("non-JSON request: %d", r.Code)
	}
	healthy.Store(false)
	if r := a.do("POST", "/api/v1/decide", map[string]any{"schema": 1}, withCookie(viewer)); r.Code != 502 {
		t.Fatalf("decider error: %d", r.Code)
	}
	if r := a.do("POST", "/api/v1/decide", map[string]any{"schema": 1}); r.Code != 401 {
		t.Fatalf("anonymous decide: %d", r.Code)
	}
}

func TestDeciderClientRefusesLinkLocalAndRedirects(t *testing.T) {
	loop, _ := NewDeciderPolicy("127.0.0.0/8")
	c := loop.client(2 * time.Second)
	if _, err := c.Get("http://169.254.169.254/latest/meta-data"); err == nil {
		t.Fatal("the metadata address was reachable")
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redir.Close()
	resp, err := c.Get(redir.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("a redirect was followed: %d", resp.StatusCode)
	}
}

// failHistory fails the event and snapshot writes while its flags are set.
type failHistory struct {
	store.Store
	events, snapshots atomic.Bool
}

func (f *failHistory) AddEvents(ctx context.Context, org string, evs []store.Event) error {
	if f.events.Load() {
		return errors.New("database is locked")
	}
	return f.Store.AddEvents(ctx, org, evs)
}

func (f *failHistory) AddHistory(ctx context.Context, org string, at time.Time, data []byte) error {
	if f.snapshots.Load() {
		return errors.New("database is locked")
	}
	return f.Store.AddHistory(ctx, org, at, data)
}

// A write that fails while recording is retried by the next scan: a lost event write loses no event, and a
// lost snapshot write does not store its events a second time.
func TestRecorderLosesNoEventAndRepeatsNoneAfterAFailedWrite(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	f := &failHistory{Store: r.hub.C.Store}
	r.hub.C.Store = f
	setNodes := func(names ...string) {
		r.hub.mu.Lock()
		defer r.hub.mu.Unlock()
		v := liveView(*r.now, names...)
		r.hub.views[id] = v
	}
	scan := func() {
		*r.now = r.now.Add(time.Minute)
		r.hub.ScanNow(r.ctx)
	}
	nodeEvents := func() (n int) {
		evs, _ := r.st.ListEvents(r.ctx, "org-1", store.EventQuery{Limit: 100})
		for _, e := range evs {
			if e.TargetKind == "node" {
				n++
			}
		}
		return n
	}
	snapshots := func() int {
		pts, _ := r.st.ListHistory(r.ctx, "org-1", time.Time{}, time.Time{})
		return len(pts)
	}
	setNodes("n1", "n2")
	scan()
	if snapshots() != 1 || nodeEvents() != 0 {
		t.Fatalf("baseline: %d snapshots, %d node events", snapshots(), nodeEvents())
	}

	// The events cannot be written: nothing is stored, and once the database answers the change is still found.
	f.events.Store(true)
	setNodes("n1", "n2", "n3")
	scan()
	if nodeEvents() != 0 || snapshots() != 1 {
		t.Fatalf("while events fail: %d node events, %d snapshots", nodeEvents(), snapshots())
	}
	f.events.Store(false)
	scan()
	if nodeEvents() != 1 || snapshots() != 2 {
		t.Fatalf("after events recover: %d node events, %d snapshots, want 1 and 2", nodeEvents(), snapshots())
	}

	// The snapshot cannot be written: the events are, once; the snapshot follows, and the events are not repeated.
	f.snapshots.Store(true)
	setNodes("n1", "n2")
	scan()
	if nodeEvents() != 2 || snapshots() != 2 {
		t.Fatalf("while snapshots fail: %d node events, %d snapshots, want 2 and 2", nodeEvents(), snapshots())
	}
	f.snapshots.Store(false)
	scan()
	if nodeEvents() != 2 || snapshots() != 3 {
		t.Fatalf("after snapshots recover: %d node events, %d snapshots, want 2 and 3", nodeEvents(), snapshots())
	}
}
