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
	__u32 rtt_us;
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

static __always_inline void add_flow(const struct flow_key *key, struct sock *sk, __u64 conns, __u64 out, __u64 in, __u32 retrans, __u32 rtt_us) {
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
	if (rtt_us)
		v->rtt_us = rtt_us;
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
		}
		if (bpf_map_update_elem(&socks, &id, &si, BPF_ANY) != 0) {
			count_lost();
			return 0;
		}
		// Counted now, so a connection that lives for days is a dependency from its first second. No RTT
		// sample exists yet this early, so 0 (unknown) rather than a guess.
		add_flow(&si.key, sk, 1, 0, 0, 0, 0);
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
		// srtt_us is kept as an 8x fixed-point average (see struct tcp_sock's comment); >>3 recovers
		// microseconds. A connection that never left slow start can close with no sample at all (0).
		add_flow(&si->key, sk, 0, out - si->last_out, in - si->last_in, dretrans, tp->srtt_us >> 3);
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
	__u32 retrans = 0, srtt_raw = 0;
	// Best-effort: a socket this old is already tracked by role/key regardless of whether these two reads
	// succeed, so a failure here just means no retransmit/RTT update this round, not a dropped flow.
	bpf_probe_read_kernel(&retrans, sizeof(retrans), &tp->total_retrans);
	bpf_probe_read_kernel(&srtt_raw, sizeof(srtt_raw), &tp->srtt_us);
	if (out > si->last_out || in > si->last_in || retrans > si->last_retrans) {
		__u64 dout = out > si->last_out ? out - si->last_out : 0;
		__u64 din = in > si->last_in ? in - si->last_in : 0;
		__u32 dretrans = retrans > si->last_retrans ? retrans - si->last_retrans : 0;
		si->last_out = out > si->last_out ? out : si->last_out;
		si->last_in = in > si->last_in ? in : si->last_in;
		si->last_retrans = retrans > si->last_retrans ? retrans : si->last_retrans;
		add_flow(&si->key, sk, 0, dout, din, dretrans, srtt_raw >> 3);
	}
	return 0;
}
