package server

import (
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
