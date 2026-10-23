package server

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"ikhnos/internal/accounts"
	"ikhnos/internal/agents"
	"ikhnos/internal/decisionloop"
	"ikhnos/internal/store"
	"ikhnos/internal/testkit"
)

// feed adds heartbeat self-telemetry samples to the agent's view: one every 30 seconds for the given span, with the link byte
// counter growing at bytesPerSec and the agent's CPU at cpu cores.
func (r *propRig) feed(from time.Time, span time.Duration, bytesPerSec float64, cumulative *uint64, cpuSeconds *float64) {
	r.h.Mu.Lock()
	defer r.h.Mu.Unlock()
	v := r.h.ViewFor(r.id)
	for at := from; !at.After(from.Add(span)); at = at.Add(30 * time.Second) {
		*cumulative += uint64(bytesPerSec * 30)
		*cpuSeconds += 0.1 * 30
		v.Self.Add(agents.SelfStatsSample{At: at, RSSBytes: 50 << 20, CPUSeconds: *cpuSeconds, LinkBytesCumulative: *cumulative}, at)
	}
}

// acceptAndConfirm accepts a proposal and plays the agent's side: it echoes the digest of the Config just sent, as its next
// heartbeat would.
func (r *propRig) acceptAndConfirm(t *testing.T, id string) time.Time {
	t.Helper()
	if _, err := r.h.C.Loop.AcceptProposal(testkit.AsRole(accounts.RoleEditor), "edith", id, ""); err != nil {
		t.Fatal(err)
	}
	r.h.Mu.Lock()
	defer r.h.Mu.Unlock()
	v := r.h.ViewFor(r.id)
	v.CfgHash, v.CfgSentAt = "h-after", r.now.Add(time.Second)
	*r.now = r.now.Add(2 * time.Second)
	v.NoteConfigEcho("h-after", *r.now)
	return *r.now
}

func outcomeOf(t *testing.T, r *propRig, id string) (store.ProposalOutcome, bool) {
	t.Helper()
	o, err := r.st.GetProposalOutcome(context.Background(), "org-1", id)
	return o, err == nil
}

func TestAnAppliedProposalGetsAnOutcomeThatSetsWhatWasObservedBesideWhatWasPredicted(t *testing.T) {
	r := propRigFor(t)
	var bytes uint64
	var cpu float64
	start := *r.now
	r.feed(start, 4*time.Minute, 1000, &bytes, &cpu) // before: 1000 B/s
	*r.now = start.Add(4 * time.Minute)
	p, err := r.submit(t, decisionloop.AccessChange{Pause: []string{"flow"}}) // predicts link_bytes_per_sec -0.5
	if err != nil {
		t.Fatal(err)
	}
	inForce := r.acceptAndConfirm(t, p.ID)

	r.h.C.Loop.RecordOutcomes(context.Background())
	if _, ok := outcomeOf(t, r, p.ID); ok {
		t.Fatal("an outcome was recorded before the window after the change had filled")
	}
	r.feed(inForce, 5*time.Minute, 500, &bytes, &cpu) // after: 500 B/s
	*r.now = inForce.Add(5*time.Minute + time.Second)
	r.h.C.Loop.RecordOutcomes(context.Background())

	o, ok := outcomeOf(t, r, p.ID)
	if !ok || o.Result != "measured" || o.InForceAt == nil || !o.InForceAt.Equal(inForce.Truncate(time.Millisecond)) {
		t.Fatalf("the outcome: %+v (recorded %v)", o, ok)
	}
	var obs struct {
		Before, After, Change map[string]float64
	}
	if err := json.Unmarshal(o.Observed, &obs); err != nil {
		t.Fatal(err)
	}
	if math.Abs(obs.Before["link_bytes_per_sec"]-1000) > 1 || math.Abs(obs.After["link_bytes_per_sec"]-500) > 1 || math.Abs(obs.Change["link_bytes_per_sec"]+0.5) > 0.01 {
		t.Fatalf("observed: %+v", obs)
	}
	if string(o.Predicted) != `{"link_bytes_per_sec":-0.5}` {
		t.Fatalf("predicted: %s", o.Predicted)
	}
	// once only, with one audit row
	*r.now = r.now.Add(time.Minute)
	r.h.C.Loop.RecordOutcomes(context.Background())
	if n := count(r.auditActions(t), "proposal-outcome"); n != 1 {
		t.Fatalf("%d outcome rows", n)
	}
	// and the route shows it
	_, editor := r.user(t, "edith", accounts.RoleEditor)
	got := r.do("GET", "/api/v1/proposals/"+p.ID, nil, withCookie(editor)).json(t)
	oc, _ := got["outcome"].(map[string]any)
	if oc == nil || oc["result"] != "measured" {
		t.Fatalf("the proposal document: %v", got)
	}
}

func TestAnOutcomeSaysUnconfirmedIfTheAgentNeverReportsTheChangeInForce(t *testing.T) {
	r := propRigFor(t)
	p, _ := r.submit(t, decisionloop.AccessChange{Pause: []string{"flow"}})
	if _, err := r.h.C.Loop.AcceptProposal(testkit.AsRole(accounts.RoleEditor), "edith", p.ID, ""); err != nil {
		t.Fatal(err)
	}
	*r.now = r.now.Add(10 * time.Minute)
	r.h.C.Loop.RecordOutcomes(context.Background())
	if _, ok := outcomeOf(t, r, p.ID); ok {
		t.Fatal("recorded before the give-up time")
	}
	*r.now = r.now.Add(25 * time.Minute)
	r.h.C.Loop.RecordOutcomes(context.Background())
	o, ok := outcomeOf(t, r, p.ID)
	if !ok || o.Result != "unconfirmed" || o.InForceAt != nil {
		t.Fatalf("%+v %v", o, ok)
	}
}

func TestAnOutcomeSaysNoDataIfThereAreTooFewSamplesToCompare(t *testing.T) {
	r := propRigFor(t)
	p, _ := r.submit(t, decisionloop.AccessChange{Pause: []string{"flow"}})
	inForce := r.acceptAndConfirm(t, p.ID) // no samples were ever fed
	*r.now = inForce.Add(10 * time.Minute)
	r.h.C.Loop.RecordOutcomes(context.Background())
	if _, ok := outcomeOf(t, r, p.ID); ok {
		t.Fatal("recorded before the give-up time although samples might still arrive")
	}
	*r.now = inForce.Add(40 * time.Minute)
	r.h.C.Loop.RecordOutcomes(context.Background())
	o, ok := outcomeOf(t, r, p.ID)
	if !ok || o.Result != "no_data" || o.InForceAt == nil {
		t.Fatalf("%+v %v", o, ok)
	}
}

// Only a proposal that was applied has an Outcome; a pending, rejected or failed one has none.
func TestOnlyAppliedProposalsGetAnOutcome(t *testing.T) {
	r := propRigFor(t)
	pend, _ := r.submit(t, decisionloop.AccessChange{Pause: []string{"flow"}})
	rej, _ := r.submit(t, decisionloop.AccessChange{Pause: []string{"probes"}})
	if _, err := r.h.C.Loop.RejectProposal(testkit.AsRole(accounts.RoleEditor), "edith", rej.ID, ""); err != nil {
		t.Fatal(err)
	}
	*r.now = r.now.Add(2 * time.Hour)
	r.h.C.Loop.RecordOutcomes(context.Background())
	for _, id := range []string{pend.ID, rej.ID} {
		if _, ok := outcomeOf(t, r, id); ok {
			t.Fatalf("%s has an outcome", id)
		}
	}
}

// An agent that was already running the current Config before the change was decided has not confirmed the change: only an echo
// at or after the decision counts.
func TestAnEchoFromBeforeTheDecisionDoesNotConfirmTheChange(t *testing.T) {
	r := propRigFor(t)
	r.h.Mu.Lock()
	v := r.h.ViewFor(r.id)
	v.CfgHash, v.CfgApplied, v.CfgInForceAt = "h-before", "h-before", r.now.Add(-time.Hour)
	r.h.Mu.Unlock()
	p, _ := r.submit(t, decisionloop.AccessChange{Pause: []string{"flow"}})
	if _, err := r.h.C.Loop.AcceptProposal(testkit.AsRole(accounts.RoleEditor), "edith", p.ID, ""); err != nil {
		t.Fatal(err)
	}
	r.h.Mu.Lock()
	v.CfgHash = "h-before" // the push did not change what the server holds as sent, and the agent still names it
	r.h.Mu.Unlock()
	*r.now = r.now.Add(time.Hour)
	r.h.C.Loop.RecordOutcomes(context.Background())
	o, ok := outcomeOf(t, r, p.ID)
	if !ok || o.Result != "unconfirmed" {
		t.Fatalf("%+v %v", o, ok)
	}
}
