package linkprober

import (
	"flag"
	"time"

	"ikhnos/internal/link"
)

// baselineFlags are the settings of the baseline that sits under the one-way probe: connect timing (a SYN and its answer to each
// peer's responder). Read from the environment the chart sets, like the others.
type baselineFlags struct {
	tcpEvery, tcpTimeout *time.Duration
}

func addBaselineFlags(fs *flag.FlagSet) baselineFlags {
	f := baselineFlags{}
	f.tcpEvery = fs.Duration("tcp-interval", durEnv("IKHNOS_LINK_TCP_INTERVAL", 15*time.Second), "how often a connection to each peer's responder is timed (0: never)")
	f.tcpTimeout = fs.Duration("tcp-timeout", durEnv("IKHNOS_LINK_TCP_TIMEOUT", 3*time.Second), "how long one connection attempt may take")
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
