package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"continuum/internal/measure"
)

// Settings are the knobs an administrator can turn without redeploying anything. They are stored
// per organization; anything missing or out of range falls back to a default.
type Settings struct {
	// SnapshotMinutes is how often the topology is recorded (it is also recorded right after a change).
	SnapshotMinutes int `json:"snapshotMinutes"`
	// RetentionDays is how long history is kept, thinned as it ages; MaxHistoryMB caps its size.
	RetentionDays int `json:"retentionDays"`
	MaxHistoryMB  int `json:"maxHistoryMb"`
	// TombstoneRetentionDays is how long a record that disappeared from an agent's report is still shown as gone
	// before it is removed for good.
	TombstoneRetentionDays int `json:"tombstoneRetentionDays"`
	// EventRetentionDays is how long the event/drift log is kept before old entries are pruned. Zero (the default)
	// means events are kept forever; this never touches the audit trail, which is never pruned here.
	EventRetentionDays int `json:"eventRetentionDays"`
	// ConsistencyMinutes is how often each agent re-sends its full picture so the server can check its own.
	ConsistencyMinutes int `json:"consistencyMinutes"`
	// StaleAfterBeats is how many missed heartbeats make an agent's records stale.
	StaleAfterBeats int `json:"staleAfterBeats"`
	// FlowStaleSeconds is how long an observed link may go unseen before it is shown as quiet. Stored in
	// seconds (not hours) so an administrator who wants near-real-time feedback after a deliberate change
	// (a migration, a decommission) can set it in seconds or minutes instead of waiting out a coarser unit;
	// the UI offers a unit picker over this same underlying value.
	FlowStaleSeconds int `json:"flowStaleSeconds"`
	// MeasureSeconds is how often agents that allow it time the path to the addresses they talk to.
	MeasureSeconds int `json:"measureSeconds"`
	// ProbeTargets are addresses an administrator asked a cluster to measure, in addition to the ones its traffic shows.
	ProbeTargets []ProbeTarget `json:"probeTargets"`
	// QuickStartBackends are observability backends (Jaeger, Prometheus) an administrator generated a
	// quick-start install command for - see QuickStartBackend.
	QuickStartBackends []QuickStartBackend `json:"quickStartBackends"`
	// DeciderURL, when set, is an HTTP endpoint that also gets to recommend placements (see docs).
	DeciderURL        string `json:"deciderUrl"`
	DeciderName       string `json:"deciderName"`
	DeciderTimeoutSec int    `json:"deciderTimeoutSec"`
	// DeciderSecret, when set, signs every request this server sends to DeciderURL (HMAC-SHA256 over the request
	// timestamp and body; see decideSign.go), so the decider can tell a call from this server apart from anyone
	// who guesses or intercepts its address. Unlike DeciderURL it is never sent back to a client once saved, even
	// to an administrator: the API only ever says whether one is set (SettingsDoc.DeciderSecretSet), the same way
	// a password or API key field would. See putSettings for how a client sets, keeps or clears it.
	DeciderSecret string `json:"deciderSecret,omitempty"`
	// ImageRegistry, ImageTag and ImageDigest say where the agent image (and the chart) come from in this
	// organisation's install commands. They are per organisation on purpose: any account can create an organisation,
	// so a server-wide value editable by "an administrator" would let a stranger redirect everyone's installs; here
	// each organisation only decides what its own clusters pull. Empty means "use the server's --image-* flags", and
	// if those are empty too, the chart's own defaults. See ImageConfig for the rules.
	ImageRegistry string `json:"imageRegistry"`
	ImageTag      string `json:"imageTag"`
	ImageDigest   string `json:"imageDigest"`
}

// ProbeTarget is a place an administrator wants measured from one cluster.
type ProbeTarget struct {
	ID        string `json:"id"`
	ClusterID string `json:"clusterId"`
	Label     string `json:"label"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
}

// quickStartBackendModality is which modality each quick-start kind is fixed to - a physical fact about
// the backend (Jaeger only ingests traces; this app's Prometheus quick-start only turns on its OTLP
// metrics receiver), not something a person picks independently of Kind.
var quickStartBackendModality = map[string]string{"jaeger": "traces", "prometheus": "metrics"}

// QuickStartBackend records that this organisation generated (or is tracking) an install command for a
// quick-start observability backend, so the telemetry destination picker can offer it and remember it's
// already set up, and so "open this tool" has somewhere to go once a person tells us where they exposed
// it. The server never deploys or dials any of this itself - same one-way trust model as every other
// install command in this app (ConnectClusterWizard, regional operators): this is a note a person chose
// to keep, not something the server depends on operationally.
type QuickStartBackend struct {
	ID string `json:"id"`
	// Kind fixes Modality (see quickStartBackendModality) - "jaeger" or "prometheus" today, see
	// quickStartBackends.ts for what each one's install command actually does.
	Kind string `json:"kind"`
	// Modality must match Kind's own fixed modality; kept explicit (rather than derived server-side only)
	// so a client can filter/display without a copy of quickStartBackendModality of its own.
	Modality string `json:"modality"`
	// Namespace is what the generated install command targets - also most of what ToolURL/the chart's own
	// Service DNS name depend on, so it's kept even though the server never acts on it directly.
	Namespace string `json:"namespace"`
	// Retention is free text echoed into the install command (e.g. "72h", "15d") - never parsed or
	// enforced here, since what each backend's own retention flag accepts differs by backend.
	Retention string `json:"retention"`
	// ToolURL, once a person has the backend reachable (port-forward, ingress, ...) and says so, is what
	// "open this tool" opens in a new tab. Never dialled by the server.
	ToolURL string `json:"toolUrl,omitempty"`
	Label   string `json:"label"`
}

func DefaultSettings() Settings {
	return Settings{SnapshotMinutes: 5, RetentionDays: 30, MaxHistoryMB: 512, ConsistencyMinutes: 15, StaleAfterBeats: 4, MeasureSeconds: 120, ProbeTargets: []ProbeTarget{}, QuickStartBackends: []QuickStartBackend{}, DeciderTimeoutSec: 10, TombstoneRetentionDays: 7, EventRetentionDays: 0, FlowStaleSeconds: 300}
}

func inRange(name string, v *int, lo, hi int) error {
	if *v < lo || *v > hi {
		return fmt.Errorf("%s must be between %d and %d", name, lo, hi)
	}
	return nil
}

// Normalize checks a settings document and fills defaults for anything left at zero, applying the default
// decider policy (public addresses only).
func (s Settings) Normalize() (Settings, error) { return s.NormalizeFor(context.Background(), nil) }

// NormalizeFor is Normalize under an operator's decider policy (see DeciderPolicy; nil is the default).
func (s Settings) NormalizeFor(ctx context.Context, dp *DeciderPolicy) (Settings, error) {
	d := DefaultSettings()
	pick := func(v *int, def int) {
		if *v == 0 {
			*v = def
		}
	}
	pick(&s.SnapshotMinutes, d.SnapshotMinutes)
	pick(&s.RetentionDays, d.RetentionDays)
	pick(&s.MaxHistoryMB, d.MaxHistoryMB)
	pick(&s.ConsistencyMinutes, d.ConsistencyMinutes)
	pick(&s.StaleAfterBeats, d.StaleAfterBeats)
	pick(&s.FlowStaleSeconds, d.FlowStaleSeconds)
	pick(&s.MeasureSeconds, d.MeasureSeconds)
	pick(&s.DeciderTimeoutSec, d.DeciderTimeoutSec)
	pick(&s.TombstoneRetentionDays, d.TombstoneRetentionDays)
	// EventRetentionDays is not picked: zero is its own default (keep forever), not a placeholder for one.
	for _, c := range []struct {
		n      string
		v      *int
		lo, hi int
	}{
		{"the snapshot interval (minutes)", &s.SnapshotMinutes, 1, 1440},
		{"history retention (days)", &s.RetentionDays, 1, 365},
		{"the history size limit (MB)", &s.MaxHistoryMB, 16, 8192},
		{"the consistency check interval (minutes)", &s.ConsistencyMinutes, 1, 240},
		{"the number of missed heartbeats", &s.StaleAfterBeats, 2, 20},
		{"the quiet-link time (seconds)", &s.FlowStaleSeconds, 1, 30 * 24 * 3600},
		{"the measurement interval (seconds)", &s.MeasureSeconds, int(measure.MinInterval / time.Second), 3600},
		{"the decider timeout (seconds)", &s.DeciderTimeoutSec, 1, 25},
		{"tombstone retention (days)", &s.TombstoneRetentionDays, 1, 90},
	} {
		if err := inRange(c.n, c.v, c.lo, c.hi); err != nil {
			return s, err
		}
	}
	// EventRetentionDays is opt-in: 0 keeps events forever; any other value must be a sane range.
	if s.EventRetentionDays != 0 {
		if err := inRange("event retention (days)", &s.EventRetentionDays, 7, 3650); err != nil {
			return s, err
		}
	}
	if len(s.ProbeTargets) > 50 {
		return s, fmt.Errorf("at most 50 measurement targets")
	}
	seen := map[string]bool{}
	out := make([]ProbeTarget, 0, len(s.ProbeTargets))
	for _, t := range s.ProbeTargets {
		t.Host, t.Label = strings.TrimSpace(t.Host), strings.TrimSpace(t.Label)
		if t.ClusterID == "" {
			return s, fmt.Errorf("choose which cluster measures %q", t.Host)
		}
		if err := measure.CheckTarget(t.Host, t.Port); err != nil {
			return s, fmt.Errorf("measurement target %q: %v", t.Host, err)
		}
		if len(t.Label) > 80 {
			return s, fmt.Errorf("a measurement label is at most 80 characters")
		}
		if t.ID == "" {
			t.ID = "pt-" + newTokenID()
		}
		if seen[t.ID] {
			return s, fmt.Errorf("duplicate measurement target id")
		}
		seen[t.ID] = true
		out = append(out, t)
	}
	s.ProbeTargets = out
	if len(s.QuickStartBackends) > 20 {
		return s, fmt.Errorf("at most 20 quick-start backends")
	}
	qsSeen := map[string]bool{}
	qsOut := make([]QuickStartBackend, 0, len(s.QuickStartBackends))
	for _, b := range s.QuickStartBackends {
		wantModality, ok := quickStartBackendModality[b.Kind]
		if !ok {
			return s, fmt.Errorf(`quick-start backend kind must be "jaeger" or "prometheus"`)
		}
		if b.Modality != wantModality {
			return s, fmt.Errorf("a %q quick-start backend's modality must be %q", b.Kind, wantModality)
		}
		b.Namespace, b.Retention, b.Label = strings.TrimSpace(b.Namespace), strings.TrimSpace(b.Retention), strings.TrimSpace(b.Label)
		if b.Namespace == "" {
			return s, fmt.Errorf("a quick-start backend needs a namespace")
		}
		if !nsNameRe.MatchString(b.Namespace) {
			return s, fmt.Errorf("%q is not a valid Kubernetes namespace name", b.Namespace)
		}
		if b.Retention == "" {
			return s, fmt.Errorf("a quick-start backend needs a retention value")
		}
		if len(b.Retention) > 20 {
			return s, fmt.Errorf("a quick-start backend's retention is at most 20 characters")
		}
		if len(b.Label) > 80 {
			return s, fmt.Errorf("a quick-start backend's label is at most 80 characters")
		}
		if b.ToolURL = strings.TrimSpace(b.ToolURL); b.ToolURL != "" {
			if len(b.ToolURL) > 2048 {
				return s, fmt.Errorf("a quick-start backend's tool URL is at most 2048 characters")
			}
			u, err := url.Parse(b.ToolURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return s, fmt.Errorf("%q is not an http(s) URL", b.ToolURL)
			}
		}
		if b.ID == "" {
			b.ID = "qsb-" + newTokenID()
		}
		if qsSeen[b.ID] {
			return s, fmt.Errorf("duplicate quick-start backend id")
		}
		qsSeen[b.ID] = true
		qsOut = append(qsOut, b)
	}
	s.QuickStartBackends = qsOut
	s.DeciderName = strings.TrimSpace(s.DeciderName)
	if len(s.DeciderName) > 60 {
		return s, fmt.Errorf("the decider's name is at most 60 characters")
	}
	if n := len(s.DeciderSecret); n > 0 && (n < 16 || n > 200) {
		return s, fmt.Errorf("the decider secret must be between 16 and 200 characters")
	}
	img, err := ImageConfig{s.ImageRegistry, s.ImageTag, s.ImageDigest}.Normalize()
	if err != nil {
		return s, err
	}
	s.ImageRegistry, s.ImageTag, s.ImageDigest = img.Registry, img.Tag, img.Digest
	if s.DeciderURL = strings.TrimSpace(s.DeciderURL); s.DeciderURL != "" {
		if len(s.DeciderURL) > 2048 {
			return s, fmt.Errorf("the decider address is at most 2048 characters")
		}
		if err := dp.CheckURL(ctx, s.DeciderURL); err != nil {
			return s, err
		}
	}
	return s, nil
}

type settingsHolder struct {
	mu sync.RWMutex
	s  Settings
	ok bool
}

// Settings returns the current settings (defaults until something was saved).
func (c *Core) Settings() Settings {
	c.settings.mu.RLock()
	defer c.settings.mu.RUnlock()
	if !c.settings.ok {
		return DefaultSettings()
	}
	return c.settings.s
}

// LoadSettings reads the stored settings at startup. Unreadable or invalid stored values fall back
// to the defaults rather than stopping the server.
func (c *Core) LoadSettings(ctx context.Context) {
	data, err := c.Store.GetSettings(ctx, c.OrgID)
	if err != nil || data == nil {
		return
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		c.Log.Warn("stored settings are unreadable; using defaults", "err", err)
		return
	}
	// A decider saved by an earlier version (or under a wider allow-list) may no longer be permitted. Switch
	// only the decider off, loudly, and keep everything else; the dial-time check would refuse it anyway.
	if u := strings.TrimSpace(s.DeciderURL); u != "" {
		if err := c.Decider.CheckURL(ctx, u); err != nil {
			c.Log.Warn("the stored external decider is switched off: "+err.Error()+". To keep using it, list its address range in --decider-allow-cidrs and save the setting again", "org", c.OrgID)
			s.DeciderURL = ""
		}
	}
	n, err := s.NormalizeFor(ctx, c.Decider)
	if err != nil {
		c.Log.Warn("stored settings are invalid; using defaults", "err", err)
		return
	}
	c.settings.mu.Lock()
	c.settings.s, c.settings.ok = n, true
	c.settings.mu.Unlock()
}

// SaveSettings validates, stores and applies new settings, and records who changed what.
func (c *Core) SaveSettings(ctx context.Context, actor string, s Settings) (Settings, error) {
	n, err := s.NormalizeFor(ctx, c.Decider)
	if err != nil {
		return Settings{}, errf(KindInvalid, "%v", err)
	}
	old := c.Settings()
	data, _ := json.Marshal(n)
	apply := func() error {
		if err := c.Store.PutSettings(ctx, c.OrgID, data, c.Now()); err != nil {
			return err
		}
		c.settings.mu.Lock()
		c.settings.s, c.settings.ok = n, true
		c.settings.mu.Unlock()
		return nil
	}
	// A change to the settings (above all the decider, which the server will call) is recorded first, and
	// does not happen if it cannot be recorded.
	if d := settingsDiff(old, n); d != "" {
		if err := c.audited(ctx, actor, "settings-changed", "settings", c.OrgID, d, apply); err != nil {
			return Settings{}, err
		}
	} else if err := apply(); err != nil {
		return Settings{}, err
	}
	if c.OnSettings != nil {
		c.OnSettings(n)
	}
	return n, nil
}

func settingsDiff(a, b Settings) string {
	var d []string
	add := func(name string, x, y any) {
		if fmt.Sprint(x) != fmt.Sprint(y) {
			d = append(d, fmt.Sprintf("%s %v → %v", name, x, y))
		}
	}
	add("snapshot every (min)", a.SnapshotMinutes, b.SnapshotMinutes)
	add("retention (days)", a.RetentionDays, b.RetentionDays)
	add("history size limit (MB)", a.MaxHistoryMB, b.MaxHistoryMB)
	add("tombstone retention (days)", a.TombstoneRetentionDays, b.TombstoneRetentionDays)
	add("event retention (days)", a.EventRetentionDays, b.EventRetentionDays)
	add("consistency check (min)", a.ConsistencyMinutes, b.ConsistencyMinutes)
	add("stale after (beats)", a.StaleAfterBeats, b.StaleAfterBeats)
	add("quiet link (s)", a.FlowStaleSeconds, b.FlowStaleSeconds)
	add("measure every (s)", a.MeasureSeconds, b.MeasureSeconds)
	add("measurement targets", len(a.ProbeTargets), len(b.ProbeTargets))
	add("quick-start backends", len(a.QuickStartBackends), len(b.QuickStartBackends))
	if a.DeciderURL != b.DeciderURL {
		if b.DeciderURL == "" {
			d = append(d, "external decider removed")
		} else {
			// Only the host: the rest of the address may carry a secret.
			host := ""
			if u, err := url.Parse(b.DeciderURL); err == nil {
				host = u.Hostname()
			}
			d = append(d, "external decider set ("+host+")")
		}
	}
	if a.DeciderSecret != b.DeciderSecret {
		switch {
		case b.DeciderSecret == "":
			d = append(d, "decider secret removed")
		case a.DeciderSecret == "":
			d = append(d, "decider secret set")
		default:
			d = append(d, "decider secret changed")
		}
	}
	// Which images clusters are told to pull is a supply-chain decision, so every change is recorded with both values.
	shown := func(v string) string {
		if v == "" {
			return "(none)"
		}
		return v
	}
	for _, c := range [][3]string{{"image registry", a.ImageRegistry, b.ImageRegistry}, {"image tag", a.ImageTag, b.ImageTag}, {"image digest", a.ImageDigest, b.ImageDigest}} {
		if c[1] != c[2] {
			d = append(d, fmt.Sprintf("%s %s → %s", c[0], shown(c[1]), shown(c[2])))
		}
	}
	return strings.Join(d, "; ")
}
