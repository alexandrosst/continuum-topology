package estate

import (
	"testing"
	"time"

	"ikhnos/internal/connectivity"
)

// The agent echoing its Config digest is what marks a change in force, and the time is that of the first echo of the Config now held.
func TestTheTimeAChangeCameIntoForceIsTheAgentsFirstEchoOfTheCurrentConfig(t *testing.T) {
	v := &View{NetView: connectivity.NetView{CfgHash: "h2", CfgSentAt: time.Unix(100, 0)}}
	v.NoteConfigEcho("h1", time.Unix(110, 0)) // still the old one
	if !v.CfgInForceAt.IsZero() {
		t.Fatalf("in force at %v on an echo of the old Config", v.CfgInForceAt)
	}
	v.NoteConfigEcho("h2", time.Unix(120, 0))
	v.NoteConfigEcho("h2", time.Unix(150, 0)) // later heartbeats do not move it
	if !v.CfgInForceAt.Equal(time.Unix(120, 0)) {
		t.Fatalf("in force at %v", v.CfgInForceAt)
	}
}
