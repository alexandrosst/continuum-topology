package probe

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/protobuf/encoding/protojson"
)

// The probe and the agent share a random secret that the Helm chart generates and mounts into both.
// Every report is signed: HMAC-SHA256 over version, timestamp, node name and body. The timestamp
// limits replay to a window (5 minutes by default) and receivers remember the signatures they accepted
// inside it (see ReplayCache), so a captured report cannot be sent again.
const (
	PathReport = "/v1/probe"
	HeaderNode = "X-Continuum-Node"
	HeaderTime = "X-Continuum-Timestamp"
	HeaderSig  = "X-Continuum-Signature"
	// MaxBody bounds a node probe report (a hardware description is a few KiB).
	MaxBody = 128 << 10
	// MaxClockSkew is the default timestamp window.
	MaxClockSkew = 5 * time.Minute
)

func Sign(secret []byte, ts, node string, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("continuum-probe-v1\n" + ts + "\n" + node + "\n"))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// Verify checks a report's signature and freshness with the default window (MaxClockSkew).
func Verify(secret []byte, ts, node, sig string, body []byte, now time.Time) error {
	return VerifyWindow(secret, ts, node, sig, body, now, MaxClockSkew)
}

// Push sends one observation to the agent.
func Push(ctx context.Context, hc *http.Client, url string, secret []byte, node string, h *continuumv1.HostProbe, now time.Time) error {
	body, err := protojson.Marshal(h)
	if err != nil {
		return err
	}
	return PostSigned(ctx, hc, url+PathReport, secret, node, body, now)
}

// PostSigned posts a body to a receiver that verifies with the same scheme: the node flow collector
// uses it too, with its own secret and its own path.
func PostSigned(ctx context.Context, hc *http.Client, fullURL string, secret []byte, node string, body []byte, now time.Time) error {
	ts := strconv.FormatInt(now.Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderNode, node)
	req.Header.Set(HeaderTime, ts)
	req.Header.Set(HeaderSig, Sign(secret, ts, node, body))
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("agent answered %s", resp.Status)
	}
	return nil
}

// Run reads and reports until ctx ends. It retries quietly with backoff while the agent is not
// reachable (the agent may start after the probe).
func Run(ctx context.Context, paths Paths, url string, secret []byte, node string, every time.Duration, logf func(msg string, kv ...any)) {
	hc := &http.Client{Timeout: 10 * time.Second}
	wait := 5 * time.Second
	for {
		err := Push(ctx, hc, url, secret, node, Read(paths), time.Now())
		next := every
		if err != nil {
			logf("could not report to the agent, will retry", "err", err, "in", wait)
			next = wait
			if wait < 2*time.Minute {
				wait *= 2
			}
		} else {
			wait = 5 * time.Second
		}
		select {
		case <-time.After(next):
		case <-ctx.Done():
			return
		}
	}
}
