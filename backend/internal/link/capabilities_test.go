package link

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"ikhnos/internal/probe"
)

func TestTheMeterNoticesTheProberOnlyForAValidRequest(t *testing.T) {
	srv, m := peersServer(t, true)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	if !m.ProberSeen().IsZero() {
		t.Fatal("seen before it asked")
	}
	post := func(sign bool) int {
		ts := strconv.FormatInt(now.Unix(), 10)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+PathPeers, http.NoBody)
		req.Header.Set(probe.HeaderNode, ProberNode)
		req.Header.Set(probe.HeaderTime, ts)
		if sign {
			req.Header.Set(probe.HeaderSig, probe.SignContext(peersContext, testSecret, ts, ProberNode, nil))
		} else {
			req.Header.Set(probe.HeaderSig, "nope")
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := post(false); got != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", got)
	}
	if !m.ProberSeen().IsZero() {
		t.Error("a request with a bad signature counted as the prober")
	}
	if got := post(true); got != http.StatusOK {
		t.Fatalf("valid request: %d", got)
	}
	if !m.ProberSeen().Equal(now) {
		t.Errorf("seen at %v, want %v", m.ProberSeen(), now)
	}
	// a meter that does not serve the peers never sees a prober
	srv2 := httptest.NewServer(NewMeter(NewPeers()).Handler())
	t.Cleanup(srv2.Close)
	resp, err := srv2.Client().Post(srv2.URL+PathPeers, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("peers served without being asked to: %d", resp.StatusCode)
	}
}

func TestWhatTheInstallCanDoIsKeptAndANilMeterIsOff(t *testing.T) {
	var none *Meter
	none.SetInstall(Install{Probe: true})
	if none.Install() != (Install{}) || !none.ProberSeen().IsZero() {
		t.Fatal("a nil meter reports something")
	}
	m := NewMeter(NewPeers())
	if m.Install() != (Install{}) {
		t.Fatal("the zero install is nothing on")
	}
	want := Install{Probe: true, Capacity: true, TelemetryExport: true}
	m.SetInstall(want)
	if m.Install() != want {
		t.Fatalf("%+v", m.Install())
	}
}
