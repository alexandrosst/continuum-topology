package server

import "ikhnos/internal/estate"

func (a *Admin) graphAPI() estate.GraphAPI {
	g, _ := a.C.Store.(estate.GraphAPI)
	return g
}
