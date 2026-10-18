package server

import (
	"fmt"
	"testing"

	continuumv1 "continuum/gen/continuumv1"

	"continuum/internal/accounts"
	"continuum/internal/decisionloop"
	"continuum/internal/kernel"
	"continuum/internal/testkit"
)

// Every place that asks "may this agent have this tier" answers from the same four numbers: the release's ceiling
// (ImplementedTier), the type's outer bound (MaxTier), what the agent's install allows (InstalledTier) and what its token
// allowed (TierCap). These tests pin what each place says today, word for word, so that asking the question in one function
// (tier.go) changes nothing a person or an agent sees.

func wantErr(t *testing.T, what string, err error, kind kernel.Kind, msg string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: accepted, want %q", what, msg)
		return
	}
	if testkit.KindOf(err) != kind || err.Error() != msg {
		t.Errorf("%s: got %d %q, want %d %q", what, testkit.KindOf(err), err.Error(), kind, msg)
	}
}

func TestEachPlaceThatBoundsATierSaysWhatItSaidBefore(t *testing.T) {
	// Token creation: only the release's range.
	e := newEnv(t)
	for _, n := range []int{-1, 3, 9} {
		_, _, err := e.core.Agents.CreateToken(e.ctx, "a", "c", n)
		wantErr(t, fmt.Sprintf("token tier %d", n), err, kernel.KindInvalid, "access tier must be 0-2 in this release")
	}
	for _, n := range []int{0, 1, 2} {
		if _, _, err := e.core.Agents.CreateToken(e.ctx, "a", "c", n); err != nil {
			t.Errorf("token tier %d: %v", n, err)
		}
	}

	// Enrollment: what an install reports must be inside the type's bound, which is wider than the release's.
	secret, _, _ := e.core.Agents.CreateToken(e.ctx, "a", "c", 2)
	d, _ := testkit.CSR(t)
	enrollAt := func(tier uint32) error {
		_, err := e.core.Agents.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: testkit.FP, InstalledAccessTier: tier, ApprovalCodeHash: testkit.HashFor(t, d)})
		return err
	}
	wantErr(t, "enrolling at 5", enrollAt(5), kernel.KindInvalid, "installed access tier out of range")
	if err := enrollAt(4); err != nil {
		t.Errorf("enrolling at 4 (past the release, inside the type): %v", err)
	}

	// Approval: one message for any of installed, token and release.
	const approveMsg = "access tier %d is above what is installed (%d), allowed by the token (%d) or implemented (%d)"
	e2 := newEnv(t)
	r1, _ := e2.enroll(t, 1, testkit.FP) // installed 1, token 2
	wantErr(t, "approving above the install", e2.approve(r1.AgentId, 2), kernel.KindInvalid, fmt.Sprintf(approveMsg, 2, 1, 2, 2))
	wantErr(t, "approving below zero", e2.approve(r1.AgentId, -1), kernel.KindInvalid, fmt.Sprintf(approveMsg, -1, 1, 2, 2))
	e3 := newEnv(t)
	secret3, _, _ := e3.core.Agents.CreateToken(e3.ctx, "a", "c", 1) // token 1
	d3, _ := testkit.CSR(t)
	hash3, code3 := testkit.ApprovalFor(t, d3)
	r3, err := e3.core.Agents.Enroll(e3.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret3, CsrDer: d3, ClusterFingerprint: testkit.FP, InstalledAccessTier: 4, ApprovalCodeHash: hash3})
	if err != nil {
		t.Fatal(err)
	}
	wantErr(t, "approving above the token", e3.core.Agents.Approve(e3.ctx, "alex", r3.AgentId, code3, 2), kernel.KindInvalid, fmt.Sprintf(approveMsg, 2, 4, 1, 2))
	wantErr(t, "approving above the release", e3.core.Agents.Approve(e3.ctx, "alex", r3.AgentId, code3, 3), kernel.KindInvalid, fmt.Sprintf(approveMsg, 3, 4, 1, 2))
	if err := e3.core.Agents.Approve(e3.ctx, "alex", r3.AgentId, code3, 1); err != nil {
		t.Errorf("approving at the token's cap: %v", err)
	}
}

func TestSettingATierSaysWhatItSaidBeforeAndInTheSameOrder(t *testing.T) {
	r := propRigFor(t) // approved 2, installed 2, token cap whatever the rig made
	adm := testkit.AsRole(accounts.RoleAdmin)
	set := func(tier int) error {
		_, err := r.h.SetAgentTier(adm, "adam", r.id, tier, func(n int) string { return fmt.Sprintf("HELM %d", n) })
		return err
	}
	wantErr(t, "negative", set(-1), kernel.KindInvalid, "access tier must be 0 or more")
	wantErr(t, "past the release", set(9), kernel.KindInvalid, "access tier 9 is not available in this release (the highest is 2)")
	// A non-administrator raising is refused before anything about the number is said.
	if err := r.st.SetAccessTier(r.ctx, r.id, 0); err != nil {
		t.Fatal(err)
	}
	_, err := r.h.SetAgentTier(testkit.AsRole(accounts.RoleEditor), "edith", r.id, 9, nil)
	wantErr(t, "an editor raising past the release", err, kernel.KindForbidden, "raising an agent's access takes an administrator, as approving it did; narrowing it does not")
	// Over the install: the message carries the command.
	if err := r.st.SetInstalledTier(r.ctx, r.id, 1); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "over the install", set(2), kernel.KindInvalid,
		"this agent's install allows at most tier 1 (its Helm value access.tier), and only the cluster's owner can raise that. Run this in that cluster, then set the tier here:\nHELM 2")
}

func TestAProposalSaysWhatItSaidBeforeAboutATiersBounds(t *testing.T) {
	r := propRigFor(t)
	cases := []struct {
		name string
		tier int
		msg  string
		kind kernel.Kind
	}{
		{"negative", -1, "access tier must be 0-2 in this release", kernel.KindInvalid},
		{"past the release", 3, "access tier must be 0-2 in this release", kernel.KindInvalid},
		{"the tier it has", 2, "the agent already has access tier 2", kernel.KindConflict},
	}
	for _, c := range cases {
		_, err := r.submit(t, decisionloop.AccessChange{Tier: intp(c.tier)})
		wantErr(t, c.name, err, c.kind, c.msg)
	}
	if err := r.st.SetAccessTier(r.ctx, r.id, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetInstalledTier(r.ctx, r.id, 1); err != nil {
		t.Fatal(err)
	}
	_, err := r.submit(t, decisionloop.AccessChange{Tier: intp(2)})
	wantErr(t, "over the install", err, kernel.KindInvalid, "this agent's install allows at most tier 1")
}

func TestAReportedCeilingIsHeldInsideTheTypesBound(t *testing.T) {
	r := propRigFor(t)
	fresh, _ := r.st.GetAgent(r.ctx, r.id)
	got := r.h.NoteCeiling(r.ctx, fresh, fresh, 9)
	if got.InstalledTier != kernel.MaxTier {
		t.Fatalf("a reported 9 became %d, want %d", got.InstalledTier, kernel.MaxTier)
	}
	if st, _ := r.st.GetAgent(r.ctx, r.id); st.InstalledTier != kernel.MaxTier || st.AccessTier != 2 {
		t.Fatalf("stored installed %d access %d, want %d and 2", st.InstalledTier, st.AccessTier, kernel.MaxTier)
	}
	fresh, _ = r.st.GetAgent(r.ctx, r.id)
	if got := r.h.NoteCeiling(r.ctx, fresh, fresh, 1); got.InstalledTier != 1 {
		t.Fatalf("a reported 1 became %d", got.InstalledTier)
	}
	if st, _ := r.st.GetAgent(r.ctx, r.id); st.AccessTier != 1 {
		t.Fatalf("lowering the ceiling to 1 left the approval at %d", st.AccessTier)
	}
}

// An agent whose install allows 2 but whose enrollment token allowed only 1: the install is wide enough and the token is the
// limit that stops a raise.
func TestATokensCapStopsATierThatTheInstallWouldAllow(t *testing.T) {
	a := newAdminRig(t)
	secret, _, err := a.core.Agents.CreateToken(a.ctx, "a", "c", 1)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := testkit.CSR(t)
	hash, code := testkit.ApprovalFor(t, d)
	r, err := a.core.Agents.Enroll(a.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: testkit.FP, InstalledAccessTier: 2, ApprovalCodeHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.core.Agents.Approve(a.ctx, "alex", r.AgentId, code, 0); err != nil {
		t.Fatal(err)
	}
	h := a.hub()
	_, err = h.SetAgentTier(testkit.AsRole(accounts.RoleAdmin), "adam", r.AgentId, 2, nil)
	wantErr(t, "setting above the token", err, kernel.KindInvalid, "access tier 2 is above what the enrollment token allowed for this agent (1)")
	_, err = h.C.Loop.SubmitProposal(testkit.AsRole(accounts.RoleEditor), "x", decisionloop.ProposalInput{Advisor: "rules-v1", Change: decisionloop.AccessChange{AgentID: r.AgentId, Tier: intp(2)}})
	wantErr(t, "proposing above the token", err, kernel.KindInvalid, "access tier 2 is above what the enrollment token allowed for this agent (1)")
	if _, err = h.SetAgentTier(testkit.AsRole(accounts.RoleAdmin), "adam", r.AgentId, 1, nil); err != nil {
		t.Fatalf("setting at the token's cap: %v", err)
	}
}
