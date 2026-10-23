package server

import (
	"ikhnos/internal/accountsapi"
	"ikhnos/internal/decisionloop"
	"ikhnos/internal/estate"
	"ikhnos/internal/fusion"
	"ikhnos/internal/fusionhttp"
	"ikhnos/internal/httpkit"
	"ikhnos/internal/linksapi"
	"ikhnos/internal/operatorsapi"
	"ikhnos/internal/settingsapi"
	"ikhnos/internal/telemetry"
	"ikhnos/internal/wire"
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
