package server

import (
	"fmt"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"

	"google.golang.org/protobuf/proto"
)

// A picture too large for one message arrives as a series of them (Sync.chunk_index, chunk_total, sync_id). The assembly keeps
// the pieces of one such series until the last arrives, and only then is the whole applied, in one step: whoever reads the
// server's state sees the old picture or the new one, never half of one. A connection that ends first takes the partial
// with it (the assembly belongs to the stream), so a dropped connection changes nothing.
//
// What may be buffered is bounded before it is buffered: every piece is counted against the same limits the assembled result
// must meet, so an agent cannot make the server hold more while assembling than it would be allowed to hold after.

// maxChunkTotal is the most messages one picture may be split into (the agent's own bound is the same).
const maxChunkTotal = 4096

// assemblyTTL is how long a picture may take to arrive in full before the pieces are dropped.
const assemblyTTL = 5 * time.Minute

type assembly struct {
	id      string
	total   int
	next    int
	started time.Time
	merged  *continuumv1.Sync

	nodes, namespaces, workloads int
	bytes                        int64
}

// errChunk is a protocol error in a series of chunks (out of order, mixed, too many). It ends the stream.
type errChunk struct{ msg string }

func (e *errChunk) Error() string { return e.msg }

// isChunk says whether a message is one piece of a series.
func isChunk(s *continuumv1.Sync) bool { return s.ChunkTotal > 1 }

// add takes the next piece. It returns the whole picture when this was the last, and an error when the series broke a rule or
// a limit; on error the caller drops the assembly.
func (a *assembly) add(s *continuumv1.Sync, l facts.Limits) (done *continuumv1.Sync, err error) {
	if int(s.ChunkIndex) != a.next || int(s.ChunkTotal) != a.total || s.SyncId != a.id {
		return nil, &errChunk{fmt.Sprintf("chunk %d of %d for %q arrived where chunk %d of %d for %q was expected", s.ChunkIndex, s.ChunkTotal, printable(s.SyncId, 40), a.next, a.total, printable(a.id, 40))}
	}
	if s.Full != a.merged.Full || s.Seq != a.merged.Seq {
		return nil, &errChunk{"the chunks of one picture must share the same seq and full flag"}
	}
	if s.ChunkIndex > 0 && (s.Cluster != nil || len(s.Modules) > 0) {
		return nil, &errChunk{"only the first chunk of a picture carries the cluster facts and the module list"}
	}
	// Count first, buffer after.
	a.nodes += len(s.Nodes)
	a.namespaces += len(s.Namespaces)
	a.workloads += len(s.Workloads)
	for _, n := range s.Nodes {
		a.bytes += int64(proto.Size(n))
	}
	for _, n := range s.Namespaces {
		a.bytes += int64(proto.Size(n))
	}
	for _, w := range s.Workloads {
		a.bytes += int64(proto.Size(w))
	}
	for _, ks := range [][]string{s.DeletedNodes, s.DeletedNamespaces, s.DeletedWorkloads} {
		for _, k := range ks {
			a.bytes += int64(len(k))
		}
	}
	if s.Cluster != nil {
		a.bytes += int64(proto.Size(s.Cluster))
	}
	switch {
	case a.nodes > l.Nodes:
		return nil, &facts.LimitError{What: "nodes", Have: int64(a.nodes), Max: int64(l.Nodes)}
	case a.namespaces > l.Namespaces:
		return nil, &facts.LimitError{What: "namespaces", Have: int64(a.namespaces), Max: int64(l.Namespaces)}
	case a.workloads > l.Workloads:
		return nil, &facts.LimitError{What: "workloads", Have: int64(a.workloads), Max: int64(l.Workloads)}
	case a.bytes > l.Bytes:
		return nil, &facts.LimitError{What: "the total size of its facts (bytes)", Have: a.bytes, Max: l.Bytes}
	}
	m := a.merged
	if s.ChunkIndex == 0 {
		m.Cluster, m.Modules = s.Cluster, s.Modules
	}
	m.Nodes = append(m.Nodes, s.Nodes...)
	m.DeletedNodes = append(m.DeletedNodes, s.DeletedNodes...)
	m.Namespaces = append(m.Namespaces, s.Namespaces...)
	m.DeletedNamespaces = append(m.DeletedNamespaces, s.DeletedNamespaces...)
	m.Workloads = append(m.Workloads, s.Workloads...)
	m.DeletedWorkloads = append(m.DeletedWorkloads, s.DeletedWorkloads...)
	a.next++
	if a.next < a.total {
		return nil, nil
	}
	return m, nil
}

// newAssembly starts a series from its first piece, or says why it cannot be one.
func newAssembly(s *continuumv1.Sync, now time.Time) (*assembly, error) {
	switch {
	case s.ChunkTotal > maxChunkTotal:
		return nil, &errChunk{fmt.Sprintf("a picture may be split into at most %d messages (this one says %d)", maxChunkTotal, s.ChunkTotal)}
	case s.ChunkIndex != 0:
		return nil, &errChunk{fmt.Sprintf("chunk %d arrived with no chunk 0 before it (the connection may have dropped mid-picture)", s.ChunkIndex)}
	case s.SyncId == "" || len(s.SyncId) > 64:
		return nil, &errChunk{"a chunked picture needs a sync_id of at most 64 characters"}
	}
	return &assembly{id: s.SyncId, total: int(s.ChunkTotal), started: now, merged: &continuumv1.Sync{Seq: s.Seq, Full: s.Full}}, nil
}

func (a *assembly) expired(now time.Time) bool { return now.Sub(a.started) > assemblyTTL }
