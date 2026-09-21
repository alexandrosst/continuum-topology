package agent

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agent/collect"
	"continuum/internal/measure"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// errReconfigure ends a stream on purpose so that the next one starts with a different collector (a changed tier). It is
// not a failure: Run reconnects at once and says nothing about an error.
var errReconfigure = errors.New("reconnecting to apply a changed access tier")

// ChunkBytes is the encoded size after which a picture is split into another message. The server refuses a single message
// over 16 MiB and checks its limits on what it assembled, so a very large cluster is sent as a series of about a megabyte each.
const ChunkBytes = 1 << 20

// maxChunks is the most messages one picture may be split into; the server refuses more than this.
const maxChunks = 4096

// splitSync cuts a Sync into messages of about `limit` encoded bytes, in order. The cluster facts and module list go in the
// first. One message when it is small enough; then chunk_total stays 0 and it is an ordinary sync.
func splitSync(s *continuumv1.Sync, limit int) []*continuumv1.Sync {
	if limit <= 0 || proto.Size(s) <= limit {
		return []*continuumv1.Sync{s}
	}
	var chunks []*continuumv1.Sync
	cur, size := &continuumv1.Sync{Seq: s.Seq, Full: s.Full, Cluster: s.Cluster, Modules: s.Modules}, 0
	if s.Cluster != nil {
		size += proto.Size(s.Cluster) + 4
	}
	for _, m := range s.Modules {
		size += proto.Size(m) + 4
	}
	room := func(n int) {
		if size > 0 && size+n > limit {
			chunks = append(chunks, cur)
			cur, size = &continuumv1.Sync{Seq: s.Seq, Full: s.Full}, 0
		}
		size += n
	}
	for _, n := range s.Nodes {
		room(proto.Size(n) + 4)
		cur.Nodes = append(cur.Nodes, n)
	}
	for _, n := range s.Namespaces {
		room(proto.Size(n) + 4)
		cur.Namespaces = append(cur.Namespaces, n)
	}
	for _, w := range s.Workloads {
		room(proto.Size(w) + 4)
		cur.Workloads = append(cur.Workloads, w)
	}
	for _, k := range s.DeletedNodes {
		room(len(k) + 4)
		cur.DeletedNodes = append(cur.DeletedNodes, k)
	}
	for _, k := range s.DeletedNamespaces {
		room(len(k) + 4)
		cur.DeletedNamespaces = append(cur.DeletedNamespaces, k)
	}
	for _, k := range s.DeletedWorkloads {
		room(len(k) + 4)
		cur.DeletedWorkloads = append(cur.DeletedWorkloads, k)
	}
	chunks = append(chunks, cur)
	if len(chunks) == 1 {
		return chunks
	}
	var id [6]byte
	_, _ = rand.Read(id[:])
	syncID := fmt.Sprintf("%d-%s", s.Seq, hex.EncodeToString(id[:]))
	for i, c := range chunks {
		c.ChunkIndex, c.ChunkTotal, c.SyncId = uint32(i), uint32(len(chunks)), syncID
	}
	return chunks
}

// ---- overrides ----

// overrides is what the agent will actually do after reading a Config: the server's wishes cut down to what the install
// allows. The server may only narrow: a tier above the ceiling the chart set is ignored, and so is anything the agent
// cannot honour, and each thing ignored is said in a problem (override_ignored) instead of being silently dropped.
type overrides struct {
	tier     int             // the tier to collect at: the lower of what the server approved and what the chart installed
	approved int             // what the server said
	paused   map[string]bool // optional collectors to stop
	excluded []string        // namespaces to leave out on top of the agent's own scope
	ignored  []string        // what was not applied, and why
}

func resolveOverrides(c *continuumv1.Config, installedTier int) overrides {
	o := overrides{approved: int(c.ApprovedAccessTier), paused: map[string]bool{}}
	o.tier = min(o.approved, installedTier)
	if o.approved > installedTier {
		o.ignored = append(o.ignored, fmt.Sprintf("the server approved access tier %d, above the tier %d this install allows (access.tier); the agent stays at tier %d", o.approved, installedTier, installedTier))
	}
	for _, name := range c.PausedCollectors {
		known := false
		for _, k := range CollectorNames {
			known = known || k == name
		}
		if !known {
			o.ignored = append(o.ignored, fmt.Sprintf("the server asked to pause %q, which is not a collector this agent has", clipMsg(name, 40)))
			continue
		}
		o.paused[name] = true
	}
	seen := map[string]bool{}
	for _, ns := range c.ExcludedNamespaces {
		if ok, why := collect.ValidExclusion(ns); !ok {
			o.ignored = append(o.ignored, fmt.Sprintf("the server asked to leave out namespace %q, which %s", clipMsg(ns, 64), why))
			continue
		}
		if !seen[ns] {
			seen[ns] = true
			o.excluded = append(o.excluded, ns)
		}
	}
	sort.Strings(o.excluded)
	return o
}

// enforce puts the overrides in force and records what is in force, and what was ignored.
func (r *runner) enforce(o overrides) {
	if r.cfg.Probes != nil {
		r.cfg.Probes.SetPaused(o.paused[collectorProbes])
	}
	if r.cfg.Flows != nil {
		r.cfg.Flows.SetPaused(o.paused[collectorFlow])
	}
	r.mu.Lock()
	c := r.collector
	r.mu.Unlock()
	if c != nil {
		c.SetExtraExclude(o.excluded)
	}
	now := time.Now()
	r.dg.mu.Lock()
	r.dg.approved = o.approved
	for _, name := range CollectorNames {
		if r.dg.paused[name] && !o.paused[name] {
			r.dg.enabled[name] = now // resumed: the clock for "silent" starts again
		}
	}
	r.dg.paused, r.dg.excluded = o.paused, o.excluded
	r.dg.mu.Unlock()
	if len(o.ignored) > 0 {
		r.probs.raise("override", CodeOverrideIgnored, continuumv1.Problem_INFO, "Part of what the server pushed was not applied, because it would give the agent more than this install allows or is not something it can do: "+strings.Join(o.ignored, "; ")+". Only `helm upgrade --reuse-values` can widen what the agent may do.", 0)
	} else {
		r.probs.clear("override")
	}
}

// ---- recovering from a panic ----

// safely runs fn and turns a panic into a logged stack and a problem instead of the end of the process. Use this
// only for a task whose goroutine quietly ending after a panic is genuinely harmless - fire-and-forget work with
// nothing downstream waiting on it. Anything else needs safelyReconnect instead: recovering the panic without
// telling anyone the goroutine is now gone is how a background task can silently stop doing its job forever
// while the agent still looks healthy (see stream()'s callers of safelyReconnect for three real examples this
// project shipped before that mistake was caught).
func (r *runner) safely(name string, fn func()) {
	defer func() {
		if p := recover(); p != nil {
			r.panicked(name, p)
		}
	}()
	fn()
}

// safelyReconnect is safely, except a panic also pushes a non-nil error onto errs (non-blocking, so a channel
// already holding one error is left alone) instead of just being logged. Use it for a background task backing
// a connection where the task dying silently would leave the connection running in some degraded way with no
// external sign of it - errs is normally stream()'s recvErr, whose only other writer is a real Recv() error, so
// pushing into it here reuses the exact path the main select loop already returns on and reconnects from.
func (r *runner) safelyReconnect(name string, fn func(), errs chan<- error) {
	defer func() {
		if p := recover(); p != nil {
			r.panicked(name, p)
			select {
			case errs <- fmt.Errorf("internal error in %s (reconnecting): %v", name, p):
			default:
			}
		}
	}()
	fn()
}

func (r *runner) panicked(name string, p any) {
	r.log.Error("internal error: a task panicked and was restarted", "task", name, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
	r.probs.raise("internal:"+name, CodeInternalError, continuumv1.Problem_ERROR,
		fmt.Sprintf("An internal error stopped the agent's %q task (%s). The agent restarted it and carries on; the stack is in the agent's log (kubectl -n continuum-system logs deploy/continuum-agent). Please report it.", name, clipMsg(fmt.Sprint(p), 120)), internalErrorRemembered)
	r.panics.Add(1)
}

// ---- what a refused connection says ----

// noteStreamError turns what the server said when it ended a stream into a problem the operator can read on the Agents
// page: a picture over the server's limits, or a message it refused. The server cannot tell an agent more than the status
// message, so this is where that message is kept until a later picture is accepted.
func (r *runner) noteStreamError(err error) {
	st, ok := status.FromError(err)
	if !ok {
		return
	}
	switch st.Code() {
	case codes.ResourceExhausted, codes.InvalidArgument:
		msg := clipMsg(st.Message(), 300)
		if strings.Contains(msg, "limit for") || strings.Contains(msg, "may carry at most") {
			r.probs.raise("sync", CodeSyncTooLarge, continuumv1.Problem_ERROR, "The server refused this agent's picture of the cluster because it is larger than the server accepts ("+msg+"). Nothing new is arriving until it fits: narrow what the agent reports with scope.namespaces or scope.exclude (`helm upgrade --reuse-values`), or ask whoever runs the server to raise its limits.", 0)
		} else {
			r.probs.raise("sync", CodeServerLimitsRefused, continuumv1.Problem_ERROR, "The server refused an update from this agent: "+msg+". The agent retries; if it keeps happening, look at the server's log.", 0)
		}
	}
}

// ---- the stream ----

func (r *runner) stream(ctx context.Context, id *Identity) error {
	conn, err := r.dial(id)
	if err != nil {
		return err
	}
	defer conn.Close()
	var bg sync.WaitGroup
	defer bg.Wait() // runs after cancel: a renewal that already has its certificate finishes saving it before the process may exit
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The stream is not cut off the moment shutdown is asked for: it lives on its own context so that a goodbye can still be sent.
	sctx, cancelStream := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelStream()

	// Renew at half the certificate's life; after renewing, reconnect to use the new certificate.
	leaf, err := x509.ParseCertificate(id.CertDER)
	if err != nil {
		return err
	}
	renewAt := time.Now().Add(time.Until(leaf.NotAfter) / 2) // half of what remains
	renewed := make(chan struct{})
	// Declared here, ahead of every goroutine below that can report a fatal error for this connection (including
	// the certificate-renewal one just below), so each of them can push into recvErr on an unrecoverable failure
	// and let the main select loop's existing `case err := <-recvErr: return err` do one honest thing: end this
	// connection and let Run's outer loop (run.go) reconnect with backoff, rather than each goroutine silently
	// going quiet in its own different way.
	srv := make(chan *continuumv1.ServerMessage)
	recvErr := make(chan error, 1)
	bg.Add(1)
	go func() {
		defer bg.Done()
		// A dead renewal goroutine would leave this connection running indefinitely on a certificate that will
		// eventually expire without anyone trying to renew it again - forcing a reconnect on panic gives a fresh
		// renewLoop its own chance next time (and once() already checks for an expired certificate on every
		// reconnect and rejoins if needed, so this is not the only safety net, but it should not be the only one
		// relied on either).
		r.safelyReconnect("certificate renewal", func() { r.renewLoop(ctx, conn, id, renewAt, renewed) }, recvErr)
	}()

	s, err := continuumv1.NewAgentServiceClient(conn).Connect(sctx)
	if err != nil {
		return err
	}
	// Hello says what this agent is and what its install allows: the ceiling, before anything else is known.
	if err := s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Hello{Hello: &continuumv1.Hello{
		AgentVersion: r.cfg.Version, InstalledAccessTier: uint32(r.cfg.Tier), Diagnostics: r.diagnostics(), Namespace: r.cfg.Namespace, ReleaseName: r.cfg.ReleaseName}}}); err != nil {
		return err
	}
	bg.Add(1)
	go func() {
		defer bg.Done()
		// A dead receive-loop goroutine would leave the main select loop below waiting on srv/recvErr forever,
		// heartbeating as if nothing were wrong while never again seeing a Config or Revoked message from the
		// server for the rest of this connection - deaf, not disconnected, and with no external sign of it.
		// safelyReconnect turns a panic here into exactly what a real Recv() error already does.
		r.safelyReconnect("receive loop", func() {
			for {
				m, err := s.Recv()
				if err != nil {
					recvErr <- err
					return
				}
				select {
				case srv <- m:
				case <-sctx.Done():
					return
				}
			}
		}, recvErr)
	}()
	// sendErr gives the reason the server ended the stream when a send fails: the send only says EOF, and what the
	// server said (a refused message, a revocation) is what the operator needs.
	sendErr := func(err error) error {
		if err != nil && errors.Is(err, io.EOF) {
			select {
			case e := <-recvErr:
				return e
			case <-time.After(time.Second):
			}
		}
		return err
	}
	send := func(m *continuumv1.AgentMessage) error { return sendErr(s.Send(m)) }

	var cfg *continuumv1.Config
	for cfg == nil {
		select {
		case m := <-srv:
			if rv := m.GetRevoked(); rv != nil {
				return r.revoked(ctx, rv.Reason)
			}
			cfg = m.GetConfig()
		case err := <-recvErr:
			return err
		case <-ctx.Done():
			_ = s.CloseSend()
			return ctx.Err()
		case <-time.After(20 * time.Second):
			return errors.New("server did not answer the hello")
		}
	}
	r.noteServerTime(cfg.ServerTimeUnix)
	ov := resolveOverrides(cfg, r.cfg.Tier)
	tier := ov.tier
	if err := r.ensureCollector(tier); err != nil {
		return err
	}
	r.enforce(ov)
	r.log.Info("streaming", "approved_tier", cfg.ApprovedAccessTier, "collecting_tier", tier, "paused", len(ov.paused), "extra_excluded_namespaces", len(ov.excluded))
	r.cfg.Health.Connected()

	var seq, fullSeq uint64
	var prev *continuumv1.Sync
	chunkBytes := r.cfg.ChunkBytes
	if chunkBytes == 0 {
		chunkBytes = ChunkBytes
	}
	sendSync := func(out *continuumv1.Sync) error {
		msgs := splitSync(out, chunkBytes)
		if len(msgs) > maxChunks {
			return fmt.Errorf("the picture of this cluster is too large to send even in pieces (%d messages)", len(msgs))
		}
		for _, m := range msgs {
			if err := send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Sync{Sync: m}}); err != nil {
				return err
			}
		}
		if out.Full {
			fullSeq = out.Seq
		}
		return nil
	}
	// A picture is sent only once every watch has either read its kind or been refused. Until then the agent would send
	// half of one, and a full picture replaces what the server holds.
	held := func() bool { return !r.collector.Ready() }
	sendDiff := func(full bool) error {
		if held() {
			return nil
		}
		cur := r.snapshot(ctx)
		out := cur
		if !full && prev != nil {
			out = diff(prev, cur)
			if out == nil {
				return nil
			}
		}
		seq++
		out.Seq = seq
		prev = cur
		return sendSync(out)
	}
	// verify sends the same picture twice over: the changes since the last message first, then everything.
	// The server has applied every change before the full picture arrives, so any difference it finds
	// between the two is a change it missed, not one still in flight.
	verify := func() error {
		if held() {
			return nil
		}
		cur := r.snapshot(ctx)
		if prev != nil {
			if out := diff(prev, cur); out != nil {
				seq++
				out.Seq = seq
				if err := sendSync(out); err != nil {
					return err
				}
			}
		}
		seq++
		cur.Seq = seq
		prev = cur
		return sendSync(cur)
	}
	beatMsg := func(force bool) *continuumv1.AgentMessage {
		return &continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Heartbeat{Heartbeat: &continuumv1.Heartbeat{
			Modules: r.collector.Modules(), ClockSkewMs: r.clockSkewMs(), Diagnostics: r.diagToSend(force)}}}
	}
	if err := sendDiff(true); err != nil {
		return err
	}
	// Tell the server the clock skew and the full diagnostics straight away rather than after the first heartbeat interval.
	if err := send(beatMsg(true)); err != nil {
		return err
	}
	beat := time.NewTicker(r.cfg.Floors.beat(time.Duration(cfg.HeartbeatSeconds) * time.Second))
	defer beat.Stop()
	// A change in what the agent would tell the operator about itself (a problem raised, a collector paused) is sent within a
	// few seconds, not at the next heartbeat, which may be half a minute away. Nothing is sent when nothing changed.
	checkEvery := diagCheckEvery
	if f := r.cfg.Floors.Beat; f > 0 {
		checkEvery = f
	}
	diagCheck := time.NewTicker(checkEvery)
	defer diagCheck.Stop()
	resyncEvery := func(c *continuumv1.Config) time.Duration {
		if c != nil && c.ResyncSeconds > 0 {
			return r.cfg.Floors.resync(time.Duration(c.ResyncSeconds) * time.Second)
		}
		return r.cfg.Resync
	}
	resyncPeriod := resyncEvery(cfg)
	resync := time.NewTicker(resyncPeriod)
	defer resync.Stop()

	// Measurements: only when this agent was started with them allowed and the server named targets. A first
	// round runs a few seconds after the targets arrive, then every MeasureSeconds. The server may pause them.
	var targets []measure.Target
	var measureTicker *time.Ticker
	var measureTick <-chan time.Time
	var measureFirst <-chan time.Time
	measurePaused := false
	measureBusy := false
	measured := make(chan *continuumv1.Measurements, 1)
	defer func() {
		if measureTicker != nil {
			measureTicker.Stop()
		}
	}()
	startRound := func() {
		if measureBusy || len(targets) == 0 {
			return
		}
		measureBusy = true
		ts := append([]measure.Target(nil), targets...)
		bg.Add(1)
		go func() {
			defer bg.Done()
			// r.safely alone would recover a panic here and let this goroutine quietly exit without ever sending
			// to measured - and since measureBusy is only ever cleared by the main loop's `case m := <-measured`,
			// one panicked round would leave it stuck true forever, silently disabling every future round for the
			// rest of this connection (startRound's `if measureBusy ... return` would refuse to start another).
			// Sending a failed-everything result on the recovered path is what actually unsticks it.
			defer func() {
				if p := recover(); p != nil {
					r.panicked("connection timing", p)
					select {
					case measured <- &continuumv1.Measurements{Refused: uint32(len(ts))}:
					case <-ctx.Done():
					}
				}
			}()
			res, refused := measure.Round(ctx, ts, r.cfg.MeasureDial, nil)
			m := &continuumv1.Measurements{Refused: uint32(refused)}
			for _, x := range res {
				m.Results = append(m.Results, &continuumv1.PathResult{TargetId: x.ID, Samples: uint32(x.Samples), Failed: uint32(x.Failed), RttMinMs: x.Min, RttP50Ms: x.P50, RttP95Ms: x.P95})
			}
			select {
			case measured <- m:
			case <-ctx.Done():
			}
		}()
	}
	applyConfig := func(c *continuumv1.Config, o overrides) {
		if p := resyncEvery(c); p != resyncPeriod {
			resyncPeriod = p
			resync.Reset(p)
		}
		changed := len(c.ProbeTargets) != len(targets) || o.paused[collectorMeasure] != measurePaused
		next := make([]measure.Target, 0, len(c.ProbeTargets))
		for i, t := range c.ProbeTargets {
			nt := measure.Target{ID: t.Id, Host: t.Host, Port: int(t.Port)}
			if i >= len(targets) || targets[i] != nt {
				changed = true
			}
			next = append(next, nt)
		}
		targets, measurePaused = next, o.paused[collectorMeasure]
		if !changed && measureTicker != nil {
			return
		}
		if measureTicker != nil {
			measureTicker.Stop()
			measureTicker, measureTick, measureFirst = nil, nil, nil
		}
		every := time.Duration(0)
		if r.cfg.Measure && !measurePaused && c.MeasureSeconds > 0 && len(targets) > 0 {
			every = r.cfg.Floors.measure(max(time.Duration(c.MeasureSeconds)*time.Second, measure.MinInterval))
			measureTicker = time.NewTicker(every)
			measureTick = measureTicker.C
			measureFirst = time.After(3 * time.Second)
		}
		r.dg.mu.Lock()
		if len(targets) != r.dg.targets || (every > 0 && r.dg.measureEvery == 0) {
			r.dg.targetsSince = time.Now()
		}
		r.dg.targets, r.dg.measureEvery = len(targets), every
		r.dg.mu.Unlock()
	}
	applyConfig(cfg, ov)
	// Observed traffic is workload-level information, so it is only collected and sent when the
	// effective access covers workloads (tier 2). Below that, reports are read and dropped. The server may
	// pause it: the pipeline then discards what arrives and Flush has nothing to send.
	var flowTick <-chan time.Time
	if r.cfg.Flows != nil && tier >= 2 {
		t := time.NewTicker(r.cfg.FlowWindow)
		defer t.Stop()
		flowTick = t.C
		r.cfg.Flows.Aggregator.Flush() // start the first window now
	}
	var debounce <-chan time.Time
	alive := time.NewTicker(30 * time.Second) // proof for the liveness probe that this loop is turning
	defer alive.Stop()
	for {
		select {
		case <-alive.C:
			r.cfg.Health.Beat()
		case <-ctx.Done():
			// Say goodbye (one last heartbeat, so the server sees a clean end and the last diagnostics), close our side,
			// and give the server a moment to close its own. Never longer: the process is on its way out.
			_ = s.Send(beatMsg(true))
			_ = s.CloseSend()
			select {
			case <-recvErr:
			case <-time.After(1500 * time.Millisecond):
			}
			return ctx.Err()
		case err := <-recvErr:
			return err
		case m := <-srv:
			if rv := m.GetRevoked(); rv != nil {
				return r.revoked(ctx, rv.Reason)
			}
			if a := m.GetAck(); a != nil && a.Seq >= fullSeq && fullSeq > 0 {
				r.probs.clear("sync") // a full picture was accepted: whatever the server refused before is over
			}
			if c := m.GetConfig(); c != nil {
				r.noteServerTime(c.ServerTimeUnix)
				o := resolveOverrides(c, r.cfg.Tier)
				if o.tier != tier {
					return errReconfigure
				}
				r.enforce(o)
				applyConfig(c, o)
				if err := send(beatMsg(false)); err != nil { // says what is now in force, if that changed
					return err
				}
			}
		case <-r.collectorChanges():
			if debounce == nil {
				debounce = time.After(r.cfg.Debounce)
			}
		case <-r.cfg.Probes.Changes():
			if debounce == nil {
				debounce = time.After(r.cfg.Debounce)
			}
		case <-debounce:
			debounce = nil
			if err := sendDiff(false); err != nil {
				return err
			}
		case <-diagCheck.C:
			if d := r.diagToSend(false); d != nil {
				if err := send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Heartbeat{Heartbeat: &continuumv1.Heartbeat{
					Modules: r.collector.Modules(), ClockSkewMs: r.clockSkewMs(), Diagnostics: d}}}); err != nil {
					return err
				}
			}
		case <-beat.C:
			if prev == nil { // the picture was held back until every watch had read its kind
				if err := sendDiff(true); err != nil {
					return err
				}
			}
			if err := send(beatMsg(false)); err != nil {
				return err
			}
		case <-resync.C:
			if err := verify(); err != nil {
				return err
			}
		case <-measureFirst:
			measureFirst = nil
			startRound()
		case <-measureTick:
			startRound()
		case m := <-measured:
			measureBusy = false
			if measurePaused {
				continue // a round that was already running when the server asked to stop is thrown away
			}
			r.dg.mu.Lock()
			r.dg.lastRound, r.dg.lastResults = time.Now(), len(m.Results)
			r.dg.mu.Unlock()
			if err := send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Measurements{Measurements: m}}); err != nil {
				return err
			}
		case <-flowTick:
			if b := r.cfg.Flows.Aggregator.Flush(); b != nil {
				if err := send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Flows{Flows: b}}); err != nil {
					return err
				}
			}
		case <-renewed:
			return nil // reconnect with the new certificate
		}
	}
}

// probeIdentity checks, once the agent is connected, that it can write its identity where it keeps it. A certificate is
// renewed twice a day, and a renewal whose result cannot be stored ends with an agent that loses its identity at the next
// expiry; better to say so now than at three in the morning.
func (r *runner) probeIdentity(ctx context.Context, id *Identity) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := r.cfg.Identity.Save(sctx, id); err != nil {
		r.identityUnwritable(err)
		return
	}
	r.probs.clear("identity")
	r.identityOK.Store(true)
}

func (r *runner) identityUnwritable(err error) {
	r.log.Error("cannot write the agent's identity", "err", err)
	r.probs.raise("identity", CodeIdentityUnwritable, continuumv1.Problem_ERROR, "The agent cannot write its identity (the Secret it keeps its key and certificate in): "+clipMsg(err.Error(), 200)+". It works now, but the certificate it renews every day cannot be saved, so it will lose its identity when the current one expires. Check the Role that grants it update on that one Secret (`kubectl -n continuum-system get role,rolebinding`) and restore it with `helm upgrade --reuse-values`.", 0)
}
