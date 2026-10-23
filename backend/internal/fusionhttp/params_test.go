package fusionhttp

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ikhnos/internal/fusionapi"
)

func TestEveryQueryParameterTheHandlersReadIsDocumented(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	var src []byte
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src = append(src, b...)
	}
	for _, m := range regexp.MustCompile(`\.(?:Get|Has)\("([a-z_]+)"\)`).FindAllStringSubmatch(string(src), -1) {
		if _, ok := ParamDefs[m[1]]; !ok && m[1] != "ids" {
			t.Errorf("the handlers read %q but it is not described in ParamDefs", m[1])
		}
	}
	for _, n := range fusionapi.FuseParamNames {
		if _, ok := ParamDefs[n]; !ok {
			t.Errorf("the fused read takes %q but it is not described", n)
		}
	}
	// Every described parameter is used by a route, every route's parameters are described, and the fused ones are
	// offered by both routes that read a fused trace.
	used := map[string]bool{}
	for _, op := range (*Guard)(nil).Ops() {
		for _, n := range op.Params {
			used[n] = true
			if _, ok := ParamDefs[n]; !ok {
				t.Errorf("%s %s lists %q, which is not described", op.Method, op.Path, n)
			}
		}
		for _, n := range op.PathParams {
			if _, ok := PathParamDefs[n]; !ok || !strings.Contains(op.Path, "{"+n+"}") {
				t.Errorf("%s %s: path parameter %q", op.Method, op.Path, n)
			}
		}
		if op.Body == "" && op.Method == "POST" {
			t.Errorf("%s %s has no body", op.Method, op.Path)
		}
	}
	for n := range ParamDefs {
		if !used[n] && n != "ids" {
			t.Errorf("%q is described but no route takes it", n)
		}
	}
	for _, path := range []string{"/traces", "/traces/{id}"} {
		for _, op := range (*Guard)(nil).Ops() {
			if op.Path == path && op.Method == "GET" {
				for _, n := range fusionapi.FuseParamNames {
					found := false
					for _, p := range op.Params {
						found = found || p == n
					}
					if !found {
						t.Errorf("GET %s does not offer %q", path, n)
					}
				}
			}
		}
	}
}
