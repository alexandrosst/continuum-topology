package kernel

import (
	"testing"
	"time"
)

func TestRFCWritesUTCAndAnOptionalTimeWritesNothingWhenAbsent(t *testing.T) {
	zone := time.FixedZone("x", 2*3600)
	at := time.Date(2026, 1, 2, 5, 4, 3, 0, zone)
	if got := RFC(at); got != "2026-01-02T03:04:03Z" {
		t.Fatalf("RFC = %q: a time is written in UTC", got)
	}
	if got := RFCP(&at); got != "2026-01-02T03:04:03Z" {
		t.Fatalf("RFCP = %q", got)
	}
	if got := RFCP(nil); got != "" {
		t.Fatalf("RFCP(nil) = %q: an absent time is the empty string", got)
	}
}
