package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// Characterization tests for the agent lifecycle and the organisation paths the store owns. The state machine
// lives in the SQL (each transition names the statuses it may start from), so it is pinned here at that level.

func enroll(t *testing.T, st *SQLite, tokenID, agentID, fingerprint string, now time.Time) Agent {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateToken(ctx, Token{ID: tokenID, OrgID: "o1", Label: "edge-" + agentID, AccessTier: 3, CreatedBy: "alex", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, []byte("hash-"+tokenID)); err != nil {
		t.Fatal(err)
	}
	a := Agent{ID: agentID, InstalledTier: 2, Fingerprint: fingerprint, CSR: []byte("csr"), PollSecretHash: []byte("poll"), Version: "v1", K8sVersion: "1.30", ConnectingIP: "203.0.113.5"}
	if _, err := st.EnrollAgent(ctx, []byte("hash-"+tokenID), a, now); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAgent(ctx, "o1", agentID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestEnrollmentConsumesTheTokenOnceAndStartsPending(t *testing.T) {
	st, ctx, now := openAccounts(t)
	got := enroll(t, st, "tk-1", "ag-1", "fp-a", now)

	if got.Status != StatusPending || got.OrgID != "o1" || got.Name != "edge-ag-1" || got.TokenID != "tk-1" || got.TierCap != 3 || got.AccessTier != 0 || !got.CreatedAt.Equal(now) {
		t.Fatalf("a freshly enrolled agent: %+v", got)
	}
	toks, _ := st.ListTokens(ctx, "o1")
	if len(toks) != 1 || toks[0].UsedAt == nil || toks[0].UsedBy != "ag-1" {
		t.Fatalf("the token should be marked used by the agent: %+v", toks)
	}
	// A used token cannot enroll a second agent.
	if _, err := st.EnrollAgent(ctx, []byte("hash-tk-1"), Agent{ID: "ag-2", Fingerprint: "fp-b", CSR: []byte("c")}, now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("reusing a token: %v, want ErrTokenInvalid", err)
	}
	// An expired token cannot enroll either, and an unknown hash looks the same.
	if err := st.CreateToken(ctx, Token{ID: "tk-old", OrgID: "o1", Label: "x", CreatedBy: "a", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, []byte("h-old")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnrollAgent(ctx, []byte("h-old"), Agent{ID: "ag-3", Fingerprint: "fp-c", CSR: []byte("c")}, now.Add(time.Hour)); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expired token: %v, want ErrTokenInvalid", err)
	}
	if _, err := st.EnrollAgent(ctx, []byte("unknown"), Agent{ID: "ag-4", CSR: []byte("c")}, now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("unknown token: %v, want ErrTokenInvalid", err)
	}
	if _, err := st.GetAgent(ctx, "o1", "ag-2"); !errors.Is(err, ErrNotFound) {
		t.Fatal("a refused enrollment must leave no agent behind")
	}
}

func TestOnlyUnusedTokensCanBeDeleted(t *testing.T) {
	st, ctx, now := openAccounts(t)
	enroll(t, st, "tk-used", "ag-1", "fp-a", now)
	if err := st.CreateToken(ctx, Token{ID: "tk-free", OrgID: "o1", Label: "x", CreatedBy: "a", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, []byte("h-free")); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteToken(ctx, "o1", "tk-used"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a used token: %v, want ErrNotFound", err)
	}
	if err := st.DeleteToken(ctx, "o2", "tk-free"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting another organisation's token: %v, want ErrNotFound", err)
	}
	if err := st.DeleteToken(ctx, "o1", "tk-free"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentStateMachineTransitions(t *testing.T) {
	st, ctx, now := openAccounts(t)
	leafExp := now.Add(24 * time.Hour)

	// pending -> approved
	enroll(t, st, "tk-1", "ag-1", "fp-a", now)
	if err := st.SetLeaf(ctx, "o1", "ag-1", []byte("leaf"), leafExp); !errors.Is(err, ErrBadState) {
		t.Fatalf("a pending agent has no certificate to renew: %v, want ErrBadState", err)
	}
	if err := st.SetAccessTier(ctx, "o1", "ag-1", 2); !errors.Is(err, ErrBadState) {
		t.Fatalf("a pending agent has no access tier to change: %v, want ErrBadState", err)
	}
	if err := st.ApproveAgent(ctx, "o1", "ag-1", 2, "alex", "cl-1", []byte("leaf0"), leafExp, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	a, _ := st.GetAgent(ctx, "o1", "ag-1")
	if a.Status != StatusApproved || a.AccessTier != 2 || a.ApprovedBy != "alex" || a.ClusterID != "cl-1" || !bytes.Equal(a.LeafDER, []byte("leaf0")) ||
		a.ApprovedAt == nil || a.LeafNotAfter == nil || !a.LeafNotAfter.Equal(leafExp) || a.Reason != "" {
		t.Fatalf("approved: %+v", a)
	}
	// An approved agent cannot be approved again, rejected, or counted against.
	if err := st.ApproveAgent(ctx, "o1", "ag-1", 2, "alex", "cl-1", nil, leafExp, now); !errors.Is(err, ErrBadState) {
		t.Fatalf("approve twice: %v, want ErrBadState", err)
	}
	if err := st.RejectAgent(ctx, "o1", "ag-1", "no", now); !errors.Is(err, ErrBadState) {
		t.Fatalf("reject an approved agent: %v, want ErrBadState", err)
	}
	if _, err := st.CountApprovalAttempt(ctx, "o1", "ag-1"); !errors.Is(err, ErrBadState) {
		t.Fatalf("count against an approved agent: %v, want ErrBadState", err)
	}
	// Approved agents can have their tier and certificate changed.
	if err := st.SetAccessTier(ctx, "o1", "ag-1", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInstalledTier(ctx, "o1", "ag-1", 3); err != nil {
		t.Fatal(err)
	}
	newExp := leafExp.Add(24 * time.Hour)
	if err := st.SetLeaf(ctx, "o1", "ag-1", []byte("leaf1"), newExp); err != nil {
		t.Fatal(err)
	}
	a, _ = st.GetAgent(ctx, "o1", "ag-1")
	if a.AccessTier != 1 || a.InstalledTier != 3 || !bytes.Equal(a.LeafDER, []byte("leaf1")) || !a.LeafNotAfter.Equal(newExp) {
		t.Fatalf("after tier and leaf changes: %+v", a)
	}

	// approved -> revoked: the certificate is erased, and it cannot happen twice.
	if err := st.RevokeAgent(ctx, "o1", "ag-1", "stolen", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	a, _ = st.GetAgent(ctx, "o1", "ag-1")
	if a.Status != StatusRevoked || a.Reason != "stolen" || a.RevokedAt == nil || len(a.LeafDER) != 0 {
		t.Fatalf("revoked: %+v", a)
	}
	if err := st.RevokeAgent(ctx, "o1", "ag-1", "again", now); !errors.Is(err, ErrBadState) {
		t.Fatalf("revoke twice: %v, want ErrBadState", err)
	}
	if err := st.SetAccessTier(ctx, "o1", "ag-1", 2); !errors.Is(err, ErrBadState) {
		t.Fatalf("a revoked agent's tier: %v, want ErrBadState", err)
	}
	if err := st.SetLeaf(ctx, "o1", "ag-1", []byte("x"), newExp); !errors.Is(err, ErrBadState) {
		t.Fatalf("a revoked agent's certificate: %v, want ErrBadState", err)
	}

	// pending -> rejected, and pending -> revoked are both allowed; neither is reversible.
	enroll(t, st, "tk-2", "ag-2", "fp-b", now)
	if err := st.RejectAgent(ctx, "o1", "ag-2", "not ours", now); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.GetAgent(ctx, "o1", "ag-2"); a.Status != StatusRejected || a.Reason != "not ours" || a.RevokedAt == nil {
		t.Fatalf("rejected: %+v", a)
	}
	if err := st.ApproveAgent(ctx, "o1", "ag-2", 1, "alex", "cl-2", nil, leafExp, now); !errors.Is(err, ErrBadState) {
		t.Fatalf("approving a rejected agent: %v, want ErrBadState", err)
	}
	enroll(t, st, "tk-3", "ag-3", "fp-c", now)
	if err := st.RevokeAgent(ctx, "o1", "ag-3", "withdrawn", now); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.GetAgent(ctx, "o1", "ag-3"); a.Status != StatusRevoked {
		t.Fatalf("a pending agent can be revoked: %+v", a)
	}
}

func TestOnlyOneApprovedAgentMayRepresentACluster(t *testing.T) {
	st, ctx, now := openAccounts(t)
	exp := now.Add(time.Hour)

	enroll(t, st, "tk-1", "ag-1", "fp-same", now)
	enroll(t, st, "tk-2", "ag-2", "fp-same", now) // pending twins are allowed
	if err := st.ApproveAgent(ctx, "o1", "ag-1", 1, "alex", "cl-1", nil, exp, now); err != nil {
		t.Fatal(err)
	}
	if err := st.ApproveAgent(ctx, "o1", "ag-2", 1, "alex", "cl-1", nil, exp, now); !errors.Is(err, ErrClusterEnrolled) {
		t.Fatalf("a second live agent for one cluster: %v, want ErrClusterEnrolled", err)
	}
	if a, _ := st.GetAgent(ctx, "o1", "ag-2"); a.Status != StatusPending {
		t.Fatalf("the refused approval must leave the agent pending: %+v", a)
	}
	// Once the first is revoked, the cluster can be taken over.
	if err := st.RevokeAgent(ctx, "o1", "ag-1", "replaced", now); err != nil {
		t.Fatal(err)
	}
	if err := st.ApproveAgent(ctx, "o1", "ag-2", 1, "alex", "cl-1", nil, exp, now); err != nil {
		t.Fatalf("after the revoke: %v", err)
	}
}

func TestResumeEnrollmentRulesPerState(t *testing.T) {
	st, ctx, now := openAccounts(t)
	exp := now.Add(time.Hour)
	r := Resume{PollSecretHash: []byte("poll2"), ApprovalHash: []byte("code2"), CSR: []byte("csr2"), Version: "v2", K8sVersion: "1.31", IP: "198.51.100.7"}

	// pending: the poll secret and versions are refreshed; the CSR and approval code are not.
	enroll(t, st, "tk-1", "ag-1", "fp-a", now)
	if err := st.ResumeEnrollment(ctx, "o1", "ag-1", r, false, now); err != nil {
		t.Fatal(err)
	}
	a, _ := st.GetAgent(ctx, "o1", "ag-1")
	if !bytes.Equal(a.PollSecretHash, []byte("poll2")) || a.Version != "v2" || a.K8sVersion != "1.31" || a.ConnectingIP != "198.51.100.7" || !bytes.Equal(a.CSR, []byte("csr")) {
		t.Fatalf("resumed pending agent: %+v", a)
	}
	// approved with the poll secret still set (never made mTLS contact): allowed. Once cleared: refused.
	if err := st.ApproveAgent(ctx, "o1", "ag-1", 1, "alex", "cl-1", nil, exp, now); err != nil {
		t.Fatal(err)
	}
	if err := st.ResumeEnrollment(ctx, "o1", "ag-1", r, false, now); err != nil {
		t.Fatalf("approved but never connected: %v", err)
	}
	if err := st.ClearPollSecret(ctx, "o1", "ag-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.ResumeEnrollment(ctx, "o1", "ag-1", r, false, now); !errors.Is(err, ErrBadState) {
		t.Fatalf("approved and connected: %v, want ErrBadState", err)
	}

	// expired: only reopen may revive it, and it returns to pending with a fresh CSR and counters.
	enroll(t, st, "tk-2", "ag-2", "fp-b", now)
	if _, err := st.CountApprovalAttempt(ctx, "o1", "ag-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ExpirePending(ctx, "o1", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.ResumeEnrollment(ctx, "o1", "ag-2", r, false, now); !errors.Is(err, ErrBadState) {
		t.Fatalf("resuming an expired agent without reopening: %v, want ErrBadState", err)
	}
	later := now.Add(2 * time.Hour)
	if err := st.ResumeEnrollment(ctx, "o1", "ag-2", r, true, later); err != nil {
		t.Fatal(err)
	}
	a, _ = st.GetAgent(ctx, "o1", "ag-2")
	if a.Status != StatusPending || a.ApprovalAttempts != 0 || !bytes.Equal(a.CSR, []byte("csr2")) || !bytes.Equal(a.ApprovalHash, []byte("code2")) || !a.CreatedAt.Equal(later) || a.Reason != "" || a.RevokedAt != nil {
		t.Fatalf("reopened: %+v", a)
	}
	if err := st.ResumeEnrollment(ctx, "o1", "ag-2", r, true, later); !errors.Is(err, ErrBadState) {
		t.Fatalf("reopening something that is no longer expired: %v, want ErrBadState", err)
	}
}

func TestExpireAndPurgePendingAgents(t *testing.T) {
	st, ctx, now := openAccounts(t)
	enroll(t, st, "tk-1", "old", "fp-a", now)
	enroll(t, st, "tk-2", "new", "fp-b", now.Add(2*time.Hour))
	enroll(t, st, "tk-3", "approved", "fp-c", now)
	if err := st.ApproveAgent(ctx, "o1", "approved", 1, "alex", "cl-3", nil, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}

	expired, err := st.ExpirePending(ctx, "o1", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].ID != "old" || expired[0].Status != StatusExpired {
		t.Fatalf("ExpirePending = %+v", expired)
	}
	if a, _ := st.GetAgent(ctx, "o1", "old"); a.Status != StatusExpired || a.Reason != "nobody approved it in time" {
		t.Fatalf("the stored agent: %+v", a)
	}
	if a, _ := st.GetAgent(ctx, "o1", "new"); a.Status != StatusPending {
		t.Fatalf("a recent pending agent must stay pending: %+v", a)
	}
	if a, _ := st.GetAgent(ctx, "o1", "approved"); a.Status != StatusApproved {
		t.Fatalf("an approved agent is never expired: %+v", a)
	}
	if again, _ := st.ExpirePending(ctx, "o1", now.Add(time.Hour)); len(again) != 0 {
		t.Fatalf("nothing is expired twice: %+v", again)
	}

	if n, err := st.PurgeExpired(ctx, "o1", now); err != nil || n != 0 {
		t.Fatalf("purge before the cutoff: %d %v", n, err)
	}
	if n, err := st.PurgeExpired(ctx, "o1", now.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("purge: %d %v, want 1", n, err)
	}
	if _, err := st.GetAgent(ctx, "o1", "old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a purged agent: %v, want ErrNotFound", err)
	}
	agents, _ := st.ListAgents(ctx, "o1")
	var ids []string
	for _, a := range agents {
		ids = append(ids, a.ID)
	}
	if !reflect.DeepEqual(ids, []string{"approved", "new"}) && !reflect.DeepEqual(ids, []string{"new", "approved"}) {
		t.Fatalf("ListAgents = %v", ids)
	}
	if agents[0].CreatedAt.After(agents[1].CreatedAt) {
		t.Fatal("ListAgents is ordered by creation time")
	}
	if other, _ := st.ListAgents(ctx, "o2"); len(other) != 0 {
		t.Fatalf("another organisation lists %d agents", len(other))
	}
}

func TestTouchKeepsWhatAnEmptyValueDoesNotReplace(t *testing.T) {
	st, ctx, now := openAccounts(t)
	enroll(t, st, "tk-1", "ag-1", "fp-a", now)

	seen := now.Add(time.Minute)
	if err := st.Touch(ctx, "o1", "ag-1", "", "", "", seen); err != nil {
		t.Fatal(err)
	}
	a, _ := st.GetAgent(ctx, "o1", "ag-1")
	if a.LastSeen == nil || !a.LastSeen.Equal(seen) || a.ConnectingIP != "203.0.113.5" || a.Version != "v1" || a.K8sVersion != "1.30" {
		t.Fatalf("an empty ip or version leaves the stored one alone: %+v", a)
	}
	if err := st.Touch(ctx, "o1", "ag-1", "198.51.100.9", "v2", "1.31", seen.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	a, _ = st.GetAgent(ctx, "o1", "ag-1")
	if a.ConnectingIP != "198.51.100.9" || a.Version != "v2" || a.K8sVersion != "1.31" {
		t.Fatalf("a non-empty value replaces it: %+v", a)
	}
}

func TestSnapshotsAreKeyedByAgentAndDeletedByKey(t *testing.T) {
	st, ctx, now := openAccounts(t)
	if _, _, err := st.LoadSnapshot(ctx, "ag-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no snapshot yet: %v, want ErrNotFound", err)
	}
	for _, k := range []string{"ag-1", "ag-1#flows", "ag-2"} {
		if err := st.SaveSnapshot(ctx, k, []byte("v1-"+k), now); err != nil {
			t.Fatal(err)
		}
	}
	later := now.Add(time.Minute)
	if err := st.SaveSnapshot(ctx, "ag-1", []byte("v2"), later); err != nil {
		t.Fatal(err)
	}
	d, at, err := st.LoadSnapshot(ctx, "ag-1")
	if err != nil || string(d) != "v2" || !at.Equal(later) {
		t.Fatalf("a snapshot is replaced in place: %q %v %v", d, at, err)
	}
	if err := st.DeleteSnapshots(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSnapshots(ctx, "ag-1", "ag-1#flows"); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]bool{"ag-1": false, "ag-1#flows": false, "ag-2": true} {
		if _, _, err := st.LoadSnapshot(ctx, k); (err == nil) != want {
			t.Fatalf("snapshot %q present = %v, want %v", k, err == nil, want)
		}
	}
}

func TestOrganisationsMembersAndInvites(t *testing.T) {
	st, ctx, now := openAccounts(t)
	mustUser(t, st, "u1", "quinn", now)
	mustUser(t, st, "u2", "Casey", now)
	mustUser(t, st, "u3", "avery", now)

	if err := st.CreateOrg(ctx, Org{ID: "o1", Name: "Athens", CreatedAt: now, CreatedBy: "u1"}, "u1"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOrg(ctx, Org{ID: "o1", Name: "dup", CreatedAt: now, CreatedBy: "u1"}, "u1"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate organisation id: %v, want ErrExists", err)
	}
	if err := st.CreateOrg(ctx, Org{ID: "o2", Name: "Patras", CreatedAt: now.Add(time.Second), CreatedBy: "u2"}, "u2"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOrg(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown organisation: %v, want ErrNotFound", err)
	}
	orgs, _ := st.ListOrgs(ctx)
	if len(orgs) != 2 || orgs[0].ID != "o1" || orgs[1].ID != "o2" {
		t.Fatalf("ListOrgs is oldest first: %+v", orgs)
	}
	if err := st.RenameOrg(ctx, "o1", "Athens HQ"); err != nil {
		t.Fatal(err)
	}
	if err := st.RenameOrg(ctx, "nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename an unknown organisation: %v, want ErrNotFound", err)
	}
	if o, _ := st.GetOrg(ctx, "o1"); o.Name != "Athens HQ" {
		t.Fatalf("renamed: %+v", o)
	}

	// The creator is the first owner.
	if m, err := st.GetMembership(ctx, "o1", "u1"); err != nil || m.Role != "owner" || m.AddedBy != "" {
		t.Fatalf("creator's membership: %+v %v", m, err)
	}
	if _, err := st.GetMembership(ctx, "o1", "u2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a non-member: %v, want ErrNotFound", err)
	}
	if err := st.AddMember(ctx, Membership{OrgID: "o1", UserID: "u2", Role: "viewer", CreatedAt: now, AddedBy: "u1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddMember(ctx, Membership{OrgID: "o1", UserID: "u2", Role: "admin", CreatedAt: now}); !errors.Is(err, ErrExists) {
		t.Fatalf("adding a member twice: %v, want ErrExists", err)
	}
	members, _ := st.ListMembers(ctx, "o1")
	if len(members) != 2 || members[0].Username != "Casey" || members[1].Username != "quinn" {
		t.Fatalf("members are ordered by lower-cased username: %+v", members)
	}
	mine, _ := st.ListMyOrgs(ctx, "u2")
	if len(mine) != 2 || mine[0].ID != "o1" || mine[0].Role != "viewer" || mine[1].ID != "o2" || mine[1].Role != "owner" {
		t.Fatalf("ListMyOrgs = %+v", mine)
	}

	// Owners are counted without disabled accounts.
	if n, _ := st.CountOwners(ctx, "o1"); n != 1 {
		t.Fatalf("CountOwners = %d", n)
	}
	off := now
	if err := st.SetDisabled(ctx, "u1", &off); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountOwners(ctx, "o1"); n != 0 {
		t.Fatalf("a disabled owner does not count: %d", n)
	}
	if err := st.SetMemberRole(ctx, "o1", "u2", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMemberRole(ctx, "o1", "u3", "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("role of a non-member: %v, want ErrNotFound", err)
	}
	if n, _ := st.CountOwners(ctx, "o1"); n != 1 {
		t.Fatalf("CountOwners after promoting: %d", n)
	}
	if err := st.RemoveMember(ctx, "o1", "u2"); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveMember(ctx, "o1", "u2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove twice: %v, want ErrNotFound", err)
	}

	// Invites: peek does not consume, use does, once.
	inv := Invite{ID: "iv-1", OrgID: "o1", Role: "editor", Label: "for avery", CreatedBy: "u1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := st.CreateInvite(ctx, inv, []byte("ih1")); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInvite(ctx, Invite{ID: "iv-2", OrgID: "o1", Role: "viewer", CreatedBy: "u1", CreatedAt: now.Add(time.Second), ExpiresAt: now.Add(time.Hour)}, []byte("ih2")); err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListInvites(ctx, "o1")
	if len(list) != 2 || list[0].ID != "iv-2" {
		t.Fatalf("ListInvites is newest first: %+v", list)
	}
	if got, err := st.PeekInvite(ctx, []byte("ih1"), now); err != nil || got.Role != "editor" || got.UsedAt != nil {
		t.Fatalf("PeekInvite = %+v %v", got, err)
	}
	if _, err := st.PeekInvite(ctx, []byte("ih1"), now.Add(2*time.Hour)); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("an expired invite: %v, want ErrTokenInvalid", err)
	}
	if _, err := st.PeekInvite(ctx, []byte("nope"), now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("an unknown invite: %v, want ErrTokenInvalid", err)
	}
	used, err := st.UseInvite(ctx, []byte("ih1"), "u3", now)
	if err != nil || used.UsedBy != "u3" || used.UsedAt == nil {
		t.Fatalf("UseInvite = %+v %v", used, err)
	}
	if m, err := st.GetMembership(ctx, "o1", "u3"); err != nil || m.Role != "editor" || m.AddedBy != "u1" {
		t.Fatalf("the invite grants its role: %+v %v", m, err)
	}
	if _, err := st.UseInvite(ctx, []byte("ih1"), "u2", now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("an invite is single use: %v, want ErrTokenInvalid", err)
	}
	// A person who is already a member cannot use another invite, and the invite stays unused.
	if _, err := st.UseInvite(ctx, []byte("ih2"), "u3", now); !errors.Is(err, ErrExists) {
		t.Fatalf("a member using an invite: %v, want ErrExists", err)
	}
	if got, err := st.PeekInvite(ctx, []byte("ih2"), now); err != nil || got.UsedAt != nil {
		t.Fatalf("the refused invite must stay usable: %+v %v", got, err)
	}
	// A used invite cannot be revoked; an unused one can.
	if err := st.RevokeInvite(ctx, "o1", "iv-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking a used invite: %v, want ErrNotFound", err)
	}
	if err := st.RevokeInvite(ctx, "o1", "iv-2"); err != nil {
		t.Fatal(err)
	}
}

func TestDeletingAnOrganisationRemovesItsMembershipsAndInvites(t *testing.T) {
	st, ctx, now := openAccounts(t)
	mustUser(t, st, "u1", "quinn", now)
	if err := st.CreateOrg(ctx, Org{ID: "o1", Name: "Athens", CreatedAt: now, CreatedBy: "u1"}, "u1"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInvite(ctx, Invite{ID: "iv-1", OrgID: "o1", Role: "viewer", CreatedBy: "u1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, []byte("ih")); err != nil {
		t.Fatal(err)
	}
	enroll(t, st, "tk-1", "ag-1", "fp-a", now)
	if err := st.SaveSnapshot(ctx, "ag-1", []byte("s"), now); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSnapshot(ctx, "ag-1#flows", []byte("f"), now); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSnapshot(ctx, "ag-1#consent", []byte("c"), now); err != nil {
		t.Fatal(err)
	}
	if err := st.AddHistory(ctx, "o1", now, []byte("h")); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEvents(ctx, "o1", []Event{ev(now, "k", "t", "c")}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSettings(ctx, "o1", []byte("{}"), now); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteOrg(ctx, "o1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOrg(ctx, "o1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("organisation still there: %v", err)
	}
	if _, err := st.GetMembership(ctx, "o1", "u1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("membership survived: %v", err)
	}
	if inv, _ := st.ListInvites(ctx, "o1"); len(inv) != 0 {
		t.Fatalf("invites survived: %+v", inv)
	}
	if ag, _ := st.ListAgents(ctx, "o1"); len(ag) != 0 {
		t.Fatalf("agents survived: %+v", ag)
	}
	for _, k := range []string{"ag-1", "ag-1#flows", "ag-1#consent"} {
		if _, _, err := st.LoadSnapshot(ctx, k); !errors.Is(err, ErrNotFound) {
			t.Fatalf("snapshot %q survived: %v", k, err)
		}
	}
	if pts, _ := st.ListHistory(ctx, "o1", time.Time{}, time.Time{}); len(pts) != 0 {
		t.Fatalf("history survived: %+v", pts)
	}
	if evs, _ := st.ListEvents(ctx, "o1", EventQuery{}); len(evs) != 0 {
		t.Fatalf("events survived: %+v", evs)
	}
	if d, _ := st.GetSettings(ctx, "o1"); d != nil {
		t.Fatalf("settings survived: %q", d)
	}
	// The account itself is not an organisation's to delete.
	if _, err := st.GetUser(ctx, "u1"); err != nil {
		t.Fatalf("deleting an organisation must not delete its members' accounts: %v", err)
	}
}

// Every table that holds an organisation's data must be emptied by DeleteOrg (or be one of the few kept on
// purpose). A new organisation-scoped table fails this test until it is handled and listed here.
func TestEveryOrganisationScopedTableIsAccountedForByDeleteOrg(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "tables.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()

	var scoped []string
	for _, n := range tables {
		cols, err := st.db.Query(`SELECT name FROM pragma_table_info('` + n + `')`)
		if err != nil {
			t.Fatal(err)
		}
		for cols.Next() {
			var c string
			cols.Scan(&c)
			if c == "org_id" {
				scoped = append(scoped, n)
			}
		}
		cols.Close()
	}
	sort.Strings(scoped)

	want := []string{
		// emptied by DeleteOrg
		"agents", "decisions", "events", "fusion_tokens", "gateway_tokens", "history", "identities", "link_iperf_credentials",
		"model_state", "operator_certs", "operators", "proposal_outcomes", "proposals", "settings", "telemetry_intents", "tokens", "tombstones", "workspace",
		// removed with the organisation row (ON DELETE CASCADE)
		"invites", "memberships",
		// kept on purpose: the audit trail outlives the organisation, and users carry a legacy empty org_id
		"audit", "users",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(scoped, want) {
		t.Fatalf("the set of organisation-scoped tables changed.\n got  %v\n want %v\nDecide whether DeleteOrg must clear the new table, then update this list.", scoped, want)
	}
}

// An agent is addressed by its id inside its organisation. Another organisation naming the same id finds no row, whatever it asks of it:
// it reads nothing and changes nothing. Only FindAgent looks across organisations, for the caller that has just proved who it is.
func TestAnotherOrganisationsAgentIsNotFoundByEveryMethod(t *testing.T) {
	st, ctx, now := openAccounts(t)
	enroll(t, st, "tk-1", "ag-1", "fp-a", now)

	if _, err := st.GetAgent(ctx, "bob", "ag-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	for name, err := range map[string]error{
		"approve":  st.ApproveAgent(ctx, "bob", "ag-1", 2, "bob", "c-1", []byte("leaf"), now.Add(time.Hour), now),
		"reject":   st.RejectAgent(ctx, "bob", "ag-1", "no", now),
		"revoke":   st.RevokeAgent(ctx, "bob", "ag-1", "no", now),
		"tier":     st.SetAccessTier(ctx, "bob", "ag-1", 2),
		"leaf":     st.SetLeaf(ctx, "bob", "ag-1", []byte("leaf"), now.Add(time.Hour)),
		"resume":   st.ResumeEnrollment(ctx, "bob", "ag-1", Resume{PollSecretHash: []byte("x")}, false, now),
		"attempts": func() error { _, err := st.CountApprovalAttempt(ctx, "bob", "ag-1"); return err }(),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}
	// These report nothing (they are best-effort), and change nothing.
	_ = st.SetInstalledTier(ctx, "bob", "ag-1", 3)
	_ = st.SetClockSkew(ctx, "bob", "ag-1", 5000)
	_ = st.ClearPollSecret(ctx, "bob", "ag-1")
	_ = st.Touch(ctx, "bob", "ag-1", "198.51.100.9", "v9", "1.99", now)

	a, err := st.GetAgent(ctx, "o1", "ag-1")
	if err != nil || a.Status != StatusPending || a.AccessTier != 0 || a.InstalledTier != 2 || a.ClockSkewMs != 0 || len(a.PollSecretHash) == 0 ||
		a.ConnectingIP != "203.0.113.5" || a.Version != "v1" || a.LastSeen != nil || a.ApprovalAttempts != 0 || len(a.LeafDER) != 0 {
		t.Fatalf("o1's agent changed: %+v %v", a, err)
	}
	// The platform-wide lookup finds it, and says which organisation it is in.
	if f, err := st.FindAgent(ctx, "ag-1"); err != nil || f.OrgID != "o1" {
		t.Errorf("FindAgent: %+v %v", f, err)
	}
	// In its own organisation the wrong state is a different answer from a missing row.
	if err := st.SetAccessTier(ctx, "o1", "ag-1", 2); !errors.Is(err, ErrBadState) {
		t.Errorf("tier of a pending agent: %v, want ErrBadState", err)
	}
}
