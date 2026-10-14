package fusionapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestOnlyNamedClustersAreWrittenOnceEach(t *testing.T) {
	got := ClusterInfoSeriesOf([]ClusterName{{"cl-b", "beta"}, {"cl-a", "alpha"}, {"cl-a", "again"}, {"cl-c", ""}, {"", "nameless id"}})
	want := []ClusterName{{"cl-a", "alpha"}, {"cl-b", "beta"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestTheClusterInfoRequestCarriesTheIdAndTheName(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	req := readMsg(t, EncodeClusterInfo([]ClusterName{{"cl-2258058a6b", "sunrise-cluster"}}, at))
	rm := readMsg(t, req[1][0])
	sm := readMsg(t, rm[2][0])
	metric := readMsg(t, sm[2][0])
	if string(metric[1][0]) != ClusterInfoMetric {
		t.Fatalf("name %q", metric[1][0])
	}
	dp := readMsg(t, readMsg(t, metric[5][0])[1][0])
	labels := map[string]string{}
	for _, a := range dp[7] {
		kv := readMsg(t, a)
		labels[string(kv[1][0])] = string(readMsg(t, kv[2][0])[1][0])
	}
	if want := map[string]string{"cluster": "cl-2258058a6b", "cluster_name": "sunrise-cluster"}; !reflect.DeepEqual(labels, want) {
		t.Fatalf("labels %v", labels)
	}
}

func TestPushClusterInfoSendsOnlyWhenThereIsAName(t *testing.T) {
	var sent int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent += len(b)
	}))
	defer srv.Close()
	c := &Client{Prometheus: srv.URL}
	if n, err := c.PushClusterInfo(context.Background(), []ClusterName{{"cl-a", "alpha"}}); err != nil || n != 1 || sent == 0 {
		t.Fatalf("%d %v %d", n, err, sent)
	}
	sent = 0
	if n, err := c.PushClusterInfo(context.Background(), []ClusterName{{"cl-a", ""}}); err != nil || n != 0 || sent != 0 {
		t.Fatalf("nothing to say sends nothing: %d %v %d", n, err, sent)
	}
	if _, err := (&Client{}).PushClusterInfo(context.Background(), []ClusterName{{"cl-a", "alpha"}}); !IsUnavailable(err) {
		t.Fatalf("no Prometheus: %v", err)
	}
}
