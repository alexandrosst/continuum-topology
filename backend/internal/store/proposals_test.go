package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func proposalFor(id, agent string) Proposal {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return Proposal{ID: id, OrgID: "org-1", AgentID: agent, Advisor: "rules-v1", ModelVersion: 7,
		Change: []byte(`{"agent":"` + agent + `","paused":["flow"]}`), Evidence: []byte(`[{"fact":"flow bytes","class":"measured"}]`),
		Confidence: "measured", Blast: []byte(`{"agents":1}`), Predicted: []byte(`{"link_bytes_per_sec":-0.5}`), Status: ProposalPending, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
}

func TestAProposalIsStoredAndReadBackWholeAndOnlyInItsOrganisation(t *testing.T) {
	s, ctx, _ := openAccounts(t)
	want := proposalFor("pr-1", "ag-1")
	want.Widens = true
	if err := s.CreateProposal(ctx, want, WithAudit(auditEv("proposal-submitted", "pr-1"))); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProposal(ctx, "org-1", "pr-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentID != "ag-1" || got.Advisor != "rules-v1" || got.ModelVersion != 7 || got.Status != ProposalPending || !got.Widens || got.Mode != "advise" ||
		string(got.Change) != string(want.Change) || string(got.Evidence) != string(want.Evidence) || string(got.Blast) != string(want.Blast) || string(got.Predicted) != string(want.Predicted) ||
		!got.CreatedAt.Equal(want.CreatedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) || got.DecidedAt != nil {
		t.Fatalf("read back %+v, stored %+v", got, want)
	}
	if _, err := s.GetProposal(ctx, "org-2", "pr-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another organisation reads it: %v", err)
	}
	if err := s.CreateProposal(ctx, want); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second proposal with the same id: %v", err)
	}
	if got := auditActions(t, s); len(got) != 1 {
		t.Fatalf("audit rows: %v (the refused duplicate must leave none)", got)
	}
}

func TestProposalsAreListedNewestFirstAndFiltered(t *testing.T) {
	s, ctx, _ := openAccounts(t)
	base := time.Now().UTC().Truncate(time.Millisecond)
	for i, c := range []struct{ id, agent string }{{"pr-a", "ag-1"}, {"pr-b", "ag-2"}, {"pr-c", "ag-1"}} {
		p := proposalFor(c.id, c.agent)
		p.CreatedAt = base.Add(time.Duration(i) * time.Second)
		if err := s.CreateProposal(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.TransitionProposal(ctx, "org-1", "pr-b", []ProposalStatus{ProposalPending}, ProposalRejected, "alex", "no", base); err != nil {
		t.Fatal(err)
	}
	ids := func(q ProposalQuery) (out []string) {
		ps, err := s.ListProposals(ctx, "org-1", q)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			out = append(out, p.ID)
		}
		return
	}
	if got := ids(ProposalQuery{}); len(got) != 3 || got[0] != "pr-c" || got[2] != "pr-a" {
		t.Fatalf("all, newest first: %v", got)
	}
	if got := ids(ProposalQuery{AgentID: "ag-1"}); len(got) != 2 {
		t.Fatalf("one agent: %v", got)
	}
	if got := ids(ProposalQuery{Status: ProposalRejected}); len(got) != 1 || got[0] != "pr-b" {
		t.Fatalf("one status: %v", got)
	}
	if got := ids(ProposalQuery{Limit: 1}); len(got) != 1 {
		t.Fatalf("limit: %v", got)
	}
	if ps, _ := s.ListProposals(ctx, "org-2", ProposalQuery{}); len(ps) != 0 {
		t.Fatalf("another organisation lists them: %+v", ps)
	}
}

// A decision is a compare-and-set: of two people deciding the same proposal, one wins and the other is told it is no longer
// pending, and the loser leaves no audit row.
func TestAProposalCanBeDecidedOnlyOnceAndALosingDecisionLeavesNoRow(t *testing.T) {
	s, ctx, _ := openAccounts(t)
	if err := s.CreateProposal(ctx, proposalFor("pr-1", "ag-1")); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	pending := []ProposalStatus{ProposalPending}
	if err := s.TransitionProposal(ctx, "org-1", "pr-1", pending, ProposalAccepted, "alex", "looks right", at, WithAudit(auditEv("proposal-accepted", "pr-1"))); err != nil {
		t.Fatal(err)
	}
	err := s.TransitionProposal(ctx, "org-1", "pr-1", pending, ProposalRejected, "bob", "no", at, WithAudit(auditEv("proposal-rejected", "pr-1")))
	if !errors.Is(err, ErrBadState) {
		t.Fatalf("the second decision: %v", err)
	}
	if err := s.TransitionProposal(ctx, "org-1", "nope", pending, ProposalRejected, "bob", "", at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown proposal: %v", err)
	}
	if err := s.TransitionProposal(ctx, "org-2", "pr-1", []ProposalStatus{ProposalAccepted}, ProposalApplied, "x", "", at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another organisation's proposal: %v", err)
	}
	got, _ := s.GetProposal(ctx, "org-1", "pr-1")
	if got.Status != ProposalAccepted || got.DecidedBy != "alex" || got.Note != "looks right" || got.DecidedAt == nil || !got.DecidedAt.Equal(at) {
		t.Fatalf("after the race: %+v", got)
	}
	if acts := auditActions(t, s); len(acts) != 1 || acts[0] != "proposal-accepted" {
		t.Fatalf("audit rows: %v", acts)
	}
	// the next step of an accepted proposal
	if err := s.TransitionProposal(ctx, "org-1", "pr-1", []ProposalStatus{ProposalAccepted}, ProposalApplied, "system", "", at); err != nil {
		t.Fatal(err)
	}
}

func TestAProposalOutcomeIsRecordedOnceAndOnlyForAProposalThatExists(t *testing.T) {
	s, ctx, _ := openAccounts(t)
	if err := s.CreateProposal(ctx, proposalFor("pr-1", "ag-1")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	o := ProposalOutcome{ProposalID: "pr-1", OrgID: "org-1", Result: "measured", Predicted: []byte(`{"flow_bytes":-0.5}`), Observed: []byte(`{"flow_bytes":-0.45}`), InForceAt: &now, RecordedAt: now}
	if _, err := s.GetProposalOutcome(ctx, "org-1", "pr-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("before it is recorded: %v", err)
	}
	if err := s.RecordProposalOutcome(ctx, o, WithAudit(auditEv("proposal-outcome", "pr-1"))); err != nil {
		t.Fatal(err)
	}
	later := o
	later.Result, later.Observed = "unconfirmed", []byte(`{}`)
	if err := s.RecordProposalOutcome(ctx, later, WithAudit(auditEv("proposal-outcome", "pr-1"))); err != nil {
		t.Fatalf("a second record must change nothing and not fail: %v", err)
	}
	got, err := s.GetProposalOutcome(ctx, "org-1", "pr-1")
	if err != nil || got.Result != "measured" || string(got.Observed) != `{"flow_bytes":-0.45}` || got.InForceAt == nil || !got.InForceAt.Equal(now) {
		t.Fatalf("the outcome: %+v %v", got, err)
	}
	if acts := auditActions(t, s); len(acts) != 1 {
		t.Fatalf("audit rows: %v (the repeat must leave none)", acts)
	}
	ghost := o
	ghost.ProposalID = "nope"
	if err := s.RecordProposalOutcome(ctx, ghost); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an outcome for no proposal: %v", err)
	}
	other := o
	other.OrgID = "org-2"
	if err := s.RecordProposalOutcome(ctx, other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an outcome from another organisation: %v", err)
	}
}

func TestIfTheAuditRowCannotBeWrittenTheProposalChangeIsNotMade(t *testing.T) {
	s, ctx, _ := openAccounts(t)
	if err := s.CreateProposal(ctx, proposalFor("pr-1", "ag-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER no_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProposal(ctx, proposalFor("pr-2", "ag-1"), WithAudit(auditEv("proposal-submitted", "pr-2"))); !errors.Is(err, ErrAuditWrite) {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.GetProposal(ctx, "org-1", "pr-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the proposal was stored although its audit row could not be written: %v", err)
	}
	at := time.Now()
	if err := s.TransitionProposal(ctx, "org-1", "pr-1", []ProposalStatus{ProposalPending}, ProposalAccepted, "alex", "", at, WithAudit(auditEv("proposal-accepted", "pr-1"))); !errors.Is(err, ErrAuditWrite) {
		t.Fatalf("transition: %v", err)
	}
	if p, _ := s.GetProposal(ctx, "org-1", "pr-1"); p.Status != ProposalPending {
		t.Fatalf("the proposal was accepted although its audit row could not be written: %+v", p)
	}
	o := ProposalOutcome{ProposalID: "pr-1", OrgID: "org-1", Result: "measured", RecordedAt: at}
	if err := s.RecordProposalOutcome(ctx, o, WithAudit(auditEv("proposal-outcome", "pr-1"))); !errors.Is(err, ErrAuditWrite) {
		t.Fatalf("outcome: %v", err)
	}
	if _, err := s.GetProposalOutcome(ctx, "org-1", "pr-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the outcome was stored although its audit row could not be written: %v", err)
	}
}

// A database written before the proposals tables existed gains them on open, and keeps its schema version, so an older
// binary can still open it afterwards.
func TestAnOlderDatabaseGainsTheProposalTablesWithoutAVersionChange(t *testing.T) {
	dir := t.TempDir() + "/s.db"
	st, err := OpenSQLite(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE proposals`, `DROP TABLE proposal_outcomes`} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var before int
	st.db.QueryRow(`PRAGMA user_version`).Scan(&before)
	st.Close()
	up, err := OpenSQLite(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	var after int
	up.db.QueryRow(`PRAGMA user_version`).Scan(&after)
	if after != before || after != SchemaVersion {
		t.Fatalf("user_version %d -> %d, SchemaVersion %d", before, after, SchemaVersion)
	}
	if err := up.CreateProposal(context.Background(), proposalFor("pr-1", "ag-1")); err != nil {
		t.Fatal(err)
	}
}

func TestDeletingAnOrganisationDeletesItsProposalsAndTheirOutcomes(t *testing.T) {
	s, ctx, _ := openAccounts(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, org := range []string{"org-1", "org-2"} {
		p := proposalFor("pr-"+org, "ag-1")
		p.OrgID = org
		if err := s.CreateProposal(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordProposalOutcome(ctx, ProposalOutcome{ProposalID: p.ID, OrgID: org, Result: "measured", RecordedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteOrg(ctx, "org-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProposal(ctx, "org-1", "pr-org-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the deleted organisation's proposal: %v", err)
	}
	if _, err := s.GetProposalOutcome(ctx, "org-1", "pr-org-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the deleted organisation's outcome: %v", err)
	}
	if _, err := s.GetProposal(ctx, "org-2", "pr-org-2"); err != nil {
		t.Fatalf("another organisation's proposal was deleted too: %v", err)
	}
	if _, err := s.GetProposalOutcome(ctx, "org-2", "pr-org-2"); err != nil {
		t.Fatalf("another organisation's outcome was deleted too: %v", err)
	}
}
