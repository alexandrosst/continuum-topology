package server

import (
	"context"
	"log/slog"
	"time"
)

// appInfoEvery is how often the info series of the Ikhnos applications is written into FUSION's Prometheus. Prometheus
// treats a series as present for five minutes after its last sample, so a minute keeps it always there while a deleted
// application or a service taken out of one drops out within five.
const appInfoEvery = time.Minute

// PushApplicationInfo writes the info series (fusionapi.AppInfoMetric) for the applications Ikhnos knows into FUSION's
// Prometheus and reports how many series it wrote. It writes nothing, and says so with no error, when FUSION is not
// running or this server has no applications to give: there is no one to read them yet.
func (a *Admin) PushApplicationInfo(ctx context.Context) (int, error) {
	if a == nil || a.Fusion == nil || a.Fusion.Kube == nil {
		return 0, nil
	}
	if st := a.Fusion.TokenStatus(ctx); st.State != "running" {
		return 0, nil
	}
	ex := a.fusionExtras()
	if ex == nil {
		return 0, nil
	}
	groups, err := ex.Applications(ctx)
	if err != nil {
		return 0, err
	}
	return a.Fusion.dataClient().PushAppInfo(ctx, groups)
}

// RunApplicationInfo keeps the info series written until ctx ends. A failure is logged when it starts and when it ends, not
// every minute.
func (a *Admin) RunApplicationInfo(ctx context.Context, log *slog.Logger) {
	failing := false
	for wait := 20 * time.Second; ; wait = appInfoEvery {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		n, err := a.PushApplicationInfo(c)
		cancel()
		switch {
		case err != nil && !failing:
			failing = true
			log.Warn("could not write FUSION's application series; filtering by application in Grafana and PromQL is unavailable until it can", "err", err)
		case err == nil && failing:
			failing = false
			log.Info("FUSION's application series are being written again", "series", n)
		}
	}
}
