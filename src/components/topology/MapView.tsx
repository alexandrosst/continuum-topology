import { geoContains, geoGraticule10, geoNaturalEarth1, geoPath } from 'd3-geo'
import type { GeoPermissibleObjects } from 'd3-geo'
import type { Feature, FeatureCollection, Geometry } from 'geojson'
import { Home, Minus, Plus } from 'lucide-react'
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import { Link } from 'react-router-dom'
import { feature } from 'topojson-client'
import type { GeometryCollection, Topology } from 'topojson-specification'
import type { Selection } from '@/components/topology/Inspector'
import { EmptyState, Pill, StatusDot, TierBadge } from '@/components/ui/primitives'
import { buildMapSites, type SiteConnection, clampPan, dominantTier, groupByProximity, groupLabel, siteConnections, unplacedClusters, type MapSite } from '@/lib/geo'
import { applyFilter, filterActive, knownOnly, type Filter } from '@/lib/filter'
import { lossBand, rttLabel, clusterLoad, type ClusterLoad } from '@/lib/metrics'
import { bytesPerSec } from '@/lib/observed'
import { placeLabel } from '@/lib/present'
import { LoadRow, peakLoad } from '@/components/topology/Load'
import { usePlacementSuggestions } from '@/lib/usePlacement'
import PlacementHint from '@/components/PlacementHint'
import { STATUS_COLOR, TIER_COLOR, TIERS, type Cluster } from '@/lib/types'
import { usePaths, useTopology } from '@/store/topology'

// The drawing surface is a fixed 1000x520 box; the SVG scales it to the available space.
const W = 1000
const H = 520
const MAX_K = 48
const MERGE_PX = 34 // dots closer than this on screen are drawn as one

type Countries = FeatureCollection<Geometry, { name?: string }>
type View = { k: number; x: number; y: number }

// Coastlines are loaded on demand: the coarse set is enough for the world view, and the detailed one is
// fetched only when someone zooms in far enough to need it.
let coarse: Promise<Countries> | undefined
let fine: Promise<Countries> | undefined
const toCountries = (t: unknown): Countries => {
  const topo = t as Topology<{ countries: GeometryCollection<{ name?: string }> }>
  return feature(topo, topo.objects.countries) as unknown as Countries
}
const loadCoarse = () => (coarse ??= import('world-atlas/countries-110m.json').then((m) => toCountries(m.default)))
const loadFine = () => (fine ??= import('world-atlas/countries-50m.json').then((m) => toCountries(m.default)))

const projection = geoNaturalEarth1().fitExtent([[8, 8], [W - 8, H - 8]], { type: 'Sphere' })
const pathGen = geoPath(projection)
const SPHERE = pathGen({ type: 'Sphere' }) ?? ''
const GRATICULE = pathGen(geoGraticule10()) ?? ''

interface Dot {
  key: string
  x: number
  y: number
  members: MapSite[]
}

/** Line speed and width from traffic: the busiest link flows fastest. Log scale, so one huge link does not freeze the rest. */
const weightOf = (bps: number, max: number) => (bps > 0 && max > 0 ? Math.min(1, Math.log10(1 + bps) / Math.log10(1 + max)) : 0)
type LinkHover = { c: SiteConnection; cx: number; cy: number }

export default function MapView({ selection, onSelect, filter }: { selection: Selection; onSelect: (s: Selection) => void; filter: Filter }) {
  const all = useTopology()
  const model = useMemo(
    () => applyFilter(all, knownOnly(filter, all)),
    [all.clusters, all.nodes, all.namespaces, all.services, all.devices, all.dependencies, all.applications, all.sites, all.siteLinks, all.externalEndpoints, filter], // eslint-disable-line react-hooks/exhaustive-deps
  )
  const { sites, clusters, devices, nodes, services, dependencies, siteLinks } = model
  const measured = usePaths()
  const [linkHover, setLinkHover] = useState<LinkHover | null>(null)
  const svg = useRef<SVGSVGElement>(null)
  const [host, setHost] = useState<HTMLElement | null>(null)
  // Tracks the <svg> node itself (for imperative reads like getScreenCTM/pointer capture) and, from the
  // same callback, its parent element in state - HoverCard/LinkCard read that during render, and a ref
  // read during render can be stale on the mounting frame.
  const svgRef = useCallback((node: SVGSVGElement | null) => {
    svg.current = node
    setHost(node?.parentElement ?? null)
  }, [])
  const [view, setView] = useState<View>({ k: 1, x: 0, y: 0 })
  const viewRef = useRef(view)
  // Written after render/commit rather than during render, so render stays a pure function of state.
  useLayoutEffect(() => {
    viewRef.current = view
  }, [view])
  const [countries, setCountries] = useState<Countries | null>(null)
  const [detailed, setDetailed] = useState(false)
  const [connections, setConnections] = useState(true)
  const [hover, setHover] = useState<{ dot: Dot; cx: number; cy: number } | null>(null)
  const drag = useRef<{ id: number; sx: number; sy: number; vx: number; vy: number; moved: boolean } | null>(null)
  const tween = useRef(0)

  useEffect(() => {
    let live = true
    loadCoarse().then((c) => live && setCountries((cur) => cur ?? c))
    return () => { live = false }
  }, [])
  useEffect(() => {
    if (view.k < 3 || detailed) return
    let live = true
    loadFine().then((c) => {
      if (!live) return
      setCountries(c)
      setDetailed(true)
    })
    return () => { live = false }
  }, [view.k, detailed])

  const mapSites = useMemo(() => buildMapSites(sites, clusters, devices, nodes, services), [sites, clusters, devices, nodes, services])
  const unplaced = useMemo(() => unplacedClusters(sites, clusters), [sites, clusters])
  const placement = usePlacementSuggestions().suggestions
  const conns = useMemo(() => siteConnections(sites, clusters, services, devices, dependencies, siteLinks, measured), [sites, clusters, services, devices, dependencies, siteLinks, measured])
  const maxBps = useMemo(() => Math.max(0, ...conns.map((c) => c.bpsAB + c.bpsBA)), [conns])

  // Base (zoom 1) position of every site; the busiest first so it keeps its own dot when two are close.
  const placed = useMemo(
    () =>
      mapSites
        .map((m) => {
          const p = projection([m.site.lng, m.site.lat])
          return p ? { m, x: p[0], y: p[1] } : null
        })
        .filter((p): p is { m: MapSite; x: number; y: number } => !!p)
        .sort((a, b) => b.m.clusters.length + b.m.devices.length - (a.m.clusters.length + a.m.devices.length) || a.m.site.name.localeCompare(b.m.site.name)),
    [mapSites],
  )
  const dots = useMemo<Dot[]>(
    () =>
      groupByProximity(placed, view.k, MERGE_PX).map((g) => ({
        key: g.map((i) => i.m.site.id).sort().join('+'),
        x: g.reduce((a, i) => a + i.x, 0) / g.length,
        y: g.reduce((a, i) => a + i.y, 0) / g.length,
        members: g.map((i) => i.m),
      })),
    [placed, view.k],
  )
  const dotOf = useMemo(() => {
    const m = new Map<string, Dot>()
    for (const d of dots) for (const s of d.members) m.set(s.site.id, d)
    return m
  }, [dots])

  // Countries that contain at least one site are tinted, so the map shows where you are present at a glance.
  const paths = useMemo(() => {
    if (!countries) return []
    return countries.features.map((f: Feature<Geometry, { name?: string }>, i: number) => ({
      key: String(f.id ?? i),
      name: f.properties?.name ?? '',
      d: pathGen(f as GeoPermissibleObjects) ?? '',
      present: mapSites.some((m) => geoContains(f as GeoPermissibleObjects, [m.site.lng, m.site.lat])),
    }))
  }, [countries, mapSites])

  const selectedSite =
    selection?.kind === 'site' ? selection.id : selection?.kind === 'cluster' ? clusters.find((c) => c.id === selection.id)?.siteId : undefined
  const selectedDot = selectedSite ? dotOf.get(selectedSite) : undefined

  /* ---------- pan and zoom ---------- */
  const toBox = useCallback((clientX: number, clientY: number) => {
    const el = svg.current
    const m = el?.getScreenCTM()
    if (!el || !m) return { x: 0, y: 0 }
    const p = new DOMPoint(clientX, clientY).matrixTransform(m.inverse())
    return { x: p.x, y: p.y }
  }, [])

  const apply = useCallback((v: View) => {
    const k = Math.min(MAX_K, Math.max(1, v.k))
    setView({ k, x: clampPan(v.x, k, W), y: clampPan(v.y, k, H) })
  }, [])

  /** Zoom by `factor` keeping the point (px,py) of the box where it is. */
  const zoomAt = useCallback(
    (factor: number, px: number, py: number) => {
      const v = viewRef.current
      const k = Math.min(MAX_K, Math.max(1, v.k * factor))
      const r = k / v.k
      apply({ k, x: px - (px - v.x) * r, y: py - (py - v.y) * r })
    },
    [apply],
  )

  /** Ease to a view; interrupted by any manual pan or zoom. */
  const flyTo = useCallback(
    (to: View) => {
      cancelAnimationFrame(tween.current)
      const from = viewRef.current
      const t0 = performance.now()
      const step = (now: number) => {
        const t = Math.min(1, (now - t0) / 260)
        const e = 1 - Math.pow(1 - t, 3)
        // interpolate the zoom on a log scale so it feels even
        const k = Math.exp(Math.log(from.k) + (Math.log(to.k) - Math.log(from.k)) * e)
        apply({ k, x: from.x + (to.x - from.x) * e, y: from.y + (to.y - from.y) * e })
        if (t < 1) tween.current = requestAnimationFrame(step)
      }
      tween.current = requestAnimationFrame(step)
    },
    [apply],
  )
  useEffect(() => () => cancelAnimationFrame(tween.current), [])

  // Wheel needs a non-passive listener to stop the page from scrolling.
  useEffect(() => {
    const el = svg.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      cancelAnimationFrame(tween.current)
      const { x, y } = toBox(e.clientX, e.clientY)
      zoomAt(Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.0018)), x, y)
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [toBox, zoomAt])

  const onPointerDown = (e: ReactPointerEvent<SVGSVGElement>) => {
    if (e.button !== 0) return
    cancelAnimationFrame(tween.current)
    drag.current = { id: e.pointerId, sx: e.clientX, sy: e.clientY, vx: view.x, vy: view.y, moved: false }
  }
  const onPointerMove = (e: ReactPointerEvent<SVGSVGElement>) => {
    const d = drag.current
    if (!d || d.id !== e.pointerId) return
    const dx = e.clientX - d.sx
    const dy = e.clientY - d.sy
    if (!d.moved && Math.hypot(dx, dy) < 4) return
    if (!d.moved) {
      d.moved = true
      svg.current?.setPointerCapture(e.pointerId)
      setHover(null)
    }
    // client pixels -> box units
    const a = toBox(0, 0)
    const b = toBox(1, 1)
    apply({ k: view.k, x: d.vx + dx * (b.x - a.x), y: d.vy + dy * (b.y - a.y) })
  }
  const endDrag = (e: ReactPointerEvent<SVGSVGElement>) => {
    if (drag.current?.id === e.pointerId) {
      if (drag.current.moved) svg.current?.releasePointerCapture(e.pointerId)
      // keep `moved` readable for the click that follows this pointerup
      const was = drag.current
      setTimeout(() => { if (drag.current === was) drag.current = null }, 0)
    }
  }

  /** Centre the box point (bx,by) at zoom k. */
  const centred = (bx: number, by: number, k: number): View => ({ k, x: W / 2 - bx * k, y: H / 2 - by * k })

  const open = (dot: Dot) => {
    if (drag.current?.moved) return
    if (dot.members.length === 1) {
      onSelect({ kind: 'site', id: dot.members[0].site.id })
      return
    }
    // Several places share this dot: zoom until they separate (or as far as needed to show them all).
    const xs = dot.members.map((m) => projection([m.site.lng, m.site.lat])![0])
    const ys = dot.members.map((m) => projection([m.site.lng, m.site.lat])![1])
    const bw = Math.max(...xs) - Math.min(...xs) || 1
    const bh = Math.max(...ys) - Math.min(...ys) || 1
    const fit = Math.min(W / bw, H / bh) * 0.45
    const k = Math.min(MAX_K, Math.max(view.k * 1.8, Math.min(fit, view.k * 12)))
    flyTo(centred((Math.min(...xs) + Math.max(...xs)) / 2, (Math.min(...ys) + Math.max(...ys)) / 2, k))
  }

  const total = mapSites.length
  const siteName = (id: string) => sites.find((x) => x.id === id)?.name ?? id
  if (sites.length === 0) {
    return (
      <div className="grid h-full place-items-center p-8">
        <EmptyState
          title="No sites yet"
          description="A site is a place — a cloud region, a data center, a factory floor. Add sites with coordinates, put your clusters on them, and they appear here."
          action={<Link to="/sites" className="text-accent hover:underline">Go to Sites</Link>}
        />
      </div>
    )
  }

  const inv = 1 / view.k
  return (
    <div className="relative h-full w-full overflow-hidden bg-nb-910">
      <svg
        ref={svgRef}
        viewBox={`0 0 ${W} ${H}`}
        preserveAspectRatio="xMidYMid meet"
        role="img"
        aria-label="World map of your sites"
        data-testid="map"
        className="h-full w-full cursor-grab touch-none select-none active:cursor-grabbing"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
        onDoubleClick={(e) => {
          const { x, y } = toBox(e.clientX, e.clientY)
          flyTo({ k: Math.min(MAX_K, view.k * 2.2), x: view.x - (x - view.x) * 1.2, y: view.y - (y - view.y) * 1.2 })
        }}
        onClick={(e) => {
          if (e.target === e.currentTarget || (e.target as Element).getAttribute('data-bg')) {
            if (!drag.current?.moved) onSelect(null)
          }
        }}
      >
        <g transform={`translate(${view.x} ${view.y}) scale(${view.k})`}>
          <path d={SPHERE} style={{ fill: 'var(--color-nb-910)' }} data-bg="1" />
          <path d={GRATICULE} fill="none" style={{ stroke: 'var(--color-nb-850)' }} strokeWidth={0.5} vectorEffect="non-scaling-stroke" opacity={0.7} />
          <g data-bg="1">
            {paths.map((p) => (
              <path
                key={p.key}
                d={p.d}
                data-bg="1"
                style={{ fill: p.present ? 'color-mix(in srgb, var(--color-accent) 14%, var(--color-nb-800))' : 'var(--color-nb-850)', stroke: 'var(--color-nb-700)' }}
                strokeWidth={0.6}
                vectorEffect="non-scaling-stroke"
              >
                <title>{p.name}</title>
              </path>
            ))}
          </g>
          <path d={SPHERE} fill="none" style={{ stroke: 'var(--color-nb-800)' }} strokeWidth={1} vectorEffect="non-scaling-stroke" />

          {connections && (
            <g fill="none">
              {conns.map((c) => {
                const a = dotOf.get(c.a)
                const b = dotOf.get(c.b)
                if (!a || !b || a === b) return null
                const mx = (a.x + b.x) / 2
                const my = (a.y + b.y) / 2 - Math.hypot(b.x - a.x, b.y - a.y) * 0.16 // bow the line so overlapping ones separate
                const d = `M${a.x},${a.y} Q${mx},${my} ${b.x},${b.y}`
                const hot = !!selectedSite && (c.a === selectedSite || c.b === selectedSite)
                const bps = c.bpsAB + c.bpsBA
                const w = weightOf(bps, maxBps)
                // Faster and thicker with traffic; the dashes travel towards the side that receives more.
                // A link nobody has measured still drifts, slowly, so the map reads as alive rather than frozen.
                const flow = bps > 0 ? 2.4 - 1.7 * w : 4
                const band = c.lossPct !== undefined ? lossBand(c.lossPct) : 'ok'
                const colour = hot ? 'var(--color-accent)' : band === 'hot' ? '#f87171' : band === 'warn' ? '#fbbf24' : '#8a96a0'
                return (
                  <g key={c.a + c.b} onPointerEnter={(e) => setLinkHover({ c, cx: e.clientX, cy: e.clientY })} onPointerMove={(e) => setLinkHover({ c, cx: e.clientX, cy: e.clientY })} onPointerLeave={() => setLinkHover(null)}>
                    <path d={d} stroke="transparent" strokeWidth={12} vectorEffect="non-scaling-stroke" pointerEvents="stroke" data-testid="map-link-hit" />
                    <path
                      d={d}
                      className="map-link"
                      data-reverse={c.bpsBA > c.bpsAB ? '1' : '0'}
                      data-testid="map-link"
                      data-traffic={bps > 0 ? '1' : '0'}
                      style={{ stroke: colour, ['--flow' as string]: `${flow.toFixed(2)}s` }}
                      strokeWidth={(hot ? 1.8 : 1) + w * 2.2}
                      vectorEffect="non-scaling-stroke"
                      opacity={selectedSite && !hot ? 0.2 : bps > 0 ? 0.9 : 0.65}
                      pointerEvents="none"
                    />
                  </g>
                )
              })}
            </g>
          )}

          {dots.map((d) => {
            const n = d.members.length
            const single = n === 1
            const status = d.members.reduce((w, m) => (m.status === 'offline' || (m.status === 'degraded' && w !== 'offline') ? m.status : w), d.members[0].status)
            // Two things to read at a glance: the fill says which tier the place is in, the ring says whether it is well.
            const ring = STATUS_COLOR[status]
            const tier = dominantTier(d.members.flatMap((m) => m.clusters.map((c) => c.tier)), d.members.some((m) => m.devices.length > 0))
            const fill = tier ? TIER_COLOR[tier] : 'var(--color-nb-600, #56626b)'
            const items = d.members.reduce((a, m) => a + m.clusters.length, 0)
            const isSel = d === selectedDot
            const r = single ? 8 : 13
            const peak = d.members.reduce<number | undefined>((m, x) => x.clusters.reduce((mm, c) => { const p = peakLoad(clusterLoad(c, nodes, services)); return p === undefined ? mm : Math.max(mm ?? 0, p) }, m), undefined)
            return (
              <g
                key={d.key}
                transform={`translate(${d.x} ${d.y}) scale(${inv})`}
                className="map-dot cursor-pointer"
                role="button"
                tabIndex={0}
                aria-label={`${groupLabel(d.members)}: ${single ? `${items} cluster${items === 1 ? '' : 's'}` : `${n} sites`}`}
                data-testid={single ? `map-dot-${d.members[0].site.id}` : 'map-dot-group'}
                onClick={(e) => { e.stopPropagation(); open(d) }}
                onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(d) } }}
                onPointerEnter={(e) => { if (!drag.current?.moved) setHover({ dot: d, cx: e.clientX, cy: e.clientY }) }}
                onPointerMove={(e) => { if (!drag.current?.moved) setHover({ dot: d, cx: e.clientX, cy: e.clientY }) }}
                onPointerLeave={() => setHover(null)}
              >
                <circle r={r + 7} fill="transparent" className="map-hit" />
                {isSel && <circle r={r + 6} fill="none" style={{ stroke: 'var(--color-accent)' }} strokeWidth={2} />}
                <circle r={r} style={{ fill: 'var(--color-nb-910)' }} stroke={ring} strokeWidth={2.6} />
                <circle r={r - 4.4} style={{ fill: fill }} fillOpacity={tier ? 0.92 : 0.35} data-tier={tier ?? 'none'} data-status={status} />
                {peak !== undefined && peak >= 70 && (
                  <circle cx={r * 0.75} cy={-r * 0.75} r={4.2} style={{ fill: peak >= 90 ? '#f87171' : '#fbbf24', stroke: 'var(--color-nb-910)' }} strokeWidth={1.4} data-testid="map-load-badge">
                    <title>{`Busiest cluster here: ${peak}% of its capacity is requested`}</title>
                  </circle>
                )}
                {!single && <text textAnchor="middle" dy="0.35em" fontSize={11} fontWeight={700} fill="#fff" stroke="rgba(0,0,0,.45)" strokeWidth={2.5} paintOrder="stroke" pointerEvents="none">{n}</text>}
                <text
                  x={r + 7}
                  dy="0.35em"
                  textAnchor="start"
                  fontSize={11}
                  fontWeight={500}
                  style={{ fill: 'var(--color-nb-300)', stroke: 'var(--color-nb-910)', strokeWidth: 3, paintOrder: 'stroke' }}
                >
                  {groupLabel(d.members)}
                </text>
              </g>
            )
          })}
        </g>
      </svg>

      {/* legend */}
      <div className="pointer-events-none absolute left-3 top-3 flex max-w-[calc(100%-4.5rem)] flex-col gap-2">
        <div className="pointer-events-auto flex flex-wrap items-center gap-x-4 gap-y-1.5 rounded-lg border border-nb-850 bg-nb-925/95 px-3.5 py-2 text-xs text-nb-400">
          <span className="flex items-center gap-3" aria-label="Fill is the tier">
            {TIERS.map((t) => (
              <span key={t.value} className="flex items-center gap-1.5">
                <span className="size-2.5 rounded-full" style={{ background: TIER_COLOR[t.value] }} />
                {t.label}
              </span>
            ))}
          </span>
          <span className="h-3 w-px bg-nb-800" />
          <span className="flex items-center gap-3" aria-label="Ring is the status">
            {(['healthy', 'degraded', 'offline'] as const).map((s) => (
              <span key={s} className="flex items-center gap-1.5 capitalize">
                <span className="size-2.5 rounded-full border-2 bg-nb-910" style={{ borderColor: STATUS_COLOR[s] }} />
                {s}
              </span>
            ))}
          </span>
          <span className="h-3 w-px bg-nb-800" />
          <span>Numbers: sites close together, click to zoom</span>
        </div>
        <label className="pointer-events-auto flex w-fit cursor-pointer items-center gap-2 rounded-lg border border-nb-850 bg-nb-925/95 px-3 py-1.5 text-xs text-nb-400">
          <input type="checkbox" className="accent-[var(--color-accent)]" checked={connections} onChange={(e) => setConnections(e.target.checked)} />
          Connections between sites
        </label>
      </div>

      {/* zoom */}
      <div className="absolute right-3 top-3 flex flex-col overflow-hidden rounded-lg border border-nb-850 bg-nb-925/95">
        <button aria-label="Zoom in" onClick={() => flyTo({ ...zoomTarget(view, 1.8) })} className="grid size-8 place-items-center text-nb-400 hover:bg-nb-940 hover:text-nb-300"><Plus size={15} /></button>
        <button aria-label="Zoom out" onClick={() => flyTo({ ...zoomTarget(view, 1 / 1.8) })} className="grid size-8 place-items-center border-t border-nb-850 text-nb-400 hover:bg-nb-940 hover:text-nb-300"><Minus size={15} /></button>
        <button aria-label="Show the whole world" onClick={() => flyTo({ k: 1, x: 0, y: 0 })} className="grid size-8 place-items-center border-t border-nb-850 text-nb-400 hover:bg-nb-940 hover:text-nb-300"><Home size={14} /></button>
      </div>

      {/* what is not on the map */}
      {(unplaced.length > 0 || total < sites.length) && (
        <div className="absolute bottom-3 left-3 max-w-sm rounded-lg border border-nb-850 bg-nb-925/95 px-3.5 py-2.5 text-xs text-nb-400">
          {unplaced.length > 0 && (
            <>
              <div className="mb-1.5 text-nb-300">
                {unplaced.length} cluster{unplaced.length === 1 ? '' : 's'} not on the map yet — choose a site for {unplaced.length === 1 ? 'it' : 'them'}:
              </div>
              <div className="flex flex-wrap gap-1.5">
                {unplaced.slice(0, 8).map((c) => (
                  <button key={c.id} onClick={() => onSelect({ kind: 'cluster', id: c.id })} className="rounded-md border border-nb-800 bg-nb-930 px-2 py-0.5 hover:border-nb-700 hover:text-nb-300">
                    {c.name}{c.region ? <span className="text-nb-500"> · {c.region}</span> : null}
                  </button>
                ))}
                {unplaced.length > 8 && <span className="px-1 py-0.5">+{unplaced.length - 8} more</span>}
              </div>
              {placement.slice(0, 3).map((p) => (
                <div key={p.id} className="mt-2 border-t border-nb-850 pt-1.5">
                  <span className="text-nb-500">{clusters.find((c) => 'clusterId' in (p.apply ?? {}) && c.id === (p.apply as { clusterId: string }).clusterId)?.name}</span>
                  <PlacementHint compact suggestion={p} />
                </div>
              ))}
            </>
          )}
          {total < sites.length && <div className={unplaced.length ? 'mt-1.5' : ''}>{sites.length - total} site{sites.length - total === 1 ? ' has' : 's have'} invalid coordinates.</div>}
        </div>
      )}

      {/* hover card */}
      {hover && <HoverCard hover={hover} host={host} load={(c) => clusterLoad(c, nodes, services)} />}
      {linkHover && !hover && <LinkCard hover={linkHover} host={host} names={siteName} />}
      {filterActive(filter) && (
        <div className="pointer-events-none absolute bottom-3 left-1/2 -translate-x-1/2 rounded-md bg-nb-925/90 px-2.5 py-1 text-[11px] text-accent" data-testid="map-filtered">
          Filtered: only the chosen clusters and applications are shown
        </div>
      )}
      <div className="pointer-events-none absolute bottom-3 right-3 rounded-md bg-nb-925/80 px-2 py-1 text-[11px] text-nb-500">
        Scroll to zoom · drag to pan · {view.k.toFixed(1)}×
      </div>
    </div>
  )
}

/** Zoom towards the centre of the box. */
function zoomTarget(v: View, factor: number): View {
  const k = Math.min(MAX_K, Math.max(1, v.k * factor))
  const r = k / v.k
  const px = W / 2
  const py = H / 2
  return { k, x: px - (px - v.x) * r, y: py - (py - v.y) * r }
}

function HoverCard({ hover, host, load }: { hover: { dot: Dot; cx: number; cy: number }; host: HTMLElement | null; load: (c: Cluster) => ClusterLoad }) {
  const box = host?.getBoundingClientRect()
  if (!box) return null
  const { dot } = hover
  const left = Math.min(hover.cx - box.left + 14, box.width - 260)
  // Above the pointer when there is room for the card (about 110 px per site), below it when there is not.
  const need = 24 + (110 + 34 * Math.max(0, ...dot.members.map((m) => m.clusters.length))) * Math.min(5, dot.members.length)
  const y = hover.cy - box.top
  const below = y < need
  const top = below ? y + 18 : y - 12
  return (
    <div className="pointer-events-none absolute z-10 w-60 rounded-lg border border-nb-800 bg-nb-920 p-3 text-xs shadow-xl" style={{ left, top, transform: below ? undefined : 'translateY(-100%)' }}>
      {dot.members.slice(0, 5).map((m) => (
        <div key={m.site.id} className="mb-2 last:mb-0">
          <div className="flex items-center justify-between gap-2">
            <span className="truncate text-sm font-medium text-nb-300">{m.site.name}</span>
            <StatusDot status={m.status} />
          </div>
          <div className="text-nb-500">{placeLabel(m.site) || m.site.country}</div>
          {m.exitIps.length > 0 && (
            <div className="mt-0.5 text-nb-500" data-testid="map-exit-ip">
              Exit IP <span className="font-mono text-nb-300">{m.exitIps.join(', ')}</span> <span className="text-warn">public</span>
            </div>
          )}
          <div className="mt-1 flex flex-wrap items-center gap-1">
            {m.clusters.map((c) => <TierBadge key={c.id} tier={c.tier} />)}
            {m.clusters.length > 0 && <Pill>{m.clusters.length} cluster{m.clusters.length === 1 ? '' : 's'} · {m.nodes} nodes · {m.services} services</Pill>}
            {m.devices.length > 0 && <Pill>{m.devices.reduce((a, d) => a + d.count, 0)} devices</Pill>}
            {m.clusters.length === 0 && m.devices.length === 0 && <span className="text-nb-500">Nothing here yet</span>}
          </div>
          {m.clusters.map((c) => (
            <div key={c.id} className="mt-1.5">
              <div className="text-[11px] text-nb-400">{c.name}</div>
              <LoadRow load={load(c)} />
            </div>
          ))}
        </div>
      ))}
      {dot.members.length > 5 && <div className="text-nb-500">+{dot.members.length - 5} more sites</div>}
    </div>
  )
}

/** What one connection between two sites carries and how well it performs. Says where each number comes from. */
function LinkCard({ hover, host, names }: { hover: LinkHover; host: HTMLElement | null; names: (id: string) => string }) {
  const box = host?.getBoundingClientRect()
  if (!box) return null
  const { c } = hover
  const left = Math.min(hover.cx - box.left + 14, box.width - 250)
  const top = Math.max(8, hover.cy - box.top - 12)
  const src = c.measured ? 'measured' : 'declared'
  return (
    <div className="pointer-events-none absolute z-10 w-60 -translate-y-full rounded-lg border border-nb-800 bg-nb-920 p-3 text-xs shadow-xl" style={{ left, top }} data-testid="map-link-card">
      <div className="text-sm font-medium text-nb-300">{names(c.a)} ↔ {names(c.b)}</div>
      <dl className="mt-1.5 grid grid-cols-[minmax(0,auto)_1fr] gap-x-3 gap-y-0.5 text-nb-400">
        <dt>Dependencies</dt>
        <dd className="text-nb-200">{c.dependencies}{c.dependencies > 0 && <span className="text-nb-500"> ({c.active} seen in traffic)</span>}</dd>
        {c.bpsAB > 0 && (
          <>
            <dt className="truncate">{names(c.a)} →</dt>
            <dd className="text-nb-200">{bytesPerSec(c.bpsAB)}</dd>
          </>
        )}
        {c.bpsBA > 0 && (
          <>
            <dt className="truncate">{names(c.b)} →</dt>
            <dd className="text-nb-200">{bytesPerSec(c.bpsBA)}</dd>
          </>
        )}
        {c.rttMs !== undefined && (
          <>
            <dt>Round trip</dt>
            <dd className="text-nb-200">{rttLabel(c.rttMs)} <span className="text-nb-500">{src}</span></dd>
          </>
        )}
        {c.lossPct !== undefined && (
          <>
            <dt>Loss</dt>
            <dd className={lossBand(c.lossPct) === 'ok' ? 'text-nb-200' : lossBand(c.lossPct) === 'warn' ? 'text-warn' : 'text-bad'}>{c.lossPct.toFixed(c.lossPct < 10 ? 1 : 0)}%</dd>
          </>
        )}
      </dl>
      {c.rttMs === undefined && c.bpsAB + c.bpsBA === 0 && <p className="mt-1.5 text-nb-500">Nothing measured on this connection yet.</p>}
    </div>
  )
}
