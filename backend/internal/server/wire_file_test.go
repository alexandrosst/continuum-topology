package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ikhnos/internal/agents"
	"ikhnos/internal/httpkit"
	"ikhnos/internal/wire"
)

// wireFile is the TypeScript the API's shapes are written to, beside the page that reads them.
const wireFile = "../../../src/lib/generated/wire.ts"

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

func TestEveryHTTPKitDocumentIsListedForTheWire(t *testing.T) {
	wire.RequireDocsListed(t, "../httpkit", WireTypes())
}

// The shell's own answers were maps; each is held to the map it replaced so that the bytes on the wire are the ones they were.
func TestTheShellsAnswersAreWrittenAsTheMapsWere(t *testing.T) {
	check := func(name string, got, was any) {
		t.Helper()
		g, err := json.Marshal(got)
		if err != nil {
			t.Fatal(name, err)
		}
		w, err := json.Marshal(was)
		if err != nil {
			t.Fatal(name, err)
		}
		if string(g) != string(w) {
			t.Errorf("%s:\n got %s\nwant %s", name, g, w)
		}
	}
	check("refusal", httpkit.ErrorDoc{Error: "no"}, map[string]string{"error": "no"})
	check("ok", httpkit.OKDoc{OK: true}, map[string]bool{"ok": true})
	check("page", fusionPageDoc{Path: "/p?t=1"}, map[string]string{"path": "/p?t=1"})
	check("info", infoDoc{OrgID: "o", OrgName: "n", Role: "owner", AgentAddress: "a:1", AgentExposure: "nodeport", ReleaseName: "r", ReleaseNamespace: "ns", CAPin: "pin", Version: "v", ImplementedTier: 2,
		GeoIP: agents.GeoInfo{Enabled: true}, Install: infoInstallDoc{ChartFile: "f", ChartRef: "ref", ChartVersion: "1", ImagesConfigured: true, ImageRegistry: "reg", ImageTag: "t", ImageDigest: "d"}},
		map[string]any{"orgId": "o", "orgName": "n", "role": "owner", "agentAddress": "a:1", "agentExposure": "nodeport", "releaseName": "r", "releaseNamespace": "ns", "caPin": "pin", "version": "v",
			"implementedTier": 2, "geoip": agents.GeoInfo{Enabled: true},
			"install": map[string]any{"chartFile": "f", "chartRef": "ref", "chartVersion": "1", "imagesConfigured": true, "imageRegistry": "reg", "imageTag": "t", "imageDigest": "d"}})
}
