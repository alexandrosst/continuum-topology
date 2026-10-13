//go:build linux

package link

import (
	"context"
	"net/netip"
	"os"
	"strconv"
	"testing"
	"time"
)

// With PATHPROBE_DST set (and, for the checks, PATHPROBE_WANT_*) the prober is run against a real path, for instance one
// built from network namespaces with a link of a smaller MTU in the middle. It is skipped otherwise.
func TestAgainstARealPath(t *testing.T) {
	dst := os.Getenv("PATHPROBE_DST")
	if dst == "" {
		t.Skip("PATHPROBE_DST not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := PathProber{SrcPort: 41234}.Probe(ctx, netip.MustParseAddr(dst))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", res)
	if v := os.Getenv("PATHPROBE_WANT_MTU"); v != "" {
		if want, _ := strconv.Atoi(v); res.MTU != want {
			t.Errorf("MTU = %d, want %d", res.MTU, want)
		}
	}
	if v := os.Getenv("PATHPROBE_WANT_HOPS"); v != "" {
		if want, _ := strconv.Atoi(v); res.Hops != want || !res.Reached {
			t.Errorf("hops = %d reached = %v, want %d", res.Hops, res.Reached, want)
		}
	}
	if v := os.Getenv("PATHPROBE_WANT_BLACKHOLE"); v == "1" && !res.Blackhole {
		t.Error("a black hole was not detected")
	}
}
