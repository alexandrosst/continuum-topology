package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/pki"
	"continuum/internal/store"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/tap"
	"google.golang.org/protobuf/proto"
)

type ctxKey struct{}

// AgentFrom returns the authenticated agent for a call that passed the mTLS interceptor.
func AgentFrom(ctx context.Context) (store.Agent, bool) {
	a, ok := ctx.Value(ctxKey{}).(store.Agent)
	return a, ok
}

func toStatus(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if !errors.As(err, &e) {
		return status.Error(codes.Internal, "internal error")
	}
	code := map[Kind]codes.Code{
		KindInvalid: codes.InvalidArgument, KindUnauthenticated: codes.Unauthenticated, KindNotFound: codes.NotFound,
		KindConflict: codes.FailedPrecondition, KindRateLimited: codes.ResourceExhausted, KindInternal: codes.Internal, KindForbidden: codes.PermissionDenied,
		KindTwoFactorRequired: codes.Unauthenticated, // never actually reached over gRPC (agents don't sign in), kept correct for safety
	}[e.Kind]
	return status.Error(code, e.Msg)
}

func peerIP(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok {
		if h, _, err := net.SplitHostPort(p.Addr.String()); err == nil {
			return h
		}
		return p.Addr.String()
	}
	return "unknown"
}

// ServerTLS: TLS 1.3 only. A client certificate is verified when presented but is not
// required by the handshake, because enrolling agents have none yet; the interceptor
// below requires it for every AgentService call.
func (c *Core) ServerTLS(certs *pki.ServerCerts) *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: certs.GetCertificate,
		ClientAuth:     tls.VerifyClientCertIfGiven,
		ClientCAs:      c.CA.Pool(),
	}
}

// identity extracts and re-checks the agent behind the client certificate.
func (c *Core) identity(ctx context.Context) (store.Agent, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return store.Agent{}, status.Error(codes.Unauthenticated, "no peer")
	}
	ti, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(ti.State.VerifiedChains) == 0 {
		return store.Agent{}, status.Error(codes.Unauthenticated, "a client certificate is required")
	}
	leaf := ti.State.VerifiedChains[0][0]
	if !hasClientAuth(leaf) {
		return store.Agent{}, status.Error(codes.Unauthenticated, "certificate is not valid for client authentication")
	}
	a, err := c.AuthorizeAgent(ctx, leaf.Subject.CommonName)
	if err != nil {
		// An agent that was revoked (or removed) while it was offline should learn that it is over, not
		// just that it is unauthenticated: it would otherwise retry for ever. It holds a certificate this
		// server issued to that very agent, so telling it its own state discloses nothing.
		if prev, gerr := c.Store.GetAgent(ctx, leaf.Subject.CommonName); gerr == nil && (prev.Status == store.StatusRevoked || prev.Status == store.StatusRejected) {
			st := status.New(codes.Unauthenticated, "agent is not approved")
			if withDetail, derr := st.WithDetails(&continuumv1.Revoked{Reason: prev.Reason}); derr == nil {
				st = withDetail
			}
			return store.Agent{}, st.Err()
		}
		return store.Agent{}, toStatus(err)
	}
	// The certificate must be the one currently issued to this agent or an earlier one from the
	// same identity; revocation is what matters, and that is checked above on every call.
	return a, nil
}

func hasClientAuth(c *x509.Certificate) bool {
	for _, u := range c.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth {
			return true
		}
	}
	return false
}

// tap runs before a request's body is read, so it is where unauthenticated callers are cut off
// cheaply: an enrollment call is rate limited per address, and an AgentService call without a
// verified client certificate is refused without its (up to 16 MB) message ever being received.
func (c *Core) tap(ctx context.Context, info *tap.Info) (context.Context, error) {
	if strings.HasPrefix(info.FullMethodName, enrollmentPrefix) {
		if c.TapRL != nil && !c.TapRL.Allow("tap:"+LimitKey(peerIP(ctx))) {
			return nil, status.Error(codes.ResourceExhausted, "too many requests")
		}
		return ctx, nil
	}
	if p, ok := peer.FromContext(ctx); ok {
		if ti, ok := p.AuthInfo.(credentials.TLSInfo); ok && len(ti.State.VerifiedChains) > 0 {
			return ctx, nil
		}
	}
	Metrics.authFailures.Add(1)
	return nil, status.Error(codes.Unauthenticated, "a client certificate is required")
}

const enrollmentPrefix = "/continuum.v1.Enrollment/"

func (c *Core) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (resp any, err error) {
	defer c.recoverPanic(info.FullMethod, &err)
	if strings.HasPrefix(info.FullMethod, enrollmentPrefix) {
		// The listener already holds unauthenticated connections to a small message size; this also covers
		// a caller that authenticated and then sent an enrollment call.
		if m, ok := req.(proto.Message); ok && proto.Size(m) > maxEnrollMessage {
			return nil, status.Error(codes.InvalidArgument, "message too large for an enrollment call")
		}
		return h(ctx, req)
	}
	a, err := c.identity(ctx)
	if err != nil {
		return nil, err
	}
	return h(context.WithValue(ctx, ctxKey{}, a), req)
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w wrappedStream) Context() context.Context { return w.ctx }

func (c *Core) stream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) (err error) {
	defer c.recoverPanic(info.FullMethod, &err)
	a, err := c.identity(ss.Context())
	if err != nil {
		Metrics.authFailures.Add(1)
		return err
	}
	return h(srv, wrappedStream{ss, context.WithValue(ss.Context(), ctxKey{}, a)})
}

// Message size limits. A connection without a verified client certificate can only enroll (everything
// else is refused before its body is read), and an enrollment request is a token, a fingerprint and a
// small certificate request, so those connections are held to a tiny limit: the gRPC layer refuses a
// larger message from the length in its header, before any of it is read into memory. Connections that
// present a certificate from our CA carry the agents' facts and keep the large limit.
const (
	maxEnrollMessage = 64 << 10
	maxAgentMessage  = 16 << 20
	// maxHandshakes bounds TLS handshakes in progress at once; handshakeTimeout is how long one may take.
	maxHandshakes    = 512
	handshakeTimeout = 10 * time.Second
)

// GRPCServer is the agents' listener. It behaves like one gRPC server, but is two: each connection is
// TLS-handshaken here, and by whether the client proved an identity is handed to the server that has the
// message size limit for it. Serve, Stop and GracefulStop are used as on *grpc.Server.
type GRPCServer struct {
	tlsCfg     *tls.Config
	anon, auth *grpc.Server
	// trustProxy is Core.TrustAgentProxy, copied in at construction: every connection must present a
	// valid PROXY protocol header before its TLS handshake (readProxyHeader in proxyproto.go), and the
	// address it declares - not the TCP socket's own peer, which would be the proxy itself - is what
	// peerIP and gRPC's peer.FromContext report from here on.
	trustProxy bool

	mu      sync.Mutex
	l       net.Listener
	stopped bool
}

// NewGRPC builds the listener-facing gRPC server. Every method not under the Enrollment service
// requires an approved agent's client certificate.
func (c *Core) NewGRPC(certs *pki.ServerCerts, agentSvc continuumv1.AgentServiceServer) *GRPCServer {
	cfg := c.ServerTLS(certs)
	cfg.NextProtos = []string{"h2"}
	build := func(maxRecv int) *grpc.Server {
		s := grpc.NewServer(
			grpc.Creds(handshaken{}),
			grpc.UnaryInterceptor(c.unary),
			grpc.StreamInterceptor(c.stream),
			grpc.InTapHandle(c.tap),
			grpc.MaxRecvMsgSize(maxRecv),
			grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 20 * time.Second, PermitWithoutStream: true}),
		)
		continuumv1.RegisterEnrollmentServer(s, &enrollmentServer{c: c})
		continuumv1.RegisterAgentServiceServer(s, agentSvc)
		return s
	}
	return &GRPCServer{tlsCfg: cfg, anon: build(maxEnrollMessage), auth: build(maxAgentMessage), trustProxy: c.TrustAgentProxy}
}

// handshaken is the transport credentials of a server that is handed connections whose TLS handshake is
// already done (by GRPCServer): it only reports what the handshake established.
type handshaken struct{}

func (handshaken) ClientHandshake(context.Context, string, net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, errors.New("this transport is for servers")
}

func (handshaken) ServerHandshake(c net.Conn) (net.Conn, credentials.AuthInfo, error) {
	tc, ok := c.(*tls.Conn)
	if !ok {
		return nil, nil, errors.New("connection is not TLS")
	}
	return c, credentials.TLSInfo{State: tc.ConnectionState(), CommonAuthInfo: credentials.CommonAuthInfo{SecurityLevel: credentials.PrivacyAndIntegrity}}, nil
}

func (handshaken) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{SecurityProtocol: "tls", SecurityVersion: "1.3"}
}
func (h handshaken) Clone() credentials.TransportCredentials { return h }
func (handshaken) OverrideServerName(string) error           { return nil }

// connQueue is the listener each inner server accepts from.
type connQueue struct {
	addr  net.Addr
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newConnQueue(addr net.Addr) *connQueue {
	return &connQueue{addr: addr, conns: make(chan net.Conn, 64), done: make(chan struct{})}
}

func (q *connQueue) Accept() (net.Conn, error) {
	select {
	case c := <-q.conns:
		return c, nil
	case <-q.done:
		return nil, net.ErrClosed
	}
}
func (q *connQueue) Close() error   { q.once.Do(func() { close(q.done) }); return nil }
func (q *connQueue) Addr() net.Addr { return q.addr }

func (q *connQueue) push(c net.Conn) {
	select {
	case q.conns <- c:
	case <-q.done:
		c.Close()
	}
}

// Serve accepts connections on l until Stop or GracefulStop, then returns nil (any other end is an error).
func (g *GRPCServer) Serve(l net.Listener) error {
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		l.Close()
		return grpc.ErrServerStopped
	}
	g.l = l
	g.mu.Unlock()
	anonQ, authQ := newConnQueue(l.Addr()), newConnQueue(l.Addr())
	anonDone, authDone := make(chan struct{}), make(chan struct{})
	go func() { _ = g.anon.Serve(anonQ); close(anonDone) }()
	go func() { _ = g.auth.Serve(authQ); close(authDone) }()
	err := g.accept(l, anonQ, authQ)
	if err != nil { // the listener failed on its own: take the servers down with it
		anonQ.Close()
		authQ.Close()
		g.anon.Stop()
		g.auth.Stop()
	}
	// Otherwise Stop or GracefulStop is stopping the servers; let a graceful one finish.
	<-anonDone
	<-authDone
	return err
}

func (g *GRPCServer) accept(l net.Listener, anonQ, authQ *connQueue) error {
	slots := make(chan struct{}, maxHandshakes)
	for {
		conn, err := l.Accept()
		if err != nil {
			g.mu.Lock()
			stopped := g.stopped
			g.mu.Unlock()
			if stopped {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close() // too many handshakes in progress
			continue
		}
		go func() {
			defer func() { <-slots }()
			if g.trustProxy {
				// The header must arrive promptly, same as the handshake that follows it - a source that
				// opens a connection and sends nothing is exactly what this deadline (and, later,
				// maxHandshakes) exist to bound.
				_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
				addr, ok, err := readProxyHeader(conn)
				if err != nil {
					conn.Close()
					return
				}
				_ = conn.SetReadDeadline(time.Time{})
				if ok {
					conn = &proxiedConn{Conn: conn, remote: addr}
				}
			}
			tc := tls.Server(conn, g.tlsCfg)
			ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
			defer cancel()
			if err := tc.HandshakeContext(ctx); err != nil {
				conn.Close()
				return
			}
			st := tc.ConnectionState()
			if st.NegotiatedProtocol != "h2" {
				conn.Close()
				return
			}
			if len(st.VerifiedChains) > 0 {
				authQ.push(tc)
			} else {
				anonQ.push(tc)
			}
		}()
	}
}

// Stop closes the listener and every connection at once.
func (g *GRPCServer) Stop() {
	g.markStopped()
	g.anon.Stop()
	g.auth.Stop()
}

// StopWithin is GracefulStop with a deadline. An agent's stream lasts for as long as the agent runs, so a graceful stop
// that waits for streams to end would wait for ever: after d whatever is still open is closed (the agents reconnect on
// their own, to this server or its replacement).
func (g *GRPCServer) StopWithin(d time.Duration) {
	done := make(chan struct{})
	go func() { g.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		g.Stop()
		<-done
	}
}

// Serving reports whether the listener is up and accepting connections (used by /readyz).
func (g *GRPCServer) Serving() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.l != nil && !g.stopped
}

// GracefulStop stops accepting and lets calls in progress finish.
func (g *GRPCServer) GracefulStop() {
	g.markStopped()
	var wg sync.WaitGroup
	for _, s := range []*grpc.Server{g.anon, g.auth} {
		wg.Add(1)
		go func() { defer wg.Done(); s.GracefulStop() }()
	}
	wg.Wait()
}

func (g *GRPCServer) markStopped() {
	g.mu.Lock()
	g.stopped = true
	l := g.l
	g.mu.Unlock()
	if l != nil {
		l.Close()
	}
}

type enrollmentServer struct {
	continuumv1.UnimplementedEnrollmentServer
	c *Core
}

func (e *enrollmentServer) Enroll(ctx context.Context, r *continuumv1.EnrollRequest) (*continuumv1.EnrollResponse, error) {
	resp, err := e.c.Enroll(ctx, peerIP(ctx), r)
	return resp, toStatus(err)
}

func (e *enrollmentServer) Rejoin(ctx context.Context, r *continuumv1.RejoinRequest) (*continuumv1.RenewResponse, error) {
	resp, err := e.c.Rejoin(ctx, peerIP(ctx), r)
	return resp, toStatus(err)
}

func (e *enrollmentServer) PollEnrollment(ctx context.Context, r *continuumv1.PollRequest) (*continuumv1.PollResponse, error) {
	resp, err := e.c.Poll(ctx, peerIP(ctx), r)
	return resp, toStatus(err)
}

// BaseAgentService implements Renew; Connect is provided by the sync hub.
type BaseAgentService struct {
	continuumv1.UnimplementedAgentServiceServer
	C *Core
}

func (b *BaseAgentService) Renew(ctx context.Context, r *continuumv1.RenewRequest) (*continuumv1.RenewResponse, error) {
	a, ok := AgentFrom(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "no agent")
	}
	leaf, notAfter, err := b.C.Renew(ctx, a.ID, r.CsrDer)
	if err != nil {
		return nil, toStatus(err)
	}
	return &continuumv1.RenewResponse{LeafDer: leaf, CaDer: b.C.CA.DER, NotAfter: tsProto(&notAfter)}, nil
}
