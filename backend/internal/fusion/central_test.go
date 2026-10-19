package fusion

import (
	"context"
	"testing"
)

func TestAServerWithoutAControlHasNoCentralOperatorAndNotATypedNil(t *testing.T) {
	var f *FusionControl
	if c := f.Central(); c != nil {
		t.Fatalf("a nil control gave %#v, want a nil interface: callers ask whether there is one", c)
	}
}

func TestTheCentralOperatorPortReadsTheControlsFieldsAndState(t *testing.T) {
	f := &FusionControl{Name: "fusion", Namespace: "continuum-system", Org: "org-1", ClusterUID: "uid-1"}
	c := f.Central()
	if c == nil {
		t.Fatal("a control gave no central operator")
	}
	if c.Org() != "org-1" || c.Namespace() != "continuum-system" || c.ClusterUID() != "uid-1" || c.ServiceName() != "fusion-central" {
		t.Errorf("port reads org %q namespace %q cluster %q service %q", c.Org(), c.Namespace(), c.ClusterUID(), c.ServiceName())
	}
	if c.Exposed() {
		t.Error("a control with no public address is not exposed")
	}
	if c.Endpoint() != f.CentralEndpoint() {
		t.Errorf("endpoint %q, want the control's %q", c.Endpoint(), f.CentralEndpoint())
	}
	// A control that cannot scale FUSION says it is off and not available, which is what the central operator's health is read from.
	if m := c.Managed(context.Background()); m.Available || m.State != "off" {
		t.Errorf("managed state of a control with no cluster access = %+v, want unavailable and off", m)
	}
}
