package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// DeciderPolicy decides which addresses the server may connect to on behalf of the external decider.
//
// The decider is an address an administrator types in, and the server makes the request from inside
// its own network, so left unchecked it is a way to reach anything the server can reach (the cloud
// metadata service, the database, other pods). The policy is therefore closed by default: only
// addresses that are publicly routable are allowed. An operator who really runs the decider next to
// the server opens exactly the ranges it needs with --decider-allow-cidrs.
//
// The same policy is applied twice: when the address is saved (so the administrator gets a clear
// message) and again on the address actually dialed (so a name that later resolves somewhere else,
// DNS rebinding, is still refused).
type DeciderPolicy struct {
	allow []netip.Prefix
	// lookup and resolver are replaced by tests; nil means the system's.
	lookup   func(ctx context.Context, host string) ([]netip.Addr, error)
	resolver *net.Resolver
}

// NewDeciderPolicy parses the operator's comma-separated list of CIDRs (or single addresses). An empty
// list gives the default policy: nothing that is private, loopback or link-local.
func NewDeciderPolicy(cidrs string) (*DeciderPolicy, error) {
	p := &DeciderPolicy{}
	for _, f := range strings.Split(cidrs, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		pf, err := netip.ParsePrefix(f)
		if err != nil {
			a, aerr := netip.ParseAddr(f)
			if aerr != nil {
				return nil, fmt.Errorf("--decider-allow-cidrs: %q is not a CIDR such as 10.0.0.0/8 or 127.0.0.1/32", f)
			}
			pf = netip.PrefixFrom(a, a.BitLen())
		}
		// An IPv4-mapped prefix (::ffff:10.0.0.0/104) means the IPv4 range.
		if pf.Addr().Is4In6() && pf.Bits() >= 96 {
			pf = netip.PrefixFrom(pf.Addr().Unmap(), pf.Bits()-96)
		}
		p.allow = append(p.allow, pf.Masked())
	}
	return p, nil
}

// Allowed reports the ranges the operator opened, for the startup log.
func (p *DeciderPolicy) Allowed() []netip.Prefix {
	if p == nil {
		return nil
	}
	return p.allow
}

// neverAllowed are addresses no allow-list can open: the cloud metadata services (they hand out the
// node's credentials), and addresses that are not a destination at all.
var neverAllowed = mustPrefixes(
	"169.254.0.0/16",     // link-local: 169.254.169.254 (AWS, GCP, Azure, ...), ECS's 169.254.170.2
	"fe80::/10",          // IPv6 link-local
	"fd00:ec2::/32",      // AWS metadata over IPv6 (fd00:ec2::254)
	"168.63.129.16/32",   // Azure wire server
	"100.100.100.200/32", // Alibaba Cloud metadata
	"192.0.0.192/32",     // Oracle Cloud (legacy) metadata
	"0.0.0.0/8",          // "this network", includes the unspecified address
	"::/128",             // unspecified
	"224.0.0.0/4",        // multicast
	"ff00::/8",           // multicast
	"240.0.0.0/4",        // reserved, includes the broadcast address
)

// privateRanges are refused unless the operator allow-listed them.
var privateRanges = mustPrefixes(
	"127.0.0.0/8",
	"::1/128",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10", // carrier-grade NAT (also Tailscale and several clouds' internal ranges)
	"fc00::/7",      // IPv6 unique local
	"192.0.0.0/24",  // IETF protocol assignments
	"198.18.0.0/15", // benchmarking
	"::/96",         // deprecated IPv4-compatible IPv6
)

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, x := range s {
		out[i] = netip.MustParsePrefix(x)
	}
	return out
}

func inAny(a netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

var translators = mustPrefixes("64:ff9b::/96", "64:ff9b:1::/48", "2002::/16")

// embedded returns the IPv4 address hidden inside an IPv6 one that translates to IPv4 (NAT64 and 6to4),
// so 64:ff9b::7f00:1 cannot be used to reach 127.0.0.1 through a translator.
func embedded(a netip.Addr) (netip.Addr, bool) {
	if !a.Is6() || !inAny(a, translators) {
		return netip.Addr{}, false
	}
	b := a.As16()
	if b[0] == 0x20 { // 6to4: the IPv4 address follows the 2002 prefix
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	}
	return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
}

// CheckAddr returns nil when the server may connect to a. A nil policy is the default one.
func (p *DeciderPolicy) CheckAddr(a netip.Addr) error {
	a = a.WithZone("").Unmap()
	if e, ok := embedded(a); ok {
		if err := p.CheckAddr(e); err != nil {
			return err
		}
	}
	if inAny(a, neverAllowed) {
		return fmt.Errorf("%s is a link-local, cloud-metadata, multicast or otherwise unusable address, which no setting can allow", a)
	}
	if !inAny(a, privateRanges) {
		return nil
	}
	if p != nil && inAny(a, p.allow) {
		return nil
	}
	return fmt.Errorf("%s is a private or loopback address; the server calls those only if the operator lists the range in --decider-allow-cidrs", a)
}

// numericHost matches the odd ways to write an IPv4 address (2130706433, 0x7f.1, 0177.0.0.1) that some
// resolvers accept and Go's parser does not. No genuine decider is addressed like that.
var numericHost = regexp.MustCompile(`^(0[xX][0-9a-fA-F.]*|[0-9.]+)$`)

// hostAddrs returns the addresses a host name refers to: itself when it is an IP literal, otherwise
// what DNS says now. A name that does not resolve is not an error here (the dial-time check still applies).
func (p *DeciderPolicy) hostAddrs(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	if numericHost.MatchString(host) {
		return nil, fmt.Errorf("%q is an unusual way to write an IP address; write it in the usual dotted form", host)
	}
	lookup := p.lookupFunc()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := lookup(ctx, host)
	if err != nil {
		return nil, nil
	}
	return addrs, nil
}

func (p *DeciderPolicy) lookupFunc() func(ctx context.Context, host string) ([]netip.Addr, error) {
	if p != nil && p.lookup != nil {
		return p.lookup
	}
	return func(ctx context.Context, h string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", h)
	}
}

// CheckURL validates a decider address when it is saved.
func (p *DeciderPolicy) CheckURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("the decider address must be an http or https URL")
	}
	if u.User != nil {
		return fmt.Errorf("the decider address must not contain a user name or password")
	}
	addrs, err := p.hostAddrs(ctx, u.Hostname())
	if err != nil {
		return fmt.Errorf("the decider address: %v", err)
	}
	for _, a := range addrs {
		if err := p.CheckAddr(a); err != nil {
			return fmt.Errorf("the decider address is not allowed: %v", err)
		}
	}
	return nil
}

// errDeciderDenied marks a connection refused by the policy, so the caller can say why.
type errDeciderDenied struct{ err error }

func (e errDeciderDenied) Error() string { return e.err.Error() }
func (e errDeciderDenied) Unwrap() error { return e.err }

// client builds the HTTP client used for the decider. The policy is applied to the address actually
// dialed, after name resolution, so it cannot be bypassed by DNS that answers differently the second
// time. Redirects are not followed (a 3xx is reported as an unexpected status), proxies from the
// environment are ignored, and connections are not reused.
func (p *DeciderPolicy) client(timeout time.Duration) *http.Client {
	var resolver *net.Resolver
	if p != nil {
		resolver = p.resolver
	}
	d := net.Dialer{Timeout: 5 * time.Second, Resolver: resolver, Control: func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return errDeciderDenied{err}
		}
		a, err := netip.ParseAddr(host)
		if err != nil {
			return errDeciderDenied{fmt.Errorf("cannot check the address %q", host)}
		}
		if err := p.CheckAddr(a); err != nil {
			return errDeciderDenied{err}
		}
		return nil
	}}
	return &http.Client{
		Timeout:       timeout,
		Transport:     &http.Transport{DialContext: d.DialContext, Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func isDeciderDenied(err error) bool {
	var d errDeciderDenied
	return errors.As(err, &d)
}
