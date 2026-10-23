// Package fusionpages is the rules of the gateway to FUSION's own pages (Prometheus, Grafana), apart from the HTTP of it: who holds a ticket
// or a page session and for how long (Access), and what a page may ask of FUSION and what may come back from it (the proxy's policy).
// The cookie, the origin check, the limiters and the proxy itself are the server's; they are wired around these.
package fusionpages

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"ikhnos/internal/kernel"
)

// Opening Prometheus or Grafana from the UI is a link into a new tab. The session cookie is SameSite=Strict, which a browser
// sends only when the navigation starts on the same site - and a new tab, a different spelling of the host (localhost and
// 127.0.0.1) or a link followed through a redirect does not count as that. The page then sees nobody signed in.
//
// So the UI asks for the link instead (POST /fusion/pages, an ordinary authenticated call that carries the session): the
// answer is the page's path with a ticket that works once, for thirty seconds. Redeeming it sets a second cookie that is good for
// these pages alone (Path=/fusion/), HttpOnly, and Lax - the one that has to come with a navigation from anywhere - and
// redirects to the page without the ticket. It reaches no other part of the server, the server's own session cookie stays
// Strict, and a person who stops being an administrator of FUSION's organisation loses the pages at their next request,
// because that is checked every time, not when the cookie was given.
//
// Nothing here is stored: a restart of the server asks the person to open the page again.

const (
	Cookie      = "ikhnos_fusion_ui"
	TicketParam = "ikhnos_ticket"
	TicketTTL   = 30 * time.Second
	SessionTTL  = 8 * time.Hour
	// MaxGrants bounds what a loop of requests can make the server hold: tickets and page sessions each have a
	// table of this size. They are counted apart so that a table of sessions that is full never costs a ticket (see
	// RedeemAndOpen).
	MaxGrants = 4096
	// MaxGrantsPerUser bounds what one person can hold of each kind. Opening a page is something an administrator
	// does a few times a day; a script (or a stolen cookie) looping on it would otherwise fill the whole table and shut every
	// other administrator out of the pages until the entries expire. Past the cap the person's own oldest is dropped.
	MaxGrantsPerUser = 16
	// sweepEvery is how often expired grants are cleared out even when the table is not full.
	sweepEvery = time.Minute
)

// Grant is who a ticket or a page session is for.
type Grant struct {
	UserID, Name string
	expires      time.Time
	seq          uint64 // the order grants were made in, to tell a person's oldest (see makeRoomLocked)
}

// Access is the table of tickets and page sessions. The zero value is not ready: make it with NewAccess.
type Access struct {
	mu                sync.Mutex
	tickets, sessions map[string]Grant // keyed by the hash of the secret
	swept             time.Time        // when expired grants were last cleared out
	seq               uint64
}

func NewAccess() *Access {
	return &Access{tickets: map[string]Grant{}, sessions: map[string]Grant{}}
}

// sweepLocked drops what has expired, at most once every sweepEvery unless force is set (a table that is full
// must look at once). Without this an expired entry stayed until the table filled up, so the table's size said nothing
// about how many pages were really open. f.mu is held.
func (f *Access) sweepLocked(now time.Time, force bool) {
	if !force && now.Sub(f.swept) < sweepEvery {
		return
	}
	f.swept = now
	for _, t := range []map[string]Grant{f.tickets, f.sessions} {
		for k, v := range t {
			if !v.expires.After(now) {
				delete(t, k)
			}
		}
	}
}

// makeRoomLocked gets m ready to take one more grant for userID: expired ones are swept, the person's oldest is evicted
// if they already hold the most they may, and it says whether there is room in the table. f.mu is held.
func (f *Access) makeRoomLocked(m map[string]Grant, userID string, now time.Time) bool {
	f.sweepLocked(now, false)
	if len(m) >= MaxGrants {
		f.sweepLocked(now, true)
	}
	for {
		var oldest string
		var oldestSeq uint64
		n := 0
		for k, v := range m {
			if v.UserID != userID {
				continue
			}
			if n++; oldest == "" || v.seq < oldestSeq {
				oldest, oldestSeq = k, v.seq
			}
		}
		if n < MaxGrantsPerUser {
			break
		}
		delete(m, oldest)
	}
	return len(m) < MaxGrants
}

// put stores a new grant under a fresh secret and returns it; "" when the table is full of live ones.
func (f *Access) put(m map[string]Grant, g Grant, now time.Time) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.makeRoomLocked(m, g.UserID, now) {
		return ""
	}
	secret, err := kernel.NewSecret()
	if err != nil {
		return ""
	}
	f.seq++
	g.seq = f.seq
	m[string(kernel.HashSecret(secret))] = g
	return secret
}

// Ticket makes a one-time ticket for a person.
func (f *Access) Ticket(userID, name string, now time.Time) string {
	return f.put(f.tickets, Grant{UserID: userID, Name: name, expires: now.Add(TicketTTL)}, now)
}

// Outcome is what RedeemAndOpen found.
type Outcome int

const (
	Opened  Outcome = iota // the ticket is used up and a page session is open
	Invalid                // unknown, used or expired (and gone either way)
	Busy                   // good, but there is no room for a page session: the ticket is kept
)

// RedeemAndOpen is what a request carrying a ticket does: it uses the ticket up and opens the page session it is for,
// as one step. The room for the session is made BEFORE the ticket is spent, so a table that has no room (or a secret that
// could not be made) answers "busy" and leaves the ticket as it was - the person's retry a moment later still works,
// instead of finding a link that was burnt without giving them anything. An unknown, used or expired ticket answers
// "invalid"; it is deleted, so it answers once whatever the outcome.
func (f *Access) RedeemAndOpen(ticket string, now time.Time) (secret string, who Grant, out Outcome) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := string(kernel.HashSecret(ticket))
	g, ok := f.tickets[k]
	if !ok {
		return "", Grant{}, Invalid
	}
	if !g.expires.After(now) {
		delete(f.tickets, k)
		return "", Grant{}, Invalid
	}
	if !f.makeRoomLocked(f.sessions, g.UserID, now) {
		return "", g, Busy
	}
	secret, err := kernel.NewSecret()
	if err != nil {
		return "", g, Busy
	}
	delete(f.tickets, k)
	g.expires = now.Add(SessionTTL)
	f.seq++
	g.seq = f.seq
	f.sessions[string(kernel.HashSecret(secret))] = g
	return secret, g, Opened
}

// Session is who a page session cookie belongs to.
func (f *Access) Session(secret string, now time.Time) (Grant, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := string(kernel.HashSecret(secret))
	g, ok := f.sessions[k]
	if ok && !g.expires.After(now) {
		delete(f.sessions, k)
		return Grant{}, false
	}
	return g, ok
}

// End ends one page session.
func (f *Access) End(secret string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, string(kernel.HashSecret(secret)))
}

// EndUser ends every page session and ticket a person holds. It is what signing out, a new password, and losing the role
// that gives access (or the membership itself) do: the page cookie is checked on every request anyway, but a grant that is
// not removed would come back to life if the person were made an administrator again, and until then it would sit in the
// table. Safe on a nil table (an Admin that was never given its handler).
func (f *Access) EndUser(userID string) {
	if f == nil || userID == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range []map[string]Grant{f.tickets, f.sessions} {
		for k, v := range t {
			if v.UserID == userID {
				delete(t, k)
			}
		}
	}
}

// TicketOf is the ticket on a request to a page, "" when there is none (or the request could not be one).
func TicketOf(r *http.Request) string {
	if r.Method != http.MethodGet || !strings.Contains(r.URL.RawQuery, TicketParam+"=") {
		return ""
	}
	return r.URL.Query().Get(TicketParam)
}
