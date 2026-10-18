package server

import "continuum/internal/estate"

func (a *Admin) graphAPI() estate.GraphAPI {
	g, _ := a.C.Store.(estate.GraphAPI)
	return g
}
