package fusionhttp

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestTheReadRoutesAreDescribedWithoutAGuardToBindThemTo(t *testing.T) {
	var g *Guard // the table is read to describe the API before there is a server
	ops := g.ReadOps()
	if len(ops) == 0 {
		t.Fatal("no read routes")
	}
	seen := map[string]bool{}
	for _, op := range ops {
		key := op.Method + " " + op.Path
		if seen[key] {
			t.Errorf("%s twice", key)
		}
		seen[key] = true
		if op.Handler == nil || op.Response == "" || op.Tag == "" {
			t.Errorf("%s: handler %v, response %q, tag %q", key, op.Handler != nil, op.Response, op.Tag)
		}
		for _, p := range op.Params {
			if _, ok := ParamDefs[p]; !ok {
				t.Errorf("%s takes %q, which no parameter table describes", key, p)
			}
		}
		for _, p := range op.PathParams {
			if _, ok := PathParamDefs[p]; !ok {
				t.Errorf("%s has {%s}, which no path parameter table describes", key, p)
			}
		}
	}
}

func TestABatchBodyTakesPromqlAsAnObjectAListOrAString(t *testing.T) {
	for name, c := range map[string]struct {
		in   any
		want []string
		bad  bool
	}{
		"object": {map[string]any{"b": "y", "a": "x"}, []string{"a=x", "b=y"}, false},
		"list":   {[]any{"a=x", "b=y"}, []string{"a=x", "b=y"}, false},
		"string": {"a=x", []string{"a=x"}, false},
		"absent": {nil, nil, false},
		"number": {3.0, nil, true},
		"mixed":  {[]any{"a=x", 1.0}, nil, true},
	} {
		got, err := promqlValues(c.in)
		if (err != nil) != c.bad || !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, %v", name, got, err)
		}
	}
}

func TestABatchOptionIsReadAsTheQueryStringWouldHaveGivenIt(t *testing.T) {
	for in, want := range map[string]string{`"logs"`: "logs", `true`: "true", `15`: "15", `["logs","metrics"]`: "logs,metrics", `null`: ""} {
		var v any
		dec := json.NewDecoder(strings.NewReader(in))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			t.Fatal(err)
		}
		if got, err := paramString(v); err != nil || got != want {
			t.Errorf("%s: %q, %v", in, got, err)
		}
	}
	if _, err := paramString([]any{1.0}); err == nil {
		t.Error("a list of numbers was accepted")
	}
}

func TestStreamingIsAskedForByTheQueryOrTheAcceptHeader(t *testing.T) {
	for _, c := range []struct {
		target, accept string
		want, bad      bool
	}{
		{"/x", "", false, false},
		{"/x?stream=true", "", true, false},
		{"/x", "application/x-ndjson", true, false},
		{"/x?stream=false", "application/x-ndjson", false, false}, // the explicit parameter wins
		{"/x?stream=maybe", "", false, true},
	} {
		r := httptest.NewRequest("GET", c.target, nil)
		if c.accept != "" {
			r.Header.Set("Accept", c.accept)
		}
		got, err := wantsStream(r)
		if got != c.want || (err != nil) != c.bad {
			t.Errorf("%s accept %q: %v, %v", c.target, c.accept, got, err)
		}
	}
}
