package fusionapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPromStorageIsReadFromTheMetricsPage(t *testing.T) {
	page := `# HELP prometheus_tsdb_storage_blocks_bytes x
prometheus_tsdb_storage_blocks_bytes 1.048576e+06
prometheus_tsdb_wal_storage_size_bytes 2048
prometheus_tsdb_head_chunks_storage_size_bytes 1024
prometheus_tsdb_lowest_timestamp 1.7e+12
prometheus_tsdb_something_else 99
`
	got := parsePromStorage([]byte(page))
	if got.Bytes != 1048576+2048+1024 {
		t.Fatalf("bytes = %d", got.Bytes)
	}
	if !got.Oldest.Equal(time.UnixMilli(1700000000000).UTC()) {
		t.Fatalf("oldest = %v", got.Oldest)
	}
	// an empty database reports the largest int64 as its lowest timestamp: that is no time
	if empty := parsePromStorage([]byte("prometheus_tsdb_lowest_timestamp 9.223372036854776e+18\n")); !empty.Oldest.IsZero() {
		t.Fatalf("an empty database has no oldest sample, got %v", empty.Oldest)
	}
}

func TestVolumeUsageReadsTheKubeletsNumbersByClaim(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("query")
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
		 {"metric":{"k8s_persistentvolumeclaim_name":"data-fusion-loki-0","k8s_namespace_name":"obs"},"value":[1,"5000"]},
		 {"metric":{"k8s_persistentvolumeclaim_name":"data-fusion-loki-0","k8s_namespace_name":"obs"},"value":[1,"7000"]}]}}`))
	}))
	defer srv.Close()
	c := &Client{Prometheus: srv.URL}
	got, err := c.VolumeUsage(context.Background(), AllSignals(), []string{"data-fusion-loki-0", "bad name;{"})
	if err != nil {
		t.Fatal(err)
	}
	if u := got["data-fusion-loki-0"]; u.Used != 7000 || u.Namespace != "obs" {
		t.Fatalf("got %+v", got)
	}
	if asked == "" || strings.Contains(asked, "bad name") {
		t.Fatalf("a name that is not a claim name must not reach the query: %q", asked)
	}
	// a restricted scope may not run it
	if _, err := c.VolumeUsage(context.Background(), Scope{Signals: Signals, Namespaces: []string{"a"}}, []string{"x"}); err == nil {
		t.Fatal("a limited scope must not run a raw query")
	}
}
