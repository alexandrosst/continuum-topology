package server

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"continuum/internal/accounts"
	"continuum/internal/agents"
	"continuum/internal/decisionloop"
	"continuum/internal/estate"
	"continuum/internal/kernel"
	"continuum/internal/store"
)

func asRole(role string) context.Context {
	return accounts.WithCaller(context.Background(), accounts.Principal{Role: role})
}

type propRig struct {
	*adminRig
	h  *estate.Hub
	id string
}

// propRigFor is an approved agent at tier 2 whose install allows tier 2, with a live stream so a change can be pushed.
func propRigFor(t *testing.T) *propRig {
	t.Helper()
	a := newAdminRig(t)
	id := a.approvedAgentID(t, fp)
	if err := a.st.SetInstalledTier(a.ctx, id, 2); err != nil {
		t.Fatal(err)
	}
	if err := a.st.SetAccessTier(a.ctx, id, 2); err != nil {
		t.Fatal(err)
	}
	return &propRig{adminRig: a, h: a.hub(), id: id}
}

func (r *propRig) submit(t *testing.T, ch decisionloop.AccessChange) (store.Proposal, error) {
	t.Helper()
	ch.AgentID = r.id
	return r.h.C.Loop.SubmitProposal(asRole(accounts.RoleEditor), "token:advisor", decisionloop.ProposalInput{Advisor: "rules-v1", ModelVersion: 3, Change: ch,
		Evidence:  []decisionloop.EvidenceFact{{Name: "flow bytes", Value: "12 MB/s", Class: "measured"}, {Name: "owner intent", Class: "declared"}},
		Predicted: map[string]float64{"link_bytes_per_sec": -0.5}})
}

func (r *propRig) auditActions(t *testing.T) []string {
	t.Helper()
	evs, err := r.st.ListAudit(r.ctx, "org-1", 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		out = append(out, e.Action)
	}
	return out
}

func count(list []string, s string) (n int) {
	for _, x := range list {
		if x == s {
			n++
		}
	}
	return
}

// An advisor changes nothing: after a submission the agent is exactly as it was, and the proposal waits.
func TestSubmittingAProposalChangesNothingAboutTheAgent(t *testing.T) {
	r := propRigFor(t)
	before, _ := r.st.GetAgent(r.ctx, r.id)
	p, err := r.submit(t, decisionloop.AccessChange{Pause: []string{"flow"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != store.ProposalPending || p.Widens || p.Confidence != "declared" || p.Mode != "advise" || p.Advisor != "rules-v1" || p.ModelVersion != 3 {
		t.Fatalf("the proposal: %+v", p)
	}
	after, _ := r.st.GetAgent(r.ctx, r.id)
	if after.AccessTier != before.AccessTier {
		t.Fatalf("the tier moved from %d to %d on a submission", before.AccessTier, after.AccessTier)
	}
	if c := r.h.ConsentOf(r.id); len(c.Paused) != 0 || len(c.Excluded) != 0 {
		t.Fatalf("the overrides changed on a submission: %+v", c)
	}
	acts := r.auditActions(t)
	if count(acts, "proposal-submitted") != 1 || count(acts, "agent-consent-changed") != 0 || count(acts, "agent-tier-changed") != 0 {
		t.Fatalf("audit rows: %v", acts)
	}
}

func TestAProposalIsValidatedAndTheServerWorksOutWhetherItWidens(t *testing.T) {
	r := propRigFor(t)
	bad := []struct {
		name string
		ch   decisionloop.AccessChange
	}{
		{"nothing", decisionloop.AccessChange{}},
		{"tier and overrides", decisionloop.AccessChange{Tier: intp(1), Pause: []string{"flow"}}},
		{"an unknown collector", decisionloop.AccessChange{Pause: []string{"nope"}}},
		{"a system namespace", decisionloop.AccessChange{Exclude: []string{"kube-system"}}},
		{"pause and resume the same", decisionloop.AccessChange{Pause: []string{"flow"}, Resume: []string{"flow"}}},
		{"the tier it already has", decisionloop.AccessChange{Tier: intp(2)}},
		{"a tier above the install", decisionloop.AccessChange{Tier: intp(3)}},
		{"a negative tier", decisionloop.AccessChange{Tier: intp(-1)}},
		{"resuming what is not paused", decisionloop.AccessChange{Resume: []string{"flow"}}},
	}
	for _, c := range bad {
		if _, err := r.submit(t, c.ch); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	if _, err := r.h.C.Loop.SubmitProposal(asRole(accounts.RoleEditor), "x", decisionloop.ProposalInput{Advisor: "", Change: decisionloop.AccessChange{AgentID: r.id, Pause: []string{"flow"}}}); err == nil {
		t.Error("a proposal without an advisor name was accepted")
	}
	if _, err := r.h.C.Loop.SubmitProposal(asRole(accounts.RoleEditor), "x", decisionloop.ProposalInput{Advisor: "a", Change: decisionloop.AccessChange{AgentID: "ag-nope", Pause: []string{"flow"}}}); err == nil {
		t.Error("a proposal for an unknown agent was accepted")
	}
	if _, err := r.h.C.Loop.SubmitProposal(asRole(accounts.RoleEditor), "x", decisionloop.ProposalInput{Advisor: "a", Change: decisionloop.AccessChange{AgentID: r.id, Pause: []string{"flow"}},
		Predicted: map[string]float64{"happiness": 1}}); err == nil {
		t.Error("a prediction of an unknown metric was accepted")
	}
	if _, err := r.h.C.Loop.SubmitProposal(asRole(accounts.RoleEditor), "x", decisionloop.ProposalInput{Advisor: "a", Change: decisionloop.AccessChange{AgentID: r.id, Pause: []string{"flow"}},
		Evidence: []decisionloop.EvidenceFact{{Name: "x", Class: "certain"}}}); err == nil {
		t.Error("evidence of an unknown class was accepted")
	}
	if got := r.auditActions(t); count(got, "proposal-submitted") != 0 {
		t.Fatalf("a refused submission left audit rows: %v", got)
	}

	if err := r.st.SetAccessTier(r.ctx, r.id, 1); err != nil {
		t.Fatal(err)
	}
	up, err := r.submit(t, decisionloop.AccessChange{Tier: intp(2)})
	if err != nil || !up.Widens {
		t.Fatalf("a higher tier: %+v %v", up, err)
	}
	down, err := r.submit(t, decisionloop.AccessChange{Tier: intp(0)})
	if err != nil || down.Widens {
		t.Fatalf("a lower tier: %+v %v", down, err)
	}
	if _, err := r.h.SetConsent(asRole(accounts.RoleAdmin), "adam", r.id, agents.Consent{Paused: []string{"flow"}}); err != nil {
		t.Fatal(err)
	}
	lift, err := r.submit(t, decisionloop.AccessChange{Resume: []string{"flow"}})
	if err != nil || !lift.Widens {
		t.Fatalf("lifting a pause: %+v %v", lift, err)
	}
	mixed, err := r.submit(t, decisionloop.AccessChange{Resume: []string{"flow"}, Pause: []string{"probes"}})
	if err != nil || !mixed.Widens {
		t.Fatalf("lifting one pause while adding another still widens: %+v %v", mixed, err)
	}
	if _, err := r.h.SetConsent(asRole(accounts.RoleAdmin), "adam", r.id, agents.Consent{Paused: []string{"flow"}, Excluded: []string{"shop"}}); err != nil {
		t.Fatal(err)
	}
	inc, err := r.submit(t, decisionloop.AccessChange{Include: []string{"shop"}})
	if err != nil || !inc.Widens {
		t.Fatalf("including a namespace that was left out: %+v %v", inc, err)
	}
	more, err := r.submit(t, decisionloop.AccessChange{Pause: []string{"probes"}, Exclude: []string{"billing"}})
	if err != nil || more.Widens {
		t.Fatalf("adding a pause and an exclusion: %+v %v", more, err)
	}
}

func TestAcceptingANarrowingProposalMakesTheChangeThroughTheUsualPathAndAuditsEachStep(t *testing.T) {
	r := propRigFor(t)
	p, err := r.submit(t, decisionloop.AccessChange{Pause: []string{"flow", "probes"}, Exclude: []string{"shop"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.h.C.Loop.AcceptProposal(asRole(accounts.RoleEditor), "edith", p.ID, "fine")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.ProposalApplied || got.DecidedBy != "edith" {
		t.Fatalf("after accepting: %+v", got)
	}
	c := r.h.ConsentOf(r.id)
	if strings.Join(c.Paused, ",") != "flow,probes" || strings.Join(c.Excluded, ",") != "shop" {
		t.Fatalf("the overrides: %+v", c)
	}
	acts := r.auditActions(t)
	for _, want := range []string{"proposal-submitted", "proposal-accepted", "agent-consent-changed", "proposal-applied"} {
		if count(acts, want) != 1 {
			t.Fatalf("want one %s row, audit rows: %v", want, acts)
		}
	}
	// a tier proposal goes through SetAgentTier
	tp, err := r.submit(t, decisionloop.AccessChange{Tier: intp(0)})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.h.C.Loop.AcceptProposal(asRole(accounts.RoleEditor), "edith", tp.ID, ""); err != nil || got.Status != store.ProposalApplied {
		t.Fatalf("the tier proposal: %+v %v", got, err)
	}
	if a, _ := r.st.GetAgent(r.ctx, r.id); a.AccessTier != 0 {
		t.Fatalf("the tier is %d", a.AccessTier)
	}
	if count(r.auditActions(t), "agent-tier-changed") != 1 {
		t.Fatalf("audit rows: %v", r.auditActions(t))
	}
}

// Widening takes an administrator, and the check is on the person who accepts, not on who proposed. Without a signed-in caller
// it is refused too.
func TestAcceptingAWideningProposalTakesAnAdministratorAndAnEditorIsRefusedWithoutAnyChange(t *testing.T) {
	r := propRigFor(t)
	if _, err := r.h.SetConsent(asRole(accounts.RoleAdmin), "adam", r.id, agents.Consent{Paused: []string{"flow"}}); err != nil {
		t.Fatal(err)
	}
	p, err := r.submit(t, decisionloop.AccessChange{Resume: []string{"flow"}})
	if err != nil || !p.Widens {
		t.Fatalf("%+v %v", p, err)
	}
	for name, ctx := range map[string]context.Context{"editor": asRole(accounts.RoleEditor), "no caller": context.Background()} {
		_, err := r.h.C.Loop.AcceptProposal(ctx, "someone", p.ID, "")
		var e *kernel.Error
		if !errors.As(err, &e) || e.Kind != kernel.KindForbidden {
			t.Fatalf("%s: want a forbidden error, got %v", name, err)
		}
	}
	if got, _, _ := r.h.C.Loop.GetProposal(context.Background(), p.ID); got.Status != store.ProposalPending {
		t.Fatalf("a refused acceptance moved the proposal to %s", got.Status)
	}
	if c := r.h.ConsentOf(r.id); !slices.Contains(c.Paused, "flow") {
		t.Fatalf("a refused acceptance lifted the pause: %+v", c)
	}
	if got, err := r.h.C.Loop.AcceptProposal(asRole(accounts.RoleAdmin), "adam", p.ID, ""); err != nil || got.Status != store.ProposalApplied {
		t.Fatalf("the administrator: %+v %v", got, err)
	}
	if c := r.h.ConsentOf(r.id); slices.Contains(c.Paused, "flow") {
		t.Fatalf("the pause is still on: %+v", c)
	}
}

// The check is made against the agent as it is when the proposal is accepted. A proposal that narrowed when it was made can
// widen later; one that no longer changes anything is refused.
func TestAProposalIsCheckedAgainstTheAgentAsItIsWhenItIsAccepted(t *testing.T) {
	r := propRigFor(t)
	narrow, err := r.submit(t, decisionloop.AccessChange{Tier: intp(0)})
	if err != nil || narrow.Widens {
		t.Fatalf("%+v %v", narrow, err)
	}
	if err := r.st.SetAccessTier(r.ctx, r.id, 0); err != nil { // someone has already done it
		t.Fatal(err)
	}
	if _, err := r.h.C.Loop.AcceptProposal(asRole(accounts.RoleAdmin), "adam", narrow.ID, ""); err == nil {
		t.Fatal("a proposal that changes nothing now was accepted")
	}
	if got, _, _ := r.h.C.Loop.GetProposal(context.Background(), narrow.ID); got.Status != store.ProposalPending {
		t.Fatalf("status %s", got.Status)
	}

	// proposed against tier 2 as "set 1" (narrowing), then the agent drops to 0: now the same change would widen
	r2 := propRigFor(t)
	set1, err := r2.submit(t, decisionloop.AccessChange{Tier: intp(1)})
	if err != nil || set1.Widens {
		t.Fatalf("%+v %v", set1, err)
	}
	if err := r2.st.SetAccessTier(r2.ctx, r2.id, 0); err != nil {
		t.Fatal(err)
	}
	_, err = r2.h.C.Loop.AcceptProposal(asRole(accounts.RoleEditor), "edith", set1.ID, "")
	var e *kernel.Error
	if !errors.As(err, &e) || e.Kind != kernel.KindForbidden {
		t.Fatalf("an editor accepting what now widens: %v", err)
	}
	if a, _ := r2.st.GetAgent(r2.ctx, r2.id); a.AccessTier != 0 {
		t.Fatalf("the tier moved to %d", a.AccessTier)
	}
}

func TestAnExpiredProposalCannotBeAccepted(t *testing.T) {
	r := propRigFor(t)
	p, err := r.h.C.Loop.SubmitProposal(asRole(accounts.RoleEditor), "x", decisionloop.ProposalInput{Advisor: "a", Change: decisionloop.AccessChange{AgentID: r.id, Pause: []string{"flow"}}, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	*r.now = r.now.Add(2 * time.Minute)
	_, err = r.h.C.Loop.AcceptProposal(asRole(accounts.RoleAdmin), "adam", p.ID, "")
	var e *kernel.Error
	if !errors.As(err, &e) || e.Kind != kernel.KindConflict {
		t.Fatalf("accepting after the expiry: %v", err)
	}
	got, _, _ := r.h.C.Loop.GetProposal(context.Background(), p.ID)
	if got.Status != store.ProposalExpired {
		t.Fatalf("status %s", got.Status)
	}
	if c := r.h.ConsentOf(r.id); len(c.Paused) != 0 {
		t.Fatalf("an expired proposal paused something: %+v", c)
	}
	acts := r.auditActions(t)
	if count(acts, "proposal-expired") != 1 || count(acts, "proposal-accepted") != 0 {
		t.Fatalf("audit rows: %v", acts)
	}
	if _, err := r.h.C.Loop.RejectProposal(asRole(accounts.RoleEditor), "edith", p.ID, ""); err == nil {
		t.Fatal("an expired proposal was rejected")
	}
	// the list shows it as expired, not pending
	if ps, _ := r.h.C.Loop.ListProposals(context.Background(), store.ProposalQuery{Status: store.ProposalPending}); len(ps) != 0 {
		t.Fatalf("pending: %+v", ps)
	}
}

func TestARejectedProposalChangesNothingAndCannotBeAcceptedLater(t *testing.T) {
	r := propRigFor(t)
	p, _ := r.submit(t, decisionloop.AccessChange{Pause: []string{"flow"}})
	got, err := r.h.C.Loop.RejectProposal(asRole(accounts.RoleEditor), "edith", p.ID, "not now")
	if err != nil || got.Status != store.ProposalRejected || got.Note != "not now" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := r.h.C.Loop.AcceptProposal(asRole(accounts.RoleAdmin), "adam", p.ID, ""); err == nil {
		t.Fatal("a rejected proposal was accepted")
	}
	if c := r.h.ConsentOf(r.id); len(c.Paused) != 0 {
		t.Fatalf("a rejected proposal paused something: %+v", c)
	}
	if _, err := r.h.C.Loop.RejectProposal(asRole(accounts.RoleEditor), "edith", "pr-nope", ""); err == nil {
		t.Fatal("rejecting an unknown proposal worked")
	}
}

// Of several people accepting the same proposal at once, one makes the change and the rest are told it is no longer pending.
func TestOfSeveralSimultaneousAcceptancesExactlyOneAppliesTheChange(t *testing.T) {
	r := propRigFor(t)
	p, _ := r.submit(t, decisionloop.AccessChange{Pause: []string{"flow"}})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var wins, losses int
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.h.C.Loop.AcceptProposal(asRole(accounts.RoleEditor), "edith", p.ID, "")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else {
				losses++
			}
		}()
	}
	wg.Wait()
	if wins != 1 || losses != 7 {
		t.Fatalf("%d accepted and %d refused", wins, losses)
	}
	acts := r.auditActions(t)
	if count(acts, "proposal-accepted") != 1 || count(acts, "agent-consent-changed") != 1 || count(acts, "proposal-applied") != 1 {
		t.Fatalf("audit rows: %v", acts)
	}
}

func TestTooManyWaitingProposalsAreRefused(t *testing.T) {
	r := propRigFor(t)
	for i := 0; i < decisionloop.MaxPendingPerAgent; i++ {
		if _, err := r.submit(t, decisionloop.AccessChange{Pause: []string{"flow"}}); err != nil {
			t.Fatalf("proposal %d: %v", i, err)
		}
	}
	if _, err := r.submit(t, decisionloop.AccessChange{Pause: []string{"flow"}}); err == nil {
		t.Fatal("an agent was allowed more waiting proposals than the limit")
	}
}

// If the audit row cannot be written the change is not made: a proposal is not stored, and one being decided stays pending.
func TestAProposalAndItsDecisionAreNotMadeIfTheirAuditRowCannotBeWritten(t *testing.T) {
	r := propRigFor(t)
	fa := &failAudit{Store: r.h.C.Store}
	orig := r.h.C.Store
	r.h.C.Store = fa
	defer func() { r.h.C.Store = orig }()

	fa.fail.Store(true)
	if _, err := r.submit(t, decisionloop.AccessChange{Pause: []string{"flow"}}); err == nil {
		t.Fatal("the submission succeeded without its audit row")
	}
	fa.fail.Store(false)
	if ps, _ := r.h.C.Loop.ListProposals(context.Background(), store.ProposalQuery{}); len(ps) != 0 {
		t.Fatalf("stored: %+v", ps)
	}
	p, err := r.submit(t, decisionloop.AccessChange{Pause: []string{"flow"}})
	if err != nil {
		t.Fatal(err)
	}
	fa.fail.Store(true)
	if _, err := r.h.C.Loop.AcceptProposal(asRole(accounts.RoleEditor), "edith", p.ID, ""); err == nil {
		t.Fatal("the acceptance succeeded without its audit row")
	}
	fa.fail.Store(false)
	got, _, _ := r.h.C.Loop.GetProposal(context.Background(), p.ID)
	if got.Status != store.ProposalPending {
		t.Fatalf("status %s", got.Status)
	}
	if c := r.h.ConsentOf(r.id); len(c.Paused) != 0 {
		t.Fatalf("the change was made: %+v", c)
	}
}
