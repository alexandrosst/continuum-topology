package probe

import (
	"container/list"
	"crypto/hmac"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// DefaultReplayEntries bounds the replay cache. Only reports that carry a valid signature enter it, so
// only a holder of the secret can grow it, and each entry lives no longer than the timestamp window.
const DefaultReplayEntries = 65536

// ReplayCache remembers the signatures of accepted reports for as long as their timestamp would still be
// accepted, so a captured report cannot be sent again inside that window (which would, for flow reports,
// count the same bytes twice). It is in memory and bounded: when full, the oldest entry is dropped to make
// room, which can only re-open replay of a report that is already the oldest in the window.
type ReplayCache struct {
	mu    sync.Mutex
	max   int
	order *list.List // of *replayEntry, oldest first
	byKey map[string]*list.Element
}

type replayEntry struct {
	sig     string
	expires time.Time
}

func NewReplayCache(max int) *ReplayCache {
	if max <= 0 {
		max = DefaultReplayEntries
	}
	return &ReplayCache{max: max, order: list.New(), byKey: map[string]*list.Element{}}
}

// Seen records sig, valid until expires, and reports whether it was already recorded and still live.
func (c *ReplayCache) Seen(sig string, expires, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for e := c.order.Front(); e != nil; e = c.order.Front() {
		if en := e.Value.(*replayEntry); en.expires.After(now) {
			break
		}
		c.drop(e)
	}
	if e, ok := c.byKey[sig]; ok {
		if e.Value.(*replayEntry).expires.After(now) {
			return true
		}
		c.drop(e)
	}
	for c.order.Len() >= c.max {
		c.drop(c.order.Front())
	}
	c.byKey[sig] = c.order.PushBack(&replayEntry{sig, expires})
	return false
}

func (c *ReplayCache) drop(e *list.Element) {
	delete(c.byKey, e.Value.(*replayEntry).sig)
	c.order.Remove(e)
}

// Len is the number of live entries (for tests).
func (c *ReplayCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Signed describes how a receiver checks one signed POST.
type Signed struct {
	Secret  []byte
	MaxBody int64
	// Window is how far a report's timestamp may be from the receiver's clock; zero means MaxClockSkew.
	Window time.Duration
	Replay *ReplayCache
	NodeOK func(string) bool
	Now    time.Time
}

// ErrReplay marks a report whose signature was already accepted.
var ErrReplay = errors.New("report already received")

// ReadSigned authenticates a request cheaply before it reads a byte of the body: the declared length, the
// node name, the timestamp and the shape of the signature are all checked first, so an unauthenticated
// sender cannot make the receiver buffer anything. It then reads at most MaxBody bytes, verifies the HMAC
// (constant-time) and rejects a replay. On failure it returns the HTTP status to answer with and the
// reason to log (never to send).
func ReadSigned(w http.ResponseWriter, req *http.Request, s Signed) (node string, body []byte, status int, err error) {
	if req.ContentLength > s.MaxBody {
		return "", nil, http.StatusRequestEntityTooLarge, errors.New("body larger than allowed")
	}
	node = req.Header.Get(HeaderNode)
	if s.NodeOK != nil && !s.NodeOK(node) {
		return "", nil, http.StatusBadRequest, errors.New("bad node name")
	}
	window := s.Window
	if window <= 0 {
		window = MaxClockSkew
	}
	ts := req.Header.Get(HeaderTime)
	if err := checkTime(ts, s.Now, window); err != nil {
		return "", nil, http.StatusUnauthorized, err
	}
	sig := req.Header.Get(HeaderSig)
	if _, err := decodeSig(sig); err != nil {
		return "", nil, http.StatusUnauthorized, err
	}
	body, rerr := io.ReadAll(http.MaxBytesReader(w, req.Body, s.MaxBody))
	if rerr != nil {
		return "", nil, http.StatusRequestEntityTooLarge, errors.New("body larger than allowed or unreadable")
	}
	if err := VerifyWindow(s.Secret, ts, node, sig, body, s.Now, window); err != nil {
		return "", nil, http.StatusUnauthorized, err
	}
	if s.Replay != nil {
		sec, _ := strconv.ParseInt(ts, 10, 64)
		if s.Replay.Seen(sig, time.Unix(sec, 0).Add(window+time.Second), s.Now) {
			return "", nil, http.StatusConflict, ErrReplay
		}
	}
	return node, body, 0, nil
}

func checkTime(ts string, now time.Time, window time.Duration) error {
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("bad timestamp")
	}
	if d := now.Sub(time.Unix(sec, 0)); d > window || d < -window {
		return errors.New("timestamp outside the allowed clock skew")
	}
	return nil
}

func decodeSig(sig string) ([]byte, error) {
	b, err := hex.DecodeString(sig)
	if err != nil || len(b) != 32 {
		return nil, errors.New("bad signature")
	}
	return b, nil
}

// VerifyWindow is Verify with a configurable timestamp window.
func VerifyWindow(secret []byte, ts, node, sig string, body []byte, now time.Time, window time.Duration) error {
	if err := checkTime(ts, now, window); err != nil {
		return err
	}
	got, err := decodeSig(sig)
	if err != nil {
		return err
	}
	want, _ := hex.DecodeString(Sign(secret, ts, node, body))
	if !hmac.Equal(want, got) { // constant-time
		return errors.New("bad signature")
	}
	return nil
}
