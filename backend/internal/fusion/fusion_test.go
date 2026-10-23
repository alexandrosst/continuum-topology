package fusion

import (
	"strings"
	"testing"

	"ikhnos/internal/kernel"
	"ikhnos/internal/store"
)

// A claim whose provisioner does not enforce the requested size (the local-path one) is told apart from one that does.
func TestSizeNotEnforcedNamesTheProvisionersThatIgnoreTheRequestedSize(t *testing.T) {
	if !SizeNotEnforced("rancher.io/local-path") || SizeNotEnforced("ebs.csi.aws.com") {
		t.Fatal("the local-path provisioner ignores the size and the EBS one does not")
	}
}

func TestExplainPod(t *testing.T) {
	for name, tc := range map[string]struct {
		pod    kernel.KubePod
		claim  string
		reason string
		hard   bool
	}{
		"image pull":         {kernel.KubePod{WaitingReason: "ImagePullBackOff"}, "", "Cannot download the image", true},
		"first image pull":   {kernel.KubePod{WaitingReason: "ErrImagePull"}, "", "Cannot download the image", true},
		"bad image":          {kernel.KubePod{WaitingReason: "InvalidImageName"}, "", "The image name is not valid", true},
		"crash loop":         {kernel.KubePod{WaitingReason: "CrashLoopBackOff", LastTerminatedReason: "Error"}, "", "Keeps stopping and restarting", true},
		"out of memory":      {kernel.KubePod{WaitingReason: "CrashLoopBackOff", LastTerminatedReason: "OOMKilled"}, "", "Ran out of memory and keeps restarting", true},
		"bad config":         {kernel.KubePod{WaitingReason: "CreateContainerConfigError"}, "", "Its configuration is incomplete", true},
		"cannot start":       {kernel.KubePod{WaitingReason: "RunContainerError"}, "", "The container could not be started", true},
		"creating":           {kernel.KubePod{WaitingReason: "ContainerCreating"}, "Bound", "Starting the container", false},
		"creating, no claim": {kernel.KubePod{WaitingReason: "ContainerCreating"}, "", "Starting the container", false},
		"creating, volume":   {kernel.KubePod{WaitingReason: "ContainerCreating"}, "Pending", "Waiting for a volume", true},
		"init":               {kernel.KubePod{WaitingReason: "PodInitializing"}, "", "Starting up", false},
		"no room":            {kernel.KubePod{Phase: "Pending", Unschedulable: true, ScheduleMessage: "0/3 nodes are available: 3 Insufficient memory."}, "", "No node has room", true},
		"taints":             {kernel.KubePod{Phase: "Pending", Unschedulable: true, ScheduleMessage: "0/3 nodes are available: 3 node(s) had untolerated taint."}, "", "No suitable node", true},
		"unbound claim":      {kernel.KubePod{Phase: "Pending", Unschedulable: true, ScheduleMessage: "0/3 nodes are available: pod has unbound immediate PersistentVolumeClaims."}, "", "Waiting for a volume", true},
		"claim pending":      {kernel.KubePod{Phase: "Pending", Unschedulable: true, ScheduleMessage: "0/3 nodes are available: 3 Insufficient cpu."}, "Pending", "Waiting for a volume", true},
		"pending, volume":    {kernel.KubePod{Phase: "Pending"}, "Pending", "Waiting for a volume", true},
		"unknown code":       {kernel.KubePod{WaitingReason: "SomethingNew"}, "", "", false},
		"nothing wrong":      {kernel.KubePod{Phase: "Running"}, "Bound", "", false},
	} {
		if reason, hard := ExplainPod(tc.pod, tc.claim); reason != tc.reason || hard != tc.hard {
			t.Errorf("%s: %q hard=%v, want %q hard=%v", name, reason, hard, tc.reason, tc.hard)
		}
	}
}

// fusionName must give the same prefix the ikhnos-fusion chart's own "name" helper does: the operator's
// route endpoints are derived from it, and a mismatch would point an operator at Services that do not exist.
// The cases below are the ones internal/chart/fusion_test.go pins on the chart side.
func TestFusionNameMatchesTheChartHelper(t *testing.T) {
	a50 := strings.Repeat("a", 50)
	cases := []struct{ release, want string }{
		{"fusion", "fusion"},
		{"prod", "prod-fusion"},
		{"my-fusion", "my-fusion"},
		{"fusion-eu", "fusion-eu"},
		{a50, a50}, // 50 + "-fusion" is cut back to 50
		{strings.Repeat("a", 49), strings.Repeat("a", 49)}, // cut at 50 leaves a trailing '-', which the chart trims
		{strings.Repeat("a", 48), strings.Repeat("a", 48) + "-f"},
	}
	for _, c := range cases {
		if got := fusionName(c.release); got != c.want {
			t.Errorf("fusionName(%q) = %q, want %q", c.release, got, c.want)
		}
	}
}

func TestFusionRoutesAreEachStoresInClusterAddress(t *testing.T) {
	routes := fusionRoutes(store.Destination{Kind: store.DestinationFusion, FusionRelease: "fusion", FusionNamespace: "ikhnos-system"})
	want := []fusionRoute{
		{store.ModalityMetrics, "fusion-prometheus.ikhnos-system.svc:9090/api/v1/otlp", "http"},
		{store.ModalityLogs, "fusion-loki.ikhnos-system.svc:3100/otlp", "http"},
		{store.ModalityTraces, "fusion-tempo.ikhnos-system.svc:4317", "grpc"},
	}
	if len(routes) != len(want) {
		t.Fatalf("routes = %+v", routes)
	}
	for i := range want {
		if routes[i] != want[i] {
			t.Errorf("route %d = %+v, want %+v", i, routes[i], want[i])
		}
	}
}
