package chart

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func containerOf(t *testing.T, r fusionRendered, set string) corev1.Container {
	t.Helper()
	s, ok := r.sets[set]
	if !ok {
		t.Fatalf("no StatefulSet %s", set)
	}
	return s.Spec.Template.Spec.Containers[0]
}

func envFrom(c corev1.Container, name string) (cm, key string, ok bool) {
	for _, e := range c.Env {
		if e.Name == name && e.ValueFrom != nil && e.ValueFrom.ConfigMapKeyRef != nil {
			return e.ValueFrom.ConfigMapKeyRef.Name, e.ValueFrom.ConfigMapKeyRef.Key, true
		}
	}
	return "", "", false
}

// While the server manages FUSION (switch.managed), how long each store keeps its data is read from one ConfigMap the
// server changes, so a `helm upgrade` cannot undo it. Off, the stores take the chart's values as they always did.
func TestFusionRetentionIsTheServersWhenItManagesFusion(t *testing.T) {
	r := fusionRender(t, "obs", "--set", "switch.managed=true", "--set", "prometheus.retention=30d", "--set", "loki.retention=72h", "--set", "tempo.retention=48h")
	cm, ok := r.configs["obs-fusion-settings"]
	if !ok {
		t.Fatalf("no settings ConfigMap; have %v", mapKeys(r.configs))
	}
	// The first install's values are what the chart's own values say.
	want := map[string]string{"prometheus.retention": "30d", "prometheus.retentionSize": "8704MB", "loki.retention": "72h", "tempo.retention": "48h"}
	for k, v := range want {
		if cm.Data[k] != v {
			t.Errorf("settings[%s] = %q, want %q", k, cm.Data[k], v)
		}
	}
	prom := containerOf(t, r, "obs-fusion-prometheus")
	for flag, env := range map[string]string{"--storage.tsdb.retention.time": "PROMETHEUS_RETENTION", "--storage.tsdb.retention.size": "PROMETHEUS_RETENTION_SIZE"} {
		if v, ok := argValue(prom.Args, flag); !ok || v != "$("+env+")" {
			t.Errorf("Prometheus %s = %q, want $(%s)", flag, v, env)
		}
	}
	for _, c := range []struct{ set, env, key string }{
		{"obs-fusion-prometheus", "PROMETHEUS_RETENTION", "prometheus.retention"}, {"obs-fusion-prometheus", "PROMETHEUS_RETENTION_SIZE", "prometheus.retentionSize"},
		{"obs-fusion-loki", "LOKI_RETENTION", "loki.retention"}, {"obs-fusion-tempo", "TEMPO_RETENTION", "tempo.retention"},
	} {
		name, key, ok := envFrom(containerOf(t, r, c.set), c.env)
		if !ok || name != "obs-fusion-settings" || key != c.key {
			t.Errorf("%s: %s comes from %q %q (found %v), want obs-fusion-settings %s", c.set, c.env, name, key, ok, c.key)
		}
	}
	// Loki and Tempo expand the variable in their config; the config does not hold the number any more.
	for _, c := range []struct{ set, cfgKey, env string }{{"obs-fusion-loki", "loki.yaml", "LOKI_RETENTION"}, {"obs-fusion-tempo", "tempo.yaml", "TEMPO_RETENTION"}} {
		if !strings.Contains(strings.Join(containerOf(t, r, c.set).Args, " "), "-config.expand-env=true") {
			t.Errorf("%s does not expand environment variables in its config", c.set)
		}
		cfg := r.configs[c.set].Data[c.cfgKey]
		if !strings.Contains(cfg, "${"+c.env+"}") {
			t.Errorf("%s config does not use ${%s}:\n%s", c.set, c.env, cfg)
		}
	}
}

func TestFusionRetentionIsTheChartsOwnWhenTheServerDoesNotManageIt(t *testing.T) {
	r := fusionRender(t, "obs", "--set", "prometheus.retention=30d")
	if _, ok := r.configs["obs-fusion-settings"]; ok {
		t.Error("a settings ConfigMap exists although the server does not manage FUSION")
	}
	prom := containerOf(t, r, "obs-fusion-prometheus")
	if v, _ := argValue(prom.Args, "--storage.tsdb.retention.time"); v != "30d" {
		t.Errorf("retention = %q, want 30d", v)
	}
	for _, set := range []string{"obs-fusion-prometheus", "obs-fusion-loki", "obs-fusion-tempo"} {
		if env := containerOf(t, r, set).Env; len(env) != 0 {
			t.Errorf("%s has environment %v", set, env)
		}
	}
	if strings.Contains(strings.Join(containerOf(t, r, "obs-fusion-loki").Args, " "), "expand-env") {
		t.Error("Loki expands environment variables although nothing needs it")
	}
	if cfg := r.configs["obs-fusion-loki"].Data["loki.yaml"]; !strings.Contains(cfg, "retention_period: 168h") {
		t.Errorf("Loki retention is not the value:\n%s", cfg)
	}
	if cfg := r.configs["obs-fusion-tempo"].Data["tempo.yaml"]; !strings.Contains(cfg, "block_retention: 72h") {
		t.Errorf("Tempo retention is not the value:\n%s", cfg)
	}
}
