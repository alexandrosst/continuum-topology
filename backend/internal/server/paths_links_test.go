package server

import (
	"context"
	"errors"

	"continuum/internal/store"
)

// agentsDown is a store whose list of the organisation's agents cannot be read (a busy database).
type agentsDown struct{ store.Store }

func (agentsDown) ListAgents(context.Context, string) ([]store.Agent, error) {
	return nil, errors.New("database is locked")
}
