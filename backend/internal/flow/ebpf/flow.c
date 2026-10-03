//go:build ignore

// Counting-only TCP flow observer. It never looks at packets or payloads: it watches sockets change
// state. A connection is counted when it is established; the bytes it carries are added to a counter
// keyed by (role, local address, peer address, port) when it closes and, if the optional snapshot
// program is loaded, every time the collector walks the open sockets, so long-lived connections show
// their traffic while they are still open.
//
// Roles: a socket that reached ESTABLISHED from SYN_SENT is a client (it dialed); from SYN_RECV it is a
// server (it accepted). Sockets already established when the program was loaded have no role and are
// skipped, and counted as lost, so a report says honestly what it could not see.

#include "common.h"
#include "bpf_helpers.h"
#include "bpf_tracing.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define AF_INET 2
#define AF_INET6 10

#define TCP_ESTABLISHED 1
#define TCP_SYN_SENT 2
#define TCP_SYN_RECV 3
#define TCP_CLOSE 7

#define ROLE_CLIENT 1
#define ROLE_SERVER 2

// Only the fields we read; CO-RE relocates them to the running kernel's layout by name.
struct in6_addr {
	union {
		__u8 u6_addr8[16];
	} in6_u;
} __attribute__((preserve_access_index));

struct sock_common {
	__be32 skc_daddr;
	__be32 skc_rcv_saddr;
	__be16 skc_dport;
	__u16 skc_num;
	unsigned short skc_family;
	struct in6_addr skc_v6_daddr;
	struct in6_addr skc_v6_rcv_saddr;
} __attribute__((preserve_access_index));

struct socket;
struct dst_entry;
struct sock {
	struct sock_common __sk_common;
	struct socket *sk_socket;
	// The route the kernel already resolved for this socket - not the same thing as an explicit
	// SO_BINDTODEVICE, which almost nothing sets. See put_iface.
	struct dst_entry *sk_dst_cache;
	// The errno left behind by a connection that never reached ESTABLISHED (ECONNREFUSED, ETIMEDOUT,
	// ECONNRESET, EHOSTUNREACH/ENETUNREACH) - the kernel's own diagnosis, read only when the socket is
	// about to close having never gotten there. See add_failed.
	int sk_err;
	// Cumulative receive-side drops. The real kernel type is atomic_t - a struct wrapping one int, not a
	// bare int as this field was first declared here - and CO-RE's field relocation matches by kind as
	// well as name, so a plain `int` here fails to resolve against every kernel's BTF (confirmed by
	// hand: this is not a host-specific quirk, every struct sock has declared this as atomic_t for
	// years, so it fails identically everywhere, taking on_state - and with it, all of eBPF flow
	// observation - down with it). Declared with the same shape as the real type and read through
	// .counter below, the same pattern any CO-RE program reading an atomic_t field uses. Incremented
	// when this socket's own receive buffer was full and a packet had to be dropped - for both TCP and
	// UDP. A different failure mode from retransmits (the sender's view of loss on the wire): this is
	// the local application not draining its socket fast enough, not the network losing anything in
	// transit.
	struct {
		int counter;
	} __attribute__((preserve_access_index)) sk_drops;
	// The pacing rate TCP's own congestion control last set for this socket, bytes/sec (0 = no pacer
	// active yet, e.g. a very young connection). Read alongside tcp_sock.snd_cwnd below to say whether a
	// connection is currently window-limited or pacing-limited.
	unsigned long sk_pacing_rate;
	// sk_wmem_queued: bytes of this socket's own write queue the application has handed the kernel but
	// that have not yet been acknowledged (queued, not necessarily sent - TCP can be holding them back
	// for cwnd/pacing reasons of its own). Read alongside sk_sndbuf right below it: the two together say
	// whether this socket's local send buffer is actually saturated right now (wmem_queued close to or
	// at sndbuf) - the application either not writing fast enough to notice, or itself being
	// backpressured by a congested path it cannot drain into. Plain int fields, unlike sk_drops above:
	// the kernel has never declared either of these atomic_t, so no raw-cast-and-.counter treatment is
	// needed for them the way it is for sk_drops.
	int sk_wmem_queued;
	// sk_sndbuf: the current ceiling on sk_wmem_queued above (SO_SNDBUF, auto-tuned by the kernel unless
	// the application overrode it) - ordinary bookkeeping the kernel already keeps, not a new limit
	// this program imposes or measures.
	int sk_sndbuf;
} __attribute__((preserve_access_index));

// Only what reads icsk_retransmits: the count of consecutive retransmissions the RTO timer itself has
// fired for whichever segment is currently stuck at the front of the send queue - reset to 0 the moment
// any new data gets acknowledged, so it is not cumulative the way tcp_sock.total_retrans is (see
// flow_val.rto_retransmits' own doc comment for what that means for how this gets diffed). In the real
// kernel, struct inet_sock's first member is a plain struct sock, and struct inet_connection_sock's first
// member is a struct inet_sock in turn - a "first member, so same starting address" layout, exactly what
// the kernel's own inet_csk() macro relies on to turn a plain struct sock* into a struct
// inet_connection_sock* with nothing more than a cast (no real conversion happens; the bytes are the
// kernel's one real inet_connection_sock all along). This declares only the one field read through it.
struct inet_connection_sock {
	__u8 icsk_retransmits;
} __attribute__((preserve_access_index));

struct socket {
	struct sock *sk;
} __attribute__((preserve_access_index));

// Only what names the physical device a resolved route goes out over.
struct net_device {
	char name[16]; // IFNAMSIZ
	int ifindex;
} __attribute__((preserve_access_index));

struct dst_entry {
	struct net_device *dev;
} __attribute__((preserve_access_index));

struct file {
	void *private_data;
} __attribute__((preserve_access_index));

struct bpf_iter_meta;
struct task_struct;
struct bpf_iter__task_file {
	struct bpf_iter_meta *meta;
	struct task_struct *task;
	__u32 fd;
	struct file *file;
} __attribute__((preserve_access_index));

struct tcp_sock {
	__u64 bytes_received;
	__u64 bytes_acked;
	// Cumulative retransmitted-segment count for the life of the socket, and the smoothed round-trip
	// time (an 8x fixed-point average of real samples, in microseconds - see the >>3 on the read side).
	// Both are ordinary TCP congestion-control bookkeeping the kernel already keeps; nothing here samples
	// packets or timing of its own.
	__u32 total_retrans;
	__u32 srtt_us;
	// Smoothed mean deviation of the RTT samples that fed srtt_us above - the same Jacobson/Karels
	// estimator's other half, kept by the kernel as a 4x fixed-point average (>>2 recovers microseconds,
	// the same "read side shift" convention as srtt_us's >>3). This is what this file reports as jitter:
	// not a new measurement of its own, just the variance the kernel's own RTT estimator was already
	// computing and discarding.
	__u32 mdev_us;
	// Cumulative count of segments sent for the life of the socket (ordinary TCP accounting, the same
	// thing tcp_info's tcpi_segs_out reports) - paired with total_retrans above to compute a real loss
	// percentage downstream (retransmits / segs_out) instead of only ever showing a raw retransmit count
	// with nothing to divide it by.
	__u32 segs_out;
	// Current congestion window, in segments. Kernel 5.18 moved in-tree C code that touches this onto
	// tcp_snd_cwnd()/tcp_snd_cwnd_set() accessor functions instead of the bare field, but CO-RE here
	// relocates by the field's own BTF name, not by which C helper wraps it in kernel source - the field
	// itself is unchanged by that patch, so this read is unaffected by kernel version either side of it.
	__u32 snd_cwnd;
	// The current effective SMSS (segment size), in bytes, after the kernel's own PMTU discovery has
	// already adjusted it downward from the interface MTU for whatever encapsulation sits on the path -
	// an overlay tunnel (VXLAN/WireGuard/GRE) shrinks this below what a flat network's same interface
	// would give, and that shrinkage is otherwise invisible: the interface itself still reports its own
	// MTU, not the smaller size TCP is actually using after discovering the tunnel eats some of it.
	// Nothing here measures or infers the path's real MTU directly; this just reads the number TCP's own
	// discovery already settled on.
	__u32 mss_cache;
	// rcv_wnd: the receive window this socket is currently advertising to the peer, in bytes - our own
	// credit to them, not theirs to us. 0 here means we have told the peer to stop sending: this side is
	// not draining its receive buffer fast enough (or the application simply isn't reading), the
	// node-side half of a stalled connection, as opposed to retransmit growth, which is the path losing
	// packets regardless of either end's buffers.
	__u32 rcv_wnd;
	// snd_wnd: the peer's last-advertised receive window to us, in bytes - their credit to us. 0 means
	// the peer stalled us: it told this side to stop sending, which looks identical to a congested path
	// from the sender's own perspective (no cwnd/pacing problem of its own) unless this field is read
	// too.
	__u32 snd_wnd;
} __attribute__((preserve_access_index));

// One direction of one relationship. Addresses are 16 bytes; IPv4 is stored as ::ffff:a.b.c.d.
struct flow_key {
	__u8 local[16];
	__u8 peer[16];
	__u16 port; // host order: the port dialed (client) or listened on (server)
	__u8 role;
	__u8 pad;
};

struct flow_val {
	__u64 connections;
	__u64 bytes_out; // sent by the caller
	__u64 bytes_in;  // sent by the callee
	// The physical interface this socket's traffic is actually routed over right now (see put_iface). 0 /
	// empty when the kernel has not resolved a route for it yet - a fact of its own, not a guess, so it is
	// left unset rather than defaulted to something plausible-looking.
	__s32 ifindex;
	char ifname[16]; // IFNAMSIZ
	// Retransmits are summed like the byte counters (each socket's growth since it was last accounted).
	// rtt_us is a gauge, not a sum: it is overwritten by the latest sample rather than accumulated, the
	// same latest-wins treatment as ifindex/ifname above. 0 means no sample yet, not "no delay".
	__u32 retransmits;
	// rto_retransmits: the growth, since this socket was last accounted, in tcp_sock's own
	// inet_connection_sock.icsk_retransmits (see struct inet_connection_sock above) - segments
	// retransmitted because the RTO timer itself fired with no ACK at all, as opposed to a fast
	// retransmit triggered by duplicate ACKs (reordering the network recovered from on its own, without
	// ever stalling the connection). retransmits above counts both kinds together (tcp_sock.total_retrans
	// does not distinguish them); this is the subset that actually means a full round-trip-plus-backoff
	// of dead time elapsed with nothing coming back - the real, leading sign of a degrading link, where
	// retransmits alone could just as easily mean "a little packet reordering, recovered instantly".
	// Diffed like retransmits, except icsk_retransmits is not cumulative over the socket's life the way
	// total_retrans is: the kernel resets it to 0 on every acknowledged forward progress, so a lower
	// reading than last time means real progress happened, not that time ran backwards. Treated the way
	// any reset-prone counter is: current >= last is ordinary growth (current - last); current < last is
	// treated as a reset partway through, and current itself (not current - last, which would double-
	// count nothing and underflow if simply clamped to 0 like the other counters here) is added, on the
	// assumption the reset happened at or before this read and growth resumed from 0 - an approximation
	// (it slightly undercounts whatever growth happened between the actual reset and this read, in the
	// case where it reset and then grew again before this), never an overcount, and well-documented
	// rather than silently treated like an ordinary monotonic counter.
	__u32 rto_retransmits;
	__u32 rtt_us;
	// jitter_us: the RTT estimator's own mean-deviation sample (tcp_sock.mdev_us >> 2) - a gauge, same
	// latest-wins/0-means-no-sample treatment as rtt_us right above it, read at exactly the same moments.
	__u32 jitter_us;
	// segs_out: summed like the byte counters (each socket's growth in segments sent since it was last
	// accounted) - the denominator for a real loss percentage computed downstream from retransmits above;
	// never divided here, since 0 segs_out must stay "no data to compute a percentage from", not a
	// fabricated 0%.
	__u32 segs_out;
	// cwnd/pacing_bps: the kernel's own view of what is currently limiting this connection's send rate -
	// gauges, same latest-wins/0-means-no-sample treatment as rtt_us/jitter_us, sampled at the same
	// moments. cwnd is tcp_sock.snd_cwnd in segments; pacing_bps is sock.sk_pacing_rate, the pacer's
	// target rate in bytes/sec (0 while no pacer is active yet).
	__u32 cwnd;
	__u64 pacing_bps;
	// mss_bytes: tcp_sock.mss_cache, the kernel's own current effective segment size for this socket,
	// already shrunk by PMTU discovery for whatever the path actually carries - a gauge, same
	// latest-wins/0-means-no-sample treatment as cwnd/rtt_us above, sampled at the same moments. Most
	// telling on a dependency that crosses a confirmed overlay tunnel (see model.Dependency.TunnelLink on
	// the Go side): a low reading there is the concrete, otherwise-invisible cost of that encapsulation.
	__u32 mss_bytes;
	// rcv_wnd/snd_wnd: tcp_sock.rcv_wnd (our receive window, advertised to the peer) and tcp_sock.snd_wnd
	// (the peer's receive window, advertised to us) - gauges, same latest-wins/0-means-no-sample
	// treatment as cwnd/mss_bytes above, sampled at the same moments. 0 on rcv_wnd means this side told
	// the peer to stop sending (we are not draining fast enough); 0 on snd_wnd means the peer told us to
	// stop (it is the one not draining). Read together with wmem_queued/sndbuf below to tell "the network
	// is fine but one end's socket buffers are not" apart from retransmit growth, which is a path, not a
	// buffer, problem.
	__u32 rcv_wnd;
	__u32 snd_wnd;
	// wmem_queued/sndbuf: sock.sk_wmem_queued (bytes queued in this socket's own write queue right now)
	// and sock.sk_sndbuf (the current ceiling on it) - gauges, same treatment as rcv_wnd/snd_wnd above.
	// wmem_queued at or near sndbuf means this socket's local send buffer is saturated: either the
	// application is not writing fast enough to notice, or it is itself being backpressured by a
	// congested path it cannot drain into - local, node-side pressure, not the peer's.
	__u32 wmem_queued;
	__u32 sndbuf;
	// buffer_drops: sock.sk_drops' growth since this socket was last accounted - summed like retransmits,
	// not a gauge. A different failure mode from retransmits: this socket's own receive buffer overflowed
	// because nothing drained it fast enough, not the network dropping a packet in transit.
	__u32 buffer_drops;
	// mesh_bypass_syns: outbound SYNs seen leaving this exact local/peer/port (see note_mesh_bypass,
	// under observe_egress below) whose wire-level destination was not loopback - i.e. packets that left
	// the pod for their real, original destination without being redirected to a local proxy first (see
	// note_mesh_bypass's own doc comment for why "not loopback" is the whole check). Summed like
	// retransmits/buffer_drops above, not a gauge: each matching SYN adds to it (a
	// retried SYN for the same stuck attempt counts more than once, which is fine - the point is "this
	// happened", not a precise attempt count). Only ever non-zero when the name-capture opt-in is on
	// (Options.Names - this is observed from the same cgroup_skb/egress hook as sni_host/dns_query_name,
	// not from on_state), and only for the role=ROLE_CLIENT direction: a mesh sidecar intercepts a pod's
	// own outbound calls, never an inbound accept. Whether a non-zero count here actually means "this
	// workload's mesh injection is misconfigured" - as opposed to "this workload was never meshed in the
	// first place" - is a question this program cannot answer (it has no idea what Kubernetes thinks this
	// pod should be running); that cross-check against the mesh's own declared configuration happens in
	// Go, downstream (see the server's applyMeshBypassFacts).
	__u32 mesh_bypass_syns;
	// handshake_us: how long this one connection took to go from its first SYN to ESTABLISHED - a gauge
	// set exactly once, at the moment a socket reaches ESTABLISHED (see on_state), never touched again by
	// this same socket's later traffic. Distinct from rtt_us, which is the ongoing steady-state round
	// trip: a connection can have a slow handshake (a far-away or congested path at setup time) and then
	// a perfectly normal steady-state RTT, or vice versa. 0 means no sample, not "instant".
	__u32 handshake_us;
	// A connection attempt on this same key that never reached ESTABLISHED (see add_failed) - counted
	// here, on the same row as any successful connections to/from the same peer:port, because "5 fine,
	// 2 refused" is one fact about one edge, not two. connections/bytes/retransmits/rtt_us above are never
	// touched by a failed attempt, and these below are never touched by an established one.
	__u64 failed_attempts;
	__u64 failed_refused;     // ECONNREFUSED: nothing was listening, or it actively rejected the SYN
	__u64 failed_timeout;     // ETIMEDOUT: no answer at all
	__u64 failed_reset;       // ECONNRESET: torn down mid-handshake
	__u64 failed_unreachable; // EHOSTUNREACH / ENETUNREACH: routing, not the peer, said no
};

// Remembered from the first sign of a connection attempt (SYN_SENT/SYN_RECV) until the socket closes, so a
// close that never passed through ESTABLISHED can still be attributed and counted as a failure.
struct sock_info {
	struct flow_key key;
	// The kernel counters at the last time this socket was accounted, so only the growth is added next time.
	__u64 last_out;
	__u64 last_in;
	__u32 last_retrans;
	// icsk_retransmits at the last time this socket was accounted - see flow_val.rto_retransmits' own doc
	// comment for why this is diffed differently from last_retrans right above it (icsk_retransmits can
	// go down, not just up).
	__u32 last_rto_retransmits;
	__u32 last_segs_out;
	// sock.sk_drops at the last time this socket was accounted - diffed the same way last_retrans is.
	__u32 last_drops;
	// bpf_ktime_get_ns() at the moment this connection attempt was first seen (SYN_SENT/SYN_RECV) - the
	// clock handshake_us is measured from. Set once, there, and read back (before this entry is
	// overwritten) the moment the same socket reaches ESTABLISHED; never touched afterwards.
	__u64 syn_ns;
	// 0 from the moment a connection attempt is first seen (SYN_SENT/SYN_RECV) until it reaches
	// ESTABLISHED, which sets it to 1. A close while still 0 is add_failed's job, not add_flow's.
	__u8 established;
};

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_HASH);
	__type(key, struct flow_key);
	__type(value, struct flow_val);
	__uint(max_entries, 16384);
} flows SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__type(key, __u64);
	__type(value, struct sock_info);
	__uint(max_entries, 65536);
} socks SEC(".maps");

// Index 0: closes we could not attribute or count.
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__type(key, __u32);
	__type(value, __u64);
	__uint(max_entries, 1);
} lost SEC(".maps");

static __always_inline void count_lost(void) {
	__u32 k = 0;
	__u64 *v = bpf_map_lookup_elem(&lost, &k);
	if (v)
		*v += 1;
}

static __always_inline void put_addr(__u8 dst[16], __be32 v4, const struct in6_addr *v6, unsigned short family) {
	if (family == AF_INET) {
		__builtin_memset(dst, 0, 10);
		dst[10] = 0xff;
		dst[11] = 0xff;
		__builtin_memcpy(dst + 12, &v4, 4);
	} else {
		__builtin_memcpy(dst, v6->in6_u.u6_addr8, 16);
	}
}

// Best-effort: which physical interface this socket's traffic is actually going out over, from the
// destination cache the kernel already keeps for it - never a new lookup or packet peek of our own, and
// nothing kept if the cache is empty (a socket the kernel hasn't routed yet, or one that races us into
// TCP_CLOSE before ever doing so). A route can change over a long-lived connection's life (a link
// flaps, a route table updates), so this is refreshed on every add_flow call rather than read once at
// ESTABLISHED and trusted forever.
static __always_inline void put_iface(struct flow_val *v, struct sock *sk) {
	struct dst_entry *dst = 0;
	if (bpf_probe_read_kernel(&dst, sizeof(dst), &sk->sk_dst_cache) != 0 || !dst)
		return;
	struct net_device *dev = 0;
	if (bpf_probe_read_kernel(&dev, sizeof(dev), &dst->dev) != 0 || !dev)
		return;
	int idx = 0;
	if (bpf_probe_read_kernel(&idx, sizeof(idx), &dev->ifindex) == 0)
		v->ifindex = idx;
	bpf_probe_read_kernel_str(v->ifname, sizeof(v->ifname), dev->name);
}

static __always_inline void add_flow(const struct flow_key *key, struct sock *sk, __u64 conns, __u64 out, __u64 in, __u32 retrans, __u32 rto_retrans, __u32 rtt_us, __u32 jitter_us, __u32 segs_out, __u32 handshake_us, __u32 cwnd, __u64 pacing_bps, __u32 buffer_drops, __u32 mss_bytes, __u32 rcv_wnd, __u32 snd_wnd, __u32 wmem_queued, __u32 sndbuf) {
	struct flow_val zero = {};
	struct flow_val *v = bpf_map_lookup_elem(&flows, key);
	if (!v) {
		bpf_map_update_elem(&flows, key, &zero, BPF_NOEXIST);
		v = bpf_map_lookup_elem(&flows, key);
	}
	if (!v) {
		count_lost();
		return;
	}
	v->connections += conns;
	if (key->role == ROLE_CLIENT) {
		v->bytes_out += out;
		v->bytes_in += in;
	} else {
		// Report from the caller's point of view, whichever side we are on.
		v->bytes_out += in;
		v->bytes_in += out;
	}
	v->retransmits += retrans;
	v->rto_retransmits += rto_retrans;
	if (rtt_us)
		v->rtt_us = rtt_us;
	if (jitter_us)
		v->jitter_us = jitter_us;
	v->segs_out += segs_out;
	if (handshake_us)
		v->handshake_us = handshake_us;
	if (cwnd)
		v->cwnd = cwnd;
	if (pacing_bps)
		v->pacing_bps = pacing_bps;
	if (mss_bytes)
		v->mss_bytes = mss_bytes;
	if (rcv_wnd)
		v->rcv_wnd = rcv_wnd;
	if (snd_wnd)
		v->snd_wnd = snd_wnd;
	if (wmem_queued)
		v->wmem_queued = wmem_queued;
	if (sndbuf)
		v->sndbuf = sndbuf;
	v->buffer_drops += buffer_drops;
	put_iface(v, sk);
}

// A connection attempt that closed having never reached ESTABLISHED. reason is whatever sk->sk_err held
// at that moment (0 if the read failed or the kernel left nothing there); anything not one of the four
// named cases still counts toward failed_attempts, just not toward a specific one of them.
static __always_inline void add_failed(const struct flow_key *key, int reason) {
	struct flow_val zero = {};
	struct flow_val *v = bpf_map_lookup_elem(&flows, key);
	if (!v) {
		bpf_map_update_elem(&flows, key, &zero, BPF_NOEXIST);
		v = bpf_map_lookup_elem(&flows, key);
	}
	if (!v) {
		count_lost();
		return;
	}
	v->failed_attempts += 1;
	switch (reason) {
	case 111: v->failed_refused += 1; break;     // ECONNREFUSED
	case 110: v->failed_timeout += 1; break;     // ETIMEDOUT
	case 104: v->failed_reset += 1; break;       // ECONNRESET
	case 113: case 101: v->failed_unreachable += 1; break; // EHOSTUNREACH, ENETUNREACH
	default: break;
	}
}

SEC("tp_btf/inet_sock_set_state")
int BPF_PROG(on_state, struct sock *sk, int oldstate, int newstate) {
	__u64 id = (__u64)sk;

	if (newstate == TCP_SYN_SENT || newstate == TCP_SYN_RECV) {
		// The first sign of a new connection attempt, well before ESTABLISHED. Recorded now so that if it
		// never gets there, the close below still knows whose attempt it was and can count it as failed
		// rather than silently dropping it.
		unsigned short family = sk->__sk_common.skc_family;
		if (family != AF_INET && family != AF_INET6)
			return 0;

		struct sock_info si;
		__builtin_memset(&si, 0, sizeof(si));
		si.key.role = newstate == TCP_SYN_SENT ? ROLE_CLIENT : ROLE_SERVER;
		__be32 laddr = sk->__sk_common.skc_rcv_saddr;
		__be32 daddr = sk->__sk_common.skc_daddr;
		put_addr(si.key.local, laddr, &sk->__sk_common.skc_v6_rcv_saddr, family);
		put_addr(si.key.peer, daddr, &sk->__sk_common.skc_v6_daddr, family);
		si.key.port = si.key.role == ROLE_CLIENT ? __builtin_bswap16(sk->__sk_common.skc_dport) : sk->__sk_common.skc_num;
		si.established = 0;
		si.syn_ns = bpf_ktime_get_ns();
		bpf_map_update_elem(&socks, &id, &si, BPF_ANY);
		return 0;
	}

	if (newstate == TCP_ESTABLISHED) {
		__u8 role = 0;
		if (oldstate == TCP_SYN_SENT)
			role = ROLE_CLIENT;
		else if (oldstate == TCP_SYN_RECV)
			role = ROLE_SERVER;
		else
			return 0;

		unsigned short family = sk->__sk_common.skc_family;
		if (family != AF_INET && family != AF_INET6)
			return 0;

		// Read back the SYN_SENT/SYN_RECV entry recorded for this same socket (if any) before it's
		// overwritten below, purely to recover the clock handshake_us is measured from - this is the one
		// piece of that earlier entry worth carrying forward; everything else about it (its role, key)
		// is about to be rebuilt fresh from the socket's current state anyway.
		__u32 handshake_us = 0;
		struct sock_info *prior = bpf_map_lookup_elem(&socks, &id);
		if (prior && prior->syn_ns) {
			__u64 now = bpf_ktime_get_ns();
			if (now > prior->syn_ns)
				handshake_us = (__u32)((now - prior->syn_ns) / 1000);
		}

		struct sock_info si;
		__builtin_memset(&si, 0, sizeof(si));
		si.key.role = role;
		si.established = 1;
		__be32 laddr = sk->__sk_common.skc_rcv_saddr;
		__be32 daddr = sk->__sk_common.skc_daddr;
		put_addr(si.key.local, laddr, &sk->__sk_common.skc_v6_rcv_saddr, family);
		put_addr(si.key.peer, daddr, &sk->__sk_common.skc_v6_daddr, family);
		si.key.port = role == ROLE_CLIENT ? __builtin_bswap16(sk->__sk_common.skc_dport) : sk->__sk_common.skc_num;

		// The counters already include the handshake's sequence number; starting from here leaves it out.
		struct tcp_sock *tp = bpf_skc_to_tcp_sock(sk);
		if (tp) {
			si.last_out = tp->bytes_acked;
			si.last_in = tp->bytes_received;
			si.last_retrans = tp->total_retrans;
			si.last_segs_out = tp->segs_out;
		}
		// sk_drops lives on sock, not tcp_sock - read via the original sk pointer with bpf_probe_read_kernel,
		// the same defensive treatment this file already gives sk's own scalar fields outside __sk_common
		// (see sk_err's read in the TCP_CLOSE branch below).
		bpf_probe_read_kernel(&si.last_drops, sizeof(si.last_drops), &sk->sk_drops.counter);
		// icsk_retransmits lives on inet_connection_sock, not tcp_sock - read via the same raw-cast
		// technique as struct inet_connection_sock's own doc comment, with the same defensive
		// bpf_probe_read_kernel treatment as sk_drops just above (not a trusted-pointer CO-RE dereference,
		// since this is a raw cast of sk, not a helper-returned pointer like tp).
		{
			struct inet_connection_sock *icsk = (struct inet_connection_sock *)sk;
			__u8 rto0 = 0;
			bpf_probe_read_kernel(&rto0, sizeof(rto0), &icsk->icsk_retransmits);
			si.last_rto_retransmits = rto0;
		}
		if (bpf_map_update_elem(&socks, &id, &si, BPF_ANY) != 0) {
			count_lost();
			return 0;
		}
		// Counted now, so a connection that lives for days is a dependency from its first second. No RTT/
		// jitter sample exists yet this early (0, unknown, rather than a guess); handshake_us, by contrast,
		// is known exactly right now - this is the only moment it ever will be.
		add_flow(&si.key, sk, 1, 0, 0, 0, 0, 0, 0, 0, handshake_us, 0, 0, 0, 0, 0, 0, 0, 0);
		return 0;
	}

	if (newstate != TCP_CLOSE)
		return 0;

	struct sock_info *si = bpf_map_lookup_elem(&socks, &id);
	if (!si) {
		// A connection that was already up before we started, or one whose SYN_SENT/SYN_RECV transition
		// we also missed - either way, nothing we can attribute.
		if (oldstate == TCP_ESTABLISHED || oldstate == 4 /*FIN_WAIT1*/ || oldstate == 5 /*FIN_WAIT2*/ || oldstate == 8 /*CLOSE_WAIT*/ ||
		    oldstate == 9 /*LAST_ACK*/ || oldstate == 11 /*CLOSING*/)
			count_lost();
		return 0;
	}

	if (!si->established) {
		// Never got there: a failed connection attempt, not a closed one. sk_err is whatever errno the
		// kernel left on the socket at the moment it gave up - read now, before bpf_map_delete_elem, since
		// nothing after this point can still name which attempt it belonged to.
		int err = 0;
		bpf_probe_read_kernel(&err, sizeof(err), &sk->sk_err);
		add_failed(&si->key, err);
		bpf_map_delete_elem(&socks, &id);
		return 0;
	}

	// bytes_acked and bytes_received are counted in TCP sequence space: the payload plus one number for
	// the FIN, so up to one byte per direction above what the applications wrote.
	struct tcp_sock *tp = bpf_skc_to_tcp_sock(sk);
	if (tp) {
		__u64 out = tp->bytes_acked, in = tp->bytes_received;
		__u32 retrans = tp->total_retrans;
		__u32 dretrans = retrans > si->last_retrans ? retrans - si->last_retrans : 0;
		__u32 segs_out = tp->segs_out;
		__u32 dsegs = segs_out > si->last_segs_out ? segs_out - si->last_segs_out : 0;
		// sk_drops/sk_pacing_rate live on sock, not tcp_sock - read via the original sk pointer with
		// bpf_probe_read_kernel, the same defensive treatment this file already gives sk's own scalar
		// fields outside __sk_common (see sk_err just above).
		int drops = 0;
		unsigned long pacing_rate = 0;
		int wmem_queued = 0, sndbuf = 0;
		bpf_probe_read_kernel(&drops, sizeof(drops), &sk->sk_drops.counter);
		bpf_probe_read_kernel(&pacing_rate, sizeof(pacing_rate), &sk->sk_pacing_rate);
		// wmem_queued/sndbuf live on sock, not tcp_sock, like sk_drops/sk_pacing_rate right above - same
		// defensive bpf_probe_read_kernel treatment, same reason (sk here is a raw cast, not a
		// helper-returned trusted pointer like tp).
		bpf_probe_read_kernel(&wmem_queued, sizeof(wmem_queued), &sk->sk_wmem_queued);
		bpf_probe_read_kernel(&sndbuf, sizeof(sndbuf), &sk->sk_sndbuf);
		__u32 ddrops = (__u32)drops > si->last_drops ? (__u32)drops - si->last_drops : 0;
		// icsk_retransmits: see struct inet_connection_sock's and flow_val.rto_retransmits' own doc
		// comments - the same raw-cast-and-bpf_probe_read_kernel treatment as sk_drops/sk_pacing_rate
		// just above, and the reset-aware (not clamped-to-0) diff flow_val.rto_retransmits documents.
		struct inet_connection_sock *icsk = (struct inet_connection_sock *)sk;
		__u8 rto_raw = 0;
		bpf_probe_read_kernel(&rto_raw, sizeof(rto_raw), &icsk->icsk_retransmits);
		__u32 drto = rto_raw >= si->last_rto_retransmits ? (__u32)rto_raw - si->last_rto_retransmits : (__u32)rto_raw;
		// out/in themselves can be behind si->last_out/last_in here: snapshot() (iter/task_file) runs
		// concurrently against the same unlocked socks LRU entry and may have already advanced
		// last_out/last_in past what this tracepoint just read for a long-lived socket. Without this
		// guard, out - si->last_out underflows (__u64) into a multi-exabyte "traffic spike" for one
		// report cycle - the same hazard dretrans/ddrops above, and dout/din in snapshot() itself,
		// already guard against.
		__u64 dout = out > si->last_out ? out - si->last_out : 0;
		__u64 din = in > si->last_in ? in - si->last_in : 0;
		// srtt_us/mdev_us are kept as 8x/4x fixed-point averages respectively (see struct tcp_sock's
		// comment); >>3 and >>2 recover microseconds. A connection that never left slow start can close
		// with no sample of either at all (0).
		add_flow(&si->key, sk, 0, dout, din, dretrans, drto, tp->srtt_us >> 3, tp->mdev_us >> 2, dsegs, 0,
		         tp->snd_cwnd, (__u64)pacing_rate, ddrops, tp->mss_cache, tp->rcv_wnd, tp->snd_wnd,
		         (__u32)wmem_queued, (__u32)sndbuf);
	}
	bpf_map_delete_elem(&socks, &id);
	return 0;
}

// Optional: run by the collector once per window with a task_file iterator. It visits every open file of
// every process; the ones that are sockets we track have their byte counters read and the growth added.
// A socket referenced by several processes is visited more than once, but only the first visit finds growth.
SEC("iter/task_file")
int snapshot(struct bpf_iter__task_file *ctx) {
	struct file *file = ctx->file;
	if (!file)
		return 0;
	struct socket *sock = 0;
	if (bpf_probe_read_kernel(&sock, sizeof(sock), &file->private_data) != 0 || !sock)
		return 0;
	struct sock *sk = 0;
	if (bpf_probe_read_kernel(&sk, sizeof(sk), &sock->sk) != 0 || !sk)
		return 0;
	// The file is a socket, and this is its socket, only if the sock points back at it.
	struct socket *back = 0;
	if (bpf_probe_read_kernel(&back, sizeof(back), &sk->sk_socket) != 0 || back != sock)
		return 0;

	__u64 id = (__u64)sk;
	struct sock_info *si = bpf_map_lookup_elem(&socks, &id);
	if (!si)
		return 0;
	struct tcp_sock *tp = (struct tcp_sock *)sk;
	__u64 out = 0, in = 0;
	if (bpf_probe_read_kernel(&out, sizeof(out), &tp->bytes_acked) != 0 || bpf_probe_read_kernel(&in, sizeof(in), &tp->bytes_received) != 0)
		return 0;
	__u32 retrans = 0, srtt_raw = 0, mdev_raw = 0, segs_out = 0, cwnd = 0, mss_bytes = 0;
	__u32 rcv_wnd = 0, snd_wnd = 0;
	int drops = 0, wmem_queued = 0, sndbuf = 0;
	unsigned long pacing_rate = 0;
	__u8 rto_raw = 0;
	// Best-effort: a socket this old is already tracked by role/key regardless of whether these reads
	// succeed, so a failure here just means no retransmit/RTO/RTT/jitter/loss/cwnd/pacing/drops/MSS/
	// window/send-buffer update this round, not a dropped flow.
	bpf_probe_read_kernel(&retrans, sizeof(retrans), &tp->total_retrans);
	bpf_probe_read_kernel(&srtt_raw, sizeof(srtt_raw), &tp->srtt_us);
	bpf_probe_read_kernel(&mdev_raw, sizeof(mdev_raw), &tp->mdev_us);
	bpf_probe_read_kernel(&segs_out, sizeof(segs_out), &tp->segs_out);
	bpf_probe_read_kernel(&cwnd, sizeof(cwnd), &tp->snd_cwnd);
	bpf_probe_read_kernel(&mss_bytes, sizeof(mss_bytes), &tp->mss_cache);
	bpf_probe_read_kernel(&rcv_wnd, sizeof(rcv_wnd), &tp->rcv_wnd);
	bpf_probe_read_kernel(&snd_wnd, sizeof(snd_wnd), &tp->snd_wnd);
	bpf_probe_read_kernel(&drops, sizeof(drops), &sk->sk_drops.counter);
	bpf_probe_read_kernel(&pacing_rate, sizeof(pacing_rate), &sk->sk_pacing_rate);
	// wmem_queued/sndbuf: see on_state's own TCP_CLOSE branch for why these two are read via sk (a raw
	// cast here, like tp itself a few lines up) rather than through a trusted CO-RE helper pointer.
	bpf_probe_read_kernel(&wmem_queued, sizeof(wmem_queued), &sk->sk_wmem_queued);
	bpf_probe_read_kernel(&sndbuf, sizeof(sndbuf), &sk->sk_sndbuf);
	// icsk_retransmits: see struct inet_connection_sock's and flow_val.rto_retransmits' own doc comments.
	// sk here is still the raw struct sock* this whole function started from (the (struct tcp_sock *)
	// cast a few lines up is this same function's own pre-existing, separate reinterpretation of it for
	// tcp_sock's fields), so the same raw-cast technique applies starting from it, not from tp.
	struct inet_connection_sock *icsk = (struct inet_connection_sock *)sk;
	bpf_probe_read_kernel(&rto_raw, sizeof(rto_raw), &icsk->icsk_retransmits);
	__u32 ddrops = (__u32)drops > si->last_drops ? (__u32)drops - si->last_drops : 0;
	// rto_raw != si->last_rto_retransmits (not just >) belongs in the trigger condition because, unlike
	// every other counter here, a drop - the reset flow_val.rto_retransmits documents - is itself new
	// information worth a report, not nothing happening.
	if (out > si->last_out || in > si->last_in || retrans > si->last_retrans || segs_out > si->last_segs_out || ddrops ||
	    (__u32)rto_raw != si->last_rto_retransmits) {
		__u64 dout = out > si->last_out ? out - si->last_out : 0;
		__u64 din = in > si->last_in ? in - si->last_in : 0;
		__u32 dretrans = retrans > si->last_retrans ? retrans - si->last_retrans : 0;
		__u32 dsegs = segs_out > si->last_segs_out ? segs_out - si->last_segs_out : 0;
		__u32 drto = (__u32)rto_raw >= si->last_rto_retransmits ? (__u32)rto_raw - si->last_rto_retransmits : (__u32)rto_raw;
		si->last_out = out > si->last_out ? out : si->last_out;
		si->last_in = in > si->last_in ? in : si->last_in;
		si->last_retrans = retrans > si->last_retrans ? retrans : si->last_retrans;
		si->last_segs_out = segs_out > si->last_segs_out ? segs_out : si->last_segs_out;
		si->last_drops = (__u32)drops > si->last_drops ? (__u32)drops : si->last_drops;
		si->last_rto_retransmits = rto_raw;
		add_flow(&si->key, sk, 0, dout, din, dretrans, drto, srtt_raw >> 3, mdev_raw >> 2, dsegs, 0, cwnd, (__u64)pacing_rate, ddrops, mss_bytes, rcv_wnd, snd_wnd, (__u32)wmem_queued, (__u32)sndbuf);
	}
	return 0;
}

// ---------------------------------------------------------------------------
// Optional: the two hostnames a connection still carries in the clear before anything is encrypted - the
// domain name in a DNS query, and the server name (SNI) in a TLS ClientHello. Nothing else about the
// packet is read: no DNS answers, no TLS certificate, no application data, ever, and this whole program
// only runs when flowObserver.names.enabled turns it on (see the chart) - it needs CAP_NET_ADMIN on top of
// the counting-only program above's BPF/PERFMON/SYS_RESOURCE, which is why it is its own opt-in, not part
// of on_state.
//
// Attached to the egress side of the root cgroup, so every outgoing packet from every pod on the node
// passes through once: cheap to reject the overwhelming majority (wrong protocol, wrong port, not a
// ClientHello) with a couple of header-field reads, and only the rare match pays for a copy. All the
// actual parsing - DNS name decompression, walking TLS extensions - happens in Go from the raw bytes this
// hands over; nothing here does anything more than "is this worth a look", because backward jumps and
// data-dependent loops are exactly what the verifier cannot prove safe without a live kernel to iterate
// against, and this cannot be tested against one before it ships.

// bpf_helper_defs.h only forward-declares this (it needs a pointer type for its helper signatures); this
// completes it with just the one field this program reads. Unlike every other struct in this file,
// field access on the cgroup_skb/tc context type is not CO-RE-relocated by name - the compiler emits a
// load at this struct's own computed offset, and the verifier accepts it by matching that offset against
// the kernel's fixed __sk_buff layout, so len must stay the very first field, matching upstream.
struct __sk_buff {
	__u32 len;
};

#define AF_INET_PROTO_TCP 6
#define AF_INET_PROTO_UDP 17

#define NAME_KIND_DNS_QUERY 1
#define NAME_KIND_TLS_CLIENT_HELLO 2
#define NAME_KIND_DNS_LATENCY 3
// A DNS response latency sample, matched against a pending query recorded by observe_egress below and
// computed by observe_ingress further down. Unlike the two kinds above, this one carries no payload at
// all - data/len exist only because this event reuses struct name_event's layout for the plumbing
// (ring buffer, decode loop, pendingNames list) already built for the other two kinds; `len` is
// repurposed to carry the computed round-trip in microseconds instead of a byte count, and `data` is
// never written or read for this kind. Computing it still needs the response's own 2-byte transaction ID
// (the first bytes of the DNS message, right after the UDP header - never an answer record or resolved
// address), which is why this rides along under the same Options.Names opt-in as the query-name capture
// rather than being counted on by default: it is a second, independent attach point (cgroup_skb/ingress,
// not egress) reading a little of a packet's content, the same category of thing Names already covers.

// Large enough for the overwhelming majority of real DNS queries and TLS ClientHellos (which typically
// carry their SNI within the first few hundred bytes of the first flight) without ever approaching a
// single packet's usual MTU-bound size.
#define NAME_CAP 1500

struct name_event {
	__u8 saddr[16];
	__u8 daddr[16];
	__u16 sport;
	__u16 dport;
	__u8 kind;
	__u8 pad[3];
	__u32 len; // how many bytes of data are actually valid; the rest of the fixed-size array is not
	__u8 data[NAME_CAP];
};

struct {
	__uint(type, BPF_MAP_TYPE_RINGBUF);
	__uint(max_entries, 1 << 20); // 1 MiB, shared by both kinds of event; a full ring drops the event
	                              // and counts it lost rather than blocking the packet it came from.
} names SEC(".maps");

// A query recorded here by observe_egress, matched and removed by observe_ingress (see both below) -
// bounded by LRU the same way socks is, so a query whose response never arrives (lost, or the resolver
// never answers) just ages out instead of growing this map forever.
struct dns_pending_key {
	__u8 client[16];
	__u8 server[16];
	__u16 client_port;
	__u16 txid;
};

struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__type(key, struct dns_pending_key);
	__type(value, __u64); // bpf_ktime_get_ns() when the query went out
	__uint(max_entries, 4096); // DNS query volume is a small fraction of socks' own connection volume
} dns_pending SEC(".maps");

// A ring buffer has no per-record __type(value, ...) the way a hash map does, so nothing else in this
// file gives the Go generator a reason to keep struct name_event's BTF around; this unused global is that
// reason (bpf2go's own documented trick for exactly this case).
struct name_event *unused_name_event __attribute__((unused));

// put_addr4/6 mirror put_addr above but take a raw big-endian 32-bit address (as read straight off the
// wire) instead of the kernel's own struct in6_addr, since this program never touches struct sock at all.
static __always_inline void put_addr4(__u8 dst[16], __be32 v4) {
	__builtin_memset(dst, 0, 10);
	dst[10] = 0xff;
	dst[11] = 0xff;
	__builtin_memcpy(dst + 12, &v4, 4);
}

// reserve+fill+submit one event, or count it lost (ring full, or the copy itself failed - a packet that
// raced us into being shorter than the length we computed for it a few instructions earlier, say).
static __always_inline void submit_name(struct __sk_buff *skb, __u8 kind, const __u8 saddr[16], const __u8 daddr[16], __u16 sport, __u16 dport, __u32 offset, __u32 avail) {
	__u32 want = avail < NAME_CAP ? avail : NAME_CAP;
	if (want > NAME_CAP) // redundant with the line above, but the verifier gets this one for free
		want = NAME_CAP;
	struct name_event *ev = bpf_ringbuf_reserve(&names, sizeof(*ev), 0);
	if (!ev) {
		count_lost();
		return;
	}
	__builtin_memcpy(ev->saddr, saddr, 16);
	__builtin_memcpy(ev->daddr, daddr, 16);
	ev->sport = sport;
	ev->dport = dport;
	ev->kind = kind;
	ev->len = 0;
	if (want > 0 && bpf_skb_load_bytes(skb, offset, ev->data, want) == 0)
		ev->len = want;
	if (ev->len == 0) {
		// Nothing usable was actually read - don't hand Go an empty buffer to parse for no reason.
		bpf_ringbuf_discard(ev, 0);
		count_lost();
		return;
	}
	bpf_ringbuf_submit(ev, 0);
}

// ---------------------------------------------------------------------------
// Mesh-bypass detection: a sidecar mesh (Istio, Linkerd) makes a pod's outbound calls transparent to the
// application by having iptables REDIRECT every outbound TCP packet to a proxy listening on localhost,
// in the same network namespace, before it ever leaves the pod. That REDIRECT (a DNAT) happens in
// netfilter's NF_INET_LOCAL_OUT hook, which runs before the routing decision this program's own hook
// point (BPF_CGROUP_INET_EGRESS, invoked from the IP layer's own output path, after routing) sees - so
// by the time observe_egress reads an outbound packet's IP header, a properly-intercepted packet's
// destination has *already* been rewritten to the local proxy's address, while a packet that escaped
// interception still shows its real, original destination. This is the one place in this whole file that
// can see that difference: the socket's own address (what on_state/add_flow build flow_key from) is
// never rewritten by this DNAT - the kernel keeps the application's original destination there for the
// socket's own lifetime, precisely so the application stays unaware of the redirect - so on_state alone
// can never tell a redirected connection from a bypassed one; only a packet-level read of the wire can.
//
// This deliberately checks only whether the destination is loopback at all, not which exact port on it:
// Istio's outbound redirect port is 15001 (istio-iptables' own default, stable across versions) and
// Linkerd's outbound proxy port is 4140 (linkerd2-proxy-init's own default, also stable) - see
// facts.IstioOutboundPort/LinkerdOutboundPort on the Go side for where anything that needs to say these
// numbers out loud should keep citing them - but a pod can have entirely legitimate reasons of its own to
// talk to some other loopback port that have nothing to do with a mesh proxy (a sidecar's own admin/
// metrics port, an in-process cache, a health-check loop), and getting that wrong in a stricter,
// port-matching check would misreport ordinary local traffic as a mesh bypass - a worse mistake than this
// check's actual blind spot, missing a bypass that happens to go out over loopback for some unrelated
// reason (vanishingly rare in practice: a packet that already made it past the pod's own loopback
// interface is, almost by definition, not "escaping" anywhere). Consul (envoy-sidecar/consul-dataplane)
// and Kuma are also detected elsewhere in this codebase (see mesh.go's proxyContainers) but have no
// comparably long-stable, version-independent default port worth hardcoding here at all - not that this
// check would need one for them either, since it never actually compares against a specific port.

// TCP flags byte (the 14th byte of a TCP header, right after the data-offset/reserved byte).
#define TCP_FLAG_FIN 0x01
#define TCP_FLAG_SYN 0x02
#define TCP_FLAG_RST 0x04
#define TCP_FLAG_ACK 0x10

static __always_inline __u8 is_loopback(const __u8 addr[16]) {
	// IPv4-mapped (::ffff:a.b.c.d - see put_addr4/put_addr): loopback iff the embedded v4 address is in
	// 127.0.0.0/8. Checking addr[12] alone (the first mapped octet) is enough to tell a v4-mapped address
	// from a real v6 one here, since every v4 address this program ever builds goes through put_addr4/
	// put_addr and so already carries that exact prefix.
	if (addr[10] == 0xff && addr[11] == 0xff)
		return addr[12] == 127;
	// ::1, the only IPv6 loopback address - checked byte by byte (a small, compile-time-constant-bounded
	// loop the compiler unrolls) rather than through memcmp, so the comparison's byte order stays obvious
	// rather than resting on this build's native endianness.
	for (int i = 0; i < 15; i++) {
		if (addr[i] != 0)
			return 0;
	}
	return addr[15] == 1;
}

// Bumps flow_val.mesh_bypass_syns on the exact same flow_key a successful connection to this same peer
// would get from on_state/add_flow (role=ROLE_CLIENT, this socket's local/peer/port) - deliberately the
// same flows map and the same key shape, so Go never has to correlate two differently-keyed observations
// itself; see struct flow_val's own doc comment on this field for why. Only called for a packet already
// established as a pure SYN to a non-loopback destination (see observe_egress) - loopback destinations
// are skipped by the caller before this is ever reached, covering both "properly redirected to a sidecar"
// and "genuinely local traffic for some other reason", neither of which this program can tell apart from
// here, so neither is counted either way.
static __always_inline void note_mesh_bypass(const __u8 saddr[16], const __u8 daddr[16], __u16 dport) {
	struct flow_key key;
	__builtin_memset(&key, 0, sizeof(key));
	key.role = ROLE_CLIENT;
	__builtin_memcpy(key.local, saddr, 16);
	__builtin_memcpy(key.peer, daddr, 16);
	key.port = dport;

	struct flow_val zero = {};
	struct flow_val *v = bpf_map_lookup_elem(&flows, &key);
	if (!v) {
		bpf_map_update_elem(&flows, &key, &zero, BPF_NOEXIST);
		v = bpf_map_lookup_elem(&flows, &key);
	}
	if (!v) {
		count_lost();
		return;
	}
	v->mesh_bypass_syns += 1;
}

SEC("cgroup_skb/egress")
int observe_egress(struct __sk_buff *skb) {
	__u8 v;
	if (bpf_skb_load_bytes(skb, 0, &v, 1) != 0)
		return 1;
	__u8 version = v >> 4;

	__u8 proto;
	__u32 l4_off;
	__u8 saddr[16], daddr[16];

	if (version == 4) {
		__u8 ihl_byte = v & 0x0f;
		__u32 ihl = ihl_byte * 4;
		if (ihl < 20) // a malformed header, not a real IPv4 packet
			return 1;
		if (bpf_skb_load_bytes(skb, 9, &proto, 1) != 0)
			return 1;
		__be32 s4, d4;
		if (bpf_skb_load_bytes(skb, 12, &s4, 4) != 0 || bpf_skb_load_bytes(skb, 16, &d4, 4) != 0)
			return 1;
		put_addr4(saddr, s4);
		put_addr4(daddr, d4);
		l4_off = ihl;
	} else if (version == 6) {
		if (bpf_skb_load_bytes(skb, 6, &proto, 1) != 0)
			return 1;
		// Extension headers are skipped, not walked: the overwhelming majority of real traffic has none,
		// and a chain of them is a reason to miss this one packet's name, not a reason to risk a
		// data-dependent loop the verifier cannot be shown is bounded without a kernel to check it against.
		if (bpf_skb_load_bytes(skb, 8, saddr, 16) != 0 || bpf_skb_load_bytes(skb, 24, daddr, 16) != 0)
			return 1;
		l4_off = 40;
	} else {
		return 1; // not IP at all (already encapsulated, ARP, ...)
	}

	__u32 total = skb->len;
	if (l4_off >= total)
		return 1;

	if (proto == AF_INET_PROTO_UDP) {
		if (total - l4_off < 8) // shorter than a UDP header: not a real UDP packet
			return 1;
		__u16 sport_be, dport_be;
		if (bpf_skb_load_bytes(skb, l4_off, &sport_be, 2) != 0 || bpf_skb_load_bytes(skb, l4_off + 2, &dport_be, 2) != 0)
			return 1;
		__u16 dport = __builtin_bswap16(dport_be);
		if (dport != 53)
			return 1;
		__u32 payload_off = l4_off + 8;
		if (payload_off >= total)
			return 1;
		// The DNS message's own transaction id is its first 2 bytes, right after the UDP header -
		// recorded here (keyed by this query's own client/server/port/txid) so observe_ingress below can
		// find it again when the response arrives and compute how long it took. Best-effort: if this
		// read or the map write fails, the query capture above still succeeds either way - only the
		// latency sample is lost, not the hostname.
		if (total - payload_off >= 2) {
			__u16 txid;
			if (bpf_skb_load_bytes(skb, payload_off, &txid, 2) == 0) {
				struct dns_pending_key pk;
				__builtin_memset(&pk, 0, sizeof(pk));
				__builtin_memcpy(pk.client, saddr, 16);
				__builtin_memcpy(pk.server, daddr, 16);
				pk.client_port = __builtin_bswap16(sport_be);
				pk.txid = txid;
				__u64 sent_ns = bpf_ktime_get_ns();
				bpf_map_update_elem(&dns_pending, &pk, &sent_ns, BPF_ANY);
			}
		}
		submit_name(skb, NAME_KIND_DNS_QUERY, saddr, daddr, __builtin_bswap16(sport_be), dport, payload_off, total - payload_off);
		return 1;
	}

	if (proto == AF_INET_PROTO_TCP) {
		if (total - l4_off < 20) // shorter than a minimal TCP header: not a real TCP packet
			return 1;
		__u8 doff_byte, flags_byte;
		if (bpf_skb_load_bytes(skb, l4_off + 12, &doff_byte, 1) != 0 || bpf_skb_load_bytes(skb, l4_off + 13, &flags_byte, 1) != 0)
			return 1;
		__u32 tcp_hdr_len = (doff_byte >> 4) * 4;
		if (tcp_hdr_len < 20)
			return 1;
		// Read once, up front, for both uses below (the ClientHello capture further down, and the
		// mesh-bypass check right here) - previously this read only happened right before submit_name,
		// which a pure SYN (no payload at all) never reaches.
		__u16 sport_be, dport_be;
		if (bpf_skb_load_bytes(skb, l4_off, &sport_be, 2) != 0 || bpf_skb_load_bytes(skb, l4_off + 2, &dport_be, 2) != 0)
			return 1;
		// A pure SYN - SYN set, ACK/RST/FIN all clear - is the first packet of a new outbound connection;
		// see note_mesh_bypass's own doc comment for why this, not the ClientHello match below, is where
		// mesh-bypass detection belongs. Every other packet on this same connection (the rest of the
		// handshake, and everything after it) carries no new information for this check, so this only
		// ever runs once per connection attempt (twice if the SYN itself is retransmitted).
		if ((flags_byte & (TCP_FLAG_SYN | TCP_FLAG_ACK | TCP_FLAG_RST | TCP_FLAG_FIN)) == TCP_FLAG_SYN && !is_loopback(daddr)) {
			note_mesh_bypass(saddr, daddr, __builtin_bswap16(dport_be));
		}
		__u32 payload_off = l4_off + tcp_hdr_len;
		if (payload_off >= total || total - payload_off < 6)
			return 1; // not enough of a first segment here to be a ClientHello's fixed header
		// TLS record: type=handshake(0x16), version major=3 (any TLS 1.x minor); handshake: type=ClientHello(0x01).
		// Checked as individual byte reads, not a single 6-byte struct, so a false-positive match on some
		// other protocol that merely starts with 0x16 0x03 costs one extra read, not a wrong parse - Go's
		// own parser re-validates the whole record/handshake shape before trusting anything in it anyway.
		__u8 b0, b1, b5;
		if (bpf_skb_load_bytes(skb, payload_off, &b0, 1) != 0 || bpf_skb_load_bytes(skb, payload_off + 1, &b1, 1) != 0 ||
		    bpf_skb_load_bytes(skb, payload_off + 5, &b5, 1) != 0)
			return 1;
		if (b0 != 0x16 || b1 != 0x03 || b5 != 0x01)
			return 1;
		submit_name(skb, NAME_KIND_TLS_CLIENT_HELLO, saddr, daddr, __builtin_bswap16(sport_be), __builtin_bswap16(dport_be), payload_off, total - payload_off);
		return 1;
	}

	return 1;
}


// ---------------------------------------------------------------------------
// Optional, under the same Options.Names opt-in as observe_egress above: DNS response latency. A query's
// name is only ever visible on egress (observe_egress's own DNS branch, above); the answer always
// arrives on ingress, so measuring how long it took needs this second, independent attach point -
// cgroup_skb/ingress, not egress - matching each response's transaction id and sender against the
// pending map observe_egress just wrote into. Counting-only in the sense that matters for privacy: this
// reads the response's own 2-byte transaction id (right after its UDP header) and nothing else, never an
// answer record or resolved address.
SEC("cgroup_skb/ingress")
int observe_ingress(struct __sk_buff *skb) {
	__u8 v;
	if (bpf_skb_load_bytes(skb, 0, &v, 1) != 0)
		return 1;
	__u8 version = v >> 4;

	__u8 proto;
	__u32 l4_off;
	__u8 saddr[16], daddr[16];

	if (version == 4) {
		__u8 ihl_byte = v & 0x0f;
		__u32 ihl = ihl_byte * 4;
		if (ihl < 20)
			return 1;
		if (bpf_skb_load_bytes(skb, 9, &proto, 1) != 0)
			return 1;
		__be32 s4, d4;
		if (bpf_skb_load_bytes(skb, 12, &s4, 4) != 0 || bpf_skb_load_bytes(skb, 16, &d4, 4) != 0)
			return 1;
		put_addr4(saddr, s4);
		put_addr4(daddr, d4);
		l4_off = ihl;
	} else if (version == 6) {
		if (bpf_skb_load_bytes(skb, 6, &proto, 1) != 0)
			return 1;
		if (bpf_skb_load_bytes(skb, 8, saddr, 16) != 0 || bpf_skb_load_bytes(skb, 24, daddr, 16) != 0)
			return 1;
		l4_off = 40;
	} else {
		return 1; // not IP at all
	}

	if (proto != AF_INET_PROTO_UDP)
		return 1;

	__u32 total = skb->len;
	if (l4_off >= total || total - l4_off < 8) // shorter than a UDP header: not a real UDP packet
		return 1;
	__u16 sport_be, dport_be;
	if (bpf_skb_load_bytes(skb, l4_off, &sport_be, 2) != 0 || bpf_skb_load_bytes(skb, l4_off + 2, &dport_be, 2) != 0)
		return 1;
	__u16 sport = __builtin_bswap16(sport_be); // the sender's port - 53 for a real DNS response
	if (sport != 53)
		return 1;
	__u32 payload_off = l4_off + 8;
	if (total - payload_off < 2)
		return 1;
	__u16 txid;
	if (bpf_skb_load_bytes(skb, payload_off, &txid, 2) != 0)
		return 1;

	// This packet arrives FROM the resolver (saddr) TO the client (daddr) - the mirror image of the
	// query observe_egress recorded, which keyed on the client's own saddr/daddr/sport. Swapping here
	// reconstructs that same key from the reply's point of view.
	struct dns_pending_key pk;
	__builtin_memset(&pk, 0, sizeof(pk));
	__builtin_memcpy(pk.client, daddr, 16);
	__builtin_memcpy(pk.server, saddr, 16);
	pk.client_port = __builtin_bswap16(dport_be);
	pk.txid = txid;

	__u64 *sent_ns = bpf_map_lookup_elem(&dns_pending, &pk);
	if (!sent_ns)
		return 1; // no matching query seen (missed the egress hook, or this is a retransmitted/duplicate answer)
	__u64 now = bpf_ktime_get_ns();
	__u64 sent = *sent_ns;
	bpf_map_delete_elem(&dns_pending, &pk);
	if (now <= sent)
		return 1; // clock oddity; never report a negative or zero latency

	struct name_event *ev = bpf_ringbuf_reserve(&names, sizeof(*ev), 0);
	if (!ev) {
		count_lost();
		return 1;
	}
	__builtin_memcpy(ev->saddr, daddr, 16); // local/client, matching the query event's own saddr=client convention
	__builtin_memcpy(ev->daddr, saddr, 16); // peer/resolver
	// name_event's sport/dport are stored in host byte order (see submit_name's own callers, which always
	// bswap16 before passing them in) - bswap here too, rather than storing the raw network-order bytes
	// just read off the wire.
	ev->sport = __builtin_bswap16(dport_be);
	ev->dport = __builtin_bswap16(sport_be); // 53, matching the query event's own dport=53 convention
	ev->kind = NAME_KIND_DNS_LATENCY;
	ev->len = (__u32)((now - sent) / 1000); // repurposed: microseconds, not a byte count - see the kind's own comment
	bpf_ringbuf_submit(ev, 0);
	return 1;
}
// ---- SNAT / ephemeral port exhaustion ----
//
// inet_hash_connect() is the function both tcp_v4_connect() and tcp_v6_connect() call to pick and bind
// an ephemeral source port before dialing out (net/ipv4/inet_hash_connect.c in the kernel source); it
// returns -EADDRNOTAVAIL when the port range this socket is allowed to use is exhausted - typically
// because SNAT (a NAT gateway, or a cluster's own egress masquerading) has used up every port it can
// hand out for the destination, or the host's own ephemeral port range is fully in use. This matters
// most for a gateway node juggling many short-lived outbound connections (CoAP/MQTT-style fan-out):
// such a node can keep reporting perfectly healthy-looking flows while silently failing a growing
// fraction of brand-new ones - a failure this file's every other signal is blind to, since they all
// assume a connection that never reached ESTABLISHED simply was not there to count.
//
// inet_hash_connect is an internal kernel function, not a stable tracepoint or syscall ABI: its name,
// signature and even its existence are free to change on any kernel refactor, unlike tp_btf's
// inet_sock_set_state tracepoint above, which the kernel commits to keeping. Verified against this
// build's own BTF (bpftool btf dump file /sys/kernel/btf/vmlinux) to currently be:
//   int inet_hash_connect(struct inet_timewait_death_row *death_row, struct sock *sk)
// A kernel that renames, inlines away or restructures it will simply fail the fexit program's attach
// (the kernel's own BTF-based bpf_check_attach_btf_id rejects an argument-count/type mismatch at attach
// time - a loud failure, not a silent misread), at which point this whole signal goes back to always
// reading 0 until it is adjusted for the new kernel. This is an accepted, deliberate tradeoff: the
// alternative (a stable but much coarser per-interface or per-socket proxy for "ran out of ports") does
// not exist to attach to instead.

// Index 0: connection attempts that failed with EADDRNOTAVAIL (ephemeral port / SNAT exhaustion) since
// this program was loaded. A PERCPU_ARRAY like lost above, but deliberately never reset by Collect(),
// unlike every flow/socket counter in this file: this has no natural per-window grouping worth losing
// precision over the way a byte or connection count does, and a running total survives a report being
// dropped or delayed without double-counting anything (it is read, never read-and-cleared). It resets
// to 0 only when the collector process - and so this BPF program - restarts.
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__type(key, __u32);
	__type(value, __u64);
	__uint(max_entries, 1);
} snat_exhaustion SEC(".maps");

static __always_inline void count_snat_exhaustion(void) {
	__u32 k = 0;
	__u64 *v = bpf_map_lookup_elem(&snat_exhaustion, &k);
	if (v)
		*v += 1;
}

// Preferred path: a BTF-validated fexit trampoline, cheaper than a kprobe and immune to the target
// having been inlined differently than expected, since BTF describes the function as it actually exists
// in this build's own vmlinux rather than as a fixed offset. death_row is never dereferenced (this only
// ever reads the return value), so it is left as void* rather than pulling in a CO-RE redeclaration of
// struct inet_timewait_death_row that nothing here would otherwise need.
SEC("fexit/inet_hash_connect")
int BPF_PROG(on_hash_connect_fexit, void *death_row, struct sock *sk, int ret) {
	if (ret == -99) // -EADDRNOTAVAIL (see /usr/include/asm-generic/errno.h) - not worth a #include for one constant
		count_snat_exhaustion();
	return 0;
}

// Fallback for a kernel whose BTF does not describe inet_hash_connect, or describes it with a different
// argument count/types than the fexit program above declares (which fails that attach outright, per its
// own doc comment above): a plain kretprobe, which only needs the symbol to exist and be kprobe-able,
// not any particular argument layout - the return value alone is all BPF_KRETPROBE exposes, which is
// all this needs anyway. See observer_bpf.go's openSnatExhaustion for how the two are tried in order;
// only one of them ends up attached on any given kernel, so this counter is never double-counted.
SEC("kretprobe/inet_hash_connect")
int BPF_KRETPROBE(on_hash_connect_kretprobe, int ret) {
	if (ret == -99)
		count_snat_exhaustion();
	return 0;
}
