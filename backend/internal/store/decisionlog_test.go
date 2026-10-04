package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestDecisionLogScopesByOrgAndOrdersNewestFirst(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	row := func(org, svc string, at time.Time) DecisionLog {
		return DecisionLog{
			OrgID: org, At: at, RecordedBy: "alex",
			DeciderID: "baseline", DeciderName: "Baseline (weighted cost)", DeciderKind: "builtin",
			Schema: 1, ClusterCount: 3, ServiceCount: 10, PolicyJSON: []byte(`{"latency":1}`),
			ServiceID: svc, ServiceName: svc, FromCluster: "cl-edge", ToCluster: "cl-cloud",
			Reason: "cheaper there", Benefit: 12.5, Confidence: "high", Verdict: "fits",
			BeforeCost: 40, AfterCost: 27.5, MigrationCost: 0,
		}
	}

	// Org "a" gets three rows across two runs; org "b" gets one. Recording N decisions across two orgs,
	// then checking neither org's list ever shows the other's.
	if err := st.AddDecisions(ctx, []DecisionLog{row("a", "svc-1", t0), row("a", "svc-2", t0)}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddDecisions(ctx, []DecisionLog{row("a", "svc-3", t0.Add(time.Minute))}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddDecisions(ctx, []DecisionLog{row("b", "svc-9", t0)}); err != nil {
		t.Fatal(err)
	}

	a, err := st.ListDecisions(ctx, "a", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 3 {
		t.Fatalf("org a: expected 3 rows, got %d: %+v", len(a), a)
	}
	for _, d := range a {
		if d.OrgID != "a" {
			t.Fatalf("org a's list leaked a row from org %q", d.OrgID)
		}
		if d.ServiceID == "svc-9" {
			t.Fatalf("org a's list leaked org b's row: %+v", d)
		}
	}
	// Newest first: the second run (svc-3, a minute later) comes before the first run's two rows.
	if a[0].ServiceID != "svc-3" {
		t.Fatalf("expected the newest row first, got %+v", a[0])
	}

	b, err := st.ListDecisions(ctx, "b", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 1 || b[0].ServiceID != "svc-9" || b[0].OrgID != "b" {
		t.Fatalf("org b: expected only its own row, got %+v", b)
	}

	// A third org that never recorded anything sees an empty list, not an error and not someone else's rows.
	c, err := st.ListDecisions(ctx, "c", 0)
	if err != nil || len(c) != 0 {
		t.Fatalf("org c: expected no rows, got %+v %v", c, err)
	}

	// Every field a later evaluation needs round-trips, not just the ones used for scoping.
	got := a[1] // svc-1 or svc-2, either one carries the same fixture fields
	if got.DeciderID != "baseline" || got.DeciderKind != "builtin" || got.Schema != 1 || got.ClusterCount != 3 || got.ServiceCount != 10 ||
		string(got.PolicyJSON) != `{"latency":1}` || got.FromCluster != "cl-edge" || got.ToCluster != "cl-cloud" ||
		got.Reason != "cheaper there" || got.Benefit != 12.5 || got.Confidence != "high" || got.Verdict != "fits" ||
		got.BeforeCost != 40 || got.AfterCost != 27.5 || got.RecordedBy != "alex" {
		t.Fatalf("a field did not round-trip: %+v", got)
	}

	// AddDecisions with nothing to add is a no-op, not an error.
	if err := st.AddDecisions(ctx, nil); err != nil {
		t.Fatalf("empty batch should be a no-op: %v", err)
	}
	if again, _ := st.ListDecisions(ctx, "a", 0); len(again) != 3 {
		t.Fatalf("an empty AddDecisions call changed the log: %+v", again)
	}
}

func TestDecisionLogLimitIsBoundedAndDefaulted(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()

	var rows []DecisionLog
	for i := 0; i < 5; i++ {
		rows = append(rows, DecisionLog{
			OrgID: "a", At: now.Add(time.Duration(i) * time.Second), DeciderID: "baseline", DeciderKind: "builtin",
			ServiceID: "svc", ToCluster: "cl-cloud",
		})
	}
	if err := st.AddDecisions(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ListDecisions(ctx, "a", 2); err != nil || len(got) != 2 {
		t.Fatalf("limit 2: got %d rows, err %v", len(got), err)
	}
	// Non-positive or absurd limits fall back to the default rather than erroring or returning nothing.
	if got, err := st.ListDecisions(ctx, "a", 0); err != nil || len(got) != 5 {
		t.Fatalf("limit 0 (default): got %d rows, err %v", len(got), err)
	}
	if got, err := st.ListDecisions(ctx, "a", -1); err != nil || len(got) != 5 {
		t.Fatalf("negative limit: got %d rows, err %v", len(got), err)
	}
}
