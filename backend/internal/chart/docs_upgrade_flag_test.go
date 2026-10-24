package chart

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every upgrade command this product prints uses --reset-then-reuse-values, because --reuse-values replaces a newer chart's defaults with
// the old release's whole set of values and breaks on a value the newer chart added. The documents a person copies commands from must
// not tell them to use the flag the product refuses to print: a page may name it only to say why not.
func TestTheDocumentsDoNotTellAPersonToUpgradeWithReuseValues(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var files []string
	_ = filepath.WalkDir(filepath.Join(root, "docs-site", "docs"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".mdx")) && !strings.Contains(p, string(filepath.Separator)+"contributing"+string(filepath.Separator)) {
			files = append(files, p)
		}
		return nil
	})
	for _, c := range []string{"ikhnos-agent", "ikhnos-regional-operator", "ikhnos-fusion"} {
		files = append(files, filepath.Join(c, "README.md"))
	}
	files = append(files, filepath.Join(root, "deploy", "README.md"))
	if len(files) < 5 {
		t.Fatalf("found only %d documents to read: %v", len(files), files)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue // a chart without a README
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if !strings.Contains(line, "--reuse-values") {
				continue
			}
			low := strings.ToLower(line)
			if strings.Contains(low, "not `--reuse-values`") || strings.Contains(low, "unlike `--reuse-values`") || strings.Contains(low, "rather than `--reuse-values`") || strings.Contains(low, "never `--reuse-values`") {
				continue
			}
			t.Errorf("%s:%d tells a person to use --reuse-values: %s", f, i+1, strings.TrimSpace(line))
		}
	}
}
