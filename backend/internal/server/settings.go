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
	// FlowStaleHours is how long an observed link may go unseen before it is shown as quiet.
	FlowStaleHours int `json:"flowStaleHours"`
	// MeasureSeconds is how often agents that allow it time the path to the addresses they talk to.
	MeasureSeconds int `json:"measureSeconds"`
	// ProbeTargets are addresses an administrator asked a cluster to measure, in addition to the ones its traffic shows.
	ProbeTargets []ProbeTarget `json:"probeTargets"`
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

func DefaultSettings() Settings {
	return Settings{SnapshotMinutes: 5, RetentionDays: 30, MaxHistoryMB: 512, ConsistencyMinutes: 15, StaleAfterBeats: 4, FlowStaleHours: 24,
		MeasureSeconds: 120, ProbeTargets: []ProbeTarget{}, DeciderTimeoutSec: 10, TombstoneRetentionDays: 7, EventRetentionDays: 0}
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
	pick(&s.FlowStaleHours, d.FlowStaleHours)
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
		{"the quiet-link time (hours)", &s.FlowStaleHours, 1, 720},
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
	add("quiet link (h)", a.FlowStaleHours, b.FlowStaleHours)
	add("measure every (s)", a.MeasureSeconds, b.MeasureSeconds)
	add("measurement targets", len(a.ProbeTargets), len(b.ProbeTargets))
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
