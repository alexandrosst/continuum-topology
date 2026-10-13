//go:build linux

package link

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// TestAgainstARealIperfServer runs the scheduler's real client path against a real `iperf3 -s`. Set
// CONTINUUM_TEST_IPERF3_ADDR=host:port (a global-unicast address: peers reject loopback) to run it; the server must be
// started by the caller, optionally with CONTINUUM_TEST_IPERF3_USER/KEY/PASSWORD_FILE for its authenticated mode.
func TestAgainstARealIperfServer(t *testing.T) {
	hp := os.Getenv("CONTINUUM_TEST_IPERF3_ADDR")
	if hp == "" {
		t.Skip("CONTINUUM_TEST_IPERF3_ADDR not set")
	}
	host, port, _ := strings.Cut(hp, ":")
	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", host)})
	m := NewMeter(p)
	m.EnableProbes()
	c := &Capacity{
		Meter: m, Port: atoiOr(port, 8862), Duration: 3 * time.Second, Omit: time.Second, Parallel: 4, Reverse: true,
		UDP:        UDPRamp{Enabled: true, Start: 500e6, Max: 4e9, Step: 2 * time.Second, Loss: 0.01},
		StartAfter: -1, Pause: 100 * time.Millisecond, Bin: os.Getenv("CONTINUUM_TEST_IPERF3_BIN"),
		User: os.Getenv("CONTINUUM_TEST_IPERF3_USER"), KeyFile: os.Getenv("CONTINUUM_TEST_IPERF3_KEY"), PasswordFile: os.Getenv("CONTINUUM_TEST_IPERF3_PASSWORD_FILE"),
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for i := 0; i < 60; i++ {
		if c.step(ctx) == 5*time.Second {
			break
		}
	}
	got := render(m)
	t.Logf("udp clean %v saturated %v", got[`continuum_link_throughput_udp_clean_bits_per_second{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",dscp="0"}`],
		got[`continuum_link_throughput_udp_saturated{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",dscp="0"}`])
	for k, v := range got {
		if strings.Contains(k, "throughput_bits_per_second") || strings.Contains(k, "failures_total") && v != "0" {
			t.Logf("%s = %s", k, v)
		}
	}
	if v := got[`continuum_link_throughput_bits_per_second{cluster="cl-me",peer="cl-a",method="iperf3",direction="forward",protocol="tcp",streams="1",dscp="0"}`]; v == "" || v == "0" {
		t.Errorf("no forward throughput: %q", v)
	}
	if v := got[`continuum_link_throughput_bits_per_second{cluster="cl-me",peer="cl-a",method="iperf3",direction="reverse",protocol="tcp",streams="4",dscp="0"}`]; v == "" || v == "0" {
		t.Errorf("no parallel reverse throughput: %q", v)
	}
	for _, r := range tpFailReasons {
		if got[`continuum_link_throughput_test_failures_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="`+r+`"}`] != "0" {
			t.Errorf("failure %s", r)
		}
	}
}

func atoiOr(s string, d int) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return d
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return d
	}
	return n
}
