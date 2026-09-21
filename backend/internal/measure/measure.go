// Package measure times TCP connections to other places, to learn how far away they are in
// network terms. It connects and closes; it never sends or reads data.
package measure

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// MaxTargets bounds how many addresses one agent will time; the rest are refused.
	MaxTargets = 32
	// MinInterval is the shortest time between two rounds.
	MinInterval = 30 * time.Second
	Timeout     = 2 * time.Second
	Samples     = 5
)

type Target struct {
	ID   string
	Host string
	Port int
}

// Result is one round of samples to one target. RTTs are milliseconds; they are zero when every
// attempt failed.
type Result struct {
	ID      string
	Samples int
	Failed  int
	Min     float64
	P50     float64
	P95     float64
}

// controlPlanePorts are refused by default whatever the address: a measurement that connects to the
// Kubernetes API server (6443), the kubelet (10250, 10255), etcd (2379, 2380), the controller manager
// (10257) or the scheduler (10259) is reconnaissance of the cluster's most sensitive services, and the
// server that names the targets is not trusted with that.
var controlPlanePorts = map[int]string{
	6443: "the Kubernetes API server", 10250: "the kubelet", 10255: "the kubelet (read-only port)",
	2379: "etcd", 2380: "etcd", 10257: "the controller manager", 10259: "the scheduler",
}

// Policy decides what the agent may measure on top of the hard block (loopback, link-local including cloud
// metadata, unspecified, multicast), which no setting lifts. The zero value is the default: no control-plane
// port and not the Kubernetes API server's own address (KUBERNETES_SERVICE_HOST).
type Policy struct {
	// AllowPorts lifts the default refusal of the control-plane ports listed there, for a cluster whose
	// applications really are served on one of them.
	AllowPorts []int
	// AllowCIDRs lifts the refusal of the API server's address when it lies inside one of these ranges.
	AllowCIDRs []netip.Prefix
	// APIServer is the API server's address or name; empty means the KUBERNETES_SERVICE_HOST variable.
	APIServer string
}

var (
	policyMu sync.RWMutex
	current  Policy
)

// SetPolicy replaces the policy Round, Probe and CheckTarget use; the agent's command line would call it.
func SetPolicy(p Policy) {
	policyMu.Lock()
	current = p
	policyMu.Unlock()
}

func currentPolicy() Policy {
	policyMu.RLock()
	defer policyMu.RUnlock()
	return current
}

func (p Policy) checkPort(port int) error {
	what, denied := controlPlanePorts[port]
	if !denied {
		return nil
	}
	for _, a := range p.AllowPorts {
		if a == port {
			return nil
		}
	}
	return fmt.Errorf("port %d (%s) is not measured; the agent's operator can allow it explicitly", port, what)
}

func (p Policy) apiServer() string {
	if p.APIServer != "" {
		return p.APIServer
	}
	return os.Getenv("KUBERNETES_SERVICE_HOST")
}

func (p Policy) checkAddr(a netip.Addr) error {
	if err := blockedAddr(a); err != nil {
		return err
	}
	if api, err := netip.ParseAddr(p.apiServer()); err == nil && api.Unmap() == a.Unmap() {
		for _, c := range p.AllowCIDRs {
			if c.Contains(a.Unmap()) {
				return nil
			}
		}
		return errors.New("the Kubernetes API server's address is not measured; the agent's operator can allow it explicitly")
	}
	return nil
}

// checkHost refuses the API server named as a host name.
func (p Policy) checkHost(host string) error {
	if api := p.apiServer(); api != "" && strings.EqualFold(strings.TrimSuffix(host, "."), strings.TrimSuffix(api, ".")) {
		if _, err := netip.ParseAddr(api); err != nil {
			return errors.New("the Kubernetes API server is not measured")
		}
	}
	return nil
}

// blockedAddr reports addresses that must never be probed whoever asks: loopback, link-local (which
// includes the cloud metadata service), unspecified and multicast.
func blockedAddr(a netip.Addr) error {
	a = a.Unmap()
	switch {
	case a.IsLoopback():
		return errors.New("loopback addresses are not measured")
	case a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast():
		return errors.New("link-local addresses (including the cloud metadata service) are not measured")
	case a.IsUnspecified():
		return errors.New("the unspecified address is not measured")
	case a.IsMulticast():
		return errors.New("multicast addresses are not measured")
	}
	return nil
}

// CheckTarget validates what can be checked without the network: a plausible host name or address,
// a real port, and no forbidden address.
func CheckTarget(host string, port int) error { return currentPolicy().CheckTarget(host, port) }

// CheckTarget is the package function with an explicit policy.
func (p Policy) CheckTarget(host string, port int) error {
	host = strings.TrimSpace(host)
	if host == "" || len(host) > 253 {
		return errors.New("give a host name or an address")
	}
	if port < 1 || port > 65535 {
		return errors.New("the port must be between 1 and 65535")
	}
	if strings.ContainsAny(host, " /\\@?#[]") {
		return errors.New("give only a host name or an address, without a scheme or a path")
	}
	if err := p.checkPort(port); err != nil {
		return err
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return p.checkAddr(a)
	}
	if err := p.checkHost(host); err != nil {
		return err
	}
	for _, r := range host {
		if !(r == '-' || r == '.' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return errors.New("the host name has characters that are not allowed")
		}
	}
	return nil
}

// Dialer is what Probe connects with; tests replace it.
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// Probe times `samples` connections to t. A host name is resolved once, and each address it
// resolves to is checked again, so a name that points at a forbidden address is refused as well.
func Probe(ctx context.Context, t Target, samples int, timeout time.Duration, dial Dialer, resolve func(ctx context.Context, host string) ([]netip.Addr, error)) (Result, error) {
	return currentPolicy().Probe(ctx, t, samples, timeout, dial, resolve)
}

// Probe is the package function with an explicit policy.
func (p Policy) Probe(ctx context.Context, t Target, samples int, timeout time.Duration, dial Dialer, resolve func(ctx context.Context, host string) ([]netip.Addr, error)) (Result, error) {
	if err := p.CheckTarget(t.Host, t.Port); err != nil {
		return Result{}, err
	}
	if dial == nil {
		d := net.Dialer{Timeout: timeout}
		dial = d.DialContext
	}
	var ip netip.Addr
	if a, err := netip.ParseAddr(t.Host); err == nil {
		ip = a
	} else {
		if resolve == nil {
			resolve = func(ctx context.Context, h string) ([]netip.Addr, error) {
				ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", h)
				return ips, err
			}
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		ips, err := resolve(rctx, t.Host)
		cancel()
		if err != nil || len(ips) == 0 {
			return Result{ID: t.ID, Samples: samples, Failed: samples}, nil // unresolvable counts as unreachable
		}
		ip = ips[0]
		for _, a := range ips {
			if err := p.checkAddr(a); err != nil {
				return Result{}, fmt.Errorf("%s resolves to a forbidden address: %w", t.Host, err)
			}
		}
	}
	if err := p.checkAddr(ip); err != nil {
		return Result{}, err
	}
	addr := net.JoinHostPort(ip.String(), fmt.Sprint(t.Port))
	res := Result{ID: t.ID, Samples: samples}
	var ok []float64
	for i := 0; i < samples; i++ {
		if ctx.Err() != nil {
			break
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		start := time.Now()
		c, err := dial(cctx, "tcp", addr)
		el := time.Since(start)
		cancel()
		if err != nil {
			res.Failed++
		} else {
			_ = c.Close()
			ok = append(ok, float64(el.Microseconds())/1000)
		}
		if i < samples-1 {
			select {
			case <-time.After(150 * time.Millisecond):
			case <-ctx.Done():
			}
		}
	}
	if len(ok) > 0 {
		sort.Float64s(ok)
		res.Min = ok[0]
		res.P50 = percentile(ok, 0.5)
		res.P95 = percentile(ok, 0.95)
	}
	return res, nil
}

// percentile of an ascending slice, by nearest rank.
func percentile(s []float64, p float64) float64 {
	if len(s) == 0 {
		return 0
	}
	i := int(p*float64(len(s))+0.999999) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i]
}

// Round times every target, a few at a time, and returns the results together with how many were
// refused (over the limit or forbidden).
func Round(ctx context.Context, targets []Target, dial Dialer, resolve func(ctx context.Context, host string) ([]netip.Addr, error)) (results []Result, refused int) {
	return currentPolicy().Round(ctx, targets, dial, resolve)
}

// Round is the package function with an explicit policy.
func (p Policy) Round(ctx context.Context, targets []Target, dial Dialer, resolve func(ctx context.Context, host string) ([]netip.Addr, error)) (results []Result, refused int) {
	if len(targets) > MaxTargets {
		refused += len(targets) - MaxTargets
		targets = targets[:MaxTargets]
	}
	type out struct {
		i   int
		r   Result
		err error
	}
	ch := make(chan out)
	sem := make(chan struct{}, 4)
	for i, t := range targets {
		go func(i int, t Target) {
			sem <- struct{}{}
			defer func() { <-sem }()
			r, err := p.Probe(ctx, t, Samples, Timeout, dial, resolve)
			ch <- out{i, r, err}
		}(i, t)
	}
	got := make([]*Result, len(targets))
	for range targets {
		o := <-ch
		if o.err != nil {
			refused++
			continue
		}
		r := o.r
		got[o.i] = &r
	}
	for _, r := range got {
		if r != nil {
			results = append(results, *r)
		}
	}
	return results, refused
}
