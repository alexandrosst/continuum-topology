package server

import (
	"continuum/internal/accountsapi"
	"continuum/internal/decisionloop"
	"continuum/internal/estate"
	"continuum/internal/fusion"
	"continuum/internal/fusionhttp"
	"continuum/internal/httpkit"
	"continuum/internal/linksapi"
	"continuum/internal/operatorsapi"
	"continuum/internal/settingsapi"
	"continuum/internal/telemetry"
	"continuum/internal/wire"
)

// WireTypes are the shapes the shell puts on the wire itself, and the ones every module's routes share (the refusal, "ok", a reason), for
// the TypeScript written from the Go types (internal/wire).
func WireTypes() []wire.Root {
	return []wire.Root{
		{Value: httpkit.ErrorDoc{}},
		{Value: httpkit.OKDoc{}},
		{Value: httpkit.ReasonRequest{}},
		{Value: infoDoc{}},
		{Value: openFusionPageRequest{}},
		{Value: fusionPageDoc{}},
	}
}

// wireRoots are the shapes of every package that puts one on the wire. A package that mounts routes and is not here has its shapes written
// by hand in the page, which is the thing the generated file exists to end.
func wireRoots() []wire.Root {
	var roots []wire.Root
	for _, r := range [][]wire.Root{
		accountsapi.WireTypes(), decisionloop.WireTypes(), estate.WireTypes(), fusion.WireTypes(), fusionhttp.WireTypes(),
		linksapi.WireTypes(), operatorsapi.WireTypes(), settingsapi.WireTypes(), telemetry.WireTypes(), WireTypes(),
	} {
		roots = append(roots, r...)
	}
	return roots
}
