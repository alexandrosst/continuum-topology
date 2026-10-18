package server

import "sync"

// A module's own server-wide state (the rate limiters of sign-in, the cache of the opened CAs of operators, the coalescing of
// heartbeat writes) used to be a field of the Core, so that the Core named the types of every module. It is a slot instead: a
// module asks for its state by a key, and builds it the first time. The Core holds slots and knows nothing of what is in them.
//
// There are two kinds, as there were two kinds of field. ServerState is one for the whole server, shared by every organisation's
// view of the Core (ForOrg copies the pointer). OrgState is one for each organisation (ForOrg starts it empty).
type stateSlots struct {
	mu sync.Mutex
	m  map[string]any
}

func slot[T any](s *stateSlots, key string, build func() T) T {
	if s == nil {
		// A Core made by hand, not by NewCore (some tests): nothing is shared, and the state is made for the call.
		return build()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[key]; ok {
		return v.(T)
	}
	if s.m == nil {
		s.m = map[string]any{}
	}
	v := build()
	s.m[key] = v
	return v
}

// ServerState returns the state a module keeps for the whole server under key, building it with build the first time.
func ServerState[T any](c *Core, key string, build func() T) T { return slot(c.shared, key, build) }

// OrgState returns the state a module keeps for this organisation under key, building it with build the first time.
func OrgState[T any](c *Core, key string, build func() T) T { return slot(c.orgState, key, build) }
