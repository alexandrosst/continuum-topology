package estate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/accounts"
	"continuum/internal/agentcaps"
	"continuum/internal/agents"
	"continuum/internal/connectivity"
	"continuum/internal/facts"
	"continuum/internal/flow/wire"
	"continuum/internal/geoip"
	"continuum/internal/interpret"
	"continuum/internal/kernel"
	"continuum/internal/model"
	"continuum/internal/observed"
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

// View is what the server currently knows about one agent's cluster. It is three parts, one for each module that keeps something per
// agent, and each part is declared with the module that owns it: EstView here (what the agent reported about its cluster and the
// liveness of its stream), agents.View (the agent's own account of itself and its health) and connectivity.NetView (the paths, addresses and config
// the agent is told). Nothing is a field of the view itself, so a use of one is a use of the module that owns it.
type View struct {
	EstView
	agents.View
	connectivity.NetView
}

// EstView is what the hub holds of what an agent reported about its cluster, and of the liveness of its stream.
type EstView struct {
	State     *facts.State
	LastSync  time.Time
	LastBeat  time.Time
	dirtyAt   time.Time
	savedAt   time.Time
	Connected bool

	Flows      *observed.FlowTable
	obs        observed.Health
	flowsDirty bool
	flowsSaved time.Time
	// podFlows is this agent's latest per-pod traffic breakdown (FlowBatch.pod_flows), replaced wholesale
	// by every accepted batch in noteFlows below - never merged/accumulated across batches the way flows
	// above is, so it is always "what this agent's pods were doing as of its last report", not a running
	// total. Not persisted across a server restart (same as the rest of view): a fresh snapshot arrives
	// with the agent's next report anyway, and nothing here is meant to survive longer than that.
	podFlows []*continuumv1.Flow
	// linkSat is this agent's latest per-node, per-interface link throughput/saturation
	// (FlowBatch.collectors[].link_saturation), keyed by node name - replaced wholesale on every accepted
	// batch, the same "right now, not accumulated" treatment as podFlows above and for the same reason:
	// it is a gauge of the window the collector just reported, not a running total.
	linkSat map[string][]*continuumv1.LinkSaturation
	// snatExhaustion is this agent's latest per-node SNAT/ephemeral-port-exhaustion reading
	// (FlowBatch.collectors[].snat_exhaustion) - the same wholesale-replaced-every-batch treatment as
	// linkSat right above, and for the same reason: a collector's own counter is a running total since
	// it was loaded (see flow.c's snat_exhaustion doc comment), but this view only ever holds the most
	// recent one reported, never a sum of several.
	snatExhaustion map[string]uint64
	// thermalThrottle is this agent's latest per-node CPU thermal-throttling tracepoint reading
	// (FlowBatch.collectors[].thermal_throttle) - the same wholesale-replaced-every-batch treatment as
	// linkSat/snatExhaustion above, and for the same reason.
	thermalThrottle map[string]*continuumv1.ThermalThrottle

	// The last consistency check: a full picture compared with what the server held.
	consAt    time.Time
	consDrift facts.Drift
	consCount int
}

func NewView() *View {
	return &View{View: agents.View{Self: agents.NewSelfRing(agents.SelfStatsWindow, func(s agents.SelfStatsSample) time.Time { return s.At })}, EstView: EstView{State: facts.New(), Flows: observed.NewFlowTable()}, NetView: connectivity.NetView{Paths: map[string]*connectivity.PathTrack{}, Targets: map[string]connectivity.IssuedTarget{}}}
}

// EndReason is why a stream ends: the words the agent is told, and whether it is told to stop for good.
type EndReason struct {
	text    string
	revoked bool
}

// Session is one agent's live stream.
type Session struct {
	// Cancel ends the stream.
	Cancel func(EndReason)
	// Push carries messages for the stream's own goroutine to send (only it may write to the stream).
	Push chan *continuumv1.ServerMessage
}

// Hub receives agent streams, keeps their facts, and builds the topology the UI reads.
type Hub struct {
	*agents.BaseAgentService
	C   *Service
	Log *slog.Logger

	// FlowStaleAfter, when set, overrides the setting: an observed edge unseen for this long is shown as
	// stale, never deleted.
	FlowStaleAfter time.Duration

	// ConsistencyEvery, when set, overrides the consistency-check setting (tests use seconds).
	ConsistencyEvery time.Duration

	// Geo suggests a location for each agent's connecting address; nil when no database is configured.
	Geo *agents.Geo

	// Limits, when set, replaces the default caps on what one agent may make the server hold (tests use small ones).
	Limits *facts.Limits

	// TombstoneRetention is how long a record that disappeared stays visible as "gone" (0: seven days).
	TombstoneRetention time.Duration

	// Mu guards Views and Sessions. Views (what the Hub holds per agent) and Sessions (the live streams) are exported only for the
	// tests of what stands on the Hub, which put it in a state and read it back; nothing else takes them (the shell asks with
	// ReadStates, Connected and the Hub's other methods).
	Mu       sync.Mutex
	Views    map[string]*View
	Sessions map[string]*Session
	rec      *recorder
	// Tw is the twin's runtime: the registry, the tombstones and the model cache. Exported for the tests that read the cache counters.
	Tw *TwinRT

	// serverSelf is this server process's own self-telemetry history (see SampleSelfStats and
	// ServerSelfStatsSample), sampled by Hub.Run's own ticker - guarded by mu, like views/sessions above.
	serverSelf agents.SelfRing[agents.ServerSelfStatsSample]
	// flow is what SampleSelfStats diffs to derive ServerSelfStatsSample.FlowIngestBytesPerSec.
	flow agents.FlowIngest
}

func NewHub(c *Service) *Hub {
	h := &Hub{BaseAgentService: &agents.BaseAgentService{C: c.Agents}, C: c, Log: c.Log, Views: map[string]*View{}, Sessions: map[string]*Session{}}
	c.Loop.Access, c.Loop.Load = hubAccess{h}, hubLoad{h}
	h.rec = newRecorder(h)
	h.Tw = newTwinRT()
	h.serverSelf = agents.NewSelfRing(0, func(s agents.ServerSelfStatsSample) time.Time { return s.At })
	c.OnWorkspace = h.workspaceChanged
	c.OnRevoke = h.Drop
	c.OnSettings = h.settingsChanged
	c.AppHints = func(ctx context.Context) map[string]string {
		doc, err := h.State(ctx)
		if err != nil {
			return nil
		}
		return HintsOf(doc)
	}
	return h
}

// DropAll ends every live stream, telling the agents to stop for good.
func (h *Hub) DropAll(reason string) {
	h.Mu.Lock()
	ss := make([]*Session, 0, len(h.Sessions))
	for _, s := range h.Sessions {
		ss = append(ss, s)
	}
	h.Mu.Unlock()
	for _, s := range ss {
		s.Cancel(EndReason{reason, true})
	}
}

// ReadStates runs fn with what each agent last reported, by agent id, while it holds the Hub's lock, for a caller outside
// the Hub that wants to copy something out of it. fn must not keep the map or a state, and must not call the Hub.
func (h *Hub) ReadStates(fn func(states map[string]*facts.State)) {
	h.Mu.Lock()
	defer h.Mu.Unlock()
	states := make(map[string]*facts.State, len(h.Views))
	for id, v := range h.Views {
		if v.State != nil {
			states[id] = v.State
		}
	}
	fn(states)
}

// Connected is how many agents have a live stream.
func (h *Hub) Connected() int {
	h.Mu.Lock()
	defer h.Mu.Unlock()
	return len(h.Sessions)
}

// Drop ends a live stream immediately (revocation).
func (h *Hub) Drop(agentID string) {
	h.Tw.gen.Add(1) // the agent's records change state at once: revoked
	h.Mu.Lock()
	s := h.Sessions[agentID]
	h.Mu.Unlock()
	if s != nil {
		// The agent is told why, in the words the administrator gave, so its operator can read it in the pod's log.
		text := "revoked by an administrator"
		if a, err := h.C.Store.GetAgent(bg(), agentID); err == nil && a.Reason != "" {
			text += ": " + a.Reason
		}
		s.Cancel(EndReason{text, true})
	}
}

// Restore reloads the last persisted facts so the UI has data right after a server restart.
func (h *Hub) Restore(ctx context.Context) {
	agentList, err := h.C.Store.ListAgents(ctx, h.C.OrgID)
	if err != nil {
		h.Log.Error("restore failed", "err", err)
		return
	}
	h.twinLoad(ctx)
	for _, a := range agentList {
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
		v := NewView()
		v.State, v.LastSync, v.LastBeat, v.savedAt = st, at, at, at
		if fdata, fat, err := h.C.Store.LoadSnapshot(ctx, FlowsKey(a.ID)); err == nil {
			if ft, err := LoadFlowTable(fdata); err == nil {
				v.Flows, v.flowsSaved = ft, fat
			} else {
				h.Log.Warn("stored flow table dropped", "agent", a.ID, "err", err)
			}
		}
		h.Mu.Lock()
		h.Views[a.ID] = v
		h.Mu.Unlock()
	}
	// Overrides an administrator put on an agent outlive a restart, whether or not the agent has sent a picture yet.
	for _, a := range agentList {
		if a.Status != store.StatusApproved {
			continue
		}
		if data, _, err := h.C.Store.LoadSnapshot(ctx, agents.ConsentKey(a.ID)); err == nil && len(data) > 2 {
			h.Mu.Lock()
			v := h.ViewFor(a.ID)
			h.Mu.Unlock()
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
	agent, ok := agents.AgentFrom(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "no agent")
	}
	ip := agents.PeerIP(ctx)
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
				kernel.Metrics.StreamPanics.Add(1)
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
	ended := make(chan EndReason, 1)
	sess := &Session{Push: make(chan *continuumv1.ServerMessage, 4), Cancel: func(reason EndReason) {
		select {
		case ended <- reason:
		default:
		}
	}}
	h.Mu.Lock()
	if old := h.Sessions[agent.ID]; old != nil {
		old.Cancel(EndReason{"replaced by a newer connection", false})
	}
	h.Sessions[agent.ID] = sess
	v := h.Views[agent.ID]
	if v == nil {
		v = NewView()
		h.Views[agent.ID] = v
	}
	v.Connected = true
	v.Link.Connects++
	v.Link.Since = h.C.Now()
	h.Mu.Unlock()
	defer func() {
		h.Mu.Lock()
		if h.Sessions[agent.ID] == sess {
			delete(h.Sessions, agent.ID)
			v.Connected = false
		}
		h.Mu.Unlock()
		h.persist(context.Background(), agent.ID, true)
		h.persistFlows(context.Background(), agent.ID, true)
		log.Info("agent disconnected")
	}()

	// Revocation may have happened after the interceptor's check but before this session became
	// reachable by Drop; look again now that it is registered, so no revoked agent stays connected.
	fresh, err := h.C.Agents.AuthorizeAgent(ctx, agent.ID)
	if err != nil {
		return agents.ToStatus(err)
	}
	agent.AccessTier = fresh.AccessTier
	if agent.ConnectingIP != "" && agent.ConnectingIP != ip {
		h.C.Audit(ctx, accounts.AgentActor(agent.ID).String(), "agent-address-changed", "agent", agent.ID, "connected from "+ip+", previously "+agent.ConnectingIP)
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
	hello.AgentVersion, hello.KubernetesVersion = kernel.Printable(hello.AgentVersion, 40), kernel.Printable(hello.KubernetesVersion, 40)
	_ = h.C.Store.Touch(ctx, agent.ID, ip, hello.AgentVersion, hello.KubernetesVersion, h.C.Now())
	_ = h.C.Store.ClearPollSecret(ctx, agent.ID)
	log.Info("agent connected", "ip", ip, "version", hello.AgentVersion)
	if hello.Diagnostics != nil {
		h.Mu.Lock()
		v.NoteDiagnostics(hello.Diagnostics, true, h.C.Now())
		h.Mu.Unlock()
	}
	if ns := kernel.Printable(hello.Namespace, 63); ns != "" {
		h.Mu.Lock()
		v.Ext.Namespace = ns
		h.Mu.Unlock()
	}
	if rel := kernel.Printable(hello.ReleaseName, 53); rel != "" { // Helm's own release-name length limit
		h.Mu.Lock()
		v.Ext.ReleaseName = rel
		h.Mu.Unlock()
	}
	h.Mu.Lock()
	v.Ext.Protocol, v.Ext.Caps = hello.ProtocolVersion, agentcaps.Clean(hello.Capabilities)
	h.Mu.Unlock()
	agent = h.NoteCeiling(ctx, agent, fresh, int(hello.InstalledAccessTier))
	if err := stream.Send(&continuumv1.ServerMessage{Msg: &continuumv1.ServerMessage_Config{Config: h.ConfigFor(agent, v)}}); err != nil {
		return err
	}
	h.Mu.Lock()
	v.CfgInForceAt = time.Time{}
	v.CfgSentAt, v.CfgApplied = h.C.Now(), "" // a new stream: what the agent had in force on the last one says nothing about this
	h.Mu.Unlock()
	fullSeen := false                     // a full picture is a consistency check only after the first one of this connection
	var asm *assembly                     // the pieces of a picture that arrives in several messages, until the last one
	limiter := kernel.NewLimiter(120, 20) // per minute, burst
	for {
		select {
		case end := <-ended:
			if end.revoked { // only a real revocation tells the agent to stop for good
				_ = stream.Send(&continuumv1.ServerMessage{Msg: &continuumv1.ServerMessage_Revoked{Revoked: &continuumv1.Revoked{Reason: end.text}}})
			}
			return status.Error(codes.Aborted, end.text)
		case m := <-sess.Push:
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
			h.Mu.Lock()
			v.Link.Note(r.m, h.C.Now())
			h.Mu.Unlock()
			cur, err := h.C.Agents.AuthorizeAgent(ctx, agent.ID) // revocation applies mid-stream too
			if err != nil {
				return agents.ToStatus(err)
			}
			switch m := r.m.Msg.(type) {
			case *continuumv1.AgentMessage_Sync:
				// An agent speaks only for the cluster a human approved.
				if c := m.Sync.Cluster; c != nil && c.Uid != cur.Fingerprint {
					kernel.Metrics.SyncsRefused.Add(1)
					h.C.Audit(ctx, accounts.AgentActor(agent.ID).String(), "sync-refused", "agent", agent.ID, "facts claimed a different cluster than the one approved")
					return status.Error(codes.PermissionDenied, "sync is for a different cluster than the one approved")
				}
				if err := validateSync(m.Sync); err != nil {
					h.refuseSync(ctx, cur, v, err)
					return status.Error(codes.InvalidArgument, err.Error())
				}
				if n := boundSyncFacts(m.Sync); n > 0 {
					h.noteImplausibleFacts(cur.ID, v, n)
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
				drift, checked, err := h.ApplySync(cur, sy, fullSeen)
				if err != nil {
					h.refuseSync(ctx, cur, v, err)
					return status.Error(codes.ResourceExhausted, err.Error())
				}
				kernel.Metrics.SyncsApplied.Add(1)
				if chunked {
					kernel.Metrics.SyncsChunked.Add(1)
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
				h.NoteMeasurements(agent.ID, m.Measurements)
			case *continuumv1.AgentMessage_Heartbeat:
				if err := validateModules(m.Heartbeat.Modules); err != nil {
					h.refuseSync(ctx, cur, v, err)
					return status.Error(codes.InvalidArgument, err.Error())
				}
				skew := min(max(m.Heartbeat.ClockSkewMs, -maxSkewMs), maxSkewMs)
				now := h.C.Now()
				h.Mu.Lock()
				v.LastBeat = now
				if len(m.Heartbeat.Modules) > 0 {
					v.State.Modules = m.Heartbeat.Modules
				}
				v.NoteSelfStats(m.Heartbeat.SelfStats, now)
				v.NoteLinkCaps(m.Heartbeat.LinkCapabilities, now, skew)
				if m.Heartbeat.Diagnostics != nil {
					v.NoteDiagnostics(m.Heartbeat.Diagnostics, false, h.C.Now())
				}
				resend := v.NoteConfigEcho(m.Heartbeat.AppliedConfigDigest, now)
				if asm != nil && asm.expired(h.C.Now()) {
					asm = nil // a picture that never finished arriving
				}
				// The skew is stored only when it moved by more than a second, so a steady clock costs no writes.
				storeSkew := !v.SkewKnown && skew != cur.ClockSkewMs || v.SkewKnown && abs64(skew-v.SkewMs) > 1000
				v.SkewMs, v.SkewKnown = skew, true
				h.Mu.Unlock()
				if resend {
					go h.PushOne(agent.ID)
				}
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

// ApplySync merges facts, first dropping anything above the tier a human approved. When the message
// is a full picture that is not the first of its connection, it also says how far the server's own
// picture had drifted from it (checked is true); applying it afterwards repairs the difference.
func (h *Hub) ApplySync(a store.Agent, s *continuumv1.Sync, fullSeen bool) (drift facts.Drift, checked bool, err error) {
	agents.DropAboveTier(a.AccessTier, s)
	// Captured after the tier ceiling has narrowed s but before an admin's own exclusion list does, so a
	// namespace the admin has asked to leave out is still counted as "reported" - see agents.ViewExt.knownNamespaces.
	reportedNS := make([]string, 0, len(s.Namespaces))
	for _, n := range s.Namespaces {
		reportedNS = append(reportedNS, n.Name)
	}
	h.dropConsentOverrides(a.ID, s)
	h.Mu.Lock()
	defer h.Mu.Unlock()
	v := h.Views[a.ID]
	// Whatever the agent claims, what the server ends up holding for it stays within the limits. Checked
	// before anything is merged, so a refused message changes nothing - knownNamespaces included: it is
	// folded in below, only once the sync is known to be accepted, not here where a Check failure would
	// otherwise let a rejected sync's namespace names through anyway.
	if err := v.State.Check(s, h.limits()); err != nil {
		return drift, false, err
	}
	v.Ext.NoteNamespaces(reportedNS, s.Full)
	if s.Full && fullSeen {
		drift, checked = v.State.CheckDrift(s), true
		v.consAt, v.consDrift = h.C.Now(), drift
		v.consCount++
	}
	h.noteVanished(a, v, s, h.C.Now()) // what this message removes is remembered as a tombstone before it goes
	v.State.Apply(s)
	h.Tw.gen.Add(1)
	v.Refusal = ""
	v.LastSync, v.LastBeat, v.dirtyAt = h.C.Now(), h.C.Now(), h.C.Now()
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
	if h.ConsentOf(agentID).Has("flow") {
		// The agent should already have stopped sending this; the same defense-in-depth agents.DropAboveTier
		// gives the tier ceiling, extended to a paused collector.
		return false, nil
	}
	dropped, err := observed.SanitizeFlowBatch(fb)
	if err != nil {
		return false, err
	}
	// A data-integrity backstop independent of observed.SanitizeFlowBatch's own shape checks above: a
	// physically impossible value (e.g. a saturation percentage outside 0-100) is omitted from just
	// its own field, never the whole batch - see observed.BoundFlowFacts' own doc comment.
	implausible := observed.BoundFlowFacts(fb)
	h.Mu.Lock()
	defer h.Mu.Unlock()
	v := h.Views[agentID]
	if v == nil {
		return false, nil
	}
	if dropped.N > 0 {
		kernel.Metrics.FlowItemsDropped.Add(int64(dropped.N))
		if v.obs.NoteDropped(dropped.N, now) {
			h.Log.Warn("flow batch had items the server cannot use; they were dropped, the rest of the batch kept", "agent", agentID, "dropped", dropped.N, "first_reason", dropped.First)
		}
	}
	if implausible > 0 {
		kernel.Metrics.ImplausibleFacts.Add(int64(implausible))
		if now.Sub(v.ImplausibleLogged) >= observed.RefusalEvery {
			v.ImplausibleLogged = now
			h.Log.Warn("flow batch had a physically impossible value; affected field(s) omitted, rest of the batch kept", "agent", agentID, "count", implausible)
		}
	}
	v.Flows.Apply(fb, now)
	v.obs.Note(fb, now)
	v.podFlows = fb.PodFlows
	linkSat := map[string][]*continuumv1.LinkSaturation{}
	snatExhaustion := map[string]uint64{}
	thermalThrottle := map[string]*continuumv1.ThermalThrottle{}
	for _, c := range fb.Collectors {
		if len(c.LinkSaturation) > 0 {
			linkSat[c.Node] = c.LinkSaturation
		}
		// Only recorded when non-zero, the same "non-empty when something's there" treatment
		// linkSat above gives an empty slice: a node with nothing to report here (no collector, or a
		// collector that never saw an exhausted connect()) simply has no entry, indistinguishable
		// from a node this server has not heard from on this fact at all - which is fine, since the
		// one thing worth surfacing to a person is a node that has seen this happen at least once.
		if c.SnatExhaustion > 0 {
			snatExhaustion[c.Node] = c.SnatExhaustion
		}
		// Same "only when there's something to say" treatment as snatExhaustion above - see
		// ThermalThrottle's own doc comment for why cpu_freq_change_count alone, with no thermal trip,
		// still counts as "something to say" (it corroborates the tracepoint subsystem is alive).
		if c.ThermalThrottle != nil && (c.ThermalThrottle.CpuFreqChangeCount > 0 || c.ThermalThrottle.ThermalTripCount > 0) {
			thermalThrottle[c.Node] = c.ThermalThrottle
		}
	}
	v.linkSat = linkSat
	v.snatExhaustion = snatExhaustion
	v.thermalThrottle = thermalThrottle
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

// refuseSync notes that an agent's message was refused: it is shown with the agent, logged, and (at
// most once per observed.RefusalEvery) recorded as an audit row and a warning event, which is where an
// administrator looks. The agent itself receives the reason as the error that ends its stream.
func (h *Hub) refuseSync(ctx context.Context, a store.Agent, v *View, why error) {
	kernel.Metrics.SyncsRefused.Add(1)
	now := h.C.Now()
	h.Mu.Lock()
	v.Refusal = clipText(why.Error(), 300)
	record := now.Sub(v.RefusalLogged) >= observed.RefusalEvery
	if record {
		v.RefusalLogged = now
	}
	h.Mu.Unlock()
	h.Log.Warn("agent message refused", "agent", a.ID, "name", a.Name, "reason", why.Error())
	if !record {
		return
	}
	h.C.Audit(ctx, accounts.AgentActor(a.ID).String(), "sync-refused", "agent", a.ID, why.Error())
	h.storeEvent(ctx, store.Event{At: now, Kind: "sync-refused", TargetKind: store.TargetAgent, TargetID: a.ID, Name: a.Name, ClusterID: a.ClusterID, ClusterName: a.Name,
		Detail: "The server refused a message from " + a.Name + ": " + clipText(why.Error(), 300), Cause: "the agent reported more, or larger, facts than this server accepts, or something malformed", Severity: store.SeverityWarning})
}

func clipText(s string, n int) string { return kernel.Printable(s, n) }

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

// LoadFlowTable reads a stored flow table under the limits of a live one.
func LoadFlowTable(data []byte) (*observed.FlowTable, error) {
	if len(data) > facts.MaxBytes {
		return nil, fmt.Errorf("the stored flow table is %d bytes, over the limit of %d", len(data), facts.MaxBytes)
	}
	t, err := observed.UnmarshalFlowTable(data)
	if err != nil {
		return nil, err
	}
	if len(t.Edges) > observed.MaxFlowEdges {
		return nil, fmt.Errorf("the stored flow table has %d edges, over the limit of %d", len(t.Edges), observed.MaxFlowEdges)
	}
	for _, e := range t.Edges {
		if err := wire.CheckKey(e.Key); err != nil {
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
	h.storeEvent(ctx, store.Event{At: h.C.Now(), Kind: "drift", TargetKind: store.TargetAgent, TargetID: a.ID, Name: a.Name, ClusterID: a.ClusterID,
		ClusterName: a.Name, Detail: detail, Cause: "a change the agent sent did not reach the server, or the server was restarted between two syncs", Severity: store.SeverityWarning})
}

// Bounds on what one agent may report. A real cluster is far below these; they exist so a
// compromised agent cannot exhaust the server's memory or fill the database.
const (
	maxNodes      = 5000
	maxNamespaces = 5000
	maxWorkloads  = 50000
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
	// An entity must name itself. Everything else is a size, and sizes are cut, not refused (facts.SanitizeSync:
	// every string, map and list in every fact, including fields added to the protocol later, and the same call the
	// agent makes before sending), so a cluster larger than the limits loses detail instead of being refused.
	for _, k := range syncKeys(s) {
		if k == "" || len(k) > maxStr {
			return errors.New("a fact has no key or too long a one")
		}
	}
	return facts.SanitizeSync(s)
}

func syncKeys(s *continuumv1.Sync) []string {
	var keys []string
	for _, n := range s.Nodes {
		keys = append(keys, n.Key)
	}
	for _, n := range s.Namespaces {
		keys = append(keys, n.Key)
	}
	for _, w := range s.Workloads {
		keys = append(keys, w.Key)
	}
	return keys
}

// ---- data-integrity backstop: physically impossible values, independent of the shape checks above ----
//
// validateSync/observed.SanitizeFlowBatch above refuse a message that is malformed - too big, too long, the
// wrong shape. They never ask whether a well-formed value could actually have been measured. A
// negative RTT, a CPU-pressure percentage over 100, or a negative pod count cannot come from any real
// kernel counter or kubelet reading; nothing stops a compromised or merely buggy agent from sending one
// anyway, and nothing before this caught it, so it would otherwise sit in the topology model exactly
// like a real reading, including feeding a placement decision.
//
// boundSyncFacts and observed.BoundFlowFacts below null out only the one field that is impossible, keeping the
// rest of the record - dropping the whole record would hide a real signal (and make a collector bug
// harder to diagnose), and clamping the value to the nearest plausible number would fabricate a reading
// that was never actually taken. Both are the same "absence is a fact, never a fabricated zero"
// discipline this model already applies to a probe that never ran (see HostProbe.cpu_pressure_pct's own
// doc comment), extended to a reading that did arrive but cannot be trusted. Only ever applied to a
// field already represented as a pointer (nil means "not reported"): a field with no such convention
// (Node.CPU, say) has no way to say "omitted" without changing what it means, so it is left alone here.

// boundPct returns p unchanged when it is nil or holds a plausible percentage (0-100 inclusive), and
// nil, with ok true, when it holds a value no real measurement can produce (negative, over 100, or
// NaN) - never clamped to the nearest end of the range, which would report a number nothing measured.
func boundPct(p *float64) (out *float64, dropped bool) {
	if p == nil {
		return nil, false
	}
	if v := *p; v != v || v < 0 || v > 100 {
		return nil, true
	}
	return p, false
}

// boundNonNeg is boundPct's twin for a field with a floor but no ceiling of its own (a power reading,
// in watts): a real measurement is never negative, but nothing here second-guesses how large.
func boundNonNeg(p *float64) (out *float64, dropped bool) {
	if p == nil {
		return nil, false
	}
	if v := *p; v != v || v < 0 {
		return nil, true
	}
	return p, false
}

// boundCount is boundNonNeg's int32 twin, for a pointer count field (PodCount, PendingPodCount): a
// count of pods can never be negative.
func boundCount(p *int32) (out *int32, dropped bool) {
	if p == nil || *p >= 0 {
		return p, false
	}
	return nil, true
}

// boundSyncFacts nulls out any field of s that holds a value no real probe or kubelet reading could
// produce, leaving the rest of every record untouched, and returns how many fields it dropped - see
// this section's own doc comment above for why a field is nulled rather than dropping the record or
// clamping the value.
func boundSyncFacts(s *continuumv1.Sync) int {
	n := 0
	if c := s.Cluster; c != nil {
		if v, dropped := boundCount(c.PendingPodCount); dropped {
			c.PendingPodCount = v
			n++
		}
	}
	for _, nd := range s.Nodes {
		if v, dropped := boundCount(nd.PodCount); dropped {
			nd.PodCount = v
			n++
		}
		p := nd.Probe
		if p == nil {
			continue
		}
		if v, dropped := boundPct(p.CpuPressurePct); dropped {
			p.CpuPressurePct = v
			n++
		}
		if v, dropped := boundPct(p.MemoryPressurePct); dropped {
			p.MemoryPressurePct = v
			n++
		}
		if v, dropped := boundPct(p.IoPressurePct); dropped {
			p.IoPressurePct = v
			n++
		}
		if v, dropped := boundNonNeg(p.HostWatts); dropped {
			p.HostWatts = v
			n++
		}
	}
	return n
}

// noteImplausibleFacts counts n field(s) just nulled out of one agent's report for being physically
// impossible (see boundSyncFacts/observed.BoundFlowFacts) and, at most once per observed.RefusalEvery for this agent,
// warns about it - the same rate-limited-diagnostic treatment refuseSync already gives a full refusal,
// extended to a report that was still accepted and applied, minus the one value nothing could trust.
func (h *Hub) noteImplausibleFacts(agentID string, v *View, n int) {
	kernel.Metrics.ImplausibleFacts.Add(int64(n))
	now := h.C.Now()
	h.Mu.Lock()
	record := now.Sub(v.ImplausibleLogged) >= observed.RefusalEvery
	if record {
		v.ImplausibleLogged = now
	}
	h.Mu.Unlock()
	if record {
		h.Log.Warn("agent report had a physically impossible value; affected field(s) omitted, rest of the report kept", "agent", agentID, "count", n)
	}
}

func validateModules(ms []*continuumv1.ModuleStatus) error {
	if len(ms) > 64 {
		return errors.New("an agent may report at most 64 modules")
	}
	for _, m := range ms {
		if len(m.Name) > 64 {
			return errors.New("module status is malformed")
		}
		m.Reason = kernel.Printable(m.Reason, facts.MaxReason) // raw error text: cut, never a reason to end the stream
	}
	return nil
}

func (h *Hub) persist(ctx context.Context, id string, force bool) {
	h.Mu.Lock()
	v := h.Views[id]
	if v == nil || v.dirtyAt.IsZero() || (!force && time.Since(v.savedAt) < snapshotEvery) || v.dirtyAt.Before(v.savedAt) {
		h.Mu.Unlock()
		return
	}
	snap := v.State.Snapshot() // cheap; encoding it is the slow part, so that happens outside h.mu
	v.savedAt = h.C.Now()
	h.Mu.Unlock()
	data, err := proto.Marshal(snap)
	if err == nil && len(data) > snapshotCap(h.limits()) {
		err = fmt.Errorf("the state is %d bytes, over the limit of %d for what is stored", len(data), snapshotCap(h.limits()))
	} else if err == nil {
		err = h.C.Store.SaveSnapshot(ctx, id, data, h.C.Now())
	}
	if err != nil {
		kernel.Metrics.StoreErrors.Add(1)
		h.Log.Error("snapshot save failed", "agent", id, "err", err)
	}
}

func FlowsKey(agentID string) string { return agentID + "#flows" }

func (h *Hub) persistFlows(ctx context.Context, id string, force bool) {
	h.Mu.Lock()
	v := h.Views[id]
	if v == nil || !v.flowsDirty || (!force && time.Since(v.flowsSaved) < snapshotEvery) {
		h.Mu.Unlock()
		return
	}
	data, err := v.Flows.Marshal()
	v.flowsSaved, v.flowsDirty = h.C.Now(), false
	h.Mu.Unlock()
	if err == nil && len(data) > facts.MaxBytes {
		err = fmt.Errorf("the flow table is %d bytes, over the limit of %d for what is stored", len(data), facts.MaxBytes)
	} else if err == nil {
		err = h.C.Store.SaveSnapshot(ctx, FlowsKey(id), data, h.C.Now())
	}
	if err != nil {
		kernel.Metrics.StoreErrors.Add(1)
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
	// ProtocolVersion and Capabilities are what the agent's last Hello said it speaks and does (see internal/agentcaps). They are
	// kept in memory only, so after a server restart they are 0 and empty until the agent connects again; 0 with a stream that has
	// said hello means an agent from before the version, and 0 without one means nothing is known (the Compatible condition tells
	// the two apart). No screen reads them yet. Capabilities is never null.
	ProtocolVersion uint32   `json:"protocolVersion"`
	Capabilities    []string `json:"capabilities"`
	// Conditions are facts about this agent decided in one place (agents.AgentConditions): whether a stream is open, whether the Config
	// last sent is the one in force, whether its protocol is one this server serves. Computed with the server's clock each time
	// the document is built. No screen reads them yet and nothing the server does depends on them.
	Conditions []agents.Condition `json:"conditions"`
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
	Observer *observed.ObserverDoc `json:"observer,omitempty"`
	// Consistency is the result of the last check of the server's picture against the agent's full one.
	Consistency *ConsistencyDoc `json:"consistency,omitempty"`
	// Measuring is how many addresses the agent has been asked to time (0 when it is not measuring).
	Measuring int `json:"measuring"`
	// Link is what this agent has sent since the server started; absent if it has not connected in that time.
	Link *LinkDoc `json:"link,omitempty"`
	// Scope is the rule the agent applies to which namespaces it reports; absent when it sees them all.
	Scope *agents.ScopeDoc `json:"scope,omitempty"`
	// ClockSkewMs is the agent's clock minus the server's as the agent measured it; the UI warns when it is minutes.
	ClockSkewMs int64 `json:"clockSkewMs,omitempty"`
	// ApprovalAttemptsLeft is how many codes may still be tried for a pending agent that has one.
	ApprovalAttemptsLeft *int `json:"approvalAttemptsLeft,omitempty"`
	// PendingExpiresAt is when a pending enrollment expires if nobody approves it.
	PendingExpiresAt string `json:"pendingExpiresAt,omitempty"`
	// Diagnostics is the agent's own account of itself and Consent the overrides an administrator has put on it. Both are only
	// in the document for people who may change an agent's access; the state handler removes them for everyone else.
	Diagnostics *agents.DiagnosticsDoc `json:"diagnostics,omitempty"`
	Consent     *agents.ConsentDoc     `json:"consent,omitempty"`
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
// already uses - tw.gen, bumped only when a real input changes (ApplySync, a revocation, a saved
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
	// (ApplySync takes it to merge state), so a slow audit-log query sitting inside a locked section
	// would stall live agent traffic behind an unrelated read, for every org sharing this process - not
	// just the one asking for its audit log. Audit freshness and topology freshness are different
	// contracts; this is never cached, unlike the topology pass below.
	var auditEvents []store.AuditEvent
	if withAudit {
		auditEvents, _ = h.C.Store.ListAudit(ctx, h.C.OrgID, 50)
	}

	agentDocs, agentList, err := h.buildAgentDocs(ctx, now)
	if err != nil {
		return StateDoc{}, err
	}

	tw := h.Tw
	gen := tw.gen.Load()
	tw.mu.Lock()
	if c := tw.stateCache; c != nil && c.gen == gen && c.stale == window && now.Sub(c.at) >= 0 && now.Sub(c.at) < modelCacheTTL {
		topology, tombstones, observation, eb := c.topology, c.tombstones, c.observation, c.eb
		tw.mu.Unlock()
		return h.finishState(agentDocs, topology, tombstones, observation, eb, now, auditEvents, after), nil
	}
	tw.mu.Unlock()

	topology, tombstones, observation, eb, err := h.buildTopology(ctx, agentList, now, window)
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
	doc := StateDoc{GeneratedAt: kernel.RFC(now), Agents: agentDocs, Topology: topology, Tombstones: tombstones, Observation: observation}
	doc.AuditLog = make([]AuditDoc, 0, len(auditEvents))
	for i := len(auditEvents) - 1; i >= 0; i-- {
		e := auditEvents[i]
		doc.AuditLog = append(doc.AuditLog, AuditDoc{ID: "au-" + strconv.FormatInt(e.ID, 10), OrgID: e.OrgID, At: kernel.RFC(e.At), Actor: e.Actor, Action: e.Action, TargetKind: e.TargetKind, TargetID: e.TargetID, Detail: e.Detail})
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
	agentList, err := h.C.Store.ListAgents(ctx, h.C.OrgID)
	if err != nil {
		return nil, nil, err
	}
	h.Mu.Lock()
	defer h.Mu.Unlock()
	agentDocs := make([]AgentDoc, 0, len(agentList))
	for _, a := range agentList {
		v := h.Views[a.ID]
		d := AgentDoc{
			ID: a.ID, OrgID: a.OrgID, Name: a.Name, ClusterID: a.ClusterID, Version: a.Version, K8sVersion: a.K8sVersion,
			AccessTier: a.AccessTier, InstalledTier: a.InstalledTier, TierCap: a.TierCap, Status: string(a.Status), Fingerprint: a.Fingerprint,
			ConnectingIP: a.ConnectingIP, CertExpiresAt: kernel.RFCP(a.LeafNotAfter), LastHeartbeat: kernel.RFCP(a.LastSeen), RequestedAt: kernel.RFC(a.CreatedAt),
			Reason: a.Reason, Modules: []ModuleDoc{}, ClockSkewMs: a.ClockSkewMs,
		}
		if a.Status == store.StatusPending {
			left := max(kernel.MaxApprovalAttempts-a.ApprovalAttempts, 0)
			d.ApprovalAttemptsLeft = &left
			d.PendingExpiresAt = kernel.RFC(a.CreatedAt.Add(h.C.Agents.EffectivePendingTTL()))
		}
		d.ConnectingGeo = h.Geo.Locate(a.ConnectingIP)
		if d.ConnectingGeo == nil {
			d.ConnectingGeoReason = h.Geo.UnlocatableReason(a.ConnectingIP)
		}
		if v != nil {
			d.Namespace, d.ReleaseName = v.Ext.Namespace, v.Ext.ReleaseName
			d.ProtocolVersion, d.Capabilities = v.Ext.Protocol, append([]string{}, v.Ext.Caps...)
		}
		if d.Capabilities == nil {
			d.Capabilities = []string{}
		}
		d.Conditions = agents.AgentConditions(condInputsOf(v), h.C.Now())
		if a.Status == store.StatusRevoked || a.Status == store.StatusRejected {
			if helm, secret, guessed, err := agents.TeardownCommands(d.Namespace, d.ReleaseName); err != nil {
				h.C.Log.Error("the commands that remove an agent cannot be written", "err", err) // the agent is shown without them
			} else {
				d.Teardown = &TeardownDoc{Helm: helm, Secret: secret, NamespaceGuessed: guessed}
			}
		}
		if v != nil {
			d.Connected = v.Connected
			d.Synced = !v.LastSync.IsZero()
			if !v.LastBeat.IsZero() {
				d.LastHeartbeat = kernel.RFC(v.LastBeat)
			}
			if v.Link.Connects > 0 {
				l := &LinkDoc{Connects: v.Link.Connects, Bytes: v.Link.Bytes, Syncs: v.Link.Syncs, Flows: v.Link.Flows, Measurements: v.Link.Meas, Heartbeats: v.Link.Beats}
				if v.Connected {
					l.ConnectedSince = kernel.RFC(v.Link.Since)
				}
				if !v.Link.LastSync.IsZero() {
					l.LastSync = kernel.RFC(v.Link.LastSync)
				}
				if !v.Link.LastFlows.IsZero() {
					l.LastFlows = kernel.RFC(v.Link.LastFlows)
				}
				d.Link = l
			}
			if sc := v.State.Cluster.GetScope(); sc != nil {
				d.Scope = &agents.ScopeDoc{Description: sc.Description, Total: int(sc.NamespacesTotal), InScope: int(sc.NamespacesInScope)}
			}
			d.Observer = v.obs.Doc(h.C.Now())
			if a.Status == store.StatusApproved {
				d.Diagnostics = v.diagDoc()
				d.Consent = v.consentDoc()
			}
			d.Measuring = len(v.Targets)
			if v.consCount > 0 {
				d.Consistency = &ConsistencyDoc{LastCheck: kernel.RFC(v.consAt), Checks: v.consCount, Differences: v.consDrift.Total()}
				if v.consDrift.Total() > 0 {
					d.Consistency.Summary = v.consDrift.String()
				}
			}
			for _, m := range v.State.Modules {
				d.Modules = append(d.Modules, ModuleDoc{Name: m.Name, Status: map[continuumv1.ModuleStatus_State]string{
					continuumv1.ModuleStatus_OK: "ok", continuumv1.ModuleStatus_SKIPPED: "skipped", continuumv1.ModuleStatus_ERROR: "error"}[m.State], Reason: m.Reason})
			}
			if v.Refusal != "" { // why nothing new is arriving from this agent
				d.Modules = append(d.Modules, ModuleDoc{Name: "server limits", Status: "error", Reason: v.Refusal})
			}
		}
		agentDocs = append(agentDocs, d)
	}
	sort.Slice(agentDocs, func(i, j int) bool { return agentDocs[i].RequestedAt < agentDocs[j].RequestedAt })
	return agentDocs, agentList, nil
}

// buildTopology does the expensive, h.mu-guarded work of interpreting every approved agent's facts,
// assessing staleness, and correlating flows/tunnels/cluster-links - exactly what stateFor used to do
// inline, together with the agent-doc loop, on every single call. stateFor above caches this against
// tw.gen; this function itself knows nothing about caching, so it is always a real, full rebuild. Takes
// the agent list buildAgentDocs already fetched, rather than fetching it again.
func (h *Hub) buildTopology(ctx context.Context, agentList []store.Agent, now time.Time, window time.Duration) (model.Topology, []TombstoneDoc, twin.ObservationDoc, *effectiveBuild, error) {
	eb := newEffectiveBuild(window)
	topo := model.Topology{Clusters: []model.Cluster{}, Nodes: []model.Node{}, Namespaces: []model.Namespace{}, Services: []model.Service{}, Agents: []model.Agent{}, Suggestions: []model.Suggestion{},
		Dependencies: []model.Dependency{}, ExternalEndpoints: []model.ExternalEndpoint{}, Paths: []model.Path{}, ClusterLinks: []model.ClusterLink{}, ClusterPairConnectivity: []model.ClusterPairConnectivity{}}
	var onboarded, located []observed.Cluster
	h.Mu.Lock()
	defer h.Mu.Unlock()
	showRevoked := h.revokedToShow(agentList)
	for _, a := range agentList {
		v := h.Views[a.ID]
		revoked := showRevoked[a.ID]
		if (a.Status != store.StatusApproved && !revoked) || v == nil || v.LastSync.IsZero() {
			continue
		}
		recs := h.nodeRecords(a.ClusterID, v.State, now)
		// The agent's own most recent self-telemetry sample, read straight off its ring (see
		// SelfStatsFor's own doc for the exported equivalent of this same read) - nil until its first
		// heartbeat carrying SelfStats arrives, never fabricated.
		var selfStats *model.AgentSelfStats
		if hist := v.Self.List(); len(hist) > 0 {
			last := hist[len(hist)-1]
			selfStats = &model.AgentSelfStats{T: kernel.RFC(last.At), RSSBytes: last.RSSBytes, Goroutines: last.Goroutines}
		}
		t := interpret.Interpret(interpret.Input{OrgID: h.C.OrgID, AgentID: a.ID, ClusterID: a.ClusterID, Name: a.Name, State: v.State, Now: v.LastSync, AccessTier: a.AccessTier, NodeIDs: nodeIDMap(recs), PodFlows: v.podFlows, LinkSaturation: v.linkSat, SnatExhaustion: v.snatExhaustion, ThermalThrottle: v.thermalThrottle, SelfStats: selfStats})
		var revokedAt time.Time
		if a.RevokedAt != nil {
			revokedAt = *a.RevokedAt
		}
		obs := twin.Assess(twin.AssessInput{Now: now, LastObserved: v.LastBeat, Connected: v.Connected, Revoked: revoked, RevokedAt: revokedAt, StaleAfter: window})
		markObservation(&t, obs, recs)
		h.reviveSeen(t)
		eb.agents[a.ID] = twin.AgentInfo{ID: a.ID, Name: a.Name, ClusterID: a.ClusterID, Tier: a.AccessTier}
		eb.obs[a.ID] = obs
		eb.facts[a.ClusterID] = v.State
		for _, r := range recs {
			eb.nodes[r.ID] = r
		}
		topo.Clusters = append(topo.Clusters, t.Clusters...)
		topo.Nodes = append(topo.Nodes, t.Nodes...)
		topo.Namespaces = append(topo.Namespaces, t.Namespaces...)
		topo.Services = append(topo.Services, t.Services...)
		topo.Agents = append(topo.Agents, t.Agents...)
		topo.Suggestions = append(topo.Suggestions, t.Suggestions...)
		if revoked {
			continue // a revoked agent's picture is kept to be shown, and is never used to draw traffic or place anything
		}
		oc := observed.Cluster{ID: a.ClusterID, Name: a.Name, AgentID: a.ID, State: v.State, Flows: v.Flows, EgressIP: publicIP(a.ConnectingIP)}
		located = append(located, oc)
		if a.AccessTier >= 2 && v.Flows != nil {
			onboarded = append(onboarded, oc)
		}
	}
	if len(onboarded) > 0 {
		topo.Dependencies, topo.ExternalEndpoints = observed.Topology(h.C.OrgID, onboarded, now, h.StaleFlows())
		// Needs every cluster's services already collected into topo.Services above (their Mesh facts
		// come from each agent's own interpret.Interpret, never seen by observed.Topology itself) - see
		// observed.ApplyMeshBypassFacts' own doc comment for why this cross-check cannot happen any earlier.
		observed.ApplyMeshBypassFacts(topo.Dependencies, topo.Services)
		topo.Suggestions = append(topo.Suggestions, agents.Suspicions(h.C.OrgID, onboarded, located, now)...)
	}
	// Needs every cluster's nodes in one list (a tunnel's other end is often in a different cluster, or
	// this very same one), so this runs once here rather than per cluster inside the loop above.
	connectivity.CorrelateTunnels(topo.Nodes)
	names := map[string]string{}
	for _, c := range topo.Clusters {
		names[c.ID] = c.Name
	}
	// So connectivity.CorrelateClusterLinks can tell which cluster a dependency's calling service belongs to, for its
	// own network-health rollup (FlowsObserved/AvgRttMs/AvgLossPct) - built here rather than inside it,
	// keeping that function pure data matching with no topology-wide bookkeeping of its own.
	serviceClusterID := map[string]string{}
	for _, s := range topo.Services {
		serviceClusterID[s.ID] = s.ClusterID
	}
	// Same reasoning as connectivity.CorrelateTunnels just above: a cluster-pair relationship can only be seen once
	// every cluster's nodes are in one list, so this also runs once here, after names is built.
	topo.ClusterLinks = connectivity.CorrelateClusterLinks(topo.Nodes, names, topo.Dependencies, serviceClusterID)
	// Builds on topo.ClusterLinks just computed above - see connectivity.CorrelateClusterPairConnectivity's own doc
	// for why it takes that output as-is rather than recomputing the tunnel/subnet correlation itself.
	topo.ClusterPairConnectivity = connectivity.CorrelateClusterPairConnectivity(topo.Nodes, names, topo.Dependencies, serviceClusterID, topo.ClusterLinks)
	topo.Paths = h.pathDocs(agentList, located, names, now)
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
	return observed.DefaultStaleWindow
}

// publicIP returns ip when it is a public address (the only kind that identifies a cluster from outside).
func publicIP(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
		return ""
	}
	return a.String()
}
