package probe

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

var guardSecret = []byte("0123456789abcdef0123456789abcdef")

func signedReq(secret []byte, node string, body []byte, at time.Time) *http.Request {
	ts := strconv.FormatInt(at.Unix(), 10)
	req := httptest.NewRequest("POST", PathReport, bytes.NewReader(body))
	req.Header.Set(HeaderNode, node)
	req.Header.Set(HeaderTime, ts)
	req.Header.Set(HeaderSig, Sign(secret, ts, node, body))
	return req
}

func serve(h http.Handler, req *http.Request) int {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

func TestReplayedReportIsRejectedAndNotAppliedTwice(t *testing.T) {
	now := time.Now()
	r := NewReceiver(guardSecret, nil)
	r.now = func() time.Time { return now }
	h := r.Handler()
	body := []byte(`{"probeVersion":"1"}`)
	if c := serve(h, signedReq(guardSecret, "n1", body, now)); c != 204 {
		t.Fatalf("first: %d", c)
	}
	select {
	case <-r.Changes():
	default:
		t.Fatal("expected a change")
	}
	if c := serve(h, signedReq(guardSecret, "n1", body, now)); c != http.StatusConflict {
		t.Fatalf("replay: %d, want 409", c)
	}
	// A captured report cannot be moved to another node either: the node is signed.
	if c := serve(h, signedReq(guardSecret, "n2", body, now)); c != 204 {
		t.Fatalf("a fresh signature for another node: %d", c)
	}
	// Once the window has passed the timestamp check refuses it, whether or not the cache remembers.
	now = now.Add(MaxClockSkew + 2*time.Second)
	if c := serve(h, signedReq(guardSecret, "n1", body, now.Add(-MaxClockSkew-time.Second))); c != 401 {
		t.Fatalf("stale: %d", c)
	}
}

func TestTimestampWindowIsConfigurable(t *testing.T) {
	now := time.Now()
	r := NewReceiver(guardSecret, nil)
	r.now = func() time.Time { return now }
	r.Window = 30 * time.Second
	h := r.Handler()
	body := []byte(`{}`)
	if c := serve(h, signedReq(guardSecret, "n1", body, now.Add(-20*time.Second))); c != 204 {
		t.Fatalf("inside: %d", c)
	}
	if c := serve(h, signedReq(guardSecret, "n1", body, now.Add(-40*time.Second))); c != 401 {
		t.Fatalf("outside the narrowed window: %d", c)
	}
	if c := serve(h, signedReq(guardSecret, "n1", body, now.Add(40*time.Second))); c != 401 {
		t.Fatalf("in the future: %d", c)
	}
}

// unreadable fails the test if the receiver reads the body: cheap checks must come first.
type unreadable struct{ t *testing.T }

func (u unreadable) Read([]byte) (int, error) {
	u.t.Helper()
	u.t.Error("the receiver read the body of a request it could have refused from its headers")
	return 0, fmt.Errorf("must not be read")
}

func TestOversizeAndUnauthenticatedRequestsAreRefusedBeforeTheBodyIsRead(t *testing.T) {
	now := time.Now()
	r := NewReceiver(guardSecret, nil)
	r.now = func() time.Time { return now }
	h := r.Handler()
	ts := strconv.FormatInt(now.Unix(), 10)
	mk := func(length int64, mut func(*http.Request)) *http.Request {
		req := httptest.NewRequest("POST", PathReport, unreadable{t})
		req.ContentLength = length
		req.Header.Set(HeaderNode, "n1")
		req.Header.Set(HeaderTime, ts)
		req.Header.Set(HeaderSig, Sign(guardSecret, ts, "n1", nil))
		if mut != nil {
			mut(req)
		}
		return req
	}
	for name, c := range map[string]struct {
		req  *http.Request
		want int
	}{
		"declared too large": {mk(MaxBody+1, nil), 413},
		"bad node":           {mk(10, func(r *http.Request) { r.Header.Set(HeaderNode, "../x") }), 400},
		"stale timestamp":    {mk(10, func(r *http.Request) { r.Header.Set(HeaderTime, "1") }), 401},
		"no signature":       {mk(10, func(r *http.Request) { r.Header.Del(HeaderSig) }), 401},
		"short signature":    {mk(10, func(r *http.Request) { r.Header.Set(HeaderSig, "abcd") }), 401},
	} {
		if got := serve(h, c.req); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	// A body that lies about its length (chunked, no Content-Length) is still cut at the limit.
	big := bytes.Repeat([]byte("x"), MaxBody+10)
	req := signedReq(guardSecret, "n1", big, now)
	req.ContentLength = -1
	if c := serve(h, req); c != 413 {
		t.Errorf("chunked oversize: %d", c)
	}
	if MaxBody != 128<<10 {
		t.Errorf("probe limit is %d", MaxBody)
	}
}

func TestWrongSecretAndTamperedBodyAreRefused(t *testing.T) {
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := Sign(guardSecret, ts, "n1", []byte("a"))
	if err := Verify(guardSecret, ts, "n1", sig, []byte("a"), now); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"body":   Verify(guardSecret, ts, "n1", sig, []byte("b"), now),
		"node":   Verify(guardSecret, ts, "n2", sig, []byte("a"), now),
		"secret": Verify([]byte("another secret of some length!!"), ts, "n1", sig, []byte("a"), now),
		"upper":  Verify(guardSecret, ts, "n1", strings.ToUpper(sig)+"00", []byte("a"), now),
		"empty":  Verify(guardSecret, ts, "n1", "", []byte("a"), now),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReplayCacheIsBoundedAndExpires(t *testing.T) {
	now := time.Now()
	c := NewReplayCache(100)
	for i := 0; i < 1000; i++ {
		if c.Seen("s"+strconv.Itoa(i), now.Add(time.Minute), now) {
			t.Fatal("false replay")
		}
	}
	if c.Len() != 100 {
		t.Fatalf("len %d, want the bound 100", c.Len())
	}
	if !c.Seen("s999", now.Add(time.Minute), now) {
		t.Fatal("the newest entry must still be remembered")
	}
	// After expiry the memory is released and the same signature is no longer a replay (the timestamp
	// check refuses it by then anyway).
	if c.Seen("s999", now.Add(2*time.Minute), now.Add(90*time.Second)) {
		t.Fatal("an expired entry counted as a replay")
	}
	if c.Len() != 1 {
		t.Fatalf("expired entries were not released: %d", c.Len())
	}
}
