package testkit

import (
	"context"
	"crypto/tls"
	"net"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agents"
	"continuum/internal/pki"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Serve starts the agents' gRPC listener on a free local port with svc behind it, and stops it when the test ends. It returns the address.
func Serve(t *testing.T, ag *agents.Service, certs *pki.ServerCerts, svc continuumv1.AgentServiceServer) string {
	t.Helper()
	srv := ag.NewGRPC(certs, svc)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	t.Cleanup(srv.Stop)
	return l.Addr().String()
}

// Dial connects to the listener at addr with the TLS configuration given.
func Dial(t *testing.T, addr string, cfg *tls.Config) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return cc
}

// DialProxied is Dial, but first writes a v1 PROXY protocol line declaring proxiedIP on the raw TCP connection, the way a load balancer
// configured to send it would - before grpc-go layers TLS on top of the same connection.
func DialProxied(t *testing.T, addr string, cfg *tls.Config, proxiedIP string) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
			if err != nil {
				return nil, err
			}
			if _, err := c.Write([]byte("PROXY TCP4 " + proxiedIP + " 127.0.0.1 54321 8443\r\n")); err != nil {
				c.Close()
				return nil, err
			}
			return c, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return cc
}
