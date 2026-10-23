package agents

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
)

// TestLinkStatsFlowBytesCountsOnlyFlowMessages covers flowBytes: the same "as encoded on the wire"
// measure bytes uses, but counting only AgentMessage_Flows messages - what sampleSelfStats sums across
// every agent to derive the server's own self-telemetry flow-ingestion rate (see
// ServerSelfStatsSample.FlowIngestBytesPerSec's own doc comment).
func TestLinkStatsFlowBytesCountsOnlyFlowMessages(t *testing.T) {
	var l linkStats
	at := time.Unix(2000, 0)
	flow1 := &ikhnosv1.AgentMessage{Msg: &ikhnosv1.AgentMessage_Flows{Flows: &ikhnosv1.FlowBatch{Seq: 1, Flows: []*ikhnosv1.Flow{{}, {}}}}}
	flow2 := &ikhnosv1.AgentMessage{Msg: &ikhnosv1.AgentMessage_Flows{Flows: &ikhnosv1.FlowBatch{Seq: 2}}}
	beat := &ikhnosv1.AgentMessage{Msg: &ikhnosv1.AgentMessage_Heartbeat{Heartbeat: &ikhnosv1.Heartbeat{}}}
	l.Note(flow1, at)
	l.Note(beat, at)
	l.Note(flow2, at)
	want := int64(proto.Size(flow1)) + int64(proto.Size(flow2))
	if l.FlowBytes != want {
		t.Fatalf("flowBytes = %d, want %d (the two flow messages only, not the heartbeat)", l.FlowBytes, want)
	}
	if l.Bytes != want+int64(proto.Size(beat)) {
		t.Fatalf("bytes = %d, want every message counted", l.Bytes)
	}
}

// The agent page shows what each agent has sent; the gauge must count every message once, by kind, and
// measure it as encoded on the wire.
func TestLinkStatsCountEachMessageByKind(t *testing.T) {
	var l linkStats
	at := time.Unix(1000, 0)
	msgs := []*ikhnosv1.AgentMessage{
		{Msg: &ikhnosv1.AgentMessage_Heartbeat{Heartbeat: &ikhnosv1.Heartbeat{}}},
		{Msg: &ikhnosv1.AgentMessage_Heartbeat{Heartbeat: &ikhnosv1.Heartbeat{}}},
		{Msg: &ikhnosv1.AgentMessage_Sync{Sync: &ikhnosv1.Sync{}}},
	}
	var want int64
	for i, m := range msgs {
		l.Note(m, at.Add(time.Duration(i)*time.Second))
		want += int64(proto.Size(m))
	}
	if l.Beats != 2 || l.Syncs != 1 || l.Flows != 0 || l.Meas != 0 {
		t.Fatalf("counts wrong: %+v", l)
	}
	if l.Bytes != want {
		t.Fatalf("bytes = %d, want %d", l.Bytes, want)
	}
	if !l.LastSync.Equal(at.Add(2*time.Second)) || !l.lastAny.Equal(at.Add(2*time.Second)) {
		t.Fatalf("timestamps wrong: %+v", l)
	}
}
