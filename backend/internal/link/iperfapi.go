package link

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/probe"
)

// How the iperf3 credentials get from the agent to the pods that use them.
//
// The agent receives them from the server in its Config (mutually authenticated TLS) and keeps them in memory. Two kinds of
// pod need them, and each asks the agent with a signed request of its own, so that neither can read what only the other needs:
//
//   - the prober (the cluster that runs the tests) asks for its peers as before and, with `iperf` in the request, gets the
//     client credentials of the peers it tests, sealed (PeersDoc.IperfSealed). It holds public keys and passwords, never a
//     private key;
//   - the responder (the iperf3 server on every node) asks POST /v1/link/iperf-server with a secret and a MAC context that
//     only it and the agent have, and gets its cluster's private key and users file, sealed. It cannot read the peer list, and
//     the prober cannot read this.
//
// The agent's link address is plain HTTP inside the cluster network, so the credentials never travel in the clear: the answer is
// sealed (AES-256-GCM) under a key derived from the endpoint's own secret, with the request's signature as additional data. Someone
// who watches the network sees ciphertext; a captured answer cannot be replayed to a different request; a forged answer is refused.
// The requests carry a random nonce, are signed with the existing scheme (timestamp window, MAC bound to a context, replay cache), and
// a replayed request is refused before anything is read.
const (
	// PathIperfServer is where the agent answers the responder.
	PathIperfServer = "/v1/link/iperf-server"
	// IperfServerNode is the name the responder signs as; the agent accepts no other for this path.
	IperfServerNode = "link-iperf-server"

	iperfServerContext = "ikhnos-link-iperf-server-v1"
	iperfServerSeal    = "ikhnos-link-iperf-server-seal-v1"
	peersIperfSeal     = "ikhnos-link-peers-iperf-seal-v1"

	maxCredBody     = 1 << 10
	maxSealedAnswer = 256 << 10
)

// credRequest is the body of a signed request for credentials. The nonce makes every request, and so every signature,
// unique; it is also what the answer is bound to.
type credRequest struct {
	Nonce string `json:"nonce"`
	Iperf bool   `json:"iperf,omitempty"`
}

func newCredRequest(iperf bool) ([]byte, error) {
	n := make([]byte, 16)
	if _, err := rand.Read(n); err != nil {
		return nil, err
	}
	return json.Marshal(credRequest{Nonce: hex.EncodeToString(n), Iperf: iperf})
}

func parseCredRequest(body []byte) (credRequest, error) {
	var r credRequest
	if err := json.Unmarshal(body, &r); err != nil {
		return r, errors.New("bad request body")
	}
	if len(r.Nonce) < 16 || len(r.Nonce) > 64 {
		return r, errors.New("bad nonce")
	}
	return r, nil
}

func sealKey(secret []byte, label string) ([]byte, error) {
	return hkdf.Key(sha256.New, secret, []byte("ikhnos-link-seal-v1"), label, 32)
}

// seal encrypts plain under a key derived from secret and label, binding it to aad (the signature of the request it answers).
// The result is nonce || ciphertext.
func seal(secret []byte, label, aad string, plain []byte) ([]byte, error) {
	k, err := sealKey(secret, label)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	n := make([]byte, g.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return nil, err
	}
	return g.Seal(n, n, plain, []byte(aad)), nil
}

// open is the inverse of seal. The error says nothing about why it failed.
func open(secret []byte, label, aad string, sealed []byte) ([]byte, error) {
	k, err := sealKey(secret, label)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	if len(sealed) < g.NonceSize()+g.Overhead() {
		return nil, errors.New("sealed answer is too short")
	}
	out, err := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], []byte(aad))
	if err != nil {
		return nil, errors.New("sealed answer does not open")
	}
	return out, nil
}

// ---- the agent's side ----

// iperfClientWire is one peer's client credentials as they travel inside the sealed answer to the prober.
type iperfClientWire struct {
	PublicKeyPEM string `json:"public_key_pem"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

// ServeIperf lets the agent answer the responder's request for the server credentials, and put the prober's client credentials
// into the peer list it asks for. secret is the one shared with the responder pods, and with nothing else. Without it the path
// does not exist and no credential is ever sent to a pod.
func (m *Meter) ServeIperf(secret []byte) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.iperfSecret = append([]byte(nil), secret...)
	m.iperfReplay = probe.NewReplayCache(0)
	m.mu.Unlock()
}

// SetIperfServer replaces the credentials the responder pods fetch with those a Config carried (nil: none). It returns why it
// refused them, if it did; a refused credential leaves the pods with nothing, which makes them stop serving, never serve open.
func (m *Meter) SetIperfServer(c *ikhnosv1.LinkIperfServer) (refused string) {
	if m == nil {
		return ""
	}
	var s *IperfServer
	if c != nil && (c.PrivateKeyPem != "" || c.UsersCsv != "") {
		v := IperfServer{PrivateKeyPEM: Secret(c.PrivateKeyPem), UsersCSV: Secret(c.UsersCsv)}
		if err := v.Validate(); err != nil {
			refused = err.Error()
		} else {
			s = &v
		}
	}
	m.mu.Lock()
	if len(m.iperfSecret) == 0 {
		s = nil // nothing will ever read it, and no pod may have it: do not keep it
	}
	m.iperfSrv = s
	m.iperfSet = true
	m.mu.Unlock()
	return refused
}

func (m *Meter) iperfServerHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		m.mu.Lock()
		secret, replay := m.iperfSecret, m.iperfReplay
		m.mu.Unlock()
		if len(secret) == 0 {
			http.NotFound(w, req)
			return
		}
		_, body, status, err := probe.ReadSigned(w, req, probe.Signed{Secret: secret, MaxBody: maxCredBody, Replay: replay, NodeOK: func(n string) bool { return n == IperfServerNode }, Now: m.now(), Context: iperfServerContext})
		if err != nil {
			http.Error(w, http.StatusText(status), status)
			return
		}
		if _, err := parseCredRequest(body); err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		cur, configured := m.iperfSrv, m.iperfSet
		m.mu.Unlock()
		if !configured {
			// A freshly started agent has not had its Config yet, so it does not know whether credentials are issued for this
			// cluster. Answering "none" would make every responder stop its server for the seconds until the Config arrives, and
			// a prober whose cycle began then would fail its first test and back off for half an hour. 503 is "ask again": the
			// responder keeps serving what it has (see IperfServerRunner).
			w.Header().Set("Retry-After", "5")
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		doc := struct {
			PrivateKeyPEM string `json:"private_key_pem"`
			UsersCSV      string `json:"users_csv"`
		}{}
		if cur != nil {
			doc.PrivateKeyPEM, doc.UsersCSV = cur.PrivateKeyPEM.Reveal(), cur.UsersCSV.Reveal()
		}
		plain, _ := json.Marshal(doc)
		sealed, err := seal(secret, iperfServerSeal, req.Header.Get(probe.HeaderSig), plain)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(sealed)
	}
}

// sealedClients is, for the prober that asked, the sealed client credentials of the peers it tests (empty when there are none).
func (m *Meter) sealedClients(secret []byte, sig string) string {
	out := map[string]iperfClientWire{}
	for _, pe := range m.Peers.List() {
		if pe.Throughput && pe.Iperf != nil {
			out[pe.ID] = iperfClientWire{PublicKeyPEM: pe.Iperf.PublicKeyPEM, Username: pe.Iperf.Username, Password: pe.Iperf.Password.Reveal()}
		}
	}
	if len(out) == 0 {
		return ""
	}
	plain, _ := json.Marshal(out)
	sealed, err := seal(secret, peersIperfSeal, sig, plain)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(sealed)
}

// ---- the pods' side ----

func signedCredRequest(ctx context.Context, url, signContext, node string, secret []byte, now time.Time, body []byte) (*http.Request, string, error) {
	ts := strconv.FormatInt(now.Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	sig := probe.SignContext(signContext, secret, ts, node, body)
	req.Header.Set(probe.HeaderNode, node)
	req.Header.Set(probe.HeaderTime, ts)
	req.Header.Set(probe.HeaderSig, sig)
	return req, sig, nil
}

// ErrIperfNotReady is FetchIperfServer's error when the agent has not had its Config yet and so cannot say whether credentials are
// issued: not an answer of "none", and the responder must not stop its server on it.
var ErrIperfNotReady = errors.New("the agent has not received its configuration yet")

// FetchIperfServer asks the agent at url (scheme, host and port) for this cluster's iperf3 server credentials. A zero IperfServer
// with a nil error means the agent has none for this cluster: the server must not run.
func FetchIperfServer(ctx context.Context, hc *http.Client, url string, secret []byte, now time.Time) (IperfServer, error) {
	body, err := newCredRequest(false)
	if err != nil {
		return IperfServer{}, err
	}
	req, sig, err := signedCredRequest(ctx, url+PathIperfServer, iperfServerContext, IperfServerNode, secret, now, body)
	if err != nil {
		return IperfServer{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return IperfServer{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusServiceUnavailable {
			return IperfServer{}, ErrIperfNotReady
		}
		return IperfServer{}, fmt.Errorf("agent answered %s", resp.Status)
	}
	sealed, err := io.ReadAll(io.LimitReader(resp.Body, maxSealedAnswer))
	if err != nil {
		return IperfServer{}, errors.New("could not read the agent's answer")
	}
	plain, err := open(secret, iperfServerSeal, sig, sealed)
	if err != nil {
		return IperfServer{}, errors.New("the agent's answer could not be opened (wrong secret, or not an answer to this request)")
	}
	defer clear(plain)
	var doc struct {
		PrivateKeyPEM string `json:"private_key_pem"`
		UsersCSV      string `json:"users_csv"`
	}
	if err := json.Unmarshal(plain, &doc); err != nil {
		return IperfServer{}, errors.New("the agent's answer is not a credential document")
	}
	s := IperfServer{PrivateKeyPEM: Secret(doc.PrivateKeyPEM), UsersCSV: Secret(doc.UsersCSV)}
	if s.PrivateKeyPEM == "" && s.UsersCSV == "" {
		return IperfServer{}, nil
	}
	if err := s.Validate(); err != nil {
		return IperfServer{}, fmt.Errorf("the agent's credentials were refused: %w", err)
	}
	return s, nil
}
