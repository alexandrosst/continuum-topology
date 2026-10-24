package controller

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// crdPath is the definition the controller's chart installs.
var crdPath = filepath.Join("..", "..", "..", "deploy", "helm", "ikhnos-controller", "crds", "ikhnosagents.yaml")

func loadCRD(t *testing.T) (spec, status map[string]any, version map[string]any, top map[string]any) {
	t.Helper()
	b, err := os.ReadFile(crdPath)
	if err != nil {
		t.Fatal(err)
	}
	var crd map[string]any
	if err := yaml.Unmarshal(b, &crd); err != nil {
		t.Fatal(err)
	}
	vs := dig(t, crd, "spec", "versions").([]any)
	if len(vs) != 1 {
		t.Fatalf("expected one version, got %d", len(vs))
	}
	v := vs[0].(map[string]any)
	props := dig(t, v, "schema", "openAPIV3Schema", "properties").(map[string]any)
	return props["spec"].(map[string]any), props["status"].(map[string]any), v, crd
}

func dig(t *testing.T, m map[string]any, path ...string) any {
	t.Helper()
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object", strings.Join(path, "."))
		}
		if cur, ok = mm[p]; !ok {
			t.Fatalf("the CRD has no %s", strings.Join(path, "."))
		}
	}
	return cur
}

func TestTheDefinitionIsTheOneTheControllerReads(t *testing.T) {
	_, _, v, crd := loadCRD(t)
	if dig(t, crd, "spec", "group") != Group || dig(t, crd, "spec", "names", "kind") != Kind || dig(t, crd, "spec", "names", "plural") != Resource {
		t.Fatalf("the CRD names %v/%v/%v, the controller %s/%s/%s", dig(t, crd, "spec", "group"), dig(t, crd, "spec", "names", "kind"), dig(t, crd, "spec", "names", "plural"), Group, Kind, Resource)
	}
	if dig(t, crd, "metadata", "name") != Resource+"."+Group {
		t.Fatalf("the CRD is named %v", dig(t, crd, "metadata", "name"))
	}
	if dig(t, crd, "spec", "scope") != "Namespaced" {
		t.Fatal("the resource is namespaced: the controller reads one namespace")
	}
	if v["name"] != Version || v["served"] != true || v["storage"] != true {
		t.Fatalf("the version is %v", v)
	}
	// The controller writes status on its own subresource; without it every status write would be refused.
	if _, ok := dig(t, v, "subresources").(map[string]any)["status"]; !ok {
		t.Fatal("no status subresource")
	}
}

// walk says where the schema and a Go type disagree about the fields of an object: one the type has and the schema lacks is pruned
// away by the API server (a status the controller writes would silently vanish), and one the schema has and the type lacks is a
// promise nothing keeps.
func walk(t *testing.T, path string, schema map[string]any, typ reflect.Type) {
	t.Helper()
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	want, _ := schema["type"].(string)
	switch typ.Kind() {
	case reflect.Struct:
		if want != "object" {
			t.Errorf("%s: schema type %q, Go struct", path, want)
			return
		}
		props, _ := schema["properties"].(map[string]any)
		fields := map[string]reflect.StructField{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				t.Errorf("%s.%s has no json name", path, f.Name)
				continue
			}
			fields[name] = f
		}
		var names []string
		for n := range fields {
			names = append(names, n)
		}
		for n := range props {
			if _, ok := fields[n]; !ok {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			f, inGo := fields[n]
			p, inSchema := props[n]
			switch {
			case !inSchema:
				t.Errorf("%s.%s is a field of the Go type and not of the schema", path, n)
			case !inGo:
				t.Errorf("%s.%s is in the schema and not a field of the Go type", path, n)
			default:
				walk(t, path+"."+n, p.(map[string]any), f.Type)
			}
		}
		// What the schema requires is a property it has.
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if _, ok := props[r.(string)]; !ok {
					t.Errorf("%s requires %v and has no such property", path, r)
				}
			}
		}
	case reflect.Slice:
		if want != "array" {
			t.Errorf("%s: schema type %q, Go slice", path, want)
			return
		}
		walk(t, path+"[]", schema["items"].(map[string]any), typ.Elem())
	case reflect.String:
		if want != "string" {
			t.Errorf("%s: schema type %q, Go string", path, want)
		}
	case reflect.Bool:
		if want != "boolean" {
			t.Errorf("%s: schema type %q, Go bool", path, want)
		}
	case reflect.Int, reflect.Int32, reflect.Int64:
		if want != "integer" {
			t.Errorf("%s: schema type %q, Go integer", path, want)
		}
	case reflect.Float64:
		if want != "number" {
			t.Errorf("%s: schema type %q, Go number", path, want)
		}
	default:
		t.Errorf("%s: no rule for %s", path, typ.Kind())
	}
}

func TestTheSchemaHasTheFieldsOfTheSpecAndTheStatusAndNoOthers(t *testing.T) {
	spec, status, _, _ := loadCRD(t)
	walk(t, "spec", spec, reflect.TypeOf(Spec{}))
	walk(t, "status", status, reflect.TypeOf(Status{}))
}

func TestWhatTheSchemaRequiresIsWhatThePackageCannotDoWithout(t *testing.T) {
	spec, _, _, _ := loadCRD(t)
	got := func(m map[string]any) []string {
		var out []string
		for _, r := range m["required"].([]any) {
			out = append(out, r.(string))
		}
		sort.Strings(out)
		return out
	}
	if g := got(spec); !reflect.DeepEqual(g, []string{"server"}) {
		t.Errorf("spec requires %v", g)
	}
	server := dig(t, spec, "properties", "server").(map[string]any)
	if g := got(server); !reflect.DeepEqual(g, []string{"address", "caPin"}) {
		t.Errorf("spec.server requires %v", g)
	}
	// What the schema bounds is what Problems bounds.
	tier := dig(t, spec, "properties", "accessTier").(map[string]any)
	if tier["minimum"] != float64(0) || tier["maximum"] != float64(2) {
		t.Errorf("accessTier is bounded %v..%v", tier["minimum"], tier["maximum"])
	}
}

func TestTheSchemaNamesEveryPhaseTheControllerWritesAndNoMore(t *testing.T) {
	_, status, _, _ := loadCRD(t)
	var enum []string
	for _, e := range dig(t, status, "properties", "phase", "enum").([]any) {
		enum = append(enum, e.(string))
	}
	want := []string{PhasePending, PhaseInstalling, PhaseReady, PhaseFailed, PhaseInvalid, PhaseBlocked, PhaseDeleting}
	sort.Strings(enum)
	sort.Strings(want)
	if !reflect.DeepEqual(enum, want) {
		t.Fatalf("phases in the schema %v, in the controller %v", enum, want)
	}
	// A condition's status is one of Kubernetes' three.
	var cs []string
	for _, e := range dig(t, status, "properties", "conditions", "items", "properties", "status", "enum").([]any) {
		cs = append(cs, e.(string))
	}
	sort.Strings(cs)
	if want := []string{"False", "True", "Unknown"}; !reflect.DeepEqual(cs, want) {
		t.Fatalf("condition statuses %v, want %v", cs, want)
	}
}

// What the schema allows of the telemetry is what the install package accepts: a word the schema lets through that the agent would refuse
// is a resource that is created and then marked Invalid, which is the thing a schema is there to stop.
func TestTheSchemaOfTheTelemetryAllowsTheWordsTheAgentAcceptsAndNoOthers(t *testing.T) {
	spec, _, _, _ := loadCRD(t)
	words := func(path ...string) []string {
		var out []string
		for _, e := range dig(t, spec, append([]string{"properties", "telemetry", "properties"}, append(path, "enum")...)...).([]any) {
			out = append(out, e.(string))
		}
		sort.Strings(out)
		return out
	}
	for name, c := range map[string]struct {
		got  []string
		want []string
	}{
		"destination.protocol":    {words("destination", "properties", "protocol"), []string{"grpc", "http", "zipkin"}},
		"routes.metrics.protocol": {words("routes", "properties", "metrics", "properties", "protocol"), []string{"grpc", "http", "zipkin"}},
		"routes.logs.protocol":    {words("routes", "properties", "logs", "properties", "protocol"), []string{"grpc", "http", "zipkin"}},
		"routes.traces.protocol":  {words("routes", "properties", "traces", "properties", "protocol"), []string{"grpc", "http", "zipkin"}},
		"energy.kind":             {words("energy", "properties", "kind"), []string{"bundle-kepler", "existing"}},
		"accelerators.kind":       {words("accelerators", "properties", "kind"), []string{"bundle-dcgm", "existing"}},
		"processing.debug":        {words("processing", "properties", "debug"), []string{"basic", "detailed"}},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: the schema allows %v, the agent accepts %v", name, c.got, c.want)
		}
	}
	pct := dig(t, spec, "properties", "telemetry", "properties", "processing", "properties", "tracesSamplingPercent").(map[string]any)
	if pct["minimum"] != float64(0) || pct["maximum"] != float64(100) {
		t.Errorf("tracesSamplingPercent is bounded %v..%v", pct["minimum"], pct["maximum"])
	}
}
