package server

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"ikhnos/internal/fusionhttp"
	"ikhnos/internal/linksapi"
)

var timeT = reflect.TypeOf(time.Time{})

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// goFields returns json names -> type of struct fields (flattening embedded structs).
func goFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if f.Anonymous && name == "" {
			for k, v := range goFields(deref(f.Type)) {
				out[k] = v
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}

type schemas map[string]map[string]any

func (s schemas) resolve(n map[string]any) map[string]any {
	if r, ok := n["$ref"].(string); ok {
		return s.resolve(s[strings.TrimPrefix(r, "#/components/schemas/")])
	}
	if all, ok := n["allOf"].([]any); ok {
		merged := map[string]any{"type": "object", "properties": map[string]any{}}
		for _, a := range all {
			r := s.resolve(a.(map[string]any))
			for k, v := range r["properties"].(map[string]any) {
				merged["properties"].(map[string]any)[k] = v
			}
		}
		return merged
	}
	return n
}

func compare(t *testing.T, s schemas, path string, gt reflect.Type, node map[string]any, seen map[string]bool) {
	gt = deref(gt)
	node = s.resolve(node)
	switch gt.Kind() {
	case reflect.Slice, reflect.Array:
		if node["type"] == "array" {
			if it, ok := node["items"].(map[string]any); ok {
				compare(t, s, path+"[]", gt.Elem(), it, seen)
			}
		}
		return
	case reflect.Map:
		return
	case reflect.Struct:
		if gt == timeT {
			return
		}
	default:
		return
	}
	props, _ := node["properties"].(map[string]any)
	if props == nil {
		if node["type"] == "object" || node["type"] == nil {
			t.Logf("INFO %s: Go struct %s but schema has no properties (free object)", path, gt.Name())
		}
		return
	}
	key := path + gt.String()
	if seen[key] {
		return
	}
	seen[key] = true
	gf := goFields(gt)
	var onlyGo, onlySchema []string
	for k := range gf {
		if _, ok := props[k]; !ok {
			onlyGo = append(onlyGo, k)
		}
	}
	for k := range props {
		if _, ok := gf[k]; !ok {
			onlySchema = append(onlySchema, k)
		}
	}
	sort.Strings(onlyGo)
	sort.Strings(onlySchema)
	if len(onlyGo) > 0 || len(onlySchema) > 0 {
		t.Errorf("DRIFT %s (%s): encoded but not in OpenAPI: %v ; in OpenAPI but never encoded: %v", path, gt.Name(), onlyGo, onlySchema)
	}
	for k, ft := range gf {
		if p, ok := props[k].(map[string]any); ok {
			compare(t, s, path+"."+k, ft, p, seen)
		}
	}
}

// The OpenAPI description of the FUSION API is written by hand; the types its handlers encode are Go's. This reads both and fails when
// a field is encoded that the description does not have (a generated client would drop it, a strict validator would refuse the
// answer), or described that is never encoded. The pairs are each response type and the schema that stands for it; a type renamed
// or a schema removed fails here too, instead of leaving a response nobody checks.
func TestOpenAPISchemasMatchEncodedTypes(t *testing.T) {
	spec := fusionhttp.OpenAPI(nil)
	b, _ := json.Marshal(spec["components"].(map[string]any)["schemas"])
	var s schemas
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	byGo := map[string]any{}
	for _, r := range append(fusionhttp.WireTypes(), linksapi.WireTypes()...) {
		byGo[deref(reflect.TypeOf(r.Value)).Name()] = r.Value
	}
	pairs := map[string]string{"statusDoc": "Status", "applicationsDoc": "ApplicationList", "servicesDoc": "ServiceList", "tracesDoc": "TraceList",
		"traceBatchDoc": "BulkResult", "logsDoc": "LogResult", "metricNamesDoc": "NameList", "metricSeriesDoc": "SeriesList", "metricRangeDoc": "MetricRange",
		"AppTopology": "ApplicationTopology", "Fused": "FusedTrace", "Overview": "Overview", "Trace": "Trace", "TraceSummary": "TraceSummary", "fusionLinksDoc": "LinkList"}
	for g, sn := range pairs {
		v, ok := byGo[g]
		if !ok {
			t.Errorf("no go type %s", g)
			continue
		}
		node, ok := s[sn]
		if !ok {
			t.Errorf("no schema %s", sn)
			continue
		}
		compare(t, s, sn, reflect.TypeOf(v), node, map[string]bool{})
	}
}
