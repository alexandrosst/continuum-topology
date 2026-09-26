// Package chart carries the agent's Helm chart inside the server binary, so an install command never
// points at a directory that only exists in a source checkout: the server hands out the packaged chart itself.
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
var files embed.FS

const dir = "continuum-agent"

var (
	once   sync.Once
	pkg    []byte
	pkgErr error

	versionOnce sync.Once
	versionVal  string
)

// Version is the chart version from Chart.yaml.
func Version() string {
	versionOnce.Do(func() { versionVal = parseVersion() })
	return versionVal
}

func parseVersion() string {
	b, err := files.ReadFile(dir + "/Chart.yaml")
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
func Filename() string { return "continuum-agent-" + Version() + ".tgz" }

// Package returns the chart as a .tgz that `helm install` accepts. The bytes are identical on every call
// (sorted entries, no timestamps), so a browser or cache can tell nothing changed.
func Package() ([]byte, error) {
	once.Do(func() { pkg, pkgErr = build() })
	return pkg, pkgErr
}

func build() ([]byte, error) {
	var names []string
	err := fs.WalkDir(files, dir, func(p string, d fs.DirEntry, err error) error {
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
		b, err := files.ReadFile(n)
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
