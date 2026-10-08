package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"continuum/internal/fusionapi"
)

func (k *fakeKube) Settings(_ context.Context, name string) (map[string]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.settingsErr != nil {
		return nil, k.settingsErr
	}
	if k.settings == nil || name != "continuum-fusion-settings" {
		return nil, ErrKubeNotFound
	}
	out := map[string]string{}
	for key, v := range k.settings {
		out[key] = v
	}
	return out, nil
}

func (k *fakeKube) PatchSettings(_ context.Context, name string, data map[string]string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.err != nil {
		return k.err
	}
	k.ops = append(k.ops, "settings")
	for key, v := range data {
		k.settings[key] = v
	}
	return nil
}

func (k *fakeKube) Claim(_ context.Context, name string) (KubeClaim, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	c, ok := k.rclaims[name]
	if !ok {
		return KubeClaim{}, ErrKubeNotFound
	}
	return c, nil
}

func (k *fakeKube) ResizeClaim(_ context.Context, name string, bytes int64) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.resizeErr != nil && (k.resizeErrOn == "" || k.resizeErrOn == name) {
		return k.resizeErr
	}
	c := k.rclaims[name]
	c.Requested = bytes // the cluster takes a while to catch up: Capacity stays
	k.rclaims[name] = c
	k.ops = append(k.ops, "resize:"+name)
	return nil
}

func (k *fakeKube) RestartPod(_ context.Context, name string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.restartErr != nil {
		return k.restartErr
	}
	k.ops = append(k.ops, "restart:"+name)
	return nil
}

const gib = int64(1) << 30

// retentionRig is FUSION as the chart leaves it with the server managing it: three stores on 10 GiB volumes.
func retentionRig(t *testing.T) (*FusionControl, *fakeKube, *adminRig) {
	t.Helper()
	a := newAdminRig(t)
	f, k := newFusion(t, a)
	f.Org = "org-1"
	created := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f.Now = func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
	k.settings = map[string]string{"prometheus.retention": "15d", "prometheus.retentionSize": "8704MB", "loki.retention": "168h", "tempo.retention": "72h"}
	k.rclaims = map[string]KubeClaim{}
	for _, n := range []string{"prometheus", "loki", "tempo"} {
		k.rclaims["data-continuum-fusion-"+n+"-0"] = KubeClaim{Phase: "Bound", StorageClass: "fast", Requested: 10 * gib, Capacity: 10 * gib, Created: created}
	}
	for n := range k.replicas {
		k.replicas[n] = 1
	}
	k.allReady()
	return f, k, a
}

func intp(n int) *int { return &n }

func storeOf(t *testing.T, d RetentionDoc, component string) RetentionStore {
	t.Helper()
	for _, s := range d.Stores {
		if s.Component == component {
			return s
		}
	}
	t.Fatalf("no %s store in %+v", component, d)
	return RetentionStore{}
}

func TestRetentionShowsEachStoresSettingAndVolume(t *testing.T) {
	f, _, a := retentionRig(t)
	d := f.Retention(context.Background(), a.a.C.ForOrg("org-1"))
	if !d.Available || !d.Running || len(d.Stores) != 3 {
		t.Fatalf("doc = %+v", d)
	}
	m, l, tr := storeOf(t, d, "metrics"), storeOf(t, d, "logs"), storeOf(t, d, "traces")
	if m.Days != 15 || !m.ExactDays || m.Value != "15d" || m.VolumeBytes != 10*gib || m.StorageClass != "fast" || m.MaxDays != 1095 {
		t.Errorf("metrics = %+v", m)
	}
	if l.Days != 7 || tr.Days != 3 || l.MaxDays != 365 {
		t.Errorf("logs %+v traces %+v", l, tr)
	}
	if m.SizeLimitBytes == nil || *m.SizeLimitBytes != 8704<<20 {
		t.Errorf("size limit = %v", m.SizeLimitBytes)
	}
}

func TestRetentionSaysWhyItIsUnavailable(t *testing.T) {
	f, k, a := retentionRig(t)
	ctx := context.Background()
	k.settings = nil
	if d := f.Retention(ctx, a.a.C.ForOrg("org-1")); d.Available || d.Reason != "unmanaged" || !strings.Contains(d.Message, "Helm") {
		t.Errorf("no settings object: %+v", d)
	}
	k.settings = map[string]string{}
	k.settingsErr = ErrKubeForbidden
	if d := f.Retention(ctx, a.a.C.ForOrg("org-1")); d.Available || d.Reason != "forbidden" {
		t.Errorf("Role without the grant: %+v", d)
	}
	k.settingsErr = nil
	other := a.a.C.ForOrg("org-2")
	if d := f.Retention(ctx, other); d.Available || d.Reason != "other-org" {
		t.Errorf("another organisation: %+v", d)
	}
	var nilFusion *FusionControl
	if d := nilFusion.Retention(ctx, a.a.C.ForOrg("org-1")); d.Available {
		t.Errorf("no switch: %+v", d)
	}
}

func TestSetRetentionGrowsTheVolumeBeforeSavingAndRestartsOnlyWhatChanged(t *testing.T) {
	f, k, a := retentionRig(t)
	ctx := context.Background()
	d, err := f.SetRetention(ctx, a.a.C.ForOrg("org-1"), "alex", RetentionRequest{
		Metrics: &RetentionChange{Days: intp(30), VolumeGiB: intp(20)},
		Traces:  &RetentionChange{Days: intp(7)},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The volume grows first, the settings are saved, then the changed stores restart; Loki is untouched.
	want := "resize:data-continuum-fusion-prometheus-0,settings,restart:continuum-fusion-prometheus-0,restart:continuum-fusion-tempo-0"
	if got := strings.Join(k.ops, ","); got != want {
		t.Errorf("ops = %s\nwant  %s", got, want)
	}
	if k.settings["prometheus.retention"] != "30d" || k.settings["tempo.retention"] != "168h" || k.settings["loki.retention"] != "168h" {
		t.Errorf("settings = %v", k.settings)
	}
	// A size limit that was still the default follows the bigger volume (85% of 20 GiB in MB).
	if k.settings["prometheus.retentionSize"] != "17408MB" {
		t.Errorf("size limit = %q", k.settings["prometheus.retentionSize"])
	}
	if m := storeOf(t, d, "metrics"); m.Days != 30 || m.VolumeBytes != 20*gib || !m.Resizing {
		t.Errorf("answer = %+v", m)
	}
	rows, _ := a.a.C.Store.AuditSince(ctx, 0, 500)
	var found string
	for _, e := range rows {
		if e.Action == "fusion-retention-changed" {
			found = e.Detail
		}
	}
	for _, part := range []string{"Prometheus retention 15d -> 30d", "Prometheus volume 10 GiB -> 20 GiB", "Tempo retention 72h -> 168h", "restarted Prometheus, Tempo"} {
		if !strings.Contains(found, part) {
			t.Errorf("audit detail %q lacks %q", found, part)
		}
	}
}

func TestSetRetentionKeepsASizeLimitSomeoneChose(t *testing.T) {
	f, k, a := retentionRig(t)
	k.settings["prometheus.retentionSize"] = "5GB"
	if _, err := f.SetRetention(context.Background(), a.a.C.ForOrg("org-1"), "alex", RetentionRequest{Metrics: &RetentionChange{VolumeGiB: intp(20)}}); err != nil {
		t.Fatal(err)
	}
	if k.settings["prometheus.retentionSize"] != "5GB" {
		t.Errorf("a chosen size limit was replaced: %q", k.settings["prometheus.retentionSize"])
	}
	// Growing the volume alone changes nothing the store reads, so it is not restarted.
	if got := strings.Join(k.ops, ","); got != "resize:data-continuum-fusion-prometheus-0" {
		t.Errorf("ops = %s", got)
	}
}

func TestSetRetentionLeavesTheSettingsAloneWhenTheVolumeCannotGrow(t *testing.T) {
	f, k, a := retentionRig(t)
	k.resizeErr = ErrKubeNoResize
	_, err := f.SetRetention(context.Background(), a.a.C.ForOrg("org-1"), "alex", RetentionRequest{Logs: &RetentionChange{Days: intp(30), VolumeGiB: intp(50)}})
	if err == nil || !strings.Contains(err.Error(), "allowVolumeExpansion") || !strings.Contains(err.Error(), "fast") {
		t.Fatalf("err = %v", err)
	}
	if len(k.ops) != 0 || k.settings["loki.retention"] != "168h" {
		t.Errorf("a failed grow changed something: ops %v settings %v", k.ops, k.settings)
	}
}

func TestSetRetentionSaysWhatWasGrownWhenALaterVolumeFails(t *testing.T) {
	f, k, a := retentionRig(t)
	k.resizeErr, k.resizeErrOn = errors.New("quota exceeded"), "data-continuum-fusion-loki-0"
	_, err := f.SetRetention(context.Background(), a.a.C.ForOrg("org-1"), "alex", RetentionRequest{
		Metrics: &RetentionChange{Days: intp(30), VolumeGiB: intp(20)}, Logs: &RetentionChange{Days: intp(30), VolumeGiB: intp(20)}})
	if err == nil || !strings.Contains(err.Error(), "Prometheus was already grown") || !strings.Contains(err.Error(), "no retention was changed") {
		t.Fatalf("err = %v", err)
	}
	if k.settings["prometheus.retention"] != "15d" || k.settings["loki.retention"] != "168h" {
		t.Errorf("settings changed after a failed grow: %v", k.settings)
	}
	for _, op := range k.ops {
		if strings.HasPrefix(op, "restart") || op == "settings" {
			t.Errorf("op %s after a failed grow", op)
		}
	}
}

func TestSetRetentionChecksEverythingBeforeChangingAnything(t *testing.T) {
	f, k, a := retentionRig(t)
	delete(k.rclaims, "data-continuum-fusion-loki-0") // its volume cannot be read
	_, err := f.SetRetention(context.Background(), a.a.C.ForOrg("org-1"), "alex", RetentionRequest{
		Metrics: &RetentionChange{VolumeGiB: intp(20)}, Logs: &RetentionChange{VolumeGiB: intp(20)}})
	if err == nil || len(k.ops) != 0 {
		t.Fatalf("validation must come before any change: err %v ops %v", err, k.ops)
	}
}

func TestSetRetentionRefusesWhatCannotBeDone(t *testing.T) {
	f, k, a := retentionRig(t)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		req  RetentionRequest
		want string
	}{
		"shrink":             {RetentionRequest{Metrics: &RetentionChange{VolumeGiB: intp(5)}}, "not made smaller"},
		"zero days":          {RetentionRequest{Logs: &RetentionChange{Days: intp(0)}}, "between 1 and 365"},
		"too many (logs)":    {RetentionRequest{Logs: &RetentionChange{Days: intp(366)}}, "between 1 and 365"},
		"too many (metrics)": {RetentionRequest{Metrics: &RetentionChange{Days: intp(1096)}}, "between 1 and 1095"},
		"huge volume":        {RetentionRequest{Traces: &RetentionChange{VolumeGiB: intp(99999)}}, "between 1 and"},
		"nothing asked":      {RetentionRequest{}, "say which"},
		"empty change":       {RetentionRequest{Logs: &RetentionChange{}}, "say which"},
	} {
		_, err := f.SetRetention(ctx, a.a.C.ForOrg("org-1"), "alex", tc.req)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	if len(k.ops) != 0 {
		t.Errorf("a refused request changed %v", k.ops)
	}
	// A volume still growing from the last time is not grown again.
	c := k.rclaims["data-continuum-fusion-loki-0"]
	c.Requested = 20 * gib
	k.rclaims["data-continuum-fusion-loki-0"] = c
	if _, err := f.SetRetention(ctx, a.a.C.ForOrg("org-1"), "alex", RetentionRequest{Logs: &RetentionChange{VolumeGiB: intp(40)}}); err == nil || !strings.Contains(err.Error(), "still being grown") {
		t.Errorf("growing twice: %v", err)
	}
	// Another organisation may not change it.
	if _, err := f.SetRetention(ctx, a.a.C.ForOrg("org-2"), "eve", RetentionRequest{Logs: &RetentionChange{Days: intp(2)}}); err == nil {
		t.Error("another organisation changed FUSION's retention")
	}
}

func TestSetRetentionAlreadySoChangesNothing(t *testing.T) {
	f, k, a := retentionRig(t)
	d, err := f.SetRetention(context.Background(), a.a.C.ForOrg("org-1"), "alex", RetentionRequest{
		Metrics: &RetentionChange{Days: intp(15), VolumeGiB: intp(10)}, Logs: &RetentionChange{Days: intp(7)}})
	if err != nil || len(k.ops) != 0 || !d.Available {
		t.Fatalf("err %v ops %v doc %+v", err, k.ops, d)
	}
}

func TestSetRetentionDoesNotRestartStoresThatAreOff(t *testing.T) {
	f, k, a := retentionRig(t)
	for n := range k.replicas {
		k.replicas[n] = 0
	}
	k.allReady()
	if _, err := f.SetRetention(context.Background(), a.a.C.ForOrg("org-1"), "alex", RetentionRequest{Logs: &RetentionChange{Days: intp(14)}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(k.ops, ","); got != "settings" {
		t.Errorf("ops = %s (the new value is read when they next start)", got)
	}
}

func TestSetRetentionSavesEvenIfARestartFails(t *testing.T) {
	f, k, a := retentionRig(t)
	k.restartErr = errors.New("boom")
	d, err := f.SetRetention(context.Background(), a.a.C.ForOrg("org-1"), "alex", RetentionRequest{Logs: &RetentionChange{Days: intp(14)}})
	if err != nil {
		t.Fatal(err)
	}
	if k.settings["loki.retention"] != "336h" || len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "Loki") {
		t.Errorf("settings %v warnings %v", k.settings, d.Warnings)
	}
}

// The numbers behind "how much room does this need": what the stores report about their own disk.
func TestRetentionMeasuresGrowthFromWhatTheStoresReport(t *testing.T) {
	f, k, a := retentionRig(t)
	now := f.now()
	oldest := now.Add(-5 * 24 * time.Hour).UnixMilli()
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics":
			w.Write([]byte("prometheus_tsdb_storage_blocks_bytes 4e+09\nprometheus_tsdb_wal_storage_size_bytes 5e+08\nprometheus_tsdb_lowest_timestamp " + strconv.FormatInt(oldest, 10) + "\n"))
		case "/api/v1/query":
			w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` +
				`{"metric":{"k8s_persistentvolumeclaim_name":"data-continuum-fusion-loki-0","k8s_namespace_name":"continuum"},"value":[1,"3000000000"]},` +
				`{"metric":{"k8s_persistentvolumeclaim_name":"data-continuum-fusion-tempo-0","k8s_namespace_name":"other"},"value":[1,"9000000000"]}]}}`))
		}
	}))
	defer prom.Close()
	f.Data = &fusionapi.Client{Prometheus: prom.URL}
	_ = k

	d := f.Retention(context.Background(), a.a.C.ForOrg("org-1"))
	m := storeOf(t, d, "metrics")
	if m.UsedBytes == nil || *m.UsedBytes != 4_500_000_000 || m.UsedSource != "database" {
		t.Fatalf("metrics use = %+v", m)
	}
	// 4.5 GB over five days of data.
	if m.BytesPerDay == nil || *m.BytesPerDay != 900_000_000 || m.DataDays == nil || *m.DataDays < 4.99 || *m.DataDays > 5.01 {
		t.Errorf("metrics growth = %v over %v days", m.BytesPerDay, m.DataDays)
	}
	// 8704 MiB at 0.9 GB/day keeps about ten days, fewer than the 15 asked for: the card must be able to say so.
	if m.SizeLimitDays == nil || *m.SizeLimitDays < 9.9 || *m.SizeLimitDays > 10.2 {
		t.Errorf("size limit keeps %v days", m.SizeLimitDays)
	}
	// Loki: the kubelet's count for its volume, 30 days old (older than its 7 days of retention, so a week's worth).
	l := storeOf(t, d, "logs")
	if l.UsedBytes == nil || *l.UsedBytes != 3_000_000_000 || l.UsedSource != "volume" || l.BytesPerDay == nil || *l.BytesPerDay != 3_000_000_000/7 {
		t.Errorf("logs = %+v", l)
	}
	// A volume reported from another namespace is not this FUSION's.
	if tr := storeOf(t, d, "traces"); tr.UsedBytes != nil {
		t.Errorf("another namespace's volume was taken: %+v", tr)
	}
}

func TestRetentionSurvivesStoresThatCannotBeMeasured(t *testing.T) {
	f, _, a := retentionRig(t) // Data is an empty client: nothing to ask
	d := f.Retention(context.Background(), a.a.C.ForOrg("org-1"))
	for _, s := range d.Stores {
		if s.UsedBytes != nil || s.BytesPerDay != nil {
			t.Errorf("%s: invented a measurement: %+v", s.Component, s)
		}
		if !s.VolumeKnown {
			t.Errorf("%s: the volume is known from the claim", s.Component)
		}
	}
}

func TestRetentionValueParsing(t *testing.T) {
	for in, want := range map[string]float64{"15d": 15, "168h": 7, "1w2d": 9, "1y": 365, "36h": 1.5, "90m": 90.0 / 1440} {
		if got, ok := parseRetentionDays(in); !ok || got < want-1e-9 || got > want+1e-9 {
			t.Errorf("parseRetentionDays(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "d", "15", "15x", "-3d", "3d junk", "1.5d"} {
		if _, ok := parseRetentionDays(bad); ok {
			t.Errorf("parseRetentionDays(%q) accepted", bad)
		}
	}
	if n, ok := parseRetentionSize("8704MB"); !ok || n != 8704<<20 {
		t.Errorf("size = %d %v", n, ok)
	}
	if n, ok := parseRetentionSize("10GB"); !ok || n != 10<<30 {
		t.Errorf("size = %d %v", n, ok)
	}
	if _, ok := parseRetentionSize("lots"); ok {
		t.Error("size accepted nonsense")
	}
	// The chart's own default for a 10Gi volume: the server must derive the same text to know a value is still the default.
	if got := derivedRetentionSize(10 * gib); got != "8704MB" {
		t.Errorf("derived = %q", got)
	}
}

func TestFusionRetentionOverHTTP(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "alex", RoleAdmin)
	_, viewer := a.user(t, "vera", RoleViewer)
	if r := a.do("GET", "/api/v1/fusion/retention", nil, withCookie(admin)); r.Code != 200 || r.json(t)["available"] != false {
		t.Fatalf("no switch: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("PUT", "/api/v1/fusion/retention", map[string]any{"logs": map[string]any{"days": 3}}, withCookie(admin)); r.Code != 409 {
		t.Fatalf("no switch, change: %d %s", r.Code, r.Body.String())
	}

	f, k := newFusion(t, a)
	a.a.Fusion = f
	k.settings = map[string]string{"prometheus.retention": "15d", "loki.retention": "168h", "tempo.retention": "72h"}
	k.rclaims = map[string]KubeClaim{}
	for _, n := range []string{"prometheus", "loki", "tempo"} {
		k.rclaims["data-continuum-fusion-"+n+"-0"] = KubeClaim{Requested: 10 * gib, Capacity: 10 * gib}
	}
	r := a.do("GET", "/api/v1/fusion/retention", nil, withCookie(admin))
	if doc := r.json(t); r.Code != 200 || doc["available"] != true || len(doc["stores"].([]any)) != 3 {
		t.Fatalf("read: %d %s", r.Code, r.Body.String())
	}
	r = a.do("PUT", "/api/v1/fusion/retention", map[string]any{"logs": map[string]any{"days": 14, "volumeGiB": 20}}, withCookie(admin))
	if r.Code != 200 || k.settings["loki.retention"] != "336h" {
		t.Fatalf("change: %d %s settings %v", r.Code, r.Body.String(), k.settings)
	}
	if r := a.do("PUT", "/api/v1/fusion/retention", map[string]any{"logs": map[string]any{"days": 0}}, withCookie(admin)); r.Code != 400 {
		t.Errorf("bad days: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("PUT", "/api/v1/fusion/retention", map[string]any{"logs": map[string]any{"weeks": 2}}, withCookie(admin)); r.Code != 400 {
		t.Errorf("unknown field: %d", r.Code)
	}
	if r := a.do("PUT", "/api/v1/fusion/retention", map[string]any{"logs": map[string]any{"days": 3}}, withCookie(viewer)); r.Code != 403 {
		t.Errorf("viewer change: %d", r.Code)
	}
	if r := a.do("GET", "/api/v1/fusion/retention", nil, withCookie(viewer)); r.Code != 403 {
		t.Errorf("viewer read: %d", r.Code)
	}
}
