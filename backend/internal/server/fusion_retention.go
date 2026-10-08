package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"continuum/internal/fusionapi"
)

// How long FUSION keeps what it has saved, set from the FUSION card without touching Helm.
//
// With the server managing FUSION (the chart's switch.managed) the retention of each store lives in one ConfigMap the
// server owns (see fusion.settingsName in the chart): the stores read it when they start, and an upgrade of the chart
// renders the live values back instead of putting its own defaults over them. Changing a retention is then three steps,
// in this order so that a failure never leaves the settings promising what the disk cannot hold:
//
//  1. grow the store's volume when a bigger one was asked for (a claim can grow but not shrink, and only when its storage
//     class allows expansion - the cluster says so, and the answer is passed on);
//  2. write the new values into the ConfigMap;
//  3. restart the stores whose values changed, so they read them. Their gateway holds what arrives meanwhile.

const (
	retentionMaxDaysMetrics = 1095 // Prometheus: three years
	retentionMaxDaysOther   = 365  // Loki, Tempo
	retentionMaxVolumeGiB   = 16384
	// retentionUsageEvery is how long a measurement of the stores' disk use is reused: the card refreshes often and the
	// numbers move slowly.
	retentionUsageEvery = 20 * time.Second
	// A store keeps its volume below these shares of it so that compaction has room to work (Prometheus is told so with its
	// size limit; Loki and Tempo have none, so the share is a recommendation).
	promVolumeShare  = 0.85
	otherVolumeShare = 0.87
)

type retentionSpec struct {
	component string
	key       string // the ConfigMap key
	hours     bool   // written in hours (Loki, Tempo) rather than days (Prometheus)
	maxDays   int
	share     float64
}

var retentionSpecs = []retentionSpec{
	{"metrics", "prometheus.retention", false, retentionMaxDaysMetrics, promVolumeShare},
	{"logs", "loki.retention", true, retentionMaxDaysOther, otherVolumeShare},
	{"traces", "tempo.retention", true, retentionMaxDaysOther, otherVolumeShare},
}

const oneGiB = int64(1) << 30

const promSizeKey = "prometheus.retentionSize"

// RetentionStore is one store's retention and the volume it lives on.
type RetentionStore struct {
	Component string `json:"component"` // metrics | logs | traces
	Label     string `json:"label"`
	// Value is the setting as written ("15d", "168h"); Days is it in whole days (rounded up) and ExactDays says whether it
	// was a whole number of days (a value set with Helm may be "36h", and the card then says so rather than hide it).
	Value     string `json:"value"`
	Days      int    `json:"days"`
	ExactDays bool   `json:"exactDays"`
	MinDays   int    `json:"minDays"`
	MaxDays   int    `json:"maxDays"`

	// The volume. VolumeKnown is false when the claim could not be read (then none of the rest is meaningful).
	VolumeKnown   bool   `json:"volumeKnown"`
	VolumeBytes   int64  `json:"volumeBytes"`   // what was asked of the cluster
	CapacityBytes int64  `json:"capacityBytes"` // what the cluster has made available so far
	StorageClass  string `json:"storageClass,omitempty"`
	// CanGrow is the cluster's answer to a trial resize (nil: not known); GrowNote says why not when it is false. Checked
	// without changing anything, so the card can say so before anyone presses Save.
	CanGrow  *bool  `json:"canGrow,omitempty"`
	GrowNote string `json:"growNote,omitempty"`
	// SizeNotEnforced is true for storage that does not hold a volume to its size (local-path and other host directories): the
	// size is only what was asked for, and the node's disk is the real limit.
	SizeNotEnforced bool   `json:"sizeNotEnforced,omitempty"`
	Resizing        bool   `json:"resizing,omitempty"`
	ResizeNote      string `json:"resizeNote,omitempty"`

	// Use. UsedSource says where UsedBytes came from: "volume" (the kubelet's own count for the whole volume) or "database"
	// (Prometheus' account of its data). Absent when it was not measured.
	UsedBytes  *int64 `json:"usedBytes,omitempty"`
	UsedSource string `json:"usedSource,omitempty"`
	// BytesPerDay is the growth to plan on and DataDays how many days of data that is based on; absent while there is too
	// little data to tell.
	BytesPerDay *int64   `json:"bytesPerDay,omitempty"`
	DataDays    *float64 `json:"dataDays,omitempty"`
	// Share is the part of the volume the data should stay within; the volume needed for d days is BytesPerDay*d/Share.
	Share float64 `json:"share"`
	// SizeLimitBytes is Prometheus' own cap on its data (retention.size), and SizeLimitDays how many days of data that cap
	// keeps at the present growth. When that is below Days, the time setting is not what limits the data.
	SizeLimitBytes *int64   `json:"sizeLimitBytes,omitempty"`
	SizeLimitDays  *float64 `json:"sizeLimitDays,omitempty"`
}

// RetentionDoc is what the FUSION card shows.
type RetentionDoc struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Message   string `json:"message,omitempty"`
	// Running says whether the stores are up; they are only restarted by a change when they are.
	Running  bool             `json:"running"`
	Stores   []RetentionStore `json:"stores"`
	Warnings []string         `json:"warnings,omitempty"`
}

// RetentionChange is what to change for one store; a nil field is left as it is.
type RetentionChange struct {
	Days *int `json:"days"`
	// VolumeGiB is the size to grow the volume to, in GiB. It cannot be below the present size.
	VolumeGiB *int `json:"volumeGiB"`
}

// RetentionRequest names the stores to change.
type RetentionRequest struct {
	Metrics *RetentionChange `json:"metrics"`
	Logs    *RetentionChange `json:"logs"`
	Traces  *RetentionChange `json:"traces"`
}

func (r RetentionRequest) of(component string) *RetentionChange {
	switch component {
	case "metrics":
		return r.Metrics
	case "logs":
		return r.Logs
	case "traces":
		return r.Traces
	}
	return nil
}

func (f *FusionControl) settingsName() string { return f.Name + "-settings" }

func (f *FusionControl) storeOf(component string) (fusionStore, bool) {
	for _, s := range f.stores() {
		if s.Component == component {
			return s, true
		}
	}
	return fusionStore{}, false
}

var retentionPart = regexp.MustCompile(`^([0-9]+)(ms|s|m|h|d|w|y)`)

// parseRetentionDays reads a retention written the way the stores take it: one or more numbers with units ("15d", "168h",
// "1w2d"), a year being 365 days. It answers in days.
func parseRetentionDays(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	var total time.Duration
	for v != "" {
		m := retentionPart.FindStringSubmatch(v)
		if m == nil {
			return 0, false
		}
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || n > 1<<20 {
			return 0, false
		}
		unit := map[string]time.Duration{"ms": time.Millisecond, "s": time.Second, "m": time.Minute, "h": time.Hour,
			"d": 24 * time.Hour, "w": 7 * 24 * time.Hour, "y": 365 * 24 * time.Hour}[m[2]]
		total += time.Duration(n) * unit
		v = v[len(m[0]):]
	}
	return total.Hours() / 24, true
}

// formatRetention writes days the way the store takes them.
func (sp retentionSpec) format(days int) string {
	if sp.hours {
		return strconv.Itoa(days*24) + "h"
	}
	return strconv.Itoa(days) + "d"
}

var sizeUnits = map[string]float64{"": 1, "B": 1, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40, "PB": 1 << 50,
	"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}

var sizeRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([A-Za-z]*)$`)

// parseRetentionSize reads Prometheus' retention.size ("8500MB", "10GB"); its units are powers of 1024.
func parseRetentionSize(v string) (int64, bool) {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, false
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	u, ok := sizeUnits[m[2]]
	if !ok {
		return 0, false
	}
	return int64(n * u), true
}

// derivedRetentionSize is the size limit the chart gives Prometheus when it is told none: 85% of its volume, in whole MB
// (fusion.prometheusRetentionSize in the chart; the two must agree, since a value that still is this one follows the volume).
func derivedRetentionSize(volumeBytes int64) string {
	mib := volumeBytes >> 20
	return fmt.Sprintf("%dMB", max(1, mib*85/100))
}

// retentionKubeOf is the KubeAPI's retention calls, or why it has none.
func (f *FusionControl) retentionKube() (retentionKube, string) {
	if f == nil || f.Kube == nil {
		return nil, "FUSION cannot be reached from this server, so its retention cannot be set here."
	}
	rk, ok := f.Kube.(retentionKube)
	if !ok {
		return nil, "This server cannot change FUSION's retention."
	}
	return rk, ""
}

type retentionMeasure struct {
	at   time.Time
	prom *fusionapi.PromStorage
	vols map[string]fusionapi.VolumeUse
}

type growCheck struct {
	requested int64
	at        time.Time
	can       *bool
	note      string
}

type retentionCache struct {
	mu    sync.Mutex
	m     retentionMeasure
	grows map[string]growCheck // by claim name
}

// retentionGrowEvery is how long a trial resize's answer is reused: a storage class does not change its mind between two looks
// at the card, and each trial is a call to the cluster.
const retentionGrowEvery = 10 * time.Minute

// provisionersWithoutQuota are storage provisioners that make a plain directory on the node: the claim's size is a label, and
// nothing stops the data from growing past it.
var provisionersWithoutQuota = []string{"local-path", "hostpath"}

func sizeNotEnforced(provisioner string) bool {
	for _, p := range provisionersWithoutQuota {
		if strings.Contains(provisioner, p) {
			return true
		}
	}
	return false
}

// canGrow asks the cluster whether a bigger volume would be accepted (a dry run: nothing is changed) and remembers the answer.
func (f *FusionControl) canGrow(ctx context.Context, rk retentionKube, claimName string, claim KubeClaim) (*bool, string) {
	f.retention.mu.Lock()
	if g, ok := f.retention.grows[claimName]; ok && g.requested == claim.Requested && f.now().Sub(g.at) < retentionGrowEvery {
		f.retention.mu.Unlock()
		return g.can, g.note
	}
	f.retention.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, fusionKubeTimeout)
	defer cancel()
	err := rk.CheckResize(cctx, claimName, claim.Requested+oneGiB)
	var can *bool
	note := ""
	switch {
	case err == nil:
		t := true
		can = &t
	case errors.Is(err, ErrKubeNoResize):
		fl := false
		can, note = &fl, "its storage class does not allow volumes to be grown"
	default:
		return nil, "" // forbidden, unreachable or the like: not known, and not worth remembering
	}
	f.retention.mu.Lock()
	if f.retention.grows == nil {
		f.retention.grows = map[string]growCheck{}
	}
	f.retention.grows[claimName] = growCheck{requested: claim.Requested, at: f.now(), can: can, note: note}
	f.retention.mu.Unlock()
	return can, note
}

// measure reads how much the stores hold. Everything here is a nicety next to the settings: whatever cannot be read is
// simply absent, and the card says "not measured".
func (f *FusionControl) measure(ctx context.Context) retentionMeasure {
	f.retention.mu.Lock()
	defer f.retention.mu.Unlock()
	if !f.retention.m.at.IsZero() && f.now().Sub(f.retention.m.at) < retentionUsageEvery {
		return f.retention.m
	}
	m := retentionMeasure{at: f.now()}
	dc := f.dataClient()
	qctx, cancel := context.WithTimeout(ctx, fusionKubeTimeout)
	defer cancel()
	if ps, err := dc.PrometheusStorage(qctx); err == nil && ps.Bytes > 0 {
		m.prom = &ps
	}
	var claims []string
	for _, sp := range retentionSpecs {
		if s, ok := f.storeOf(sp.component); ok {
			claims = append(claims, storeClaim(s))
		}
	}
	if vols, err := dc.VolumeUsage(qctx, fusionapi.AllSignals(), claims); err == nil {
		m.vols = vols
	}
	f.retention.m = m
	return m
}

func (f *FusionControl) forgetMeasure() {
	f.retention.mu.Lock()
	f.retention.m = retentionMeasure{}
	f.retention.mu.Unlock()
}

func unavailableRetention(reason, msg string) RetentionDoc {
	return RetentionDoc{Reason: reason, Message: msg, Stores: []RetentionStore{}}
}

// Retention reads every store's retention, volume and use.
func (f *FusionControl) Retention(ctx context.Context, c *Core) RetentionDoc {
	if f != nil && f.Org != "" && c.OrgID != f.Org {
		return unavailableRetention("other-org", "FUSION is shared by everything that sends to this server, so its retention is set from the server's main organisation.")
	}
	st := f.Status(ctx)
	if !st.Available {
		return unavailableRetention(st.Reason, st.Message)
	}
	rk, why := f.retentionKube()
	if rk == nil {
		return unavailableRetention("unsupported", why)
	}
	settings, err := rk.Settings(ctx, f.settingsName())
	switch {
	case errors.Is(err, ErrKubeNotFound):
		return unavailableRetention("unmanaged", "This FUSION is not managed by the server (its chart was installed without switch.managed), so its retention is set with Helm values: prometheus.retention, loki.retention and tempo.retention.")
	case errors.Is(err, ErrKubeForbidden):
		return unavailableRetention("forbidden", "This server is not allowed to read FUSION's settings. Upgrade the server chart so its Role includes them.")
	case err != nil:
		return unavailableRetention("error", "FUSION's settings could not be read: "+err.Error())
	}
	doc := RetentionDoc{Available: true, Stores: []RetentionStore{}}
	for _, comp := range st.Components {
		if comp.Component == "metrics" {
			doc.Running = comp.Desired > 0
		}
	}
	var m retentionMeasure
	if doc.Running {
		m = f.measure(ctx)
	}
	for _, sp := range retentionSpecs {
		s, ok := f.storeOf(sp.component)
		if !ok {
			continue
		}
		rs := RetentionStore{Component: sp.component, Label: s.Label, Value: settings[sp.key], MinDays: 1, MaxDays: sp.maxDays, Share: sp.share}
		if d, ok := parseRetentionDays(rs.Value); ok {
			rs.Days, rs.ExactDays = int(math.Ceil(d-1e-9)), math.Abs(d-math.Round(d)) < 1e-9
		}
		var claim KubeClaim
		if cl, err := rk.Claim(ctx, storeClaim(s)); err == nil {
			claim = cl
			rs.VolumeKnown, rs.VolumeBytes, rs.CapacityBytes = true, cl.Requested, cl.Capacity
			rs.StorageClass, rs.Resizing, rs.ResizeNote = cl.StorageClass, cl.Resizing || (cl.Capacity > 0 && cl.Capacity < cl.Requested), cl.ResizeNote
			rs.SizeNotEnforced = sizeNotEnforced(cl.Provisioner)
			if cl.Phase == "Bound" {
				rs.CanGrow, rs.GrowNote = f.canGrow(ctx, rk, storeClaim(s), cl)
			}
		}
		f.fillUse(&rs, sp, claim, m, settings)
		doc.Stores = append(doc.Stores, rs)
	}
	return doc
}

// fillUse puts the measured use, the growth to plan on and the size-limit check on a store.
func (f *FusionControl) fillUse(rs *RetentionStore, sp retentionSpec, claim KubeClaim, m retentionMeasure, settings map[string]string) {
	s, _ := f.storeOf(sp.component)
	if v, ok := m.vols[storeClaim(s)]; ok && (v.Namespace == "" || v.Namespace == f.Namespace) {
		used := v.Used
		rs.UsedBytes, rs.UsedSource = &used, "volume"
	}
	day := 24 * time.Hour
	var span time.Duration // how long the data on disk has been accumulating
	var data int64         // how much of the disk it is
	if sp.component == "metrics" && m.prom != nil {
		if rs.UsedBytes == nil {
			used := m.prom.Bytes
			rs.UsedBytes, rs.UsedSource = &used, "database"
		}
		data = m.prom.Bytes
		if !m.prom.Oldest.IsZero() {
			span = f.now().Sub(m.prom.Oldest)
		}
	} else if rs.UsedBytes != nil && !claim.Created.IsZero() {
		data, span = *rs.UsedBytes, f.now().Sub(claim.Created)
		// Once the store has run longer than it keeps data, what is on disk is a full retention's worth.
		if rs.Days > 0 && span > time.Duration(rs.Days)*day {
			span = time.Duration(rs.Days) * day
		}
	}
	// Under half a day of data says nothing about a day's worth.
	if data > 0 && span >= 12*time.Hour {
		days := span.Hours() / 24
		perDay := int64(float64(data) / days)
		rs.BytesPerDay, rs.DataDays = &perDay, &days
	}
	if sp.component == "metrics" {
		if sz, ok := parseRetentionSize(settings[promSizeKey]); ok && sz > 0 {
			rs.SizeLimitBytes = &sz
			if rs.BytesPerDay != nil && *rs.BytesPerDay > 0 {
				d := float64(sz) / float64(*rs.BytesPerDay)
				rs.SizeLimitDays = &d
			}
		}
	}
}

// retentionPlan is one store's validated change.
type retentionPlan struct {
	spec      retentionSpec
	store     fusionStore
	newValue  string // "" = unchanged
	grow      int64  // new volume size in bytes; 0 = unchanged
	claim     KubeClaim
	sizeLimit string // new Prometheus size limit; "" = unchanged
}

func (p retentionPlan) restarts() bool { return p.newValue != "" || p.sizeLimit != "" }

// SetRetention changes retention and volume sizes as the request says and answers with the state that results.
func (f *FusionControl) SetRetention(ctx context.Context, c *Core, actor string, req RetentionRequest) (RetentionDoc, error) {
	if f == nil {
		return RetentionDoc{}, errf(KindConflict, "FUSION is not available here")
	}
	f.switchMu.Lock()
	defer f.switchMu.Unlock()
	if f.Org != "" && c.OrgID != f.Org {
		return RetentionDoc{}, errf(KindForbidden, "FUSION belongs to this server's main organisation, which is the only one that can change its retention")
	}
	cur := f.Retention(ctx, c)
	if !cur.Available {
		return cur, errf(KindConflict, "%s", cur.Message)
	}
	rk, _ := f.retentionKube()
	settings, err := rk.Settings(ctx, f.settingsName())
	if err != nil {
		return RetentionDoc{}, kubeFail("read FUSION's settings", err)
	}

	// 1. Check everything before changing anything.
	var plans []retentionPlan
	asked := false
	for _, sp := range retentionSpecs {
		ch := req.of(sp.component)
		if ch == nil || (ch.Days == nil && ch.VolumeGiB == nil) {
			continue
		}
		asked = true
		s, ok := f.storeOf(sp.component)
		if !ok {
			continue
		}
		p := retentionPlan{spec: sp, store: s}
		if ch.Days != nil {
			if *ch.Days < 1 || *ch.Days > sp.maxDays {
				return RetentionDoc{}, errf(KindInvalid, "%s keeps between 1 and %d days", s.Label, sp.maxDays)
			}
			if v := sp.format(*ch.Days); v != settings[sp.key] {
				p.newValue = v
			}
		}
		if ch.VolumeGiB != nil || p.newValue != "" {
			claim, err := rk.Claim(ctx, storeClaim(s))
			if err != nil {
				return RetentionDoc{}, kubeFail("read "+s.Label+"'s volume", err)
			}
			p.claim = claim
		}
		if ch.VolumeGiB != nil {
			if *ch.VolumeGiB < 1 || *ch.VolumeGiB > retentionMaxVolumeGiB {
				return RetentionDoc{}, errf(KindInvalid, "a volume is between 1 and %d GiB", retentionMaxVolumeGiB)
			}
			want := int64(*ch.VolumeGiB) << 30
			switch {
			case want < p.claim.Requested:
				return RetentionDoc{}, errf(KindInvalid, "%s's volume is %s and a volume can be grown but not made smaller", s.Label, humanGiB(p.claim.Requested))
			case want > p.claim.Requested:
				if p.claim.Resizing || p.claim.Capacity < p.claim.Requested {
					return RetentionDoc{}, errf(KindConflict, "%s's volume is still being grown from an earlier change. Wait for it to finish, then grow it again", s.Label)
				}
				p.grow = want
				// Ask the cluster now, before anything is changed, so that a volume that cannot grow stops the whole request and
				// not just the part after the first store that did.
				if err := rk.CheckResize(ctx, storeClaim(s), want); errors.Is(err, ErrKubeNoResize) {
					return RetentionDoc{}, noResizeError(s, p.claim, "")
				}
			}
		}
		// A Prometheus size limit that was only ever the default follows its volume.
		if sp.component == "metrics" && p.grow > 0 {
			if old, ok := settings[promSizeKey]; ok && old == derivedRetentionSize(p.claim.Requested) {
				p.sizeLimit = derivedRetentionSize(p.grow)
			}
		}
		if p.newValue != "" || p.grow > 0 {
			plans = append(plans, p)
		}
	}
	if !asked {
		return RetentionDoc{}, errf(KindInvalid, "say which store's days or volume to change")
	}
	if len(plans) == 0 {
		return cur, nil // everything asked for is already so
	}

	// 2. Grow volumes. A claim cannot shrink back, so what was grown stays grown if a later step fails; the message says so.
	f.forgetMeasure()
	var grown []string
	for _, p := range plans {
		if p.grow == 0 {
			continue
		}
		if err := rk.ResizeClaim(ctx, storeClaim(p.store), p.grow); err != nil {
			f.invalidate()
			done := ""
			if len(grown) > 0 {
				done = fmt.Sprintf(" (%s was already grown and stays so; no retention was changed)", strings.Join(grown, ", "))
			}
			if errors.Is(err, ErrKubeNoResize) {
				return RetentionDoc{}, noResizeError(p.store, p.claim, done)
			}
			e := kubeFail("grow "+p.store.Label+"'s volume", err)
			return RetentionDoc{}, errf(KindConflict, "%v%s", e, done)
		}
		grown = append(grown, p.store.Label)
	}

	// 3. Save the settings.
	patch := map[string]string{}
	for _, p := range plans {
		if p.newValue != "" {
			patch[p.spec.key] = p.newValue
		}
		if p.sizeLimit != "" {
			patch[promSizeKey] = p.sizeLimit
		}
	}
	if len(patch) > 0 {
		if err := rk.PatchSettings(ctx, f.settingsName(), patch); err != nil {
			f.invalidate()
			e := kubeFail("save FUSION's retention", err)
			if len(grown) > 0 {
				return RetentionDoc{}, errf(KindConflict, "%v (%s was already grown and stays so)", e, strings.Join(grown, ", "))
			}
			return RetentionDoc{}, e
		}
	}

	// 4. Restart the stores whose settings changed, if they are running. Their gateway holds what arrives meanwhile.
	var warnings []string
	var restarted []string
	if cur.Running {
		for _, p := range plans {
			if !p.restarts() {
				continue
			}
			if err := rk.RestartPod(ctx, storePod(p.store)); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s could not be restarted (%v). The new retention is saved and applies when it next restarts.", p.store.Label, err))
				continue
			}
			restarted = append(restarted, p.store.Label)
		}
	}

	var parts []string
	for _, p := range plans {
		if p.newValue != "" {
			parts = append(parts, fmt.Sprintf("%s retention %s -> %s", p.store.Label, cmpOr(settings[p.spec.key], "unset"), p.newValue))
		}
		if p.grow > 0 {
			parts = append(parts, fmt.Sprintf("%s volume %s -> %s", p.store.Label, humanGiB(p.claim.Requested), humanGiB(p.grow)))
		}
		if p.sizeLimit != "" {
			parts = append(parts, fmt.Sprintf("Prometheus size limit %s -> %s", settings[promSizeKey], p.sizeLimit))
		}
	}
	detail := strings.Join(parts, "; ")
	if len(restarted) > 0 {
		detail += "; restarted " + strings.Join(restarted, ", ")
	}
	c.audit(ctx, actor, "fusion-retention-changed", "fusion", f.Name, detail)

	f.invalidate()
	f.forgetMeasure()
	out := f.Retention(ctx, c)
	out.Warnings = append(out.Warnings, warnings...)
	return out, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// humanGiB writes a size in bytes as GiB (or MiB below one), for messages and the audit trail.
func humanGiB(b int64) string {
	if b < 1<<30 {
		return fmt.Sprintf("%d MiB", b>>20)
	}
	g := float64(b) / (1 << 30)
	if g == math.Trunc(g) {
		return fmt.Sprintf("%d GiB", int64(g))
	}
	return fmt.Sprintf("%.1f GiB", g)
}

// noResizeError is the cluster refusing to grow a volume, in words that say what to do.
func noResizeError(s fusionStore, claim KubeClaim, done string) error {
	class := cmp.Or(claim.StorageClass, "default")
	return errf(KindConflict, "%s's volume cannot be grown: its storage class (%s) does not allow volume expansion. Nothing was changed. Keep the volume as it is and lower the days to fit it, set allowVolumeExpansion: true on the class, or use a class that can grow%s", s.Label, class, done)
}
