// Package chart carries the server's Helm charts inside the server binary, so an install command never
// points at a directory that only exists in a source checkout: the server hands out the packaged chart
// itself. It holds three charts today - Agent (continuum-agent, dialed out to by every enrolled cluster),
// RegionalOperator (continuum-regional-operator, a standalone aggregation point - see its own Chart.yaml) and
// Fusion (continuum-fusion, the stores a regional operator saves into) - each embedded and packaged
// independently.
package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

//go:embed all:continuum-agent
var agentFiles embed.FS

//go:embed all:continuum-regional-operator
var operatorFiles embed.FS

//go:embed all:continuum-fusion
var fusionFiles embed.FS

// Agent is the continuum-agent chart: what every enrolled cluster installs.
var Agent = newChart(agentFiles, "continuum-agent")

// RegionalOperator is the continuum-regional-operator chart: a standalone OTel Collector that aggregates
// telemetry already exported by a set of agents' clusters and re-exports it further up. It does not dial
// the Ikhnos server unless its opt-in heartbeat is turned on (see store.Operator's own comment) - its
// only relationship to Agent is that both are packaged and served the same way.
var RegionalOperator = newChart(operatorFiles, "continuum-regional-operator")

// Fusion is the continuum-fusion chart: Prometheus, Loki and Tempo, one durable pod per signal type, which a
// regional operator exports into. It never talks to the Ikhnos server. The Service names a regional operator is
// pointed at are a contract of this chart (fusion.name in its _helpers.tpl), so changing them is a breaking change.
var Fusion = newChart(fusionFiles, "continuum-fusion")

// Chart is one Helm chart embedded in the server binary, packaged and versioned independently of any
// other chart this package also carries.
type Chart struct {
	files embed.FS
	dir   string

	once   sync.Once
	pkg    []byte
	pkgErr error

	versionOnce sync.Once
	versionVal  string
}

func newChart(files embed.FS, dir string) *Chart {
	return &Chart{files: files, dir: dir}
}

// Version is the chart version from its Chart.yaml.
func (c *Chart) Version() string {
	c.versionOnce.Do(func() { c.versionVal = c.parseVersion() })
	return c.versionVal
}

func (c *Chart) parseVersion() string {
	b, err := c.files.ReadFile(c.dir + "/Chart.yaml")
	if err != nil {
		return "0.0.0"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "version:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return "0.0.0"
}

// Filename is what `helm package` would call the archive.
func (c *Chart) Filename() string { return c.dir + "-" + c.Version() + ".tgz" }

// Package returns the chart as a .tgz that `helm install` accepts. The bytes are identical on every call
// (sorted entries, no timestamps), so a browser or cache can tell nothing changed.
func (c *Chart) Package() ([]byte, error) {
	c.once.Do(func() { c.pkg, c.pkgErr = c.build() })
	return c.pkg, c.pkgErr
}

func (c *Chart) build() ([]byte, error) {
	var names []string
	err := fs.WalkDir(c.files, c.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	tw := tar.NewWriter(gz)
	for _, n := range names {
		b, err := c.files.ReadFile(n)
		if err != nil {
			return nil, err
		}
		if err := tw.WriteHeader(&tar.Header{Name: path.Clean(n), Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		if _, err := tw.Write(b); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- package-level forwarders to Agent, kept so every pre-existing caller compiles unchanged ----

// Version is Agent.Version().
func Version() string { return Agent.Version() }

// Filename is Agent.Filename().
func Filename() string { return Agent.Filename() }

// Package is Agent.Package().
func Package() ([]byte, error) { return Agent.Package() }
