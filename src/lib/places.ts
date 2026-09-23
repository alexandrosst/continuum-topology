// Turning "some evidence" into "a place", from tables instead of from guessing at free text.
//
// Evidence, best first:
//   1. the cloud region table (a provider's region code names a metro area exactly),
//   2. a city name in the label a person typed ("Patras HQ", "thessaloniki-lab"), matched in a table of cities,
//   3. the GeoIP location of the address the cluster reaches the server from.
// Sources that agree (within ~30 km) raise each other's confidence. Every result is only ever a *suggestion*
// a person confirms; nothing here changes the model by itself.
//
// Pure module: the data (the city table, country outlines) is passed in, so it runs the same in the
// browser, where places-data.ts loads it lazily, and in tests.
import { feature } from 'topojson-client'
import { geoContains } from 'd3-geo'
import type { Feature, Geometry } from 'geojson'
import type { GeometryCollection, Topology as TopoTopology } from 'topojson-specification'
import { findCloudRegion, type CloudProvider } from '@/data/cloud-regions'
import { EXONYMS } from '@/data/exonyms'
import { effective } from './effective'
import { providerKey, placeLabel } from './present'
import type { Cluster, Confidence, Evidence, GeoHint, Site, SiteKind, Suggestion } from './types'

export interface City {
  name: string
  /** ASCII spelling when it differs from `name` ("" otherwise). */
  ascii: string
  /** ISO 3166-1 alpha-2. */
  cc: string
  lat: number
  lng: number
  pop: number
}

export interface CountryShape {
  cc: string
  shape: Feature<Geometry>
}

export interface PlaceIndex {
  cities: City[]
  /** folded name -> cities of that name, most populous first. */
  byName: Map<string, City[]>
  /** folded name per city, same order as `cities` (for prefix search). */
  folded: string[]
  countries: CountryShape[]
}

/* ---------- building the index ---------- */

/** Lower case, no accents, single spaces: "São Paulo" and "sao  paulo" are the same place. */
export function fold(s: string): string {
  return s
    .normalize('NFD')
    .replace(/[\u0300-\u036f]/g, '')
    .replace(/ß/g, 'ss')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, ' ')
    .trim()
}

/** Parse the `name|ascii|cc|lat|lng|pop` table written by scripts/build-geodata.py. */
export function parseCities(text: string): City[] {
  const out: City[] = []
  for (const line of text.split('\n')) {
    if (!line || line.startsWith('#')) continue
    const p = line.split('|')
    if (p.length < 6) continue
    const lat = Number(p[3])
    const lng = Number(p[4])
    if (!Number.isFinite(lat) || !Number.isFinite(lng)) continue
    out.push({ name: p[0], ascii: p[1], cc: p[2], lat, lng, pop: Number(p[5]) || 0 })
  }
  return out
}

/** Outlines of the countries (world-atlas topojson, numeric ids) keyed by alpha-2 through `numeric`. */
export function countryShapes(topo: unknown, numeric: Record<string, string>): CountryShape[] {
  const t = topo as TopoTopology<{ countries: GeometryCollection }>
  const fc = feature(t, t.objects.countries)
  const out: CountryShape[] = []
  for (const f of fc.features) {
    const cc = numeric[String(Number(f.id))]
    if (cc) out.push({ cc, shape: f as Feature<Geometry> })
  }
  return out
}

export function buildIndex(cities: City[], countries: CountryShape[] = []): PlaceIndex {
  const byName = new Map<string, City[]>()
  const folded: string[] = []
  for (const c of cities) {
    const names = new Set([fold(c.name), fold(c.ascii || c.name)])
    folded.push(fold(c.ascii || c.name))
    for (const n of names) {
      if (!n) continue
      const l = byName.get(n)
      if (l) l.push(c)
      else byName.set(n, [c])
    }
  }
  // other names people use ("Patras" for Pátra): point them at the table's own entry
  for (const [alias, [name, cc]] of Object.entries(EXONYMS)) {
    const target = (byName.get(fold(name)) ?? []).find((c) => c.cc === cc)
    if (!target) continue
    const l = byName.get(alias)
    if (!l) byName.set(alias, [target])
    else if (!l.includes(target)) l.push(target)
  }
  for (const l of byName.values()) l.sort((a, b) => b.pop - a.pop)
  return { cities, byName, folded, countries }
}

/* ---------- geometry ---------- */

const R = 6371
const rad = (d: number) => (d * Math.PI) / 180

/** Great-circle distance in kilometres. */
export function distanceKm(aLat: number, aLng: number, bLat: number, bLng: number): number {
  const dLat = rad(bLat - aLat)
  const dLng = rad(bLng - aLng)
  const h = Math.sin(dLat / 2) ** 2 + Math.cos(rad(aLat)) * Math.cos(rad(bLat)) * Math.sin(dLng / 2) ** 2
  return 2 * R * Math.asin(Math.min(1, Math.sqrt(h)))
}

/** The closest listed city to a point, if one lies within `maxKm`. Ties on distance go to the bigger city. */
export function nearestCity(idx: PlaceIndex, lat: number, lng: number, maxKm = 150): { city: City; km: number } | undefined {
  let best: City | undefined
  let bestKm = Infinity
  for (const c of idx.cities) {
    // cheap reject before the trigonometry
    if (Math.abs(c.lat - lat) > 3) continue
    const km = distanceKm(lat, lng, c.lat, c.lng)
    // a much bigger city a little further away is the better label ("Piraeus" vs "Athens")
    const score = km - Math.log10(c.pop + 10) * 2
    if (km <= maxKm && score < bestKm) {
      best = c
      bestKm = score
    }
  }
  return best ? { city: best, km: distanceKm(lat, lng, best.lat, best.lng) } : undefined
}

/** The country whose outline contains the point, or undefined at sea / when no outlines are loaded. */
export function countryAt(idx: PlaceIndex, lat: number, lng: number): string | undefined {
  for (const c of idx.countries) if (geoContains(c.shape, [lng, lat])) return c.cc
  return undefined
}

/**
 * Which country coordinates are in, with a tolerance for coasts and borders: a simplified outline can miss a
 * harbour town by a few kilometres, so a city of the same country nearby counts too.
 */
export function countriesNear(idx: PlaceIndex, lat: number, lng: number): string[] {
  const out = new Set<string>()
  const inside = countryAt(idx, lat, lng)
  if (inside) out.add(inside)
  for (const c of idx.cities) {
    if (Math.abs(c.lat - lat) > 0.6) continue
    if (distanceKm(lat, lng, c.lat, c.lng) <= 40) out.add(c.cc)
  }
  return [...out]
}

/** Does this site's country agree with where its coordinates are? Undefined = fine or cannot tell. */
export function siteLocationIssue(idx: PlaceIndex, site: Pick<Site, 'lat' | 'lng' | 'country'>): string | undefined {
  const cc = site.country?.toUpperCase()
  if (!cc || !Number.isFinite(site.lat) || !Number.isFinite(site.lng)) return undefined
  if (idx.countries.length === 0) return undefined
  const near = countriesNear(idx, site.lat, site.lng)
  if (near.length === 0 || near.includes(cc)) return undefined
  const [where] = near
  return `The coordinates are in ${nameOf(where)}, not ${nameOf(cc)}.`
}

const nameOf = (cc: string) => placeLabel({ country: cc }) || cc

/* ---------- searching ---------- */

/** Cities matching what a person typed: exact names first, then names that start with it, biggest first. */
export function findCities(idx: PlaceIndex, query: string, limit = 8, cc?: string): City[] {
  const q = fold(query)
  if (q.length < 2) return []
  const want = (c: City) => !cc || c.cc === cc.toUpperCase()
  const out: City[] = []
  const seen = new Set<City>()
  const push = (c: City) => {
    if (!seen.has(c) && want(c)) {
      seen.add(c)
      out.push(c)
    }
  }
  for (const c of idx.byName.get(q) ?? []) push(c)
  if (out.length < limit) {
    const pre: City[] = []
    for (let i = 0; i < idx.cities.length; i++) if (idx.folded[i].startsWith(q) && want(idx.cities[i]) && !seen.has(idx.cities[i])) pre.push(idx.cities[i])
    pre.sort((a, b) => b.pop - a.pop)
    for (const c of pre) push(c)
  }
  return out.slice(0, limit)
}

/* ---------- candidates ---------- */

export interface PlaceCandidate {
  city: string
  country: string
  lat: number
  lng: number
  confidence: Confidence
  evidence: Evidence[]
  /** A site that already exists there: reuse it instead of creating another. */
  siteId?: string
  /** Extra name for a new site, e.g. the cloud region code. */
  siteName?: string
  /**
   * From GeoHint.estimated: not this cluster's own address, so agreeing with another source is not
   * independent corroboration the way two real signals agreeing is - merging must never lift it above
   * "low", however confident the other source is.
   */
  estimated?: boolean
}

const RANK: Record<Confidence, number> = { high: 3, medium: 2, low: 1 }
const NEAR_KM = 30

/** Words in a label that name a kind of place, not a place. */
const NOT_PLACES = new Set([
  'lab', 'labs', 'hq', 'dc', 'edge', 'site', 'cloud', 'region', 'zone', 'prod', 'production', 'staging', 'dev', 'test', 'main', 'core', 'node', 'nodes', 'cluster', 'central', 'north', 'south', 'east', 'west',
  'on', 'prem', 'onprem', 'office', 'factory', 'plant', 'warehouse', 'store', 'edge', 'far', 'rack', 'room', 'building', 'campus', 'home', 'the', 'of', 'and', 'new', 'old', 'aws', 'gcp', 'azure',
])

/** Every run of one to three words of a label, longest first: "Frankfurt am Main lab" -> "frankfurt am main", ... */
function phrases(label: string): string[] {
  const words = fold(label).split(' ').filter(Boolean)
  const out: string[] = []
  for (let n = Math.min(3, words.length); n >= 1; n--) {
    for (let i = 0; i + n <= words.length; i++) {
      const p = words.slice(i, i + n)
      if (n === 1 && (NOT_PLACES.has(p[0]) || p[0].length < 3 || /^\d+$/.test(p[0]))) continue
      out.push(p.join(' '))
    }
  }
  return out
}

const CLOUDS: Record<string, CloudProvider> = { aws: 'aws', gcp: 'gcp', azure: 'azure', hetzner: 'hetzner', ovh: 'ovh', digitalocean: 'digitalocean', scaleway: 'scaleway' }

const isPublicCloud = (provider: string) => providerKey(provider) in CLOUDS

/** What to look at for one cluster. `labels` are extra region-like strings (node zones, region labels). */
export interface PlaceInput {
  region: string
  provider: string
  labels?: string[]
  geo?: GeoHint
  egressIp?: string
}

/**
 * Ranked places for a cluster, best first. Empty when nothing is known: the caller then asks the person to
 * pick a city. `sites` lets a candidate reuse a site that is already on the map.
 */
export function placementCandidates(idx: PlaceIndex, input: PlaceInput, sites: Site[] = []): PlaceCandidate[] {
  const found: PlaceCandidate[] = []
  const pk = providerKey(input.provider)
  const provider = CLOUDS[pk]
  const labels = [input.region, ...(input.labels ?? [])].filter(Boolean)

  // 1. cloud region table
  for (const l of labels) {
    const r = findCloudRegion(l, provider)
    if (!r) continue
    found.push({
      city: r.city,
      country: r.country,
      lat: r.lat,
      lng: r.lng,
      // a region code that only fits one provider's table is strong even if the provider was not filled in
      confidence: provider ? 'high' : 'medium',
      evidence: [{ signal: `cloud region ${r.code}`, confidence: provider ? 'high' : 'medium', detail: `${r.provider.toUpperCase()} region ${r.code} is in ${r.city}.` }],
      siteName: `${r.code} · ${r.city}`,
    })
    break
  }

  // 2. a city name in the label
  const isCode = (l: string) => !!findCloudRegion(l)
  for (const l of labels) {
    if (isCode(l)) continue
    let hit: City | undefined
    let shown: string | undefined
    for (const p of phrases(l)) {
      const c = idx.byName.get(p)?.[0]
      if (c) {
        hit = c
        // a name from the exonym table is the English one the person used: show that, not the table's spelling
        if (p in EXONYMS) shown = p.replace(/\b[a-z]/g, (x) => x.toUpperCase())
        break
      }
    }
    if (!hit) continue
    found.push({
      city: shown ?? hit.name,
      country: hit.cc,
      lat: hit.lat,
      lng: hit.lng,
      confidence: 'medium',
      evidence: [{ signal: `"${l}" names ${hit.name}`, confidence: 'medium', detail: `Matched the name in the label against a table of cities (${hit.name}, ${nameOf(hit.cc)}). A label is free text, so check it.` }],
    })
    break
  }

  // 3. GeoIP of the connecting address
  const g = input.geo
  if (g && g.country) {
    const cloudy = isPublicCloud(input.provider)
    // g.estimated: the cluster's own connecting address was private (it shares a network with this
    // server, directly or through NAT/CGNAT), so this is this server's own internet connection instead -
    // a fair guess exactly because they share a network, but never better than low, and said plainly.
    const caveat = g.estimated
      ? ' This is an estimate: the cluster\'s own address is private, so this is where this server\'s own internet connection appears to be instead - right if the two share a network, off if they do not.'
      : cloudy
        ? ' The address belongs to a cloud provider, so this can be the provider\'s network rather than the cluster.'
        : ' A cluster behind a VPN or a mobile network may appear elsewhere.'
    if (g.lat !== undefined && g.lng !== undefined && g.level === 'city') {
      const acc = g.accuracyKm ?? 100
      const conf: Confidence = g.estimated ? 'low' : !cloudy && acc <= 50 ? 'medium' : 'low'
      // Prefer the table's spelling of the city when it is the same place.
      const snap = nearestCity(idx, g.lat, g.lng, 25)
      found.push({
        city: g.city || snap?.city.name || g.countryName || g.country,
        country: g.country.toUpperCase(),
        lat: g.lat,
        lng: g.lng,
        confidence: conf,
        estimated: g.estimated,
        evidence: [{ signal: `GeoIP ${input.egressIp ?? 'address'}`, confidence: conf, detail: `${g.database || 'GeoIP database'} places the address in ${[g.city, g.countryName || g.country].filter(Boolean).join(', ')}${g.accuracyKm ? ` (±${g.accuracyKm} km)` : ''}.${caveat}` }],
      })
    } else {
      // country only: centre it on the country's largest listed city, and say so
      const big = idx.cities.filter((c) => c.cc === g.country.toUpperCase()).sort((a, b) => b.pop - a.pop)[0]
      if (big)
        found.push({
          city: big.name,
          country: big.cc,
          lat: big.lat,
          estimated: g.estimated,
          lng: big.lng,
          confidence: 'low',
          evidence: [{ signal: `GeoIP ${input.egressIp ?? 'address'}`, confidence: 'low', detail: `The database only knows the country (${g.countryName || g.country}); ${big.name} is its largest city, used as a starting point.${caveat}` }],
        })
    }
  }

  // Merge candidates that name the same place: agreement is worth more than either alone.
  const merged: PlaceCandidate[] = []
  for (const c of found) {
    const twin = merged.find((m) => m.country === c.country && distanceKm(m.lat, m.lng, c.lat, c.lng) <= NEAR_KM)
    if (!twin) {
      merged.push({ ...c, evidence: [...c.evidence] })
      continue
    }
    twin.evidence.push(...c.evidence)
    if (twin.estimated || c.estimated) {
      // An estimated source isn't independent evidence about this particular cluster (it's a guess
      // about the network, not the cluster), so agreeing with it is not real corroboration - the merge
      // still records both pieces of evidence, but confidence never rises above what a real source on
      // its own would get, however confident that other source is.
      twin.confidence = 'low'
    } else {
      // two independent sources agreeing lift the better one a step (never above "high")
      const best = Math.max(RANK[twin.confidence], RANK[c.confidence])
      twin.confidence = (['low', 'medium', 'high'] as const)[Math.min(2, best)]
    }
    twin.estimated ||= c.estimated
    twin.siteName ??= c.siteName
  }
  // Country of the region table wins over a label that points to another country: keep both, ranked.
  merged.sort((a, b) => RANK[b.confidence] - RANK[a.confidence])

  for (const c of merged) {
    const site = sites.find((s) => Number.isFinite(s.lat) && s.country?.toUpperCase() === c.country && distanceKm(s.lat, s.lng, c.lat, c.lng) <= NEAR_KM)
    if (site) c.siteId = site.id
  }
  return merged
}

/* ---------- from a candidate to a site and a suggestion ---------- */

const slug = (s: string) => fold(s).replace(/ /g, '-')

/** Deterministic, so two browsers that accept the same suggestion cannot create two sites. */
export const siteIdFor = (c: Pick<PlaceCandidate, 'city' | 'country'>) => `site-${slug(c.city)}-${c.country.toLowerCase()}`

export function siteKindFor(cluster: Pick<Cluster, 'provider' | 'tier'>): SiteKind {
  if (isPublicCloud(cluster.provider)) return 'cloud-region'
  return cluster.tier === 'far-edge' || cluster.tier === 'edge' ? 'edge-site' : 'data-center'
}

export function siteFromCandidate(c: PlaceCandidate, cluster: Pick<Cluster, 'provider' | 'tier' | 'orgId'>): Site {
  return {
    id: c.siteId ?? siteIdFor(c),
    orgId: cluster.orgId,
    name: c.siteName ?? c.city,
    kind: siteKindFor(cluster),
    lat: Math.round(c.lat * 1000) / 1000,
    lng: Math.round(c.lng * 1000) / 1000,
    country: c.country,
    city: c.city,
  }
}

export const placeSuggestionId = (clusterId: string) => `sg-place-${clusterId}`

/** Alive clusters that are not on the map (no site, or a site without coordinates). */
const hasPlace = (c: Cluster, sites: Site[]) => {
  const siteId = effective(c).siteId
  const s = siteId ? sites.find((x) => x.id === siteId) : undefined
  return !!s && Number.isFinite(s.lat) && Number.isFinite(s.lng)
}

export interface PlacementModel {
  clusters: Cluster[]
  sites: Site[]
  nodes: { clusterId: string; zone?: string; deletedAt?: string }[]
  agents: { clusterId?: string; connectingIp?: string; connectingGeo?: GeoHint }[]
  suggestions: Suggestion[]
}

/**
 * Open "place this cluster" suggestions, worked out on the fly. They are not stored until a person decides:
 * every browser derives the same ones, so nothing churns between people sharing a workspace. A cluster that
 * was already decided (accepted or dismissed) is not offered again.
 */
export interface PlacementSuggestion extends Suggestion {
  /** "Patras, Greece". */
  place: string
  /** ISO 3166-1 alpha-2, for a flag next to `place`. */
  country: string
  confidence: Confidence
  /**
   * Every signal that went into `confidence`, each with its own rating - not just the winning one.
   * `detail` (on the base Suggestion) is these flattened into one sentence for callers that just want
   * text; this is for a UI that wants to show why, signal by signal (see PlacementHint).
   */
  evidence: Evidence[]
}

export function derivePlacementSuggestions(idx: PlaceIndex, m: PlacementModel): PlacementSuggestion[] {
  const out: PlacementSuggestion[] = []
  for (const c of m.clusters) {
    if (c.deletedAt || hasPlace(c, m.sites)) continue
    const id = placeSuggestionId(c.id)
    if (m.suggestions.some((s) => s.id === id && s.status !== 'open')) continue
    const agent = m.agents.find((a) => a.clusterId === c.id)
    const zones = m.nodes.filter((n) => n.clusterId === c.id && !n.deletedAt && n.zone).map((n) => n.zone as string)
    const cands = placementCandidates(
      idx,
      { region: c.region, provider: c.provider, labels: [c.labels?.['topology.kubernetes.io/region'], ...zones.slice(0, 3)].filter((x): x is string => !!x), geo: agent?.connectingGeo, egressIp: c.egressIp ?? agent?.connectingIp },
      m.sites,
    )
    const top = cands[0]
    if (!top) continue
    const place = placeLabel({ city: top.city, country: top.country })
    const reason = top.evidence.map((e) => e.detail ?? e.signal).join(' ')
    out.push({
      id,
      orgId: c.orgId,
      kind: 'site',
      title: `Put ${c.name} in ${place}`,
      detail: `${top.siteId ? 'An existing site is there. ' : ''}${reason} Confidence: ${top.confidence}.`,
      agentId: c.agentId,
      createdAt: c.lastSeen ?? c.createdAt ?? '',
      status: 'open',
      place,
      country: top.country,
      confidence: top.confidence,
      evidence: top.evidence,
      apply: top.siteId
        ? { type: 'set-cluster-site', clusterId: c.id, siteId: top.siteId }
        : { type: 'place-cluster', clusterId: c.id, site: siteFromCandidate(top, c) },
    })
  }
  return out
}
