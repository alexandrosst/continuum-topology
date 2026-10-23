package estate

import (
	"context"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/testkit"

	"google.golang.org/grpc/codes"
)

// Faults that cross streams, which a test of one stream cannot make: a storm of reconnects, a message from a stream the server has
// already replaced, a hello said twice, an agent that talks before it has said who it is, and one that talks too fast.

func heartbeat(digest string) *ikhnosv1.AgentMessage {
	return &ikhnosv1.AgentMessage{Msg: &ikhnosv1.AgentMessage_Heartbeat{Heartbeat: &ikhnosv1.Heartbeat{AppliedConfigDigest: digest}}}
}

func TestAReconnectStormLeavesOneSessionAndTheLastStreamLive(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	const storm = 12
	var earlier []chan testkit.Recvd
	for i := 0; i < storm; i++ {
		earlier = append(earlier, f.Incoming)
		f.Connect() // no drop: every older stream is still open when the next one arrives
	}
	for i, in := range earlier {
		deadline := time.After(5 * time.Second)
		for ended := false; !ended; {
			select {
			case m := <-in:
				ended = m.Err != nil
			case <-deadline:
				t.Fatalf("stream %d of the storm was still open after newer ones had connected", i)
			}
		}
	}
	r.hub.Mu.Lock()
	sessions, connected, connects := len(r.hub.Sessions), r.hub.Views[f.ID].Connected, r.hub.Views[f.ID].Link.Connects
	r.hub.Mu.Unlock()
	if sessions != 1 || !connected {
		t.Fatalf("after the storm the hub has %d sessions and connected=%v, want one and true (an old stream ending must not take the live one's place)", sessions, connected)
	}
	if connects != storm+1 {
		t.Fatalf("the hub counted %d connections, want %d", connects, storm+1)
	}
	// The live stream is the last, and it works: its Config is put in force by an echo.
	cfg := f.Configs[len(f.Configs)-1]
	f.Beat(cfg.Digest)
	f.WaitUntil("the last stream's Config to be in force", func() bool { _, applied, _ := f.inForce(); return applied == cfg.Digest })
}

func TestAMessageFromAReplacedStreamChangesNothing(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	cluster := &ikhnosv1.ClusterFacts{Uid: testkit.FP}
	if err := f.Send(testkit.SyncMsg(&ikhnosv1.Sync{Seq: 1, Full: true, Cluster: cluster, Nodes: nodes("a", 4)})); err != nil {
		t.Fatal(err)
	}
	f.WaitUntil("the first picture", func() bool { n, _, _ := held(r, f.ID); return n == 4 })
	old, oldIn := f.Stream, f.Incoming
	f.Connect()
	if err := f.Send(testkit.SyncMsg(&ikhnosv1.Sync{Seq: 2, Full: true, Cluster: cluster, Nodes: nodes("b", 2)})); err != nil {
		t.Fatal(err)
	}
	f.WaitUntil("the new stream's picture", func() bool { n, _, seq := held(r, f.ID); return n == 2 && seq == 2 })
	for ended, deadline := false, time.After(5*time.Second); !ended; {
		select {
		case m := <-oldIn:
			ended = m.Err != nil
		case <-deadline:
			t.Fatal("the replaced stream was never ended")
		}
	}
	// The old stream speaks late, with a bigger picture, a different one and a heartbeat that names no Config. The send may fail or be
	// swallowed by the transport; either way the server holds what the live stream said.
	_ = old.Send(testkit.SyncMsg(&ikhnosv1.Sync{Seq: 9, Full: true, Cluster: cluster, Nodes: nodes("late", 7)}))
	_ = old.Send(heartbeat("late"))
	f.Beat(f.Configs[len(f.Configs)-1].Digest) // a marker on the live stream, handled after anything the old one got in
	f.WaitUntil("the marker", func() bool { _, applied, _ := f.inForce(); return applied != "" })
	time.Sleep(200 * time.Millisecond)
	if n, _, seq := held(r, f.ID); n != 2 || seq != 2 {
		t.Fatalf("a replaced stream changed the picture: %d nodes at seq %d, want 2 at seq 2", n, seq)
	}
	if _, applied, _ := f.inForce(); applied == "late" {
		t.Fatal("a replaced stream's echo was taken for the live stream's")
	}
}

func TestARepeatedHelloCannotChangeWhatTheFirstOneSaid(t *testing.T) {
	r := newHubRig(t)
	first := &ikhnosv1.Hello{AgentVersion: "1.0.0", ProtocolVersion: 1, Capabilities: []string{"config-digest"}}
	f := r.newFakeAgentWith(t, fakeAgentCtx(t), first)
	f.Hello = &ikhnosv1.Hello{AgentVersion: "evil", ProtocolVersion: 9, Capabilities: []string{"diagnostics", "made-up"}, InstalledAccessTier: 4, Namespace: "kube-system", ReleaseName: "other"}
	if err := f.Send(&ikhnosv1.AgentMessage{Msg: &ikhnosv1.AgentMessage_Hello{Hello: f.Hello}}); err != nil {
		t.Fatal(err)
	}
	cfg := f.Configs[0]
	f.Beat(cfg.Digest) // handled after the second hello, on the same stream
	f.WaitUntil("the marker echo", func() bool { _, applied, _ := f.inForce(); return applied == cfg.Digest })
	r.hub.Mu.Lock()
	v := r.hub.Views[f.ID]
	proto, caps, ns, rel := v.Ext.Protocol, append([]string(nil), v.Ext.Caps...), v.Ext.Namespace, v.Ext.ReleaseName
	r.hub.Mu.Unlock()
	if proto != 1 || len(caps) != 1 || caps[0] != "config-digest" {
		t.Fatalf("a second hello changed what the agent said it can do: protocol %d, capabilities %q", proto, caps)
	}
	if ns != "" || rel != "" {
		t.Fatalf("a second hello changed where the agent says it runs: %q %q", ns, rel)
	}
	a, err := r.st.GetAgent(f.Ctx, "org-1", f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Version != "1.0.0" {
		t.Fatalf("a second hello changed the agent's recorded version to %q", a.Version)
	}
}

func TestAnAgentThatTalksBeforeItSaysWhoItIsIsRefused(t *testing.T) {
	r := newHubRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, testkit.FP)
	s := r.agentStreamNoHello(t, ctx, id, key, leaf)
	if err := s.Send(testkit.SyncMsg(&ikhnosv1.Sync{Seq: 1, Full: true, Cluster: &ikhnosv1.ClusterFacts{Uid: testkit.FP}, Nodes: nodes("x", 3)})); err != nil {
		t.Fatal(err)
	}
	_, err := s.Recv()
	if testkit.StatusCode(err) != codes.InvalidArgument {
		t.Fatalf("a sync before the hello ended the stream with %v, want InvalidArgument", err)
	}
	if n, _, _ := held(r, id); n != 0 {
		t.Fatalf("a sync before the hello was applied: %d nodes", n)
	}
}

func TestAnAgentThatSendsTooFastIsCutOffAndCanComeBack(t *testing.T) {
	r := newHubRig(t)
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	for i := 0; i < 200; i++ { // far over the 20 a burst allows, faster than the 120 a minute
		if f.Send(heartbeat("")) != nil {
			break
		}
	}
	err := f.Ended(5 * time.Second)
	if testkit.StatusCode(err) != codes.ResourceExhausted {
		t.Fatalf("a flood ended the stream with %v, want ResourceExhausted", err)
	}
	f.WaitUntil("the server to notice the stream ended", func() bool {
		r.hub.Mu.Lock()
		defer r.hub.Mu.Unlock()
		return r.hub.Sessions[f.ID] == nil
	})
	// Being cut off is for that stream: the agent comes back, is told its Config and is heard.
	cfg := f.Connect()
	f.Beat(cfg.Digest)
	f.WaitUntil("the agent to be heard again", func() bool { _, applied, _ := f.inForce(); return applied == cfg.Digest })
}
