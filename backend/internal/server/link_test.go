package server

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	continuumv1 "continuum/gen/continuumv1"
)

// TestLinkStatsFlowBytesCountsOnlyFlowMessages covers flowBytes: the same "as encoded on the wire"
// measure bytes uses, but counting only AgentMessage_Flows messages - what sampleSelfStats sums across
// every agent to derive the server's own self-telemetry flow-ingestion rate (see
// ServerSelfStatsSample.FlowIngestBytesPerSec's own doc comment).
func TestLinkStatsFlowBytesCountsOnlyFlowMessages(t *testing.T) {
	var l linkStats
	at := time.Unix(2000, 0)
	flow1 := &continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Flows{Flows: &continuumv1.FlowBatch{Seq: 1, Flows: []*continuumv1.Flow{{}, {}}}}}
	flow2 := &continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Flows{Flows: &continuumv1.FlowBatch{Seq: 2}}}
	beat := &continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Heartbeat{Heartbeat: &continuumv1.Heartbeat{}}}
	l.note(flow1, at)
	l.note(beat, at)
	l.note(flow2, at)
	want := int64(proto.Size(flow1)) + int64(proto.Size(flow2))
	if l.flowBytes != want {
		t.Fatalf("flowBytes = %d, want %d (the two flow messages only, not the heartbeat)", l.flowBytes, want)
	}
	if l.bytes != want+int64(proto.Size(beat)) {
		t.Fatalf("bytes = %d, want every message counted", l.bytes)
	}
}

// The agent page shows what each agent has sent; the gauge must count every message once, by kind, and
// measure it as encoded on the wire.
func TestLinkStatsCountEachMessageByKind(t *testing.T) {
	var l linkStats
	at := time.Unix(1000, 0)
	msgs := []*continuumv1.AgentMessage{
		{Msg: &continuumv1.AgentMessage_Heartbeat{Heartbeat: &continuumv1.Heartbeat{}}},
		{Msg: &continuumv1.AgentMessage_Heartbeat{Heartbeat: &continuumv1.Heartbeat{}}},
		{Msg: &continuumv1.AgentMessage_Sync{Sync: &continuumv1.Sync{}}},
	}
	var want int64
	for i, m := range msgs {
		l.note(m, at.Add(time.Duration(i)*time.Second))
		want += int64(proto.Size(m))
	}
	if l.beats != 2 || l.syncs != 1 || l.flows != 0 || l.meas != 0 {
		t.Fatalf("counts wrong: %+v", l)
	}
	if l.bytes != want {
		t.Fatalf("bytes = %d, want %d", l.bytes, want)
	}
	if !l.lastSync.Equal(at.Add(2*time.Second)) || !l.lastAny.Equal(at.Add(2*time.Second)) {
		t.Fatalf("timestamps wrong: %+v", l)
	}
}
