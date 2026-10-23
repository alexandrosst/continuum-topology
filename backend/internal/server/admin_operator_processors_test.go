package server

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"ikhnos/internal/accounts"
	"ikhnos/internal/install"
	"ikhnos/internal/testkit"
)

// The processors a person adds to a regional operator are chart values and nothing else: the operator record does not keep them, so the
// command that installs it is the only place they are. The server writes them into that command from the request that creates the
// operator, where the page used to append them to the command it was handed.

// goldenOperatorProcessors are the processors of the operator case the page's own tests pin (a filter and tail sampling), as the page sends them.
func goldenOperatorProcessors(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(uiCommandsGolden)
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name string `json:"name"`
		Spec struct {
			OperatorProcessors json.RawMessage `json:"operatorProcessors"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if len(c.Spec.OperatorProcessors) > 0 {
			return c.Spec.OperatorProcessors
		}
	}
	t.Fatal("the golden file has no operator processors")
	return nil
}

func TestAnOperatorIsInstalledWithTheProcessorsChosen(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	cl := a.approvedCluster(t, testkit.FP)

	plain := a.createOperatorDoc(t, cookie, extBody("athens", cl))
	body := extBody("sparta", cl)
	body["extraProcessors"] = goldenOperatorProcessors(t)
	chosen := a.createOperatorDoc(t, cookie, body)

	var ps []install.Processor
	if err := json.Unmarshal(goldenOperatorProcessors(t), &ps); err != nil {
		t.Fatal(err)
	}
	values, err := install.OperatorProcessors(ps)
	if err != nil || len(values) == 0 {
		t.Fatalf("%v %v", values, err)
	}
	// Apart from the operator's own name and id, it is the plain command with the processors after it, each value on a line of its own.
	idPlain := plain["operator"].(map[string]any)["id"].(string)
	idChosen := chosen["operator"].(map[string]any)["id"].(string)
	got := strings.ReplaceAll(strings.ReplaceAll(chosen["install"].(string), idChosen, idPlain), "sparta", "athens")
	want := strings.TrimRight(plain["install"].(string), "\n ") + install.Continued(values)
	if got != want {
		t.Fatalf("the command is\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(plain["install"].(string), "processors.") {
		t.Fatalf("an operator created without processors has some:\n%s", plain["install"])
	}
}

func TestProcessorsThatCannotBeInstalledAreRefusedBeforeAnOperatorExists(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	cl := a.approvedCluster(t, testkit.FP)
	list := func() int {
		var ops []json.RawMessage
		if err := json.Unmarshal(a.do("GET", "/api/v1/operators", nil, withCookie(cookie)).Body.Bytes(), &ops); err != nil {
			t.Fatal(err)
		}
		return len(ops)
	}
	for name, ps := range map[string]string{
		"a filter with no condition":          `[{"id":"p1","kind":"filter","name":"f","config":{"signal":"metric","conditions":[]}}]`,
		"a regular expression that is broken": `[{"id":"p1","kind":"filter","name":"f","config":{"signal":"metric","conditions":[{"field":"a","op":"matches","value":"(","level":"record"}]}}]`,
		"a kind nobody knows":                 `[{"id":"p1","kind":"exec","name":"f","config":{}}]`,
		"not a list":                          `{"kind":"filter"}`,
	} {
		before := list()
		body := extBody("op-"+strings.Fields(name)[1], cl)
		body["extraProcessors"] = json.RawMessage(ps)
		r := a.do("POST", "/api/v1/operators", body, withCookie(cookie))
		if r.Code != 400 {
			t.Errorf("%s: %d %.200s", name, r.Code, r.Body.String())
		}
		// What is wrong is said, in the words the renderer has for it.
		var parsed []install.Processor
		if json.Unmarshal([]byte(ps), &parsed) == nil {
			told, _ := r.json(t)["error"].(string)
			if _, err := install.OperatorProcessors(parsed); err != nil && told != err.(*install.ProblemsError).Problems[0] {
				t.Errorf("%s: the reason is not told: %.200s", name, r.Body.String())
			}
		}
		if after := list(); after != before {
			t.Errorf("%s: an operator was created though the request was refused", name)
		}
	}
}

// A processor's name is only a label: what reaches the command is a key made from it, in the characters a key may have, so a name that is a
// shell command is a processor with an odd name and nothing more.
func TestAProcessorNameCannotChangeWhatTheCommandDoes(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	cl := a.approvedCluster(t, testkit.FP)
	body := extBody("athens", cl)
	body["extraProcessors"] = json.RawMessage(`[{"id":"p1","kind":"filter","name":"a; rm -rf /$(id)","config":{"signal":"metric","conditions":[{"field":"a","op":"eq","value":"b","level":"record"}]}}]`)
	cmd := a.createOperatorDoc(t, cookie, body)["install"].(string)
	if strings.Contains(cmd, "rm -rf") || strings.Contains(cmd, "$(id)") || !strings.Contains(cmd, "--set processors.extraProcessorNames='{filter/a_rm_rf_id}'") {
		t.Fatalf("%s", cmd)
	}
}
