package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"continuum/internal/accounts"
	"continuum/internal/kernel"
	"continuum/internal/store"
)

// The decision loop for agent access (design note: slice 3). An advisor looks at what the server knows and proposes a change
// to one agent's access; a person decides; an accepted change is made through the same SetAgentTier and SetConsent a person
// would call, so delivery, validation, audit and the push to the agent are identical to a manual change. Nothing here lets
// an advisor change anything: submitting stores a pending Proposal and nothing else.

const (
	defaultProposalTTL = 4 * time.Hour
	MaxProposalTTL     = 72 * time.Hour
	// maxPendingPerAgent and maxPendingPerOrg bound what an advisor (or a bug in one) can leave waiting for a person.
	MaxPendingPerAgent = 20
	maxPendingPerOrg   = 200
	maxEvidenceFacts   = 20
	maxAdvisorName     = 64
	maxProposalNote    = 200
)

// AccessChange is a typed change to one agent's access, never free text. Exactly one of two kinds: a new approved tier, or
// changes to the agent's overrides given as additions and removals (so they apply to what the agent has when the change is
// accepted, not to what it had when it was proposed).
type AccessChange struct {
	AgentID string   `json:"agentId"`
	Tier    *int     `json:"tier,omitempty"`
	Pause   []string `json:"pause,omitempty"`
	Resume  []string `json:"resume,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	Include []string `json:"include,omitempty"`
}

// The classes of evidence, strongest first. A Proposal's confidence is the weakest class among its facts.
var evidenceClasses = []string{"measured", "derived", "declared", "unknown"}

// EvidenceFact is one fact an advisor used, with how it knows it and how old it is.
type EvidenceFact struct {
	Name       string `json:"name"`
	Value      string `json:"value,omitempty"`
	Class      string `json:"class"`
	ObservedAt string `json:"observedAt,omitempty"`
	Source     string `json:"source,omitempty"`
}

// outcomeMetrics are the things an advisor may predict about a change and the server can later read for the agent. A value is
// the expected relative change: -0.5 halves the metric, 0.2 raises it by a fifth.
var outcomeMetrics = map[string]bool{"link_bytes_per_sec": true, "agent_cpu_cores": true, "agent_rss_bytes": true}

// ProposalInput is what an advisor submits.
type ProposalInput struct {
	Advisor string
	// ModelVersion is the version of the effective model the advisor reasoned over; it is recorded, not checked.
	ModelVersion int64
	Change       AccessChange
	Evidence     []EvidenceFact
	Predicted    map[string]float64
	// TTL is how long the proposal may wait for a decision; zero means defaultProposalTTL.
	TTL time.Duration
}

// proposalBlast is what a change touches, worked out by the server from the change and the agent, not supplied by the advisor.
type proposalBlast struct {
	Agents     int      `json:"agents"`
	ClusterID  string   `json:"clusterId,omitempty"`
	TierFrom   *int     `json:"tierFrom,omitempty"`
	TierTo     *int     `json:"tierTo,omitempty"`
	Collectors []string `json:"collectors,omitempty"`
	Namespaces int      `json:"namespaces,omitempty"`
}

// unionStrings is a followed by what b has that a does not, in order.
func unionStrings(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, x := range b {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func describeChange(a store.Agent, ch AccessChange) string {
	var parts []string
	if ch.Tier != nil {
		parts = append(parts, fmt.Sprintf("access tier %d to %d", a.AccessTier, *ch.Tier))
	}
	for _, c := range []struct {
		verb string
		list []string
	}{{"pause", ch.Pause}, {"resume", ch.Resume}, {"exclude", ch.Exclude}, {"include", ch.Include}} {
		if len(c.list) > 0 {
			parts = append(parts, c.verb+" "+strings.Join(c.list, ", "))
		}
	}
	return fmt.Sprintf("%q: %s", a.Name, strings.Join(parts, "; "))
}

func cleanEvidence(in []EvidenceFact) ([]EvidenceFact, string, error) {
	if len(in) > maxEvidenceFacts {
		return nil, "", kernel.Errf(kernel.KindInvalid, "at most %d facts of evidence", maxEvidenceFacts)
	}
	weakest := 0
	out := make([]EvidenceFact, 0, len(in))
	for _, f := range in {
		f.Name, f.Value, f.Source = kernel.Printable(strings.TrimSpace(f.Name), 80), kernel.Printable(f.Value, 200), kernel.Printable(f.Source, 80)
		f.Class = strings.ToLower(strings.TrimSpace(f.Class))
		rank := -1
		for i, c := range evidenceClasses {
			if c == f.Class {
				rank = i
			}
		}
		if f.Name == "" || rank < 0 {
			return nil, "", kernel.Errf(kernel.KindInvalid, "each fact needs a name and a class of measured, derived, declared or unknown")
		}
		if f.ObservedAt != "" {
			if _, err := time.Parse(time.RFC3339, f.ObservedAt); err != nil {
				return nil, "", kernel.Errf(kernel.KindInvalid, "a fact's observedAt must be an RFC 3339 time")
			}
		}
		weakest = max(weakest, rank)
		out = append(out, f)
	}
	conf := "unknown"
	if len(out) > 0 {
		conf = evidenceClasses[weakest]
	}
	return out, conf, nil
}

func cleanPredicted(in map[string]float64) error {
	for k, v := range in {
		if !outcomeMetrics[k] {
			names := make([]string, 0, len(outcomeMetrics))
			for m := range outcomeMetrics {
				names = append(names, m)
			}
			sort.Strings(names)
			return kernel.Errf(kernel.KindInvalid, "%q is not a metric a prediction can name (%s)", kernel.Printable(k, 40), strings.Join(names, ", "))
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || v < -1 || v > 100 {
			return kernel.Errf(kernel.KindInvalid, "a predicted change is a relative amount between -1 (gone) and 100")
		}
	}
	return nil
}

func newProposalID() string { return "pr-" + kernel.RandHex(6) }

// SubmitProposal stores a pending Proposal from an advisor. It validates the change against the agent as it is now and
// computes whether it widens; the advisor cannot say.
func (h *Hub) SubmitProposal(ctx context.Context, actor string, in ProposalInput) (store.Proposal, error) {
	h.expireDueProposals(ctx)
	advisor := kernel.Printable(strings.TrimSpace(in.Advisor), maxAdvisorName)
	if advisor == "" {
		return store.Proposal{}, kernel.Errf(kernel.KindInvalid, "name the advisor that made this proposal")
	}
	if in.ModelVersion < 0 {
		return store.Proposal{}, kernel.Errf(kernel.KindInvalid, "the model version cannot be negative")
	}
	a, err := h.C.AgentInOrg(ctx, in.Change.AgentID)
	if err != nil {
		return store.Proposal{}, err
	}
	if a.Status != store.StatusApproved {
		return store.Proposal{}, kernel.Errf(kernel.KindConflict, "agent is %s: only an approved agent's access can be proposed for change", a.Status)
	}
	plan, err := h.planAccessChange(a, in.Change)
	if err != nil {
		return store.Proposal{}, err
	}
	widens := plan.Widens
	facts, conf, err := cleanEvidence(in.Evidence)
	if err != nil {
		return store.Proposal{}, err
	}
	if err := cleanPredicted(in.Predicted); err != nil {
		return store.Proposal{}, err
	}
	ttl := in.TTL
	if ttl <= 0 {
		ttl = defaultProposalTTL
	}
	if ttl > MaxProposalTTL {
		return store.Proposal{}, kernel.Errf(kernel.KindInvalid, "a proposal can wait at most %s for a decision", MaxProposalTTL)
	}
	pending, err := h.C.Store.ListProposals(ctx, h.C.OrgID, store.ProposalQuery{Status: store.ProposalPending, Limit: 1000})
	if err != nil {
		return store.Proposal{}, err
	}
	forAgent := 0
	for _, p := range pending {
		if p.AgentID == a.ID {
			forAgent++
		}
	}
	if forAgent >= MaxPendingPerAgent || len(pending) >= maxPendingPerOrg {
		return store.Proposal{}, kernel.Errf(kernel.KindConflict, "too many proposals are waiting for a decision; decide some first")
	}
	blast := proposalBlast{Agents: 1, ClusterID: a.ClusterID, Collectors: unionStrings(in.Change.Pause, in.Change.Resume), Namespaces: len(in.Change.Exclude) + len(in.Change.Include)}
	if in.Change.Tier != nil {
		from := a.AccessTier
		blast.TierFrom, blast.TierTo = &from, in.Change.Tier
	}
	change, _ := json.Marshal(in.Change)
	evidence, _ := json.Marshal(facts)
	blastJSON, _ := json.Marshal(blast)
	predicted, _ := json.Marshal(in.Predicted)
	now := h.C.Now()
	p := store.Proposal{ID: newProposalID(), OrgID: h.C.OrgID, AgentID: a.ID, Advisor: advisor, ModelVersion: in.ModelVersion,
		Change: change, Evidence: evidence, Confidence: conf, Blast: blastJSON, Predicted: predicted, Widens: widens, Mode: "advise",
		Status: store.ProposalPending, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	detail := fmt.Sprintf("advisor %s proposes for %s (widens: %t, confidence: %s)", advisor, describeChange(a, in.Change), widens, conf)
	if err := h.C.AuditedWrite(h.C.Store.CreateProposal(ctx, p, store.WithAudit(h.C.AuditRow(h.C.OrgID, actor, "proposal-submitted", "proposal", p.ID, detail))), "proposal-submitted"); err != nil {
		return store.Proposal{}, err
	}
	return p, nil
}

// expireDueProposals marks the pending proposals whose time has passed. It is called before anything reads or decides them, so a
// proposal past its expiry can never be accepted and a list never shows one as pending.
func (h *Hub) expireDueProposals(ctx context.Context) {
	pending, err := h.C.Store.ListProposals(ctx, h.C.OrgID, store.ProposalQuery{Status: store.ProposalPending, Limit: 1000})
	if err != nil {
		return
	}
	now := h.C.Now()
	for _, p := range pending {
		if now.Before(p.ExpiresAt) {
			continue
		}
		err := h.C.Store.TransitionProposal(ctx, h.C.OrgID, p.ID, []store.ProposalStatus{store.ProposalPending}, store.ProposalExpired, accounts.SystemActor.String(), "no decision before it expired", now,
			store.WithAudit(h.C.AuditRow(h.C.OrgID, accounts.SystemActor.String(), "proposal-expired", "proposal", p.ID, "no decision before "+p.ExpiresAt.UTC().Format(time.RFC3339))))
		if err != nil && !errors.Is(err, store.ErrBadState) {
			h.Log.Warn("a proposal could not be marked expired", "proposal", p.ID, "err", err)
		}
	}
}

// ListProposals returns the organisation's proposals, newest first, after expiring the ones past their time.
func (h *Hub) ListProposals(ctx context.Context, q store.ProposalQuery) ([]store.Proposal, error) {
	h.expireDueProposals(ctx)
	return h.C.Store.ListProposals(ctx, h.C.OrgID, q)
}

// GetProposal returns one proposal, with its Outcome when it has one.
func (h *Hub) GetProposal(ctx context.Context, id string) (store.Proposal, *store.ProposalOutcome, error) {
	h.expireDueProposals(ctx)
	p, err := h.C.Store.GetProposal(ctx, h.C.OrgID, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Proposal{}, nil, kernel.Errf(kernel.KindNotFound, "no such proposal")
	}
	if err != nil {
		return store.Proposal{}, nil, err
	}
	o, err := h.C.Store.GetProposalOutcome(ctx, h.C.OrgID, id)
	if errors.Is(err, store.ErrNotFound) {
		return p, nil, nil
	}
	if err != nil {
		return store.Proposal{}, nil, err
	}
	return p, &o, nil
}

func (h *Hub) decideTransition(ctx context.Context, p store.Proposal, from []store.ProposalStatus, to store.ProposalStatus, by, note, action string) error {
	detail := fmt.Sprintf("proposal %s from advisor %s: %s", p.ID, p.Advisor, string(to))
	if note != "" {
		detail += " (" + note + ")"
	}
	return h.C.AuditedWrite(h.C.Store.TransitionProposal(ctx, h.C.OrgID, p.ID, from, to, by, note, h.C.Now(),
		store.WithAudit(h.C.AuditRow(h.C.OrgID, by, action, "proposal", p.ID, detail))), action)
}

func cleanNote(note string) (string, error) {
	note = kernel.Printable(strings.TrimSpace(note), maxProposalNote+1)
	if len(note) > maxProposalNote {
		return "", kernel.Errf(kernel.KindInvalid, "a note is at most %d characters", maxProposalNote)
	}
	return note, nil
}

// RejectProposal ends a pending proposal without changing anything.
func (h *Hub) RejectProposal(ctx context.Context, actor, id, note string) (store.Proposal, error) {
	note, err := cleanNote(note)
	if err != nil {
		return store.Proposal{}, err
	}
	p, _, err := h.GetProposal(ctx, id)
	if err != nil {
		return p, err
	}
	err = h.decideTransition(ctx, p, []store.ProposalStatus{store.ProposalPending}, store.ProposalRejected, actor, note, "proposal-rejected")
	if errors.Is(err, store.ErrBadState) {
		return p, kernel.Errf(kernel.KindConflict, "the proposal is no longer pending")
	}
	if err != nil {
		return p, err
	}
	p, _, err = h.GetProposal(ctx, id)
	return p, err
}

// AcceptProposal decides a pending proposal in favour and makes the change. The agent is read again first and the change is
// checked against it as it is now, so a proposal made against an old state cannot do what was never asked: it may no longer be
// valid, or may now widen what it narrowed. Widening takes an administrator. Of two people accepting at once, one wins; the
// other is told it is no longer pending. The change is made by SetAgentTier or SetConsent, so it is delivered, validated and
// audited exactly as a manual one is, and the proposal ends applied, or failed if that step did not complete.
func (h *Hub) AcceptProposal(ctx context.Context, actor, id, note string) (store.Proposal, error) {
	note, err := cleanNote(note)
	if err != nil {
		return store.Proposal{}, err
	}
	p, _, err := h.GetProposal(ctx, id)
	if err != nil {
		return p, err
	}
	if p.Status != store.ProposalPending {
		return p, kernel.Errf(kernel.KindConflict, "the proposal is %s, not pending", p.Status)
	}
	var ch AccessChange
	if err := json.Unmarshal(p.Change, &ch); err != nil {
		return p, kernel.Errf(kernel.KindInvalid, "the stored change could not be read")
	}
	a, err := h.C.AgentInOrg(ctx, p.AgentID)
	if err != nil {
		return p, err
	}
	if a.Status != store.StatusApproved {
		return p, kernel.Errf(kernel.KindConflict, "agent is %s: only an approved agent's access can be changed", a.Status)
	}
	plan, err := h.planAccessChange(a, ch)
	if err != nil {
		return p, err
	}
	if !accounts.Decide(accounts.CallerFrom(ctx), accounts.ActChangeAccess, accounts.Resource{Widening: plan.Widens}).Allowed {
		return p, kernel.Errf(kernel.KindForbidden, "this change widens what the agent shares, and accepting it takes an administrator; narrowing does not")
	}
	// Claim the proposal first. Only one caller can move it out of pending, so only one makes the change.
	if err := h.decideTransition(ctx, p, []store.ProposalStatus{store.ProposalPending}, store.ProposalAccepted, actor, note, "proposal-accepted"); err != nil {
		if errors.Is(err, store.ErrBadState) {
			return p, kernel.Errf(kernel.KindConflict, "the proposal is no longer pending")
		}
		return p, err
	}
	applyErr := plan.Apply(ctx, actor)
	final, finalAction, finalNote := store.ProposalApplied, "proposal-applied", ""
	if applyErr != nil {
		final, finalAction, finalNote = store.ProposalFailed, "proposal-failed", kernel.Printable(applyErr.Error(), maxProposalNote)
	}
	if err := h.decideTransition(ctx, p, []store.ProposalStatus{store.ProposalAccepted}, final, actor, finalNote, finalAction); err != nil {
		h.Log.Error("a proposal's final state could not be recorded", "proposal", p.ID, "err", err)
	}
	got, _, gerr := h.GetProposal(ctx, id)
	if gerr != nil {
		return p, gerr
	}
	if applyErr != nil {
		return got, applyErr
	}
	return got, nil
}
