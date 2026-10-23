package linkprober

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"ikhnos/internal/cli"
	"ikhnos/internal/link"
)

// capacityFlags are the settings of the throughput tests. They are all read from the environment the chart sets, as the
// probe's are, so a value is one place in the chart's values.
type capacityFlags struct {
	enabled                             *bool
	bin                                 *string
	port                                *int
	every, duration, omit               *time.Duration
	parallel                            *int
	reverse, bidir                      *bool
	udpStart, udpMax                    *string
	udpStep                             *time.Duration
	udpLoss                             *float64
	dscp                                *string
	tcpMax, budget, skipAbove, window   *string
	busy                                *float64
	authUser, authKeyFile, authPassFile *string
	authAuto                            *bool
	authKeyDir                          *string
}

func addCapacityFlags(fs *flag.FlagSet) capacityFlags {
	const p = "IKHNOS_LINK_CAPACITY_"
	f := capacityFlags{}
	f.enabled = fs.Bool("capacity", boolEnv("IKHNOS_LINK_CAPACITY", false), "run iperf3 throughput tests to the paired clusters now and then")
	f.bin = fs.String("iperf3", cli.Env("IKHNOS_LINK_IPERF3", "/usr/local/bin/iperf3"), "the iperf3 program")
	f.port = fs.Int("iperf-port", cli.Atoi(cli.Env(p+"PORT", "8862")), "the port every cluster's iperf3 server listens on")
	f.every = fs.Duration("capacity-interval", durEnv(p+"INTERVAL", 12*time.Hour), "how often each paired cluster is put through its set of throughput tests")
	f.duration = fs.Duration("capacity-duration", durEnv(p+"DURATION", 10*time.Second), "how long each TCP test sends")
	f.omit = fs.Duration("capacity-omit", durEnv(p+"OMIT", 2*time.Second), "start of each TCP test left out of its figures (slow start)")
	f.parallel = fs.Int("capacity-parallel", cli.Atoi(cli.Env(p+"PARALLEL", "4")), "streams of the multi-stream TCP test (0: not run)")
	f.reverse = fs.Bool("capacity-reverse", boolEnv(p+"REVERSE", true), "also test from the peer to this cluster")
	f.bidir = fs.Bool("capacity-bidir", boolEnv(p+"BIDIR", false), "also run a TCP test that loads both directions at once")
	f.udpStart = fs.String("capacity-udp-start", cli.Env(p+"UDP_START", "5M"), "lowest rate of the UDP loss ramp (bits per second)")
	f.udpMax = fs.String("capacity-udp-max", cli.Env(p+"UDP_MAX", "200M"), "highest rate of the UDP loss ramp (0: no ramp)")
	f.udpStep = fs.Duration("capacity-udp-step", durEnv(p+"UDP_STEP", 5*time.Second), "how long each rate of the ramp is held")
	f.udpLoss = fs.Float64("capacity-udp-loss", floatEnv(p+"UDP_LOSS", 1), "percent of UDP datagrams lost that ends the ramp")
	f.dscp = fs.String("capacity-dscp", cli.Env(p+"DSCP", "0"), "comma-separated DSCP classes to repeat the single-stream and UDP tests for")
	f.tcpMax = fs.String("capacity-tcp-max-rate", cli.Env(p+"TCP_MAX_RATE", "0"), "cap on each TCP stream (bits per second; 0: none)")
	f.budget = fs.String("capacity-budget", cli.Env(p+"BUDGET", "10Gi"), "bytes the tests may carry per day, all peers together (0: unlimited)")
	f.busy = fs.Float64("capacity-busy", floatEnv(p+"BUSY", 0.3), "wait while real traffic uses more than this fraction of what the last test reached (0: never wait)")
	f.skipAbove = fs.String("capacity-skip-above", cli.Env(p+"SKIP_ABOVE", "0"), "wait while real traffic is above this rate, until a first test has measured the link (0: no limit)")
	f.window = fs.String("capacity-window", cli.Env(p+"WINDOW", ""), "only run between these times of day, UTC, HH:MM-HH:MM")
	f.authUser = fs.String("capacity-auth-user", cli.Env(p+"AUTH_USER", ""), "iperf3 user for the authenticated mode")
	f.authKeyFile = fs.String("capacity-auth-key-file", cli.Env(p+"AUTH_KEY_FILE", ""), "the servers' RSA public key, for the authenticated mode")
	f.authPassFile = fs.String("capacity-auth-password-file", cli.Env(p+"AUTH_PASSWORD_FILE", ""), "file holding the iperf3 user's password")
	f.authAuto = fs.Bool("capacity-auth-auto", boolEnv(p+"AUTH_AUTO", false), "use the credentials the Ikhnos server issues for each peer (fetched from the agent, in memory) and never test a peer without them; ignored when --capacity-auth-user is set")
	f.authKeyDir = fs.String("capacity-auth-key-dir", cli.Env(p+"AUTH_KEY_DIR", ""), "private scratch directory for the peers' public keys (empty: a temporary directory)")
	return f
}

// build turns the flags into the scheduler, or says what is wrong with them.
func (f capacityFlags) build() (*link.Capacity, error) {
	c := &link.Capacity{
		Bin: *f.bin, Port: *f.port, Every: *f.every, Duration: *f.duration, Omit: *f.omit, Parallel: *f.parallel,
		Reverse: *f.reverse, Bidir: *f.bidir, BusyFraction: *f.busy,
		User: *f.authUser, KeyFile: *f.authKeyFile, PasswordFile: *f.authPassFile,
		AutoAuth: *f.authAuto && *f.authUser == "", KeyDir: *f.authKeyDir,
	}
	var err error
	rate := func(name, v string) uint64 {
		n, e := link.ParseRate(v)
		if e != nil && err == nil {
			err = fmt.Errorf("--%s: %w", name, e)
		}
		return n
	}
	c.TCPMaxRate = rate("capacity-tcp-max-rate", *f.tcpMax)
	c.SkipAbove = rate("capacity-skip-above", *f.skipAbove)
	if max := rate("capacity-udp-max", *f.udpMax); max > 0 {
		c.UDP = link.UDPRamp{Enabled: true, Start: rate("capacity-udp-start", *f.udpStart), Max: max, Step: *f.udpStep, Loss: *f.udpLoss / 100}
	}
	if b, e := link.ParseBytes(*f.budget); e != nil {
		if err == nil {
			err = fmt.Errorf("--capacity-budget: %w", e)
		}
	} else {
		c.Budget = b
	}
	if w, e := link.ParseTimeWindow(*f.window); e != nil {
		if err == nil {
			err = fmt.Errorf("--capacity-window: %w", e)
		}
	} else {
		c.Window = w
	}
	for _, s := range strings.Split(*f.dscp, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		n, e := strconv.Atoi(s)
		if e != nil && err == nil {
			err = fmt.Errorf("--capacity-dscp: %q is not a number", s)
		}
		c.DSCP = append(c.DSCP, n)
	}
	if err != nil {
		return nil, err
	}
	c.Defaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(c.Bin); err != nil {
		return nil, fmt.Errorf("--iperf3: %w", err)
	}
	return c, nil
}

func boolEnv(k string, d bool) bool {
	switch strings.ToLower(os.Getenv(k)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return d
}

func floatEnv(k string, d float64) float64 {
	if v := os.Getenv(k); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return d
}
