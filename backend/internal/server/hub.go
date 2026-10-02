package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/geoip"
	"continuum/internal/interpret"
	"continuum/internal/model"
	"continuum/internal/store"
	"continuum/internal/twin"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	HeartbeatSeconds = 30
	snapshotEvery    = 30 * time.Second
)

// view is what the server currently knows about one agent's cluster.
type view struct {
	state     *facts.State
	lastSync  time.Time
	lastBeat  time.Time
	dirtyAt   time.Time
	savedAt   time.Time
	connected bool

	flows      *flowTable
	obs        observerHealth
	flowsDirty bool
	flowsSaved time.Time

	// Measured network paths, keyed by the target id the server issued for them.
	paths   map[string]*pathTrack
	targets map[string]issuedTarget
	// cfgHash identifies the last Config pushed on the stream, so a new one is sent only when it differs.
	cfgHash string

	// What this agent has sent since the server started (lost on restart; not history, only a gauge of the link).
	link linkStats

	// The last consistency check: a full picture compared with what the server held.
	consAt    time.Time
	consDrift facts.Drift
	consCount int

	// The last message refused for being over a limit or malformed, until a later one is accepted. It is
	// shown with the agent so its operator can see why nothing new arrives; refusalLogged limits how
	// often the same problem is written to the audit trail and the event list.
	refusal       string
	refusalLogged time.Time

	// skewMs is the clock skew the agent last reported (its clock minus the server's); skewKnown says it was reported.
	skewMs    int64
	skewKnown bool

	// ext is the agent's account of itself, the overrides pushed to it and the pieces of a picture still arriving.
	ext viewExt
}

func newView() *view {
	return &view{state: facts.New(), flows: newFlowTable(), paths: map[string]*pathTrack{}, targets: map[string]issuedTarget{}}
}

// linkStats counts what one agent has sent over its stream.
type linkStats struct {
	since                        time.Time // when the current connection began
	connects                     int       // connections accepted since the server started
	bytes                        int64     // size of the messages received, as encoded on the wire (before gRPC framing and TLS)
	syncs, flows, meas, beats    int
	lastSync, lastFlows, lastAny time.Time
}

func (l *linkStats) note(m *continuumv1.AgentMessage, at time.Time) {
	l.bytes += int64(proto.Size(m))
	l.lastAny = at
	switch m.Msg.(type) {
	case *continuumv1.AgentMessage_Sync:
		l.syncs++
		l.lastSync = at
	case *continuumv1.AgentMessage_Flows:
		l.flows++
		l.lastFlows = at
	case *continuumv1.AgentMessage_Measurements:
		l.meas++
	case *continuumv1.AgentMessage_Heartbeat:
		l.beats++
	}
}

type endReason struct {
	text    string
	revoked bool
}

type session struct {
	cancel func(endReason)
	// push carries messages for the stream's own goroutine to send (only it may write to the stream).
	push chan *continuumv1.ServerMessage
}

// Hub receives agent streams, keeps their facts, and builds the topology the UI reads.
type Hub struct {
	*BaseAgentService
	C   *Core
	Log *slog.Logger

	// FlowStaleAfter, when set, overrides the setting: an observed edge unseen for this long is shown as
	// stale, never deleted.
	FlowStaleAfter time.Duration

	// ConsistencyEvery, when set, overrides the consistency-check setting (tests use seconds).
	ConsistencyEvery time.Duration

	// Geo suggests a location for each agent's connecting address; nil when no database is configured.
	Geo *Geo

	// Limits, when set, replaces the default caps on what one agent may make the server hold (tests use small ones).
	Limits *facts.Limits

	// TombstoneRetention is how long a record that disappeared stays visible as "gone" (0: seven days).
	TombstoneRetention time.Duration

	mu       sync.Mutex
	views    map[string]*view
	sessions map[string]*session
	rec      *recorder
	tw       *twinRT
}

func NewHub(c *Core) *Hub {
	h := &Hub{BaseAgentService: &BaseAgentService{C: c}, C: c, Log: c.Log, views: map[string]*view{}, sessions: map[string]*session{}}
	h.rec = newRecorder(h)
	h.tw = newTwinRT()
	c.OnWorkspace = h.workspaceChanged
	c.OnRevoke = h.Drop
	c.OnSettings = h.settingsChanged
	return h
}

// Drop ends a live stream immediately (revocation).
func (h *Hub) Drop(agentID string) {
	h.tw.gen.Add(1) // the agent's records change state at once: revoked
	h.mu.Lock()
	s := h.sessions[agentID]
	h.mu.Unlock()
	if s != nil {
		// The agent is told why, in the words the administrator gave, so its operator can read it in the pod's log.
		text := "revoked by an administrator"
		if a, err := h.C.Store.GetAgent(bg(), agentID); err == nil && a.Reason != "" {
			text += ": " + a.Reason
		}
		s.cancel(endReason{text, true})
	}
}

// Restore reloads the last persisted facts so the UI has data right after a server restart.
func (h *Hub) Restore(ctx context.Context) {
	agents, err := h.C.Store.ListAgents(ctx, h.C.OrgID)
	if err != nil {
		h.Log.Error("restore failed", "err", err)
		return
	}
	h.twinLoad(ctx)
	for _, a := range agents {
		// A revoked agent's last picture is kept for the retention window, shown as revoked, never as a target.
		if a.Status != store.StatusApproved && !h.revokedWithinRetention(a) {
			continue
		}
		data, at, err := h.C.Store.LoadSnapshot(ctx, a.ID)
		if err != nil {
			continue
		}
		// What was stored is not trusted more than what arrives: a snapshot that breaks the limits (an older
		// version, a poisoned database, a hand edit) is dropped, and the agent's next full picture replaces it.
		st, err := loadState(data, h.limits())
		if err != nil {
			h.Log.Warn("stored snapshot dropped", "agent", a.ID, "err", err)
			continue
		}
		v := newView()
		v.state, v.lastSync, v.lastBeat, v.savedAt = st, at, at, at
		if fdata, fat, err := h.C.Store.LoadSnapshot(ctx, flowsKey(a.ID)); err == nil {
			if ft, err := loadFlowTable(fdata); err == nil {
				v.flows, v.flowsSaved = ft, fat
			} else {
				h.Log.Warn("stored flow table dropped", "agent", a.ID, "err", err)
			}
		}
		h.mu.Lock()
		h.views[a.ID] = v
		h.mu.Unlock()
	}
	// Overrides an administrator put on an agent outlive a restart, whether or not the agent has sent a picture yet.
	for _, a := range agents {
		if a.Status != store.StatusApproved {
			continue
		}
		if data, _, err := h.C.Store.LoadSnapshot(ctx, consentKey(a.ID)); err == nil && len(data) > 2 {
			h.mu.Lock()
			v := h.viewFor(a.ID)
			h.mu.Unlock()
			h.ensureConsent(a.ID, v)
		}
	}
}

func peerNotAfter(ctx context.Context) time.Time {
	if p, ok := peer.FromContext(ctx); ok {
		if ti, ok := p.AuthInfo.(credentials.TLSInfo); ok && len(ti.State.VerifiedChains) > 0 {
			return ti.State.VerifiedChains[0][0].NotAfter
		}
	}
	return time.Now().Add(time.Hour)
}

// Connect is the long-lived agent stream.
func (h *Hub) Connect(stream continuumv1.AgentService_ConnectServer) error {
	ctx := stream.Context()
	agent, ok := AgentFrom(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "no agent")
	}
	ip := peerIP(ctx)
	log := h.Log.With("agent", agent.ID, "name", agent.Name)

	type recvd struct {
		m   *continuumv1.AgentMessage
		err error
	}
	msgs := make(chan recvd)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		defer func() {
			if p := recover(); p != nil { // a panic while reading a message ends this stream, not the process
				Metrics.streamPanics.Add(1)
				h.Log.Error("panic recovered while reading an agent's stream", "agent", agent.ID, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
				select {
				case msgs <- recvd{nil, status.Error(codes.Internal, "internal error")}:
				case <-stop:
				}
			}
		}()
		for {
			m, err := stream.Recv()
			select {
			case msgs <- recvd{m, err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	// One session per agent: a new connection replaces the old one.
	ended := make(chan endReason, 1)
	sess := &session{push: make(chan *continuumv1.ServerMessage, 4), cancel: func(reason endReason) {
		select {
		case ended <- reason:
		default:
		}
	}}
	h.mu.Lock()
	if old := h.sessions[agent.ID]; old != nil {
		old.cancel(endReason{"replaced by a newer connection", false})
	}
	h.sessions[agent.ID] = sess
	v := h.views[agent.ID]
	if v == nil {
		v = newView()
		h.views[agent.ID] = v
	}
	v.connected = true
	v.link.connects++
	v.link.since = h.C.Now()
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.sessions[agent.ID] == sess {
			delete(h.sessions, agent.ID)
			v.connected = false
		}
		h.mu.Unlock()
		h.persist(context.Background(), agent.ID, true)
		h.persistFlows(context.Background(), agent.ID, true)
		log.Info("agent disconnected")
	}()

	// Revocation may have happened after the interceptor's check but before this session became
	// reachable by Drop; look again now that it is registered, so no revoked agent stays connected.
	fresh, err := h.C.AuthorizeAgent(ctx, agent.ID)
	if err != nil {
		return toStatus(err)
	}
	agent.AccessTier = fresh.AccessTier
	if agent.ConnectingIP != "" && agent.ConnectingIP != ip {
		h.C.audit(ctx, "agent:"+agent.ID, "agent-address-changed", "agent", agent.ID, "connected from "+ip+", previously "+agent.ConnectingIP)
	}

	// The stream must not outlive the certificate that authenticated it.
	lifetime := time.NewTimer(time.Until(peerNotAfter(ctx)))
	defer lifetime.Stop()

	// First message must be Hello.
	var hello *continuumv1.Hello
	select {
	case r := <-msgs:
		if r.err != nil {
			return r.err
		}
		hello = r.m.GetHello()
	case <-time.After(15 * time.Second):
		return status.Error(codes.DeadlineExceeded, "expected hello")
	}
	if hello == nil {
		return status.Error(codes.InvalidArgument, "first message must be hello")
	}
	hello.AgentVersion, hello.KubernetesVersion = printable(hello.AgentVersion, 40), printable(hello.KubernetesVersion, 40)
	_ = h.C.Store.Touch(ctx, agent.ID, ip, hello.AgentVersion, hello.KubernetesVersion, h.C.Now())
	_ = h.C.Store.ClearPollSecret(ctx, agent.ID)
	log.Info("agent connected", "ip", ip, "version", hello.AgentVersion)
	if hello.Diagnostics != nil {
		h.mu.Lock()
		noteDiagnostics(v, hello.Diagnostics, true, h.C.Now())
		h.mu.Unlock()
	}
	if ns := printable(hello.Namespace, 63); ns != "" {
		h.mu.Lock()
		v.ext.namespace = ns
		h.mu.Unlock()
	}
	if rel := printable(hello.ReleaseName, 53); rel != "" { // Helm's own release-name length limit
		h.mu.Lock()
		v.ext.releaseName = rel
		h.mu.Unlock()
	}
	agent = h.noteCeiling(ctx, agent, fresh, int(hello.InstalledAccessTier))
	if err := stream.Send(&continuumv1.ServerMessage{Msg: &continuumv1.ServerMessage_Config{Config: h.configFor(agent, v)}}); err != nil {
		return err
	}
	fullSeen := false              // a full picture is a consistency check only after the first one of this connection
	var asm *assembly              // the pieces of a picture that arrives in several messages, until the last one
	limiter := NewLimiter(120, 20) // per minute, burst
	for {
		select {
		case end := <-ended:
			if end.revoked { // only a real revocation tells the agent to stop for good
				_ = stream.Send(&continuumv1.ServerMessage{Msg: &continuumv1.ServerMessage_Revoked{Revoked: &continuumv1.Revoked{Reason: end.text}}})
			}
			return status.Error(codes.Aborted, end.text)
		case m := <-sess.push:
			if err := stream.Send(m); err != nil {
				return err
			}
		case <-lifetime.C:
			return status.Error(codes.Unauthenticated, "certificate expired; renew and reconnect")
		case r := <-msgs:
			if errors.Is(r.err, io.EOF) || status.Code(r.err) == codes.Canceled {
				return nil
			}
			if r.err != nil {
				return r.err
			}
			// The later pieces of one picture are not counted against the rate: the first was, and the picture's size is
			// bounded by the limits instead.
			if sy := r.m.GetSync(); (sy == nil || sy.ChunkIndex == 0 || sy.ChunkTotal < 2) && !limiter.Allow("s") {
				return status.Error(codes.ResourceExhausted, "sending too fast")
			}
			h.mu.Lock()
			v.link.note(r.m, h.C.Now())
			h.mu.Unlock()
			cur, err := h.C.AuthorizeAgent(ctx, agent.ID) // revocation applies mid-stream too
			if err != nil {
				return toStatus(err)
			}
			switch m := r.m.Msg.(type) {
			case *continuumv1.AgentMessage_Sync:
				// An agent speaks only for the cluster a human approved.
				if c := m.Sync.Cluster; c != nil && c.Uid != cur.Fingerprint {
					Metrics.syncsRefused.Add(1)
					h.C.audit(ctx, "agent:"+agent.ID, "sync-refused", "agent", agent.ID, "facts claimed a different cluster than the one approved")
					return status.Error(codes.PermissionDenied, "sync is for a different cluster than the one approved")
				}
				if err := validateSync(m.Sync); err != nil {
					h.refuseSync(ctx, cur, v, err)
					return status.Error(codes.InvalidArgument, err.Error())
				}
				sy, chunked := m.Sync, false
				if isChunk(sy) {
					// A piece of a larger picture: keep it, and apply nothing until the last one is in.
					var err error
					if sy.ChunkIndex == 0 {
						asm = nil // a new series replaces one that never finished
						if asm, err = newAssembly(sy, h.C.Now()); err != nil {
							h.refuseSync(ctx, cur, v, err)
							return status.Error(codes.InvalidArgument, err.Error())
						}
					} else if asm == nil {
						err = &errChunk{fmt.Sprintf("chunk %d arrived with no chunk 0 before it", sy.ChunkIndex)}
						h.refuseSync(ctx, cur, v, err)
						return status.Error(codes.InvalidArgument, err.Error())
					}
					whole, err := asm.add(sy, h.limits())
					if err != nil {
						asm = nil
						h.refuseSync(ctx, cur, v, err)
						var le *facts.LimitError
						if errors.As(err, &le) {
							return status.Error(codes.ResourceExhausted, err.Error())
						}
						return status.Error(codes.InvalidArgument, err.Error())
					}
					if whole == nil {
						continue
					}
					asm, sy, chunked = nil, whole, true
				} else {
					asm = nil // a whole picture or a change arriving means the series before it will not be finished
				}
				drift, checked, err := h.applySync(cur, sy, fullSeen)
				if err != nil {
					h.refuseSync(ctx, cur, v, err)
					return status.Error(codes.ResourceExhausted, err.Error())
				}
				Metrics.syncsApplied.Add(1)
				if chunked {
					Metrics.syncsChunked.Add(1)
				}
				if sy.Full {
					fullSeen = true
				}
				if checked {
					h.reportConsistency(ctx, cur, drift)
				}
				h.rec.changed.Store(true)
				if err := stream.Send(&continuumv1.ServerMessage{Msg: &continuumv1.ServerMessage_Ack{Ack: &continuumv1.Ack{Seq: sy.Seq}}}); err != nil {
					return err
				}
				h.persist(ctx, agent.ID, sy.Full)
			case *continuumv1.AgentMessage_Flows:
				applied, err := h.noteFlows(agent.ID, cur.AccessTier, m.Flows, h.C.Now())
				if err != nil {
					h.refuseSync(ctx, cur, v, err)
					return status.Error(codes.InvalidArgument, err.Error())
				}
				if applied {
					h.rec.changed.Store(true)
					h.persistFlows(ctx, agent.ID, false)
				}
			case *continuumv1.AgentMessage_Measurements:
				h.noteMeasurements(agent.ID, m.Measurements)
			case *continuumv1.AgentMessage_Heartbeat:
				if err := validateModules(m.Heartbeat.Modules); err != nil {
					h.refuseSync(ctx, cur, v, err)
					return status.Error(codes.InvalidArgument, err.Error())
				}
				skew := min(max(m.Heartbeat.ClockSkewMs, -maxSkewMs), maxSkewMs)
				h.mu.Lock()
				v.lastBeat = h.C.Now()
				if len(m.Heartbeat.Modules) > 0 {
					v.state.Modules = m.Heartbeat.Modules
				}
				if m.Heartbeat.Diagnostics != nil {
					noteDiagnostics(v, m.Heartbeat.Diagnostics, false, h.C.Now())
				}
				if asm != nil && asm.expired(h.C.Now()) {
					asm = nil // a picture that never finished arriving
				}
				// The skew is stored only when it moved by more than a second, so a steady clock costs no writes.
				storeSkew := !v.skewKnown && skew != cur.ClockSkewMs || v.skewKnown && abs64(skew-v.skewMs) > 1000
				v.skewMs, v.skewKnown = skew, true
				h.mu.Unlock()
				if storeSkew {
					_ = h.C.Store.SetClockSkew(ctx, agent.ID, skew)
				}
				_ = h.C.Store.Touch(ctx, agent.ID, ip, "", "", h.C.Now())
			case *continuumv1.AgentMessage_Hello:
				// ignore repeats
			}
		}
	}
}

// applySync merges facts, first dropping anything above the tier a human approved. When the message
// is a full picture that is not the first of its connection, it also says how far the server's own
// picture had drifted from it (checked is true); applying it afterwards repairs the difference.
func (h *Hub) applySync(a store.Agent, s *continuumv1.Sync, fullSeen bool) (drift facts.Drift, checked bool, err error) {
	dropAboveTier(a.AccessTier, s)
	// Captured after the tier ceiling has narrowed s but before an admin's own exclusion list does, so a
	// namespace the admin has asked to leave out is still counted as "reported" - see viewExt.knownNamespaces.
	reportedNS := make([]string, 0, len(s.Namespaces))
	for _, n := range s.Namespaces {
		reportedNS = append(reportedNS, n.Name)
	}
	h.dropConsentOverrides(a.ID, s)
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.views[a.ID]
	// Whatever the agent claims, what the server ends up holding for it stays within the limits. Checked
	// before anything is merged, so a refused message changes nothing - knownNamespaces included: it is
	// folded in below, only once the sync is known to be accepted, not here where a Check failure would
	// otherwise let a rejected sync's namespace names through anyway.
	if err := v.state.Check(s, h.limits()); err != nil {
		return drift, false, err
	}
	v.ext.noteNamespaces(reportedNS, s.Full)
	if s.Full && fullSeen {
		drift, checked = v.state.CheckDrift(s), true
		v.consAt, v.consDrift = h.C.Now(), drift
		v.consCount++
	}
	h.noteVanished(a, v, s, h.C.Now()) // what this message removes is remembered as a tombstone before it goes
	v.state.Apply(s)
	h.tw.gen.Add(1)
	v.refusal = ""
	v.lastSync, v.lastBeat, v.dirtyAt = h.C.Now(), h.C.Now(), h.C.Now()
	return drift, checked, nil
}

// noteFlows applies an agent's observed-traffic batch, gated by the same tier ceiling and pause consent the
// gRPC stream loop used to check inline. It reports whether the batch was applied (false when it was quietly
// ignored because the approval doesn't cover workloads yet, or the "flow" collector is paused) and any protocol
// error the batch itself contains (which the caller turns into a refused sync, unlike the two quiet cases).
func (h *Hub) noteFlows(agentID string, tier int, fb *continuumv1.FlowBatch, now time.Time) (applied bool, err error) {
	// Observed traffic is workload-level information: it is only accepted when the approval covers workloads.
	if tier < 2 {
		return false, nil
	}
	if h.consentOf(agentID).has("flow") {
		// The agent should already have stopped sending this; the same defense-in-depth dropAboveTier
		// gives the tier ceiling, extended to a paused collector.
		return false, nil
	}
	if err := validateFlowBatch(fb); err != nil {
		return false, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.views[agentID]
	if v == nil {
		return false, nil
	}
	v.flows.apply(fb, now)
	v.obs.note(fb, now)
	v.flowsDirty = true
	return true, nil
}

// snapshotCap is the most a stored copy of one agent's state may take: the state itself plus its framing.
func snapshotCap(l facts.Limits) int {
	return int(l.Bytes) + 16*(l.Nodes+l.Namespaces+l.Workloads) + 4096 // each entry adds a tag and a length
}

// limits are the caps on what one agent may make the server hold (tests lower them).
func (h *Hub) limits() facts.Limits {
	if h.Limits != nil {
		return *h.Limits
	}
	return facts.DefaultLimits()
}

// refusalEvery is how often one agent's refused messages are written to the audit trail and the event list;
// an agent that keeps sending the same thing would otherwise fill both.
const refusalEvery = 10 * time.Minute

// refuseSync notes that an agent's message was refused: it is shown with the agent, logged, and (at
// most once per refusalEvery) recorded as an audit row and a warning event, which is where an
// administrator looks. The agent itself receives the reason as the error that ends its stream.
func (h *Hub) refuseSync(ctx context.Context, a store.Agent, v *view, why error) {
	Metrics.syncsRefused.Add(1)
	now := h.C.Now()
	h.mu.Lock()
	v.refusal = clipText(why.Error(), 300)
	record := now.Sub(v.refusalLogged) >= refusalEvery
	if record {
		v.refusalLogged = now
	}
	h.mu.Unlock()
	h.Log.Warn("agent message refused", "agent", a.ID, "name", a.Name, "reason", why.Error())
	if !record {
		return
	}
	h.C.audit(ctx, "agent:"+a.ID, "sync-refused", "agent", a.ID, why.Error())
	h.storeEvent(ctx, store.Event{At: now, Kind: "sync-refused", TargetKind: "agent", TargetID: a.ID, Name: a.Name, ClusterID: a.ClusterID, ClusterName: a.Name,
		Detail: "The server refused a message from " + a.Name + ": " + clipText(why.Error(), 300), Cause: "the agent reported more, or larger, facts than this server accepts, or something malformed", Severity: "warning"})
}

func clipText(s string, n int) string { return printable(s, n) }

// loadState reads a stored snapshot and applies the same rules as a live message.
func loadState(data []byte, l facts.Limits) (*facts.State, error) {
	if len(data) > snapshotCap(l) {
		return nil, fmt.Errorf("the stored snapshot is %d bytes, over the limit of %d", len(data), snapshotCap(l))
	}
	var m continuumv1.Sync
	if err := proto.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if err := validateSync(&m); err != nil {
		return nil, err
	}
	st := facts.New()
	if err := st.Check(&m, l); err != nil {
		return nil, err
	}
	st.Apply(&m)
	return st, nil
}

// loadFlowTable reads a stored flow table under the limits of a live one.
func loadFlowTable(data []byte) (*flowTable, error) {
	if len(data) > facts.MaxBytes {
		return nil, fmt.Errorf("the stored flow table is %d bytes, over the limit of %d", len(data), facts.MaxBytes)
	}
	t, err := unmarshalFlowTable(data)
	if err != nil {
		return nil, err
	}
	if len(t.edges) > maxFlowEdges {
		return nil, fmt.Errorf("the stored flow table has %d edges, over the limit of %d", len(t.edges), maxFlowEdges)
	}
	for _, e := range t.edges {
		if err := validateFlowKey(e.Key); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// reportConsistency records what a consistency check found. Finding nothing is the normal result and
// is silent; a difference means the server missed a change, which is worth an event and a log line.
func (h *Hub) reportConsistency(ctx context.Context, a store.Agent, d facts.Drift) {
	if d.Total() == 0 {
		return
	}
	h.Log.Warn("consistency check found the server's picture out of date", "agent", a.ID, "differences", d.Total(), "detail", d.String(), "examples", d.Samples)
	detail := fmt.Sprintf("%d differences between the server's picture of %s and the cluster's own full picture (%s). The server has been corrected.", d.Total(), a.Name, d.String())
	if len(d.Samples) > 0 {
		detail += " For example: " + strings.Join(d.Samples, ", ") + "."
	}
	h.storeEvent(ctx, store.Event{At: h.C.Now(), Kind: "drift", TargetKind: "agent", TargetID: a.ID, Name: a.Name, ClusterID: a.ClusterID,
		ClusterName: a.Name, Detail: detail, Cause: "a change the agent sent did not reach the server, or the server was restarted between two syncs", Severity: "warning"})
}

// Bounds on what one agent may report. A real cluster is far below these; they exist so a
// compromised agent cannot exhaust the server's memory or fill the database.
const (
	maxNodes      = 5000
	maxNamespaces = 5000
	maxWorkloads  = 50000
	maxListItems  = 200
	maxStr        = 512
)

func validateSync(s *continuumv1.Sync) error {
	if len(s.Nodes) > maxNodes || len(s.DeletedNodes) > maxNodes ||
		len(s.Namespaces) > maxNamespaces || len(s.DeletedNamespaces) > maxNamespaces ||
		len(s.Workloads) > maxWorkloads || len(s.DeletedWorkloads) > maxWorkloads {
		return fmt.Errorf("a single sync may carry at most %d nodes, %d namespaces and %d workloads (it had %d, %d and %d)", maxNodes, maxNamespaces, maxWorkloads, len(s.Nodes), len(s.Namespaces), len(s.Workloads))
	}
	if err := validateModules(s.Modules); err != nil {
		return err
	}
	if c := s.Cluster; c != nil && (len(c.StorageClasses) > maxListItems || len(c.IngressClasses) > maxListItems || len(c.Version) > 64 || len(c.ApiHost) > maxStr) {
		return errors.New("cluster facts are malformed")
	}
	for _, n := range s.Nodes {
		if n.Key == "" || len(n.Key) > maxStr || len(n.Name) > maxStr || len(n.Labels) > 500 || len(n.Annotations) > 100 || len(n.Taints) > maxListItems || len(n.InternalIps) > 50 {
			return errors.New("node facts are malformed")
		}
	}
	for _, n := range s.Namespaces {
		if n.Key == "" || len(n.Key) > maxStr || len(n.Name) > maxStr || len(n.Labels) > 500 || len(n.Annotations) > 100 {
			return errors.New("namespace facts are malformed")
		}
	}
	for _, w := range s.Workloads {
		if w.Key == "" || len(w.Key) > maxStr || len(w.Images) > 100 || len(w.Labels) > 500 || len(w.Annotations) > 100 || len(w.NodeNames) > 5000 || len(w.Ports) > 500 || len(w.Hosts) > 500 || len(w.VolumeClaims) > 200 {
			return errors.New("workload facts are malformed")
		}
		for _, v := range w.VolumeClaims {
			if len(v.Name) > maxStr || len(v.StorageClass) > maxStr || len(v.AccessModes) > 10 || len(v.PinnedNodes) > 100 {
				return errors.New("volume claim facts are malformed")
			}
		}
		if a := w.Autoscaler; a != nil && len(a.Targets) > 50 {
			return errors.New("autoscaler facts are malformed")
		}
		if d := w.Disruption; d != nil && (len(d.MinAvailable) > 32 || len(d.MaxUnavailable) > 32) {
			return errors.New("disruption facts are malformed")
		}
	}
	for _, k := range append(append(append([]string{}, s.DeletedNodes...), s.DeletedNamespaces...), s.DeletedWorkloads...) {
		if len(k) > maxStr {
			return errors.New("a deleted key is malformed")
		}
	}
	// Every string, map and list in every fact, including fields added to the protocol later: too long is
	// refused (map values are cut), so nothing an agent sends can be arbitrarily large.
	if err := facts.SanitizeSync(s); err != nil {
		return fmt.Errorf("facts are malformed: %v", err)
	}
	return nil
}

func validateModules(ms []*continuumv1.ModuleStatus) error {
	if len(ms) > 64 {
		return errors.New("an agent may report at most 64 modules")
	}
	for _, m := range ms {
		if len(m.Name) > 64 || len(m.Reason) > maxStr {
			return errors.New("module status is malformed")
		}
	}
	return nil
}

func (h *Hub) persist(ctx context.Context, id string, force bool) {
	h.mu.Lock()
	v := h.views[id]
	if v == nil || v.dirtyAt.IsZero() || (!force && time.Since(v.savedAt) < snapshotEvery) || v.dirtyAt.Before(v.savedAt) {
		h.mu.Unlock()
		return
	}
	data, err := v.state.Marshal()
	v.savedAt = h.C.Now()
	h.mu.Unlock()
	if err == nil && len(data) > snapshotCap(h.limits()) {
		err = fmt.Errorf("the state is %d bytes, over the limit of %d for what is stored", len(data), snapshotCap(h.limits()))
	} else if err == nil {
		err = h.C.Store.SaveSnapshot(ctx, id, data, h.C.Now())
	}
	if err != nil {
		Metrics.storeErrors.Add(1)
		h.Log.Error("snapshot save failed", "agent", id, "err", err)
	}
}

func flowsKey(agentID string) string { return agentID + "#flows" }

func (h *Hub) persistFlows(ctx context.Context, id string, force bool) {
	h.mu.Lock()
	v := h.views[id]
	if v == nil || !v.flowsDirty || (!force && time.Since(v.flowsSaved) < snapshotEvery) {
		h.mu.Unlock()
		return
	}
	data, err := v.flows.marshal()
	v.flowsSaved, v.flowsDirty = h.C.Now(), false
	h.mu.Unlock()
	if err == nil && len(data) > facts.MaxBytes {
		err = fmt.Errorf("the flow table is %d bytes, over the limit of %d for what is stored", len(data), facts.MaxBytes)
	} else if err == nil {
		err = h.C.Store.SaveSnapshot(ctx, flowsKey(id), data, h.C.Now())
	}
	if err != nil {
		Metrics.storeErrors.Add(1)
		h.Log.Error("flow table save failed", "agent", id, "err", err)
	}
}

// ---- state document for the UI ----

type ModuleDoc struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type AgentDoc struct {
	ID            string `json:"id"`
	OrgID         string `json:"orgId"`
	Name          string `json:"name"`
	ClusterID     string `json:"clusterId,omitempty"`
	Version       string `json:"version"`
	K8sVersion    string `json:"kubernetesVersion,omitempty"`
	AccessTier    int    `json:"accessTier"`
	InstalledTier int    `json:"installedTier"`
	TierCap       int    `json:"tierCap"`
	Status        string `json:"status"` // pending | approved | revoked | rejected
	Fingerprint   string `json:"fingerprint"`
	ConnectingIP  string `json:"connectingIp,omitempty"`
	// Namespace is the agent's own Kubernetes namespace, from its Hello. Empty until an agent that reports it has
	// connected since this server started; a helm command falls back to a guess in that case.
	Namespace string `json:"namespace,omitempty"`
	// ReleaseName is the Helm release this agent's Hello says it was installed as. Empty under the same condition as
	// Namespace, in which case a helm command falls back to the chart's documented default release name.
	ReleaseName string `json:"releaseName,omitempty"`
	// ConnectingGeo is only a suggestion derived offline from ConnectingIP; a cloud cluster resolves to its provider's egress.
	ConnectingGeo *geoip.Result `json:"connectingGeo,omitempty"`
	// ConnectingGeoReason is set only when ConnectingGeo is nil because ConnectingIP itself could not be
	// located at all (as opposed to being locatable but simply unrecorded): "cgnat" | "private" |
	// "loopback" | "link-local" | "link-local-multicast" | "multicast" | "unspecified". The first two mean
	// the agent is reaching this server through some NAT (derived from the address's own range, not a live
	// probe - see geoip.UnlocatableReason), which is exactly the case deploy/README.md's Troubleshooting
	// section covers with geoip.publicIpService.
	ConnectingGeoReason string      `json:"connectingGeoReason,omitempty"`
	CertExpiresAt       string      `json:"certExpiresAt,omitempty"`
	LastHeartbeat       string      `json:"lastHeartbeat,omitempty"`
	RequestedAt         string      `json:"requestedAt"`
	Reason              string      `json:"reason,omitempty"`
	Connected           bool        `json:"connected"`
	Synced              bool        `json:"synced"`
	Modules             []ModuleDoc `json:"modules"`
	// Observer is what the traffic observer reports about itself; absent when no collector has ever reported.
	Observer *ObserverDoc `json:"observer,omitempty"`
	// Consistency is the result of the last check of the server's picture against the agent's full one.
	Consistency *ConsistencyDoc `json:"consistency,omitempty"`
	// Measuring is how many addresses the agent has been asked to time (0 when it is not measuring).
	Measuring int `json:"measuring"`
	// Link is what this agent has sent since the server started; absent if it has not connected in that time.
	Link *LinkDoc `json:"link,omitempty"`
	// Scope is the rule the agent applies to which namespaces it reports; absent when it sees them all.
	Scope *ScopeDoc `json:"scope,omitempty"`
	// ClockSkewMs is the agent's clock minus the server's as the agent measured it; the UI warns when it is minutes.
	ClockSkewMs int64 `json:"clockSkewMs,omitempty"`
	// LegacyEnrollment marks a pending agent that came without an approval code (an older agent).
	LegacyEnrollment bool `json:"legacyEnrollment,omitempty"`
	// ApprovalAttemptsLeft is how many codes may still be tried for a pending agent that has one.
	ApprovalAttemptsLeft *int `json:"approvalAttemptsLeft,omitempty"`
	// PendingExpiresAt is when a pending enrollment expires if nobody approves it.
	PendingExpiresAt string `json:"pendingExpiresAt,omitempty"`
	// Diagnostics is the agent's own account of itself and Consent the overrides an administrator has put on it. Both are only
	// in the document for people who may change an agent's access; the state handler removes them for everyone else.
	Diagnostics *DiagnosticsDoc `json:"diagnostics,omitempty"`
	Consent     *ConsentDoc     `json:"consent,omitempty"`
	// HardenHelm is set whenever the approved tier is below the installed ceiling: the exact command that would
	// also shrink the cluster's own RBAC to match, since narrowing here only changes what the agent reports.
	// Filled in by the /state handler, which has the image settings this needs; empty until then.
	HardenHelm string `json:"hardenHelm,omitempty"`
	// Teardown is only set for a revoked or rejected agent: revocation ends the connection, but the ServiceAccount,
	// its RBAC and the identity Secret stay in the cluster until someone removes them there. NamespaceGuessed
	// marks that this agent never reported its namespace and/or its release name, so the commands name the chart's
	// documented defaults, which may be wrong for this install.
	Teardown *TeardownDoc `json:"teardown,omitempty"`
}

type TeardownDoc struct {
	Helm             string `json:"helm"`
	Secret           string `json:"secret"`
	NamespaceGuessed bool   `json:"namespaceGuessed"`
}

// maxSkewMs bounds what an agent may claim as its clock skew (a year), so a hostile value cannot overflow anything.
const maxSkewMs = int64(365 * 24 * time.Hour / time.Millisecond)

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// ScopeDoc says which namespaces an agent leaves out. Only counts: the server never learns the names it was not sent.
type ScopeDoc struct {
	Description string `json:"description"`
	Total       int    `json:"namespaces"`
	InScope     int    `json:"inScope"`
}

// LinkDoc is a gauge of one agent's stream: how long it has been up and what it has sent.
type LinkDoc struct {
	ConnectedSince string `json:"connectedSince,omitempty"` // empty while disconnected
	Connects       int    `json:"connects"`
	Bytes          int64  `json:"bytes"`
	Syncs          int    `json:"syncs"`
	Flows          int    `json:"flows"`
	Measurements   int    `json:"measurements"`
	Heartbeats     int    `json:"heartbeats"`
	LastSync       string `json:"lastSync,omitempty"`
	LastFlows      string `json:"lastFlows,omitempty"`
}

// ConsistencyDoc says whether the server's picture of a cluster matched the cluster's own.
type ConsistencyDoc struct {
	LastCheck   string `json:"lastCheck"`
	Checks      int    `json:"checks"`
	Differences int    `json:"differences"`
	Summary     string `json:"summary,omitempty"`
}

type AuditDoc struct {
	ID         string `json:"id"`
	OrgID      string `json:"orgId"`
	At         string `json:"at"`
	Actor      string `json:"actor"`
	Action     string `json:"action"`
	TargetKind string `json:"targetKind"`
	TargetID   string `json:"targetId"`
	Detail     string `json:"detail,omitempty"`
}

type StateDoc struct {
	GeneratedAt string         `json:"generatedAt"`
	Agents      []AgentDoc     `json:"agents"`
	Topology    model.Topology `json:"topology"`
	AuditLog    []AuditDoc     `json:"auditLog"`
	// Tombstones are records that disappeared from their agent's picture within the retention window: what they
	// were, and when and why they went. Observation states the rules the records' states were computed with.
	Tombstones  []TombstoneDoc      `json:"tombstones"`
	Observation twin.ObservationDoc `json:"observation"`
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }
func rfcp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return rfc(*t)
}

// State builds what the UI polls: agents, the interpreted topology of every approved cluster, and
// recent audit events. Records of an agent that has gone quiet are flagged stale, not removed.
func (h *Hub) State(ctx context.Context) (StateDoc, error) { return h.StateFor(ctx, false) }

// StateFor is State, with the recent audit rows when withAudit is set. They name people, so only callers
// who have already checked the reader is an administrator or owner may ask for them.
func (h *Hub) StateFor(ctx context.Context, withAudit bool) (StateDoc, error) {
	return h.stateFor(ctx, withAudit, nil)
}

// stateCache is the cached half of stateFor's work: the topology interpretation/correlation pass, and the
// effectiveBuild Model() needs from it. Invalidated by exactly the signal Hub.Model's own modelCache
// already uses - tw.gen, bumped only when a real input changes (applySync, a revocation, a saved
// workspace, a tombstone added) - and guarded by tw.mu, not h.mu, so a cache hit never contends with live
// agent sync or heartbeat writes.
//
// Deliberately NOT cached here: the per-agent AgentDoc list (built fresh by buildAgentDocs on every call)
// and the audit log (fetched fresh by stateFor below). Both carry live fields - AccessTier, consent,
// diagnostics, HardenHelm's own upstream tier comparison - that change through code paths which never
// bump tw.gen, because nothing needed them to before this cache existed (gen was wired up only for what
// Model()'s heavier transform reads). Caching those against tw.gen would silently serve stale tier/
// consent/diagnostics data for up to modelCacheTTL after a change - confirmed by this change's own test
// run, not a hypothetical: narrowing an agent's access tier doesn't bump gen, so caching AgentDoc the same
// way as topology served a stale HardenHelm command on the very next poll. Topology and effectiveBuild
// have no such gap: everything they're built from (facts, declared layer, tombstones) bumps gen on every
// path that changes it, which is exactly why only they are cached.
type stateCache struct {
	at          time.Time
	gen         uint64
	stale       time.Duration
	topology    model.Topology
	tombstones  []TombstoneDoc
	observation twin.ObservationDoc
	eb          *effectiveBuild
}

// stateFor builds the state document, calling after (when set) with the finished document and what the
// model builder needs. buildAgentDocs below always runs fresh. buildTopology - the expensive interpret/
// observe/correlate pass - is cached against tw.gen (see stateCache's own doc for exactly what is and
// isn't safe to cache and why), so a poll landing between two real topology-affecting changes skips that
// whole pass, without ever taking h.mu for it.
func (h *Hub) stateFor(ctx context.Context, withAudit bool, after func(*StateDoc, *effectiveBuild)) (StateDoc, error) {
	h.twinLoad(ctx)
	now := h.C.Now()
	window := h.staleWindow()

	// Fetched here, before any lock: ListAudit is a database read keyed only by org ID, with no
	// dependency on anything built below. Every agent's heartbeat and sync ultimately waits on h.mu too
	// (applySync takes it to merge state), so a slow audit-log query sitting inside a locked section
	// would stall live agent traffic behind an unrelated read, for every org sharing this process - not
	// just the one asking for its audit log. Audit freshness and topology freshness are different
	// contracts; this is never cached, unlike the topology pass below.
	var auditEvents []store.AuditEvent
	if withAudit {
		auditEvents, _ = h.C.Store.ListAudit(ctx, h.C.OrgID, 50)
	}

	agentDocs, agents, err := h.buildAgentDocs(ctx, now)
	if err != nil {
		return StateDoc{}, err
	}

	tw := h.tw
	gen := tw.gen.Load()
	tw.mu.Lock()
	if c := tw.stateCache; c != nil && c.gen == gen && c.stale == window && now.Sub(c.at) >= 0 && now.Sub(c.at) < modelCacheTTL {
		topology, tombstones, observation, eb := c.topology, c.tombstones, c.observation, c.eb
		tw.mu.Unlock()
		return h.finishState(agentDocs, topology, tombstones, observation, eb, now, auditEvents, after), nil
	}
	tw.mu.Unlock()

	topology, tombstones, observation, eb, err := h.buildTopology(ctx, agents, now, window)
	if err != nil {
		return StateDoc{}, err
	}
	tw.mu.Lock()
	tw.stateCache = &stateCache{at: now, gen: gen, stale: window, topology: topology, tombstones: tombstones, observation: observation, eb: eb}
	tw.mu.Unlock()
	return h.finishState(agentDocs, topology, tombstones, observation, eb, now, auditEvents, after), nil
}

// finishState assembles what neither buildAgentDocs nor a cache entry carries - a fresh GeneratedAt stamp
// and the audit rows stateFor just fetched - and runs the after callback. Shared by stateFor's cache-hit
// and cache-miss paths so the two can never drift apart on what "finished" means.
func (h *Hub) finishState(agentDocs []AgentDoc, topology model.Topology, tombstones []TombstoneDoc, observation twin.ObservationDoc, eb *effectiveBuild, now time.Time, auditEvents []store.AuditEvent, after func(*StateDoc, *effectiveBuild)) StateDoc {
	doc := StateDoc{GeneratedAt: rfc(now), Agents: agentDocs, Topology: topology, Tombstones: tombstones, Observation: observation}
	doc.AuditLog = make([]AuditDoc, 0, len(auditEvents))
	for i := len(auditEvents) - 1; i >= 0; i-- {
		e := auditEvents[i]
		doc.AuditLog = append(doc.AuditLog, AuditDoc{ID: "au-" + itoa(e.ID), OrgID: e.OrgID, At: rfc(e.At), Actor: e.Actor, Action: e.Action, TargetKind: e.TargetKind, TargetID: e.TargetID, Detail: e.Detail})
	}
	if after != nil {
		after(&doc, eb)
	}
	return doc
}

// buildAgentDocs builds every agent's own document - status, tier, consent, diagnostics, teardown
// commands, and everything else that can change through a path which does not bump tw.gen (a tier
// narrowing, a consent override, a diagnostics report). Always a fresh read of h.views under h.mu, on
// every single call: see stateCache's own doc for why this, unlike the topology pass below, is never
// cached. Also returns the agent list fetched along the way, so buildTopology does not need to fetch it
// again.
func (h *Hub) buildAgentDocs(ctx context.Context, now time.Time) ([]AgentDoc, []store.Agent, error) {
	agents, err := h.C.Store.ListAgents(ctx, h.C.OrgID)
	if err != nil {
		return nil, nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	agentDocs := make([]AgentDoc, 0, len(agents))
	for _, a := range agents {
		v := h.views[a.ID]
		d := AgentDoc{
			ID: a.ID, OrgID: a.OrgID, Name: a.Name, ClusterID: a.ClusterID, Version: a.Version, K8sVersion: a.K8sVersion,
			AccessTier: a.AccessTier, InstalledTier: a.InstalledTier, TierCap: a.TierCap, Status: string(a.Status), Fingerprint: a.Fingerprint,
			ConnectingIP: a.ConnectingIP, CertExpiresAt: rfcp(a.LeafNotAfter), LastHeartbeat: rfcp(a.LastSeen), RequestedAt: rfc(a.CreatedAt),
			Reason: a.Reason, Modules: []ModuleDoc{}, ClockSkewMs: a.ClockSkewMs,
		}
		if a.Status == store.StatusPending {
			d.LegacyEnrollment = len(a.ApprovalHash) == 0
			if !d.LegacyEnrollment {
				left := max(MaxApprovalAttempts-a.ApprovalAttempts, 0)
				d.ApprovalAttemptsLeft = &left
			}
			d.PendingExpiresAt = rfc(a.CreatedAt.Add(h.C.pendingTTL()))
		}
		d.ConnectingGeo = h.Geo.Locate(a.ConnectingIP)
		if d.ConnectingGeo == nil {
			d.ConnectingGeoReason = h.Geo.UnlocatableReason(a.ConnectingIP)
		}
		if v != nil {
			d.Namespace, d.ReleaseName = v.ext.namespace, v.ext.releaseName
		}
		if a.Status == store.StatusRevoked || a.Status == store.StatusRejected {
			helm, secret, guessed := teardownCommands(d.Namespace, d.ReleaseName)
			d.Teardown = &TeardownDoc{Helm: helm, Secret: secret, NamespaceGuessed: guessed}
		}
		if v != nil {
			d.Connected = v.connected
			d.Synced = !v.lastSync.IsZero()
			if !v.lastBeat.IsZero() {
				d.LastHeartbeat = rfc(v.lastBeat)
			}
			if v.link.connects > 0 {
				l := &LinkDoc{Connects: v.link.connects, Bytes: v.link.bytes, Syncs: v.link.syncs, Flows: v.link.flows, Measurements: v.link.meas, Heartbeats: v.link.beats}
				if v.connected {
					l.ConnectedSince = rfc(v.link.since)
				}
				if !v.link.lastSync.IsZero() {
					l.LastSync = rfc(v.link.lastSync)
				}
				if !v.link.lastFlows.IsZero() {
					l.LastFlows = rfc(v.link.lastFlows)
				}
				d.Link = l
			}
			if sc := v.state.Cluster.GetScope(); sc != nil {
				d.Scope = &ScopeDoc{Description: sc.Description, Total: int(sc.NamespacesTotal), InScope: int(sc.NamespacesInScope)}
			}
			d.Observer = v.obs.doc(h.C.Now())
			if a.Status == store.StatusApproved {
				d.Diagnostics = v.diagDoc()
				d.Consent = v.consentDoc()
			}
			d.Measuring = len(v.targets)
			if v.consCount > 0 {
				d.Consistency = &ConsistencyDoc{LastCheck: rfc(v.consAt), Checks: v.consCount, Differences: v.consDrift.Total()}
				if v.consDrift.Total() > 0 {
					d.Consistency.Summary = v.consDrift.String()
				}
			}
			for _, m := range v.state.Modules {
				d.Modules = append(d.Modules, ModuleDoc{Name: m.Name, Status: map[continuumv1.ModuleStatus_State]string{
					continuumv1.ModuleStatus_OK: "ok", continuumv1.ModuleStatus_SKIPPED: "skipped", continuumv1.ModuleStatus_ERROR: "error"}[m.State], Reason: m.Reason})
			}
			if v.refusal != "" { // why nothing new is arriving from this agent
				d.Modules = append(d.Modules, ModuleDoc{Name: "server limits", Status: "error", Reason: v.refusal})
			}
		}
		agentDocs = append(agentDocs, d)
	}
	sort.Slice(agentDocs, func(i, j int) bool { return agentDocs[i].RequestedAt < agentDocs[j].RequestedAt })
	return agentDocs, agents, nil
}

// buildTopology does the expensive, h.mu-guarded work of interpreting every approved agent's facts,
// assessing staleness, and correlating flows/tunnels/cluster-links - exactly what stateFor used to do
// inline, together with the agent-doc loop, on every single call. stateFor above caches this against
// tw.gen; this function itself knows nothing about caching, so it is always a real, full rebuild. Takes
// the agent list buildAgentDocs already fetched, rather than fetching it again.
func (h *Hub) buildTopology(ctx context.Context, agents []store.Agent, now time.Time, window time.Duration) (model.Topology, []TombstoneDoc, twin.ObservationDoc, *effectiveBuild, error) {
	eb := newEffectiveBuild(window)
	topo := model.Topology{Clusters: []model.Cluster{}, Nodes: []model.Node{}, Namespaces: []model.Namespace{}, Services: []model.Service{}, Suggestions: []model.Suggestion{},
		Dependencies: []model.Dependency{}, ExternalEndpoints: []model.ExternalEndpoint{}, Paths: []model.Path{}, ClusterLinks: []model.ClusterLink{}}
	var observed, located []observedCluster
	h.mu.Lock()
	defer h.mu.Unlock()
	showRevoked := h.revokedToShow(agents)
	for _, a := range agents {
		v := h.views[a.ID]
		revoked := showRevoked[a.ID]
		if (a.Status != store.StatusApproved && !revoked) || v == nil || v.lastSync.IsZero() {
			continue
		}
		recs := h.nodeRecords(a.ClusterID, v.state, now)
		t := interpret.Interpret(interpret.Input{OrgID: h.C.OrgID, AgentID: a.ID, ClusterID: a.ClusterID, Name: a.Name, State: v.state, Now: v.lastSync, AccessTier: a.AccessTier, NodeIDs: nodeIDMap(recs)})
		var revokedAt time.Time
		if a.RevokedAt != nil {
			revokedAt = *a.RevokedAt
		}
		obs := twin.Assess(twin.AssessInput{Now: now, LastObserved: v.lastBeat, Connected: v.connected, Revoked: revoked, RevokedAt: revokedAt, StaleAfter: window})
		markObservation(&t, obs, recs)
		h.reviveSeen(t)
		eb.agents[a.ID] = twin.AgentInfo{ID: a.ID, Name: a.Name, ClusterID: a.ClusterID, Tier: a.AccessTier}
		eb.obs[a.ID] = obs
		eb.facts[a.ClusterID] = v.state
		for _, r := range recs {
			eb.nodes[r.ID] = r
		}
		topo.Clusters = append(topo.Clusters, t.Clusters...)
		topo.Nodes = append(topo.Nodes, t.Nodes...)
		topo.Namespaces = append(topo.Namespaces, t.Namespaces...)
		topo.Services = append(topo.Services, t.Services...)
		topo.Suggestions = append(topo.Suggestions, t.Suggestions...)
		if revoked {
			continue // a revoked agent's picture is kept to be shown, and is never used to draw traffic or place anything
		}
		oc := observedCluster{id: a.ClusterID, name: a.Name, agentID: a.ID, state: v.state, flows: v.flows, egressIP: publicIP(a.ConnectingIP)}
		located = append(located, oc)
		if a.AccessTier >= 2 && v.flows != nil {
			observed = append(observed, oc)
		}
	}
	if len(observed) > 0 {
		topo.Dependencies, topo.ExternalEndpoints = observedTopology(h.C.OrgID, observed, now, h.StaleFlows())
		topo.Suggestions = append(topo.Suggestions, suspicions(h.C.OrgID, observed, located, now)...)
	}
	// Needs every cluster's nodes in one list (a tunnel's other end is often in a different cluster, or
	// this very same one), so this runs once here rather than per cluster inside the loop above.
	correlateTunnels(topo.Nodes)
	names := map[string]string{}
	for _, c := range topo.Clusters {
		names[c.ID] = c.Name
	}
	// Same reasoning as correlateTunnels just above: a cluster-pair relationship can only be seen once
	// every cluster's nodes are in one list, so this also runs once here, after names is built.
	topo.ClusterLinks = correlateClusterLinks(topo.Nodes, names)
	topo.Paths = h.pathDocs(agents, located, names, now)
	tombstones := h.tombstoneDocs(now)
	observation := twin.ObservationDoc{StaleAfterSeconds: int(window / time.Second), TombstoneRetentionDays: int(h.retention() / (24 * time.Hour))}
	return topo, tombstones, observation, eb, nil
}

// StaleFlows is how long an observed edge may go unseen before it is shown as stale.
func (h *Hub) StaleFlows() time.Duration {
	if h.FlowStaleAfter > 0 {
		return h.FlowStaleAfter
	}
	if s := h.C.Settings(); s.FlowStaleSeconds > 0 {
		return time.Duration(s.FlowStaleSeconds) * time.Second
	}
	return defaultStaleWindow
}

// publicIP returns ip when it is a public address (the only kind that identifies a cluster from outside).
func publicIP(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
		return ""
	}
	return a.String()
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
