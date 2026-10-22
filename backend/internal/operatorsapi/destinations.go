package operatorsapi

import (
	"net/http"

	"continuum/internal/httpkit"
	"continuum/internal/kernel"
	"continuum/internal/operators"
	"continuum/internal/store"
)

// DestinationRole says whether a destination is the gateway in front of FUSION or an operator someone installed.
type DestinationRole string

// The roles of a destination.
const (
	RoleCentral  DestinationRole = "central"
	RoleRegional DestinationRole = "regional"
)

// WireWords is the list of DestinationRole's words, as the generated types give it.
func (DestinationRole) WireWords() []string {
	return []string{string(RoleCentral), string(RoleRegional)}
}

// operatorDestinationDoc is one place telemetry can be sent, as the destination picker reads it: just enough to
// choose and to say what is wrong with a choice, and no secret or credential of any kind.
type operatorDestinationDoc struct {
	ID     string               `json:"id"`
	Name   string               `json:"name"`
	Kind   DestinationRole      `json:"kind"`
	Status store.OperatorStatus `json:"status"`
	Health struct {
		State      operators.HealthState `json:"state"`
		LastSeenAt string                `json:"lastSeenAt,omitempty"`
	} `json:"health"`
	AddressState               AddressStatus    `json:"addressState"`
	Endpoint                   string           `json:"endpoint,omitempty"`
	ReachableFromOtherClusters bool             `json:"reachableFromOtherClusters"`
	AcceptedModalities         []store.Modality `json:"acceptedModalities,omitempty"`
	// Recommended marks the central operator while it runs or is starting: that is what a person who just wants their
	// telemetry saved should pick. Whether some other operator already receives a cluster is not something the
	// server can say about a cluster it has not been told about, so nothing else is ever recommended here.
	Recommended bool `json:"recommended"`
	// UsedBy is how many operators and telemetry intents already send here.
	UsedBy int `json:"usedBy"`
}

// CentralOperatorName is what the central operator is called, before it exists and after.
const CentralOperatorName = "Central (FUSION)"

// listOperatorDestinations is GET /operator-destinations: the active operators, central first. The central operator
// is listed from the start, even before FUSION was ever turned on (it does not exist yet then, so its entry is made
// up from FUSION's state), so a person can choose it and be told it still has to be turned on; when this server has
// no FUSION at all it is not offered.
func (h *API) listOperatorDestinations(w http.ResponseWriter, r *http.Request) {
	core := h.For(r)
	ops, err := core.ListOperators(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	usages, _ := core.OperatorUsages(r.Context())
	managed := h.ManagedState(r)
	out := []operatorDestinationDoc{}
	var regional []operatorDestinationDoc
	haveCentral := false
	for _, op := range ops {
		if op.Status != store.OperatorActive {
			continue
		}
		d := h.opDocWith(r, op, usages, managed)
		e := operatorDestinationDoc{
			ID: d.ID, Name: d.Name, Kind: RoleRegional, Status: d.Status,
			AddressState: d.AddressState, Endpoint: d.Endpoint, ReachableFromOtherClusters: d.ReachableFromOtherClusters,
			AcceptedModalities: d.AcceptedModalities, UsedBy: usageCount(usages[op.ID]),
		}
		e.Health.State, e.Health.LastSeenAt = d.Health.State, d.Health.LastSeenAt
		if op.ID == operators.CentralOperatorID {
			e.Kind, haveCentral = RoleCentral, true
			e.Recommended = d.Health.State == operators.HealthOnline || d.Health.State == operators.HealthStarting
			out = append(out, e)
			continue
		}
		regional = append(regional, e)
	}
	if !haveCentral && managed.Available {
		health := operators.CentralHealth(managed)
		e := operatorDestinationDoc{ID: operators.CentralOperatorID, Name: CentralOperatorName, Kind: RoleCentral, Status: store.OperatorActive,
			AddressState: addressNone, UsedBy: usageCount(usages[operators.CentralOperatorID]), Recommended: health.State == operators.HealthOnline || health.State == operators.HealthStarting}
		e.Health.State, e.Health.LastSeenAt = health.State, kernel.RFCP(managed.LastDataAt)
		if c := h.central(); c.Exposed() {
			e.AddressState, e.Endpoint, e.ReachableFromOtherClusters = addressSet, c.Endpoint(), true
		}
		out = append([]operatorDestinationDoc{e}, out...)
	}
	httpkit.WriteJSON(w, 200, append(out, regional...))
}

func usageCount(u operators.OperatorUsage) int { return len(u.Operators) + u.Intents }
