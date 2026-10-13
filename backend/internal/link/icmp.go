package link

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ICMP is the baseline that works without any cooperation from the other cluster: plain pings, kept by smokeping_prober (the
// Prometheus exporter that records ping round trips as a histogram). This type is only the glue around that program:
//
//   - it writes the program's configuration from the peer list, one target group per peer carrying the cluster and peer labels
//     the rest of the link series use, and restarts the program when the list changes (it cannot start with no targets, and a
//     reload keeps pingers it no longer needs);
//   - it serves what the program measured on the prober's own metrics endpoint, under the continuum_link_icmp_ prefix, so
//     there is still one endpoint to scrape and one family of names.
//
// The pings go through an unprivileged ICMP socket, which the pod's net.ipv4.ping_group_range sysctl opens up; no capability is
// needed. Many networks drop ICMP between sites, so a silent peer here is a hint, not a finding: the connect timing and the
// one-way probe are what say whether the link is down.
type ICMP struct {
	Peers *Peers
	Log   *slog.Logger

	// Bin is the smokeping_prober program and Dir a writable directory for its configuration. Listen is the loopback address it
	// serves its metrics on.
	Bin, Dir, Listen string
	// Interval is the time between two pings of one host (default 2s), Size the payload in bytes (default 56) and HostsPerPeer
	// how many of a peer's node addresses are pinged (default 3: one node being down must not look like a dead link).
	Interval     time.Duration
	Size         int
	HostsPerPeer int
	// Check is how often the peer list is compared with what is running (default 30s).
	Check time.Duration

	// Spawn starts the program; tests replace it.
	Spawn func(ctx context.Context, bin string, args []string, log *slog.Logger) (*Child, error)
	// Client reads the program's metrics.
	Client *http.Client

	mu      sync.Mutex
	current string // hash of the configuration the running program was started with ("": none)
	child   *Child
	started time.Time
}

// Child is a program the glue started and can stop.
type Child struct {
	Done chan struct{} // closed when the program has ended
	Stop func()
}

// Defaults fills what was left unset.
func (c *ICMP) Defaults() {
	if c.Bin == "" {
		c.Bin = "smokeping_prober"
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:9374"
	}
	if c.Interval == 0 {
		c.Interval = 2 * time.Second
	}
	if c.Size == 0 {
		c.Size = 56
	}
	if c.HostsPerPeer == 0 {
		c.HostsPerPeer = 3
	}
	if c.Check == 0 {
		c.Check = 30 * time.Second
	}
	if c.Spawn == nil {
		c.Spawn = spawnChild
	}
	if c.Client == nil {
		c.Client = &http.Client{Timeout: 3 * time.Second}
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
}

// Validate checks the settings so a bad value fails at start-up.
func (c *ICMP) Validate() error {
	if c.Dir == "" {
		return fmt.Errorf("a directory for the ping configuration is needed")
	}
	if c.Interval < 500*time.Millisecond || c.Interval > time.Minute {
		return fmt.Errorf("the ping interval must be between 500ms and 1m, got %s", c.Interval)
	}
	if c.Size < 24 || c.Size > 1400 {
		return fmt.Errorf("the ping size must be between 24 and 1400 bytes, got %d", c.Size)
	}
	if c.HostsPerPeer < 1 || c.HostsPerPeer > 8 {
		return fmt.Errorf("hosts per peer must be between 1 and 8, got %d", c.HostsPerPeer)
	}
	if c.Check < 5*time.Second {
		return fmt.Errorf("checking the peer list more often than every 5s is not supported, got %s", c.Check)
	}
	return nil
}

// Config renders smokeping_prober's configuration for the peers that are probed, and says how many hosts it pings. Peers and
// addresses are in a fixed order, so the same peers always give the same text and nothing restarts for no reason.
func (c *ICMP) Config() (text string, hosts int) {
	own := c.Peers.Own()
	var b strings.Builder
	b.WriteString("# Written by the link prober from the peer list; changes are overwritten.\ntargets:\n")
	for _, p := range c.Peers.List() {
		if p.NoProbe || own == "" {
			continue
		}
		var addrs []string
		for _, a := range p.Addresses {
			addrs = append(addrs, a.String())
		}
		sort.Strings(addrs)
		if len(addrs) > c.HostsPerPeer {
			addrs = addrs[:c.HostsPerPeer]
		}
		if len(addrs) == 0 {
			continue
		}
		q := make([]string, len(addrs))
		for i, a := range addrs {
			q[i] = strconv.Quote(a)
		}
		// Quoted with Go's rules, which are valid YAML double-quoted strings for the printable text an id or address holds.
		fmt.Fprintf(&b, "- hosts: [%s]\n  interval: %s\n  protocol: udp\n  size: %d\n  labels: {cluster: %s, peer: %s}\n",
			strings.Join(q, ", "), c.Interval, c.Size, strconv.Quote(own), strconv.Quote(p.ID))
		hosts += len(addrs)
	}
	return b.String(), hosts
}

// Run keeps the program matching the peer list until ctx ends, then stops it.
func (c *ICMP) Run(ctx context.Context) {
	c.Defaults()
	if err := c.Validate(); err != nil {
		c.Log.Error("link ICMP baseline not started", "err", err)
		return
	}
	defer c.stop()
	for {
		c.reconcile(ctx)
		if !sleep(ctx, c.Check) {
			return
		}
	}
}

// reconcile starts, restarts or stops the program so that it matches the peer list.
func (c *ICMP) reconcile(ctx context.Context) {
	text, hosts := c.Config()
	sum := sha256.Sum256([]byte(text))
	want := ""
	if hosts > 0 {
		want = hex.EncodeToString(sum[:8])
	}
	c.mu.Lock()
	alive := c.child != nil && !closed(c.child.Done)
	same := want == c.current && (want == "" || alive)
	// A program that keeps dying is not restarted more than once in ten seconds.
	tooSoon := want != "" && want == c.current && !alive && time.Since(c.started) < 10*time.Second
	c.mu.Unlock()
	if same || tooSoon {
		return
	}
	c.stop()
	if want == "" {
		return
	}
	path := filepath.Join(c.Dir, "smokeping.yml")
	if err := writeFileAtomic(path, []byte(text)); err != nil {
		c.Log.Error("could not write the ping configuration", "err", err)
		return
	}
	args := []string{"--config.file=" + path, "--web.listen-address=" + c.Listen, "--log.level=warn"}
	child, err := c.Spawn(ctx, c.Bin, args, c.Log)
	if err != nil {
		c.Log.Error("could not start the ping program", "err", err)
		return
	}
	c.mu.Lock()
	c.child, c.current, c.started = child, want, time.Now()
	c.mu.Unlock()
	c.Log.Info("link ICMP baseline started", "hosts", hosts)
}

func (c *ICMP) stop() {
	c.mu.Lock()
	ch := c.child
	c.child, c.current = nil, ""
	c.mu.Unlock()
	if ch != nil {
		ch.Stop()
	}
}

func closed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// spawnChild runs the program, ending it with SIGTERM (and a kill three seconds later) when stopped or when ctx ends.
func spawnChild(ctx context.Context, bin string, args []string, log *slog.Logger) (*Child, error) {
	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GOMAXPROCS=1"}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 3 * time.Second
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			log.Warn("ping program", "msg", sc.Text())
		}
	}()
	go func() {
		err := cmd.Wait()
		if cctx.Err() == nil {
			log.Warn("the ping program ended", "err", err)
		}
		close(done)
	}()
	return &Child{Done: done, Stop: func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}}, nil
}

// WriteMetrics writes what the program measured, renamed, plus whether it is running. It is registered on the meter as a source,
// so it is part of every scrape of the prober's endpoint. A program that does not answer costs the scrape at most three seconds.
func (c *ICMP) WriteMetrics(w io.Writer) {
	c.Defaults()
	if _, hosts := c.Config(); hosts == 0 {
		return
	}
	own := c.Peers.Own()
	body, err := c.fetch()
	up := 0
	if err == nil {
		up = 1
	}
	fmt.Fprintf(w, "# HELP continuum_link_icmp_running Whether the ping program is running and answering; 0 means the ICMP series below are stale or absent.\n# TYPE continuum_link_icmp_running gauge\ncontinuum_link_icmp_running{cluster=\"%s\"} %d\n", esc(own), up)
	if err != nil {
		c.Log.Debug("could not read the ping program's metrics", "err", err)
		return
	}
	io.WriteString(w, RenameICMP(body))
}

func (c *ICMP) fetch() (string, error) {
	resp, err := c.Client.Get("http://" + c.Listen + "/metrics")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(b), err
}

// icmpFamilies maps the program's metric families to ours. Everything else it exposes (its build and configuration state, the Go
// runtime) is left out: it is not about the link, and the prober already exports its own process series.
var icmpFamilies = map[string]string{
	"smokeping_requests_total":            "continuum_link_icmp_requests_total",
	"smokeping_response_duration_seconds": "continuum_link_icmp_response_duration_seconds",
	"smokeping_response_ttl":              "continuum_link_icmp_response_ttl",
	"smokeping_response_duplicates_total": "continuum_link_icmp_response_duplicates_total",
	"smokeping_receive_errors_total":      "continuum_link_icmp_receive_errors_total",
	"smokeping_send_errors_total":         "continuum_link_icmp_send_errors_total",
	"smokeping_prober_errors_total":       "continuum_link_icmp_prober_errors_total",
}

// RenameICMP keeps the wanted families of the program's Prometheus text and gives them our names.
func RenameICMP(in string) string {
	var out strings.Builder
	for _, line := range strings.Split(in, "\n") {
		if line == "" {
			continue
		}
		name := line
		prefix := ""
		if strings.HasPrefix(line, "# HELP ") || strings.HasPrefix(line, "# TYPE ") {
			prefix, name = line[:7], line[7:]
		}
		end := strings.IndexAny(name, "{ ")
		if end < 0 {
			continue
		}
		metric, rest := name[:end], name[end:]
		family := metric
		for _, suf := range []string{"_bucket", "_sum", "_count"} {
			if strings.HasSuffix(metric, suf) && prefix == "" {
				family = strings.TrimSuffix(metric, suf)
				break
			}
		}
		to, ok := icmpFamilies[family]
		if !ok {
			continue
		}
		out.WriteString(prefix + to + metric[len(family):] + rest + "\n")
	}
	return out.String()
}
