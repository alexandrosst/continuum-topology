package server

import (
	"context"
	"net/http"
	"time"

	"ikhnos/internal/connectivity"
	"ikhnos/internal/fusionapi"
	"ikhnos/internal/kernel"
	"ikhnos/internal/linksapi"
)

// linksAPI is the organisation's links API (the Sites screen's figures, FUSION's /links, the credential rotation). Built on first use,
// and once: it keeps the one read of FUSION that everyone who has the Sites screen open shares.
func (a *Admin) linksAPI() *linksapi.API {
	a.linksOnce.Do(func() {
		a.links = &linksapi.API{
			For:          func(r *http.Request) *kernel.Core { return a.core(r).Core },
			Connectivity: func(r *http.Request) *connectivity.Service { return a.core(r).Connectivity },
			Hub:          func(r *http.Request) linksapi.Hub { return a.tn(r).Hub },
			Data: func() *fusionapi.Client {
				if a.Fusion == nil {
					return nil
				}
				return a.Fusion.DataClient()
			},
			FusionOrg:      a.fusionOrg,
			FusionSettings: a.fusionLinkSettings,
			Err:            func(w http.ResponseWriter, err error) { a.fd().Err(w, err) },
			Now:            func() time.Time { return a.C.Now() },
		}
	})
	return a.links
}

// fusionLinkSettings are the pairs the FUSION organisation asked to have watched.
func (a *Admin) fusionLinkSettings(ctx context.Context) ([]kernel.LinkSetting, error) {
	org := a.fusionOrg()
	if a.P == nil {
		if a.C.OrgID == org {
			return a.C.Settings().Links, nil
		}
		return nil, &fusionapi.Error{Status: http.StatusServiceUnavailable, Msg: "the organisation's settings are not available"}
	}
	t, err := a.P.Tenant(ctx, org)
	if err != nil || t == nil {
		return nil, &fusionapi.Error{Status: http.StatusServiceUnavailable, Msg: "the organisation's settings are not available"}
	}
	return t.C.Settings().Links, nil
}
