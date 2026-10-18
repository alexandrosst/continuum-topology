package server

import (
	"context"
	"crypto/tls"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/pki"
)

// agentStream connects an approved agent and returns its stream, past the hello.
func (r *hubRig) agentStream(t *testing.T, ctx context.Context, id string, key any, leaf []byte) continuumv1.AgentService_ConnectClient {
	t.Helper()
	cert := &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}
	c := continuumv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", cert)))
	s, err := c.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Hello{Hello: &continuumv1.Hello{AgentVersion: "t"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); err != nil { // config
		t.Fatal(err)
	}
	return s
}
