package fusionapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

func infoGroups() []AppGroup {
	return []AppGroup{{ID: "app-1", Name: "Shop", Members: []AppMember{
		{Name: "cart", Namespace: "shop", Cluster: "cl-1", Aliases: []string{"cart-app", "cart"}},
		{Name: "web", Namespace: "shop"},
	}}, {ID: "app-2", Name: "Pay", Members: []AppMember{{Name: "pay", Namespace: "pay", Cluster: "cl-2"}}}}
}

func TestInfoSeriesAreOnePerServiceNameAndSorted(t *testing.T) {
	got := AppInfoSeriesOf(infoGroups())
	want := []AppInfoSeries{
		{"Shop", "app-1", "cart", "shop", "cl-1"}, {"Shop", "app-1", "cart-app", "shop", "cl-1"}, {"Shop", "app-1", "web", "shop", ""},
		{"Pay", "app-2", "pay", "pay", "cl-2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

// readMsg returns the fields of one protobuf message by number.
func readMsg(t *testing.T, b []byte) map[protowire.Number][][]byte {
	t.Helper()
	out := map[protowire.Number][][]byte{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			t.Fatalf("bad tag")
		}
		b = b[n:]
		var v []byte
		switch typ {
		case protowire.BytesType:
			v, n = protowire.ConsumeBytes(b)
		case protowire.Fixed64Type:
			_, n = protowire.ConsumeFixed64(b)
			v = b[:n]
		default:
			t.Fatalf("type %v", typ)
		}
		if n < 0 {
			t.Fatalf("bad field")
		}
		out[num] = append(out[num], v)
		b = b[n:]
	}
	return out
}

func TestTheEncodedRequestIsTheOTLPMetricShape(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	req := readMsg(t, EncodeAppInfo(AppInfoSeriesOf(infoGroups()), at))
	rm := readMsg(t, req[1][0])
	sm := readMsg(t, rm[2][0])
	metric := readMsg(t, sm[2][0])
	if string(metric[1][0]) != AppInfoMetric {
		t.Fatalf("name %q", metric[1][0])
	}
	points := readMsg(t, metric[5][0])[1]
	if len(points) != 4 {
		t.Fatalf("%d points", len(points))
	}
	dp := readMsg(t, points[0])
	labels := map[string]string{}
	for _, a := range dp[7] {
		kv := readMsg(t, a)
		labels[string(kv[1][0])] = string(readMsg(t, kv[2][0])[1][0])
	}
	want := map[string]string{"application": "Shop", "application_id": "app-1", "service_name": "cart", "k8s_namespace_name": "shop", "continuum_cluster_id": "cl-1", "member": "cart/shop/cl-1"}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("labels %v", labels)
	}
	if v, _ := protowire.ConsumeFixed64(dp[3][0]); int64(v) != at.UnixNano() {
		t.Fatalf("time %d", v)
	}
	// A member with no cluster simply lacks that label (its member value has an empty last part).
	if n := len(readMsg(t, points[2])[7]); n != 5 {
		t.Fatalf("%d attributes for a service without a cluster", n)
	}
}

func TestPushSendsProtobufAndReportsRefusals(t *testing.T) {
	var gotType, gotPath string
	var gotLen int
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType, gotPath = r.Header.Get("Content-Type"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotLen = len(b)
		if status != 200 {
			http.Error(w, `{"error":"otlp receiver off"}`, status)
		}
	}))
	defer srv.Close()
	c := &Client{Prometheus: srv.URL}
	n, err := c.PushAppInfo(context.Background(), infoGroups())
	if err != nil || n != 4 || gotType != "application/x-protobuf" || gotPath != "/api/v1/otlp/v1/metrics" || gotLen == 0 {
		t.Fatalf("%d %v %q %q %d", n, err, gotType, gotPath, gotLen)
	}
	gotLen = 0
	if n, err := c.PushAppInfo(context.Background(), nil); n != 0 || err != nil || gotLen != 0 {
		t.Fatalf("nothing to say sends nothing: %d %v %d", n, err, gotLen)
	}
	status = 404
	if _, err := c.PushAppInfo(context.Background(), infoGroups()); err == nil || statusOf(err) != http.StatusBadGateway {
		t.Fatalf("a refusal is reported: %v", err)
	}
	if _, err := (&Client{}).PushAppInfo(context.Background(), infoGroups()); !IsUnavailable(err) {
		t.Fatalf("no Prometheus: %v", err)
	}
}

// TestInfoSeriesAreAcceptedByARealPrometheus pushes to a running Prometheus started with --web.enable-otlp-receiver and
// reads the series back by the join a dashboard uses. It runs only when INFO_LIVE_PROMETHEUS names one.
func TestInfoSeriesAreAcceptedByARealPrometheus(t *testing.T) {
	base := os.Getenv("INFO_LIVE_PROMETHEUS")
	if base == "" {
		t.Skip("INFO_LIVE_PROMETHEUS is not set")
	}
	c := &Client{Prometheus: base}
	if n, err := c.PushAppInfo(context.Background(), infoGroups()); err != nil || n != 4 {
		t.Fatalf("%d %v", n, err)
	}
	time.Sleep(time.Second)
	q := url.Values{"query": {AppInfoMetric + `{application="Shop"}`}}
	resp, err := http.Get(base + "/api/v1/query?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data.Result) != 3 {
		t.Fatalf("%+v", out.Data.Result)
	}
	for _, r := range out.Data.Result {
		t.Logf("%v = %v", r.Metric, r.Value[1])
		if r.Metric["application_id"] != "app-1" || r.Metric["k8s_namespace_name"] != "shop" || r.Value[1] != "1" {
			t.Errorf("%v", r)
		}
	}
}

// A member is one service in one namespace of one cluster as a single value, so a picker's selection is exact; a part
// Ikhnos does not know is empty, never a wildcard.
func TestMemberNamesOneServiceInOneNamespaceOfOneCluster(t *testing.T) {
	got := map[string]bool{}
	for _, s := range AppInfoSeriesOf(infoGroups()) {
		got[s.Member()] = true
	}
	for _, want := range []string{"cart/shop/cl-1", "cart-app/shop/cl-1", "web/shop/", "pay/pay/cl-2"} {
		if !got[want] {
			t.Errorf("no member %q in %v", want, got)
		}
	}
}
