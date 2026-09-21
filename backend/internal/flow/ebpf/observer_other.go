//go:build !linux || !(amd64 || arm64)

package ebpf

import (
	"errors"

	continuumv1 "continuum/gen/continuumv1"
)

type Options struct{ Live bool }

type Observer struct{ LiveErr error }

func (o *Observer) Live() bool { return false }

func Open(opts ...Options) (*Observer, error) {
	return nil, errors.New("the eBPF observer is built for linux on amd64 and arm64 only")
}
func (o *Observer) Method() string                                   { return "ebpf" }
func (o *Observer) BytesKnown() bool                                 { return true }
func (o *Observer) Close() error                                     { return nil }
func (o *Observer) Collect() ([]*continuumv1.RawFlow, uint64, error) { return nil, 0, nil }
