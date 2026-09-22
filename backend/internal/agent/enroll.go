package agent

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/approval"
	"continuum/internal/pki"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// How the agent gets in: it makes a key and a random approval code, saves both, and only then asks the
// server to enroll it (sending a hash of the code, never the code). It prints the code in its own log.
// An administrator who can read that log types the code in Continuum to approve the cluster.
//
// Everything before the certificate arrives can be repeated safely: the server recognises the same token
// with the same key as the same enrollment. So the agent may crash or lose a response at any point and
// simply start again from what it saved.

const (
	// codeReminder is how often the approval code is printed again while the agent waits, so it is
	// still in the log tail (and in front of whoever looks) on a long wait.
	codeReminder = 30 * time.Minute
	// endedHold is how long a revoked agent waits before it exits, at least and at most; it is chosen at
	// random within the range so that many agents do not all wake at once.
	endedHoldMin, endedHoldMax = 5 * time.Minute, 10 * time.Minute
	// skewWarnAfter is the clock difference that is worth a warning, and skewWarnEvery how often it is repeated.
	skewWarnAfter = 2 * time.Minute
	skewWarnEvery = time.Hour
)

// ExitRevoked is the process exit code of an agent whose identity was revoked or rejected (Run returned
// ErrRevoked). It is distinct from 1 (a failure) and 2 (bad usage), so it shows in the pod status.
const ExitRevoked = 3

// extra is the state of the enrollment and clock handling that lives alongside the runner.
type extra struct {
	mu          sync.Mutex
	skew        time.Duration // the agent's clock minus the server's, when known
	skewKnown   bool
	skewWarned  time.Time
	endedLogged time.Time
}

// tokenID identifies an enrollment token without keeping it: a prefix of its hash.
func tokenID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

func (r *runner) logCode(id *Identity, why string) {
	// The code leads the line, right after a fixed short label, and is also its own field: someone
	// scanning `kubectl logs` output should be able to spot it without reading the rest of the sentence.
	r.log.Info("APPROVAL CODE: "+id.ApprovalCode+" (enter it in Continuum to approve this cluster)",
		"code", id.ApprovalCode, "why", why, "kubectl", "kubectl -n continuum-system logs deploy/continuum-agent | grep -i 'approval code'")
}

// enroll takes the agent from "no certificate" to "approved": it sends the enrollment (again, if it was
// interrupted) and waits for an administrator. If the request expires unapproved it starts over with a
// new code, on its own.
func (r *runner) enroll(ctx context.Context, id *Identity) (*Identity, error) {
	var hint time.Duration
	for {
		var err error
		if id == nil || !id.Enrolled() {
			if id, hint, err = r.sendEnrollment(ctx, id); err != nil {
				return nil, err
			}
		} else if id.ApprovalCode != "" {
			// Restarted while waiting: the same code again, so whoever looks at the log sees it.
			r.logCode(id, "still waiting after a restart")
		}
		next, expired, err := r.awaitApproval(ctx, id, hint)
		if err != nil {
			return nil, err
		}
		if !expired {
			return next, nil
		}
		id, hint = next, 0
	}
}

// sendEnrollment makes sure a key and a code are saved, then calls Enroll and saves who the server says
// the agent is. It can be called again after any failure.
func (r *runner) sendEnrollment(ctx context.Context, id *Identity) (*Identity, time.Duration, error) {
	if r.cfg.Token == "" {
		return nil, 0, errors.New("not enrolled and no enrollment token was given")
	}
	fp, kver, err := r.clusterIdentity(ctx)
	if err != nil {
		return nil, 0, err
	}
	if id == nil || id.Key == nil || id.ApprovalCode == "" {
		var key *ecdsa.PrivateKey // a key saved without a code is kept: it is only the code that is missing
		if id != nil {
			key = id.Key
		}
		if key == nil {
			var kerr error
			if key, kerr = NewKey(); kerr != nil {
				return nil, 0, kerr
			}
		}
		code, cerr := approval.New()
		if cerr != nil {
			return nil, 0, cerr
		}
		id = &Identity{Key: key, ApprovalCode: code, TokenID: tokenID(r.cfg.Token)}
		// Saved before anything is sent, so what the server hears about is never something the agent forgot.
		if err := r.cfg.Identity.Save(ctx, id); err != nil {
			return nil, 0, fmt.Errorf("could not store the pending enrollment, refusing to enroll without being able to remember it: %w", err)
		}
	}
	r.logCode(id, "waiting for an administrator")
	csr, err := NewCSR(id.Key)
	if err != nil {
		return nil, 0, err
	}
	hash, err := approval.Hash(id.ApprovalCode, csr)
	if err != nil {
		return nil, 0, err
	}
	conn, err := r.dial(&Identity{})
	if err != nil {
		return nil, 0, err
	}
	defer conn.Close()
	resp, err := continuumv1.NewEnrollmentClient(conn).Enroll(ctx, &continuumv1.EnrollRequest{
		Token: r.cfg.Token, CsrDer: csr, ClusterFingerprint: fp, AgentVersion: r.cfg.Version, KubernetesVersion: kver,
		InstalledAccessTier: uint32(r.cfg.Tier), ApprovalCodeHash: hash,
	})
	switch status.Code(err) {
	case codes.OK:
	case codes.Unauthenticated:
		return nil, 0, r.ended(ctx, id, "the enrollment token is invalid, expired or already used")
	case codes.PermissionDenied:
		return nil, 0, r.ended(ctx, id, "the enrollment token was created for a different cluster")
	default:
		return nil, 0, fmt.Errorf("enroll: %w", err)
	}
	r.noteServerTime(resp.ServerTimeUnix)
	id = &Identity{AgentID: resp.AgentId, PollSecret: resp.PollSecret, Key: id.Key, ApprovalCode: id.ApprovalCode, TokenID: id.TokenID}
	if err := r.cfg.Identity.Save(ctx, id); err != nil {
		// Not fatal to the enrollment: the key and code are saved, and asking again continues the same one.
		return nil, 0, fmt.Errorf("could not store the enrollment (asking again is safe): %w", err)
	}
	ttl := ""
	if resp.PendingTtlSeconds > 0 {
		ttl = (time.Duration(resp.PendingTtlSeconds) * time.Second).String()
	}
	r.log.Info("enrolled, waiting for an administrator to approve", "agent", id.AgentID, "cluster_fingerprint", fp, "expires_unapproved_after", ttl)
	return id, r.cfg.Floors.poll(time.Duration(resp.PollIntervalSeconds) * time.Second), nil
}

// awaitApproval polls until the enrollment is approved, rejected or expired. It asks at the pace the
// server suggests (held to 2-60 seconds and jittered) and backs off exponentially, up to two minutes, while
// the server cannot be reached.
func (r *runner) awaitApproval(ctx context.Context, id *Identity, hint time.Duration) (*Identity, bool, error) {
	conn, err := r.dial(&Identity{})
	if err != nil {
		return nil, false, err
	}
	defer conn.Close()
	cl := continuumv1.NewEnrollmentClient(conn)
	base := r.cfg.Floors.poll(hint)
	failures := 0
	remind := time.Now().Add(codeReminder)
	for {
		r.cfg.Health.Beat()
		p, err := cl.PollEnrollment(ctx, &continuumv1.PollRequest{AgentId: id.AgentID, PollSecret: id.PollSecret})
		if status.Code(err) == codes.Unauthenticated {
			// Another instance of this agent (a rolling update) may have enrolled again and been given a new
			// poll secret: what is stored then is the truth, and this one carries on with it.
			if cur, lerr := r.cfg.Identity.Load(ctx); lerr == nil && cur != nil && cur.Enrolled() && !cur.Ended() && cur.PollSecret != id.PollSecret {
				id = cur
				continue
			}
			return nil, false, r.ended(ctx, id, "the server no longer knows this enrollment (it was removed, or the server's database was reset)")
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			failures++
			r.log.Warn("could not ask whether the enrollment was approved; will try again", "err", err, "failures", failures)
			if err := sleep(ctx, nextPollDelay(base, failures, r.cfg.Floors.poll(0), rand.Float64())); err != nil {
				return nil, false, err
			}
			continue
		}
		failures = 0
		r.cfg.Health.Enrolling() // the server answered: waiting for approval is normal, not a fault
		r.noteServerTime(p.ServerTimeUnix)
		switch p.State {
		case continuumv1.PollResponse_APPROVED:
			leaf, err := x509.ParseCertificate(p.LeafDer)
			if err != nil {
				return nil, false, err
			}
			if pub, ok := leaf.PublicKey.(interface{ Equal(x crypto.PublicKey) bool }); !ok || !pub.Equal(&id.Key.PublicKey) {
				return nil, false, errors.New("server returned a certificate for a different key")
			}
			if !pki.PinMatches(r.cfg.CAPin, p.CaDer) {
				return nil, false, errors.New("server returned a CA that does not match the pin")
			}
			id.CertDER, id.CADER, id.PollSecret, id.ApprovalCode = p.LeafDer, p.CaDer, "", ""
			if err := r.cfg.Identity.Save(ctx, id); err != nil {
				return nil, false, err
			}
			r.log.Info("approved", "agent", id.AgentID, "certificate_expires", p.NotAfter.AsTime())
			return id, false, nil
		case continuumv1.PollResponse_REJECTED:
			reason := p.Reason
			if reason == "" {
				reason = "rejected by an administrator"
			}
			return nil, false, r.ended(ctx, id, reason)
		case continuumv1.PollResponse_EXPIRED:
			code, err := approval.New()
			if err != nil {
				return nil, false, err
			}
			next := &Identity{Key: id.Key, ApprovalCode: code, TokenID: id.TokenID}
			if err := r.cfg.Identity.Save(ctx, next); err != nil {
				return nil, false, err
			}
			r.log.Warn("nobody approved the request in time, so the server dropped it; enrolling again with a new approval code")
			return next, true, nil
		}
		if p.PollAfterSeconds > 0 {
			base = r.cfg.Floors.poll(time.Duration(p.PollAfterSeconds) * time.Second)
		}
		if time.Now().After(remind) {
			remind = time.Now().Add(codeReminder)
			r.logCode(id, "still waiting")
		}
		if err := sleep(ctx, nextPollDelay(base, 0, r.cfg.Floors.poll(0), rand.Float64())); err != nil {
			return nil, false, err
		}
	}
}

// nextPollDelay is how long to wait before asking again: the base pace (already held to the allowed range),
// doubled for every failure in a row up to MaxPollBackoff, spread by jitter of plus or minus 20% (rnd is a
// random number in [0,1)), and never below the floor or above the cap for its case.
func nextPollDelay(base time.Duration, failures int, floor time.Duration, rnd float64) time.Duration {
	d := base
	for i := 0; i < failures && d < MaxPollBackoff; i++ {
		d *= 2
	}
	limit := MaxPoll
	if failures > 0 {
		limit = MaxPollBackoff
	}
	d = time.Duration(float64(d) * (0.8 + 0.4*rnd))
	if floor <= 0 {
		floor = MinPoll
	}
	return clamp(d, floor, limit)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---- the end of an identity ----

// ended records that this identity is over (revoked, rejected, or unknown to the server) and returns
// ErrRevoked. What stays is a marker without key or certificate: a restarted agent then knows, without
// asking the server, that it must not carry on, and does not knock again with a token that cannot work.
//
// The end is permanent on purpose. A revoked identity that came back on its own would make revocation
// only as good as the next restart; re-approval is not supported, so the owner of the cluster has to
// decide again by giving the agent a new enrollment token (helm upgrade), which is what "start over" means.
func (r *runner) ended(ctx context.Context, id *Identity, reason string) error {
	m := &Identity{Revoked: reason}
	if id != nil {
		m.AgentID, m.TokenID = id.AgentID, id.TokenID
	}
	if m.TokenID == "" && r.cfg.Token != "" {
		m.TokenID = tokenID(r.cfg.Token)
	}
	if err := r.cfg.Identity.Save(ctx, m); err != nil {
		r.log.Warn("could not record that this agent was revoked", "err", err)
	}
	r.logEnded(reason)
	return fmt.Errorf("%w: %s", ErrRevoked, reason)
}

// logEnded says plainly that the agent is done, and what to do about it. It is written at most once
// every ten minutes, however often the agent is restarted in that time.
func (r *runner) logEnded(reason string) {
	r.ex.mu.Lock()
	defer r.ex.mu.Unlock()
	if !r.ex.endedLogged.IsZero() && time.Since(r.ex.endedLogged) < endedHoldMax {
		return
	}
	r.ex.endedLogged = time.Now()
	r.log.Error("this agent was revoked or rejected and will not connect again: "+reason+". "+
		"Revocation is permanent. To connect this cluster again, create a new enrollment token in Continuum and run `helm upgrade` with it "+
		"(--set enrollment.token=...). This agent does not contact the server any more.", "reason", reason)
}

// holdFor is how long a revoked agent waits before it exits: what the configuration says (tests), or else
// a random time between five and ten minutes.
func holdFor(configured time.Duration) time.Duration {
	if configured != 0 {
		return configured
	}
	return endedHoldMin + time.Duration(rand.Int64N(int64(endedHoldMax-endedHoldMin)))
}

// holdEnded is what a restarted, revoked agent does: say so, wait a random 5-10 minutes without contacting
// anybody, then exit with ErrRevoked. Kubernetes restarts the pod (and backs the restarts off further), so
// a revoked agent costs the server nothing and the log one line every few minutes, never a hot loop.
func (r *runner) holdEnded(ctx context.Context, id *Identity) error {
	r.logEnded(id.Revoked)
	hold := holdFor(r.cfg.RevokedHold)
	// Say so to the health endpoint, and keep its heartbeat going: this wait is long, and a probe that
	// sees no sign of life for five minutes would kill a process that is doing exactly what it should.
	r.cfg.Health.Revoked()
	for left := hold; left > 0; {
		step := min(left, time.Minute)
		if err := sleep(ctx, step); err != nil {
			return err
		}
		left -= step
		r.cfg.Health.Revoked()
	}
	return fmt.Errorf("%w: %s", ErrRevoked, id.Revoked)
}

// revokedDetail extracts the "you were revoked" answer a server puts on an Unauthenticated status.
func revokedDetail(err error) (string, bool) {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unauthenticated {
		return "", false
	}
	for _, d := range st.Details() {
		if rv, ok := d.(*continuumv1.Revoked); ok {
			if rv.Reason == "" {
				return "revoked by an administrator", true
			}
			return rv.Reason, true
		}
	}
	return "", false
}

// ---- clock skew ----

// noteServerTime compares the server's clock (Unix seconds, as sent in its answers) with this machine's. A
// machine whose clock is off by more than a couple of minutes gets certificate errors that read as
// "expired" or "not yet valid" and hide the real cause, so a large skew is called out in the log, once an hour.
func (r *runner) noteServerTime(serverUnix int64) {
	if serverUnix <= 0 {
		return // an older server that does not send it
	}
	// The server's time is whole seconds, so add half a second to be right on average.
	skew := time.Since(time.Unix(serverUnix, 500_000_000))
	r.ex.mu.Lock()
	r.ex.skew, r.ex.skewKnown = skew, true
	warn := (skew > skewWarnAfter || skew < -skewWarnAfter) && (r.ex.skewWarned.IsZero() || time.Since(r.ex.skewWarned) >= skewWarnEvery)
	if warn {
		r.ex.skewWarned = time.Now()
	}
	r.ex.mu.Unlock()
	if warn {
		dir := "ahead of"
		if skew < 0 {
			dir = "behind"
			skew = -skew
		}
		r.log.Warn(fmt.Sprintf("this machine's clock is %s %s the Continuum server's. Certificates are checked against the clock, so this can show up as "+
			"\"certificate expired\" or \"not yet valid\" errors. Fix time synchronisation (NTP, chrony) on the cluster's nodes", skew.Round(time.Second), dir))
	}
}

// clockSkewMs is what the agent reports: its clock minus the server's, in milliseconds (0 when not measured).
func (r *runner) clockSkewMs() int64 {
	r.ex.mu.Lock()
	defer r.ex.mu.Unlock()
	if !r.ex.skewKnown {
		return 0
	}
	return r.ex.skew.Milliseconds()
}

// clockHint adds a pointer to the likely cause when an error is about certificate validity.
func clockHint(err error) string {
	if err != nil && strings.Contains(err.Error(), "certificate has expired or is not yet valid") {
		return "check this machine's clock: the certificate looks expired or not yet valid, which is what a wrong clock causes"
	}
	return ""
}
