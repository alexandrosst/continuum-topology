package linkprober

import (
	"flag"
	"fmt"
	"os"
	"time"

	"continuum/internal/cli"
	"continuum/internal/link"
)

// baselineFlags are the settings of the two baselines that sit under the one-way probe: connect timing (a SYN and its answer to each
// peer's responder) and pings, kept by smokeping_prober. Read from the environment the chart sets, like the others.
type baselineFlags struct {
	tcpEvery, tcpTimeout *time.Duration
	icmp                 *bool
	icmpBin, icmpDir     *string
	icmpListen           *string
	icmpEvery            *time.Duration
	icmpSize, icmpHosts  *int
}

func addBaselineFlags(fs *flag.FlagSet) baselineFlags {
	f := baselineFlags{}
	f.tcpEvery = fs.Duration("tcp-interval", durEnv("CONTINUUM_LINK_TCP_INTERVAL", 15*time.Second), "how often a connection to each peer's responder is timed (0: never)")
	f.tcpTimeout = fs.Duration("tcp-timeout", durEnv("CONTINUUM_LINK_TCP_TIMEOUT", 3*time.Second), "how long one connection attempt may take")
	f.icmp = fs.Bool("icmp", boolEnv("CONTINUUM_LINK_ICMP", false), "ping the paired clusters with smokeping_prober")
	f.icmpBin = fs.String("icmp-bin", cli.Env("CONTINUUM_LINK_ICMP_BIN", "/usr/local/bin/smokeping_prober"), "the smokeping_prober program")
	f.icmpDir = fs.String("icmp-dir", cli.Env("CONTINUUM_LINK_ICMP_DIR", "/tmp/icmp"), "a writable directory for its configuration")
	f.icmpListen = fs.String("icmp-listen", cli.Env("CONTINUUM_LINK_ICMP_LISTEN", "127.0.0.1:9374"), "the loopback address it serves its metrics on")
	f.icmpEvery = fs.Duration("icmp-interval", durEnv("CONTINUUM_LINK_ICMP_INTERVAL", 2*time.Second), "time between two pings of one host")
	f.icmpSize = fs.Int("icmp-size", cli.Atoi(cli.Env("CONTINUUM_LINK_ICMP_SIZE", "56")), "ping payload in bytes")
	f.icmpHosts = fs.Int("icmp-hosts", cli.Atoi(cli.Env("CONTINUUM_LINK_ICMP_HOSTS", "3")), "node addresses pinged per peer")
	return f
}

// connect builds the connect timing, or nil when it is switched off.
func (f baselineFlags) connect(m *link.Meter, port int) (*link.TCPConnect, error) {
	if *f.tcpEvery <= 0 {
		return nil, nil
	}
	t := &link.TCPConnect{Meter: m, Port: port, Every: *f.tcpEvery, Timeout: *f.tcpTimeout}
	t.Defaults()
	if err := t.Validate(); err != nil {
		return nil, err
	}
	m.EnableConnects()
	return t, nil
}

// pings builds the ICMP baseline, or nil when it is switched off.
func (f baselineFlags) pings(m *link.Meter, peers *link.Peers) (*link.ICMP, error) {
	if !*f.icmp {
		return nil, nil
	}
	c := &link.ICMP{Peers: peers, Bin: *f.icmpBin, Dir: *f.icmpDir, Listen: *f.icmpListen, Interval: *f.icmpEvery, Size: *f.icmpSize, HostsPerPeer: *f.icmpHosts}
	c.Defaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(c.Bin); err != nil {
		return nil, fmt.Errorf("--icmp-bin: %w", err)
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("--icmp-dir: %w", err)
	}
	m.AddSource(c.WriteMetrics)
	return c, nil
}
