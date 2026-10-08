package fusionapi

import (
	"net/http"
	"slices"
	"sort"
	"strings"
)

// maxAppServices bounds how many service names one application may put into a query. Past it the query would be a
// regular expression too long for the stores to take well, and an application that large is not a useful filter.
const maxAppServices = 500

// FindGroup finds an Ikhnos application by id, or else by name without regard to case. A name that two applications
// share is ambiguous and is refused rather than guessed.
func FindGroup(groups []AppGroup, ref string) (*AppGroup, error) {
	ref = strings.TrimSpace(ref)
	if err := checkValue("application", ref); err != nil {
		return nil, err
	}
	for i := range groups {
		if groups[i].ID == ref {
			return &groups[i], nil
		}
	}
	var hit *AppGroup
	for i := range groups {
		if strings.EqualFold(groups[i].Name, ref) {
			if hit != nil {
				return nil, badRequest("more than one application is called %q; use its id", ref)
			}
			hit = &groups[i]
		}
	}
	if hit == nil {
		return nil, errf(http.StatusNotFound, "no application %q", ref)
	}
	return hit, nil
}

// FocusOn narrows a Scope to one Ikhnos application: the services it is made of, and the namespaces and clusters they run
// in. The caller's own limits stay in force - the focus is intersected with them, never widened by them - so a token
// limited to one namespace sees only the part of the application in it, and one that sees none of it gets "not found",
// the same as for an application that does not exist.
//
// The result is for structured queries only. A service is matched by name within the namespaces and clusters of the
// whole application, so when two members share a service name in different namespaces the focus can include a same-named
// service of a namespace that is not a member; telemetry carries no application identity, and this is the price of
// resolving the application at read time. When members lack a namespace or cluster that dimension is left alone.
func (s Scope) FocusOn(g *AppGroup) (Scope, error) {
	names := g.ServiceNames()
	if len(names) == 0 {
		return Scope{}, errf(http.StatusNotFound, "application %q has no services", g.Name)
	}
	if len(names) > maxAppServices {
		return Scope{}, badRequest("application %q has %d service names, more than the %d one filter can carry", g.Name, len(names), maxAppServices)
	}
	out := s
	out.FocusServices = names
	var err error
	if out.FocusNamespaces, err = narrow(s.Namespaces, g, g.Namespaces(), func(m AppMember) string { return m.Namespace }); err != nil {
		return Scope{}, errf(http.StatusNotFound, "no application %q", g.Name)
	}
	if out.FocusClusters, err = narrow(s.Clusters, g, g.Clusters(), func(m AppMember) string { return m.Cluster }); err != nil {
		return Scope{}, errf(http.StatusNotFound, "no application %q", g.Name)
	}
	return out, nil
}

// narrow intersects a limit the caller has with the values the application's members have in one dimension. A member
// without a value there means the application is not confined in that dimension, so the caller's limit stands as it is.
func narrow(have []string, g *AppGroup, want []string, of func(AppMember) string) ([]string, error) {
	for _, m := range g.Members {
		if of(m) == "" {
			return nil, nil // not confined in this dimension: the caller's own limit stands alone
		}
	}
	if len(have) == 0 {
		return slices.Clone(want), nil
	}
	var out []string
	for _, v := range want {
		if slices.Contains(have, v) {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, errf(http.StatusNotFound, "outside the caller's scope")
	}
	return out, nil
}

// VisibleGroups is the applications a caller may know of, each with only the services the caller may see. A caller whose
// access is limited to certain namespaces or clusters sees an application only through the services of it in them, and not
// at all when it has none there: an application it cannot see does not exist for it, and nothing about it (that it exists,
// how many services it has, that two share a name) can be learned from how a lookup fails. A caller with no limit sees
// every application, including one with no services yet.
func VisibleGroups(groups []AppGroup, s Scope) []AppGroup {
	if s.Unrestricted() {
		return groups
	}
	out := make([]AppGroup, 0, len(groups))
	for _, g := range groups {
		kept := g
		kept.Members = nil
		for _, m := range g.Members {
			if s.NamespaceVisible(m.Namespace) && s.ClusterVisible(m.Cluster) {
				kept.Members = append(kept.Members, m)
			}
		}
		if len(kept.Members) > 0 {
			out = append(out, kept)
		}
	}
	return out
}

// AppView is one Ikhnos application as the API lists it: what it is made of, and which signals FUSION has for it.
type AppView struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Services    []AppServiceView `json:"services"`
	// Signals are the signal types any of its services has telemetry for in the range.
	Signals    []string `json:"signals"`
	Namespaces []string `json:"namespaces"`
	Clusters   []string `json:"clusters"`
}

// AppServiceView is one service of an application with the signals its telemetry has in the range.
type AppServiceView struct {
	AppMember
	Signals []string `json:"signals"`
}

// DescribeGroups joins the applications with the services that have telemetry (from Client.Applications). A service is
// matched by the names its telemetry may carry (its name and its aliases), not by namespace or cluster, because the
// service listing is by service.name alone; a service of the same name elsewhere can make a member look like it has
// telemetry that it does not. The result is sorted by name, then id.
func DescribeGroups(groups []AppGroup, withTelemetry []Application) []AppView {
	have := map[string]map[string]bool{}
	for _, a := range withTelemetry {
		have[a.Name] = map[string]bool{}
		for _, sig := range a.Signals {
			have[a.Name][sig] = true
		}
	}
	out := make([]AppView, 0, len(groups))
	for _, g := range groups {
		v := AppView{ID: g.ID, Name: g.Name, Description: g.Description, Services: []AppServiceView{},
			Namespaces: orNone(g.Namespaces()), Clusters: orNone(g.Clusters())}
		all := map[string]bool{}
		for _, m := range g.Members {
			sv := AppServiceView{AppMember: m}
			own := map[string]bool{}
			for _, n := range append([]string{m.Name}, m.Aliases...) {
				for sig := range have[n] {
					own[sig], all[sig] = true, true
				}
			}
			sv.Signals = signalList(own)
			v.Services = append(v.Services, sv)
		}
		v.Signals = signalList(all)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if !strings.EqualFold(out[i].Name, out[j].Name) {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func signalList(set map[string]bool) []string {
	out := []string{}
	for _, sig := range Signals {
		if set[sig] {
			out = append(out, sig)
		}
	}
	return out
}

func orNone(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
