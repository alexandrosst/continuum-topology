package server

import (
	"os"
	"path/filepath"
	"testing"

	"continuum/internal/accountsapi"
	"continuum/internal/decisionloop"
	"continuum/internal/estate"
	"continuum/internal/fusion"
	"continuum/internal/fusionhttp"
	"continuum/internal/linksapi"
	"continuum/internal/operatorsapi"
	"continuum/internal/settingsapi"
	"continuum/internal/telemetry"
	"continuum/internal/wire"
)

// wireFile is the TypeScript the API's shapes are written to, beside the page that reads them.
const wireFile = "../../../src/lib/generated/wire.ts"

// wireRoots are the shapes of every package that puts one on the wire. A package that mounts routes and is not here has its shapes written
// by hand in the page, which is the thing this file exists to end.
func wireRoots() []wire.Root {
	var roots []wire.Root
	for _, r := range [][]wire.Root{
		accountsapi.WireTypes(), decisionloop.WireTypes(), estate.WireTypes(), fusion.WireTypes(), fusionhttp.WireTypes(),
		linksapi.WireTypes(), operatorsapi.WireTypes(), settingsapi.WireTypes(), telemetry.WireTypes(),
	} {
		roots = append(roots, r...)
	}
	return roots
}

// TestWireFileIsWhatTheServerEncodes fails when the committed TypeScript is not what the Go types say: a field renamed, dropped or made
// optional on the server shows up here as a changed line, in the review of the change that made it, and the page that reads it stops
// compiling. Accept it on purpose with: go test ./internal/server -run TestWire -update
func TestWireFileIsWhatTheServerEncodes(t *testing.T) {
	want, err := wire.Generate(wireRoots())
	if err != nil {
		t.Fatal(err)
	}
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(wireFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wireFile, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := os.Stat(filepath.Dir(filepath.Dir(wireFile))); err != nil {
		t.Skip("the page is not beside the server in this checkout")
	}
	got, err := os.ReadFile(wireFile)
	if err != nil {
		t.Fatalf("%v (create it with -update)", err)
	}
	if string(got) != want {
		t.Fatalf("src/lib/generated/wire.ts is not what the server's types say. If the change to the API is meant, regenerate it with:\n  go test ./internal/server -run TestWire -update\nand commit the result with the change.")
	}
}
