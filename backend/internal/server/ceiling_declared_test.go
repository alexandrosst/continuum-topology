package server

import (
	"context"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/agentcaps"
	"ikhnos/internal/testkit"
)

// A hello that says the install allows tier 0 is believed only from an agent that states its ceiling: a hello that says 0 because
// the field was never filled in (an agent from before it was stated) must not take an approved agent down to nothing. How the
// server tells the two apart is the subject here. For an agent that states a protocol version it is the `diagnostics` capability;
// for one that does not, it is whether the hello carried diagnostics. Either way the ceiling can only go down by a hello: nothing
// here raises an approval.
func TestWhoseZeroCeilingIsBelieved(t *testing.T) {
	diag := &ikhnosv1.Diagnostics{AgentVersion: "9", InstalledTier: 0}
	cases := []struct {
		name      string
		hello     *ikhnosv1.Hello
		wantTier  int // the installed tier recorded afterwards (it was 2)
		wantAcces int // and the approved tier (it was 2)
	}{
		{"protocol 0, no diagnostics: an agent that does not say, kept", &ikhnosv1.Hello{AgentVersion: "9"}, 2, 2},
		{"protocol 0, diagnostics in the hello: the old inference, believed", &ikhnosv1.Hello{AgentVersion: "9", Diagnostics: diag}, 0, 0},
		{"protocol 1 declaring diagnostics, none in the hello: believed", &ikhnosv1.Hello{AgentVersion: "9", ProtocolVersion: 1, Capabilities: []string{agentcaps.Diagnostics}}, 0, 0},
		{"protocol 1 not declaring it, diagnostics in the hello: the capability decides, kept", &ikhnosv1.Hello{AgentVersion: "9", ProtocolVersion: 1, Capabilities: []string{agentcaps.SelfStats}, Diagnostics: diag}, 2, 2},
		{"protocol 1 declaring nothing: kept", &ikhnosv1.Hello{AgentVersion: "9", ProtocolVersion: 1}, 2, 2},
		{"protocol 99 declaring it: believed", &ikhnosv1.Hello{AgentVersion: "9", ProtocolVersion: 99, Capabilities: []string{agentcaps.Diagnostics}}, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newHubRig(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			id, key, leaf := r.approvedAgent(t, testkit.FP) // approved at 2, installed 2
			r.connect(t, ctx, key, leaf, c.hello)
			a, err := r.st.GetAgent(ctx, "org-1", id)
			if err != nil {
				t.Fatal(err)
			}
			if a.InstalledTier != c.wantTier || a.AccessTier != c.wantAcces {
				t.Fatalf("installed %d approved %d, want %d and %d", a.InstalledTier, a.AccessTier, c.wantTier, c.wantAcces)
			}
		})
	}
}

// A hello that states a ceiling above what is recorded does not raise the approval, with or without the capability.
func TestAHigherCeilingNeverRaisesAnApproval(t *testing.T) {
	for _, h := range []*ikhnosv1.Hello{
		{AgentVersion: "9", InstalledAccessTier: 2, Diagnostics: &ikhnosv1.Diagnostics{AgentVersion: "9"}},
		{AgentVersion: "9", InstalledAccessTier: 2, ProtocolVersion: 1, Capabilities: []string{agentcaps.Diagnostics}},
	} {
		r := newHubRig(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		id, key, leaf := r.approvedAgent(t, testkit.FP)
		if err := r.st.SetAccessTier(ctx, "org-1", id, 1); err != nil {
			t.Fatal(err)
		}
		r.connect(t, ctx, key, leaf, h)
		a, _ := r.st.GetAgent(ctx, "org-1", id)
		if a.AccessTier != 1 {
			t.Fatalf("approval went from 1 to %d on a hello", a.AccessTier)
		}
		cancel()
	}
}
