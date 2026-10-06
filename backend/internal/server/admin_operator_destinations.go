package server

import (
	"net/http"

	"continuum/internal/store"
)

// operatorDestinationDoc is one place telemetry can be sent, as the destination picker reads it: just enough to
// choose and to say what is wrong with a choice, and no secret or credential of any kind.
type operatorDestinationDoc struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"` // central | regional
	Status string `json:"status"`
	Health struct {
		State      string `json:"state"`
		LastSeenAt string `json:"lastSeenAt,omitempty"`
	} `json:"health"`
	AddressState               string   `json:"addressState"`
	Endpoint                   string   `json:"endpoint,omitempty"`
	ReachableFromOtherClusters bool     `json:"reachableFromOtherClusters"`
	AcceptedModalities         []string `json:"acceptedModalities,omitempty"`
	// Recommended marks the central operator while it runs or is starting: that is what a person who just wants their
	// telemetry saved should pick. Whether some other operator already receives a cluster is not something the
	// server can say about a cluster it has not been told about, so nothing else is ever recommended here.
	Recommended bool `json:"recommended"`
	// UsedBy is how many operators and telemetry intents already send here.
	UsedBy int `json:"usedBy"`
}

// centralOperatorName is what the central operator is called, before it exists and after.
const centralOperatorName = "Central (FUSION)"

// listOperatorDestinations is GET /operator-destinations: the active operators, central first. The central operator
// is listed from the start, even before FUSION was ever turned on (it does not exist yet then, so its entry is made
// up from FUSION's state), so a person can choose it and be told it still has to be turned on; when this server has
// no FUSION at all it is not offered.
func (a *Admin) listOperatorDestinations(w http.ResponseWriter, r *http.Request) {
	core := a.core(r)
	ops, err := core.ListOperators(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	usages, _ := core.OperatorUsages(r.Context())
	fusion := a.fusionState(r)
	out := []operatorDestinationDoc{}
	var regional []operatorDestinationDoc
	haveCentral := false
	for _, op := range ops {
		if op.Status != store.OperatorActive {
			continue
		}
		d := a.opDocWith(r, op, usages, fusion)
		e := operatorDestinationDoc{
			ID: d.ID, Name: d.Name, Kind: "regional", Status: d.Status,
			AddressState: d.AddressState, Endpoint: d.Endpoint, ReachableFromOtherClusters: d.ReachableFromOtherClusters,
			AcceptedModalities: d.AcceptedModalities, UsedBy: usageCount(usages[op.ID]),
		}
		e.Health.State, e.Health.LastSeenAt = d.Health.State, d.Health.LastSeenAt
		if op.ID == CentralOperatorID {
			e.Kind, haveCentral = "central", true
			e.Recommended = d.Health.State == HealthOnline || d.Health.State == HealthStarting
			out = append(out, e)
			continue
		}
		regional = append(regional, e)
	}
	if !haveCentral && fusion.Available {
		h := centralHealth(fusion)
		e := operatorDestinationDoc{ID: CentralOperatorID, Name: centralOperatorName, Kind: "central", Status: string(store.OperatorActive),
			AddressState: addressNone, UsedBy: usageCount(usages[CentralOperatorID]), Recommended: h.State == HealthOnline || h.State == HealthStarting}
		e.Health.State, e.Health.LastSeenAt = h.State, rfcp(fusion.LastDataAt)
		if a.Fusion.Exposed() {
			e.AddressState, e.Endpoint, e.ReachableFromOtherClusters = addressSet, a.Fusion.CentralEndpoint(), true
		}
		out = append([]operatorDestinationDoc{e}, out...)
	}
	writeJSON(w, 200, append(out, regional...))
}

func usageCount(u OperatorUsage) int { return len(u.Operators) + u.Intents }
