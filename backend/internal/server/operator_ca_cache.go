package server

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"

	"continuum/internal/pki"
)

// Opening an operator's CA runs Argon2id at 64 MiB (see pki/keystore.go), and a pod that runs at 512 Mi has room
// for only a couple of those at once. Every certificate issued for an operator (an intent's command, a new source
// cluster, a re-issue) used to pay it again, so a page that asks for several commands could use the whole limit.
// The opened CA is therefore kept in memory, keyed by operator id and checked against a hash of the sealed key
// it was opened from, so a key that changed in the database is never served from the cache; revoking or deleting the
// operator drops the entry, and an entry nobody asked for within caCacheLifetime is dropped too: what the cache holds is
// a decrypted CA private key, and it should not sit in this process's memory for months because it was used once.
type operatorCAs struct {
	mu       sync.Mutex
	opened   map[string]openedOperatorCA
	inflight map[string]*openCall
	// slots bounds how many Argon2 opens run at once, whatever the cache does: two is what the pod can afford.
	slots chan struct{}
}

// caCacheLifetime is how long an opened CA is kept after it was last used: long enough for a page's worth of commands
// and a person working through the operators, short enough that an idle key is not held.
const caCacheLifetime = 15 * time.Minute

// caCacheNow is the clock the lifetime is read from; a variable only so a test can move it.
var caCacheNow = time.Now

type openedOperatorCA struct {
	sealed [sha256.Size]byte
	ca     *pki.CA
	used   time.Time
}

func newOperatorCAs() *operatorCAs {
	return &operatorCAs{opened: map[string]openedOperatorCA{}, inflight: map[string]*openCall{}, slots: make(chan struct{}, 2)}
}

// openOperatorCA is how a sealed operator CA is opened; a variable only so a test can count the opens.
var openOperatorCA = func(c *Core, certPEM, keyPEM []byte) (*pki.CA, error) { return c.CA.OpenOperatorCA(certPEM, keyPEM) }

// openCall is an open in progress, which callers for the same operator and sealed key wait for instead of repeating.
type openCall struct {
	sealed [sha256.Size]byte
	done   chan struct{}
	ca     *pki.CA
	err    error
}

// find is the cached CA opened from exactly this sealed key, if it is still fresh (it also drops every entry that has
// gone unused for caCacheLifetime), and failing that the open already under way for it. Called with o.mu held.
func (o *operatorCAs) find(id string, sum [sha256.Size]byte, now time.Time) (*pki.CA, *openCall) {
	for k, e := range o.opened {
		if now.Sub(e.used) > caCacheLifetime {
			delete(o.opened, k)
		}
	}
	if hit, ok := o.opened[id]; ok && hit.sealed == sum {
		hit.used = now
		o.opened[id] = hit
		return hit.ca, nil
	}
	if call, ok := o.inflight[id]; ok && call.sealed == sum {
		return nil, call
	}
	return nil, nil
}

// open returns the operator's opened CA, from the cache when the sealed key is the one it was opened from. Callers that
// arrive together for one operator (a page asking for each of its commands) share one open: it is an Argon2 run at
// 64 MiB, and the pod has room for two of them.
func (o *operatorCAs) open(ctx context.Context, c *Core, id string, certPEM, keyPEM []byte) (*pki.CA, error) {
	if o == nil {
		return openOperatorCA(c, certPEM, keyPEM)
	}
	sum := sha256.Sum256(append(append([]byte{}, certPEM...), keyPEM...))
	for {
		o.mu.Lock()
		ca, call := o.find(id, sum, caCacheNow())
		if ca != nil {
			o.mu.Unlock()
			return ca, nil
		}
		if call != nil {
			o.mu.Unlock()
			select {
			case <-call.done:
				if call.err == nil {
					return call.ca, nil
				}
				continue // the one that was opening gave up (its caller left, or the key would not open): try for ourselves
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		call = &openCall{sealed: sum, done: make(chan struct{})}
		o.inflight[id] = call
		o.mu.Unlock()

		call.ca, call.err = o.openInSlot(ctx, c, certPEM, keyPEM)
		o.mu.Lock()
		if o.inflight[id] == call { // not one forget disowned: the operator was revoked or deleted while its CA was being opened
			delete(o.inflight, id)
			if call.err == nil {
				o.opened[id] = openedOperatorCA{sealed: sum, ca: call.ca, used: caCacheNow()}
			}
		}
		o.mu.Unlock()
		close(call.done)
		return call.ca, call.err
	}
}

// openInSlot runs the open once one of the few slots is free.
func (o *operatorCAs) openInSlot(ctx context.Context, c *Core, certPEM, keyPEM []byte) (*pki.CA, error) {
	select {
	case o.slots <- struct{}{}:
		defer func() { <-o.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return openOperatorCA(c, certPEM, keyPEM)
}

// forget drops an operator's entry: it was revoked or deleted, and its key must not outlive it in memory.
func (o *operatorCAs) forget(id string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	delete(o.opened, id)
	delete(o.inflight, id)
	o.mu.Unlock()
}
