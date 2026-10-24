package server

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ikhnos/internal/estate"
	"ikhnos/internal/store"
)

// One organisation being brought up (a large one reads its whole state from the database, which takes seconds) does not hold up another
// organisation's requests or an agent's connection to it, and two callers for the same organisation share one load.
func TestAnOrganisationLoadingDoesNotHoldUpAnother(t *testing.T) {
	e := newEnv(t)
	if err := e.st.CreateOrg(e.ctx, store.Org{ID: "org-2", Name: "Org Two", CreatedAt: time.Now(), CreatedBy: "u-owner"}, "u-owner"); err != nil {
		t.Fatal(err)
	}
	p := NewPlatform(e.base, nil)
	started, release := make(chan struct{}), make(chan struct{})
	var built atomic.Int32
	var once sync.Once
	p.Tune = func(h *estate.Hub) {
		if h.C.OrgID == "org-1" {
			built.Add(1)
			once.Do(func() { close(started) })
			<-release
		}
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	results := make(chan error, 2)
	for i := 0; i < 2; i++ { // two callers for the slow organisation
		go func() { _, err := p.Tenant(e.ctx, "org-1"); results <- err }()
	}
	<-started

	got := make(chan error, 1)
	go func() { _, err := p.Tenant(e.ctx, "org-2"); got <- err }()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("another organisation's lookup waited for the slow one to load")
	}
	counted := make(chan struct{})
	go func() { _, _, _ = p.AgentCounts(e.ctx); close(counted) }()
	select {
	case <-counted:
	case <-time.After(5 * time.Second):
		t.Fatal("the agent count waited for the slow organisation to load")
	}

	close(release)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the slow organisation never came up")
		}
	}
	if n := built.Load(); n != 1 {
		t.Errorf("the organisation was built %d times, want once", n)
	}
	a, _ := p.Tenant(e.ctx, "org-1")
	b, _ := p.Tenant(e.ctx, "org-1")
	if a == nil || a != b {
		t.Error("the organisation is not one tenant")
	}
}
