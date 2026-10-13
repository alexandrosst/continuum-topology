//go:build !linux

package link

import (
	"context"
	"errors"
	"net/netip"
	"time"
)

// PathProber needs Linux's error queue; elsewhere it reports that it cannot run.
type PathProber struct {
	HopTimeout  time.Duration
	MaxHops     int
	DstPort     int
	SrcPort     int
	SilentLimit int
}

// Probe is not available on this platform.
func (PathProber) Probe(context.Context, netip.Addr) (PathResult, error) {
	return PathResult{}, ErrPathUnsupported
}
