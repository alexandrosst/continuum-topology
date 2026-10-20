package server

import (
	"time"

	"continuum/internal/fusion"
	"continuum/internal/fusionhttp"
)

// newDataGuard is the guard of the data API over FUSION, with what the server supplies it: the credentials a request carries,
// the address it came from, the limiters it shares with the rest of the API, and what Ikhnos knows. Everything that can change
// after the routes are mounted is read per request.
func (a *Admin) newDataGuard() *fusionhttp.Guard {
	return &fusionhttp.Guard{
		Control:      func() *fusion.FusionControl { return a.Fusion },
		Org:          a.fusionOrg,
		Accounts:     a.C.Accounts,
		Tokens:       a.C.Fusion,
		Bearer:       bearerToken,
		Session:      sessionCookie,
		ClientIP:     a.clientIP,
		AuthFailures: a.authRL,
		Reads:        a.fusionRL,
		Extras:       a.fusionExtras,
		Now:          func() time.Time { return a.C.Now() },
		Links:        a.linksAPI().FusionLinks,
		Log:          a.C.Log,
	}
}

// fd is the guard of the data API: what the handlers fail, narrow and read the time range through.
func (a *Admin) fd() *fusionhttp.Guard {
	if a.dataGuard == nil {
		a.dataGuard = a.newDataGuard()
	}
	return a.dataGuard
}

// fusionOrg is the organisation FUSION belongs to: the one the server was told, else the server's main organisation. (Not
// a.C.OrgID alone: the server's platform-wide core has none, which once made every request look like it came from another
// organisation.)
func (a *Admin) fusionOrg() string {
	if a.Fusion != nil && a.Fusion.Org != "" {
		return a.Fusion.Org
	}
	if a.C.OrgID != "" {
		return a.C.OrgID
	}
	return a.C.Accounts.MainOrg()
}
