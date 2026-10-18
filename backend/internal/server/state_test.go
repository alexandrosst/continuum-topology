package server

import (
	"sync"
	"sync/atomic"
	"testing"
)

// What a module keeps for itself is built once, and is shared as the field it replaced was: the state of the whole server by every
// organisation's view of the Core, the state of an organisation by that organisation only.

func TestServerStateIsOneForTheWholeServerAndBuiltOnce(t *testing.T) {
	base := NewCore(nil, nil, "", nil)
	var built atomic.Int32
	mk := func() *int { built.Add(1); n := 0; return &n }
	a := ServerState(base, "k", mk)
	if b := ServerState(base.ForOrg("org-1"), "k", mk); b != a {
		t.Error("an organisation's view of the Core was given its own server-wide state")
	}
	if c := ServerState(base.ForOrg("org-2"), "k", mk); c != a {
		t.Error("another organisation's view was given its own server-wide state")
	}
	if built.Load() != 1 {
		t.Errorf("built %d times, want once", built.Load())
	}
	if other := ServerState(base, "other", mk); other == a {
		t.Error("two keys gave the same state")
	}
}

func TestOrgStateIsOnePerOrganisation(t *testing.T) {
	base := NewCore(nil, nil, "", nil)
	mk := func() *int { n := 0; return &n }
	one, two := base.ForOrg("org-1"), base.ForOrg("org-2")
	a := OrgState(one, "k", mk)
	if OrgState(one, "k", mk) != a {
		t.Error("the same organisation was given a new state each time")
	}
	if OrgState(two, "k", mk) == a {
		t.Error("two organisations share the state that is meant to be one each")
	}
	if OrgState(base.ForOrg("org-1"), "k", mk) == a {
		t.Error("a new view of the same organisation was given the old view's state: it is reset by ForOrg, as the cache it replaced was")
	}
}

func TestStateOfACoreMadeByHandIsBuiltForTheCall(t *testing.T) {
	c := &Core{}
	n := 0
	mk := func() *int { n++; return &n }
	if ServerState(c, "k", mk) == nil || OrgState(c, "k", mk) == nil {
		t.Fatal("no state")
	}
	if n != 2 {
		t.Errorf("built %d times, want once for each call", n)
	}
}

func TestStateIsSafeToAskForFromManyGoroutines(t *testing.T) {
	base := NewCore(nil, nil, "", nil)
	var built atomic.Int32
	var wg sync.WaitGroup
	got := make([]*int, 32)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i] = ServerState(base.ForOrg("o"), "k", func() *int { built.Add(1); return new(int) })
		}()
	}
	wg.Wait()
	for _, g := range got {
		if g != got[0] {
			t.Fatal("goroutines were given different state")
		}
	}
	if built.Load() != 1 {
		t.Errorf("built %d times, want once", built.Load())
	}
}
