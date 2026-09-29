import {
  Background,
  BackgroundVariant,
  Controls,
  MiniMap,
  NodeToolbar,
  Panel,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useNodesState,
  useReactFlow,
  type Edge,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import clsx from 'clsx'
import { Boxes, ChevronDown, Filter as FilterIcon, Package, Plug, Plus, Radio, Server, SlidersHorizontal } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useConnectFlow } from '@/components/discovery/ConnectFlow'
import { useTelemetryFlow } from '@/components/telemetry/TelemetryFlow'
import { ClusterForm, DeviceForm, NodeForm, ServiceForm } from '@/components/forms'
import GettingStarted, { useGettingStarted } from '@/components/GettingStarted'
import Inspector, { type Selection } from '@/components/topology/Inspector'
import MapView from '@/components/topology/MapView'
import ScopeFromSelection from '@/components/topology/ScopeFromSelection'
import ViewsMenu from '@/components/topology/ViewsMenu'
import LiveStatus from '@/components/LiveStatus'
import { nodeTypes } from '@/components/topology/nodes'
import { edgeTypes } from '@/components/topology/OffsetEdge'
import { Button, EmptyState, MenuPanel, Select } from '@/components/ui/primitives'
import { PRESS_CLASS } from '@/components/ui/buttonClass'
import FilterMenu from '@/components/topology/FilterMenu'
import { api } from '@/lib/api'
import { extrasOf, TELEMETRY_SIGNALS } from '@/lib/consent'
import { applyFilter, encodeList, filterActive, isFreshApplicationView, knownOnly, parseFilter } from '@/lib/filter'
import { buildGraph, cardId, groupId, resyncNodes, selectedServiceIds, type TopoEdge, type TopoNode } from '@/lib/graph'
import { lossBand } from '@/lib/metrics'
import { anyMesh, VERDICT_COLOR } from '@/lib/mesh'
import { useAutoPlaceClusters } from '@/lib/usePlacement'
import { usePlan } from '@/lib/placement/usePlacement'
import { parseSel } from '@/lib/search'
import { TIER_COLOR, TIERS, type GroupBy, type RegionalOperator, type ViewKind } from '@/lib/types'
import { useServer } from '@/store/server'
import { useHistoryView } from '@/store/history'
import { usePaths, useTopology } from '@/store/topology'

type Mode = ViewKind | 'map'
const VIEWS: { value: Mode; label: string; hint: string }[] = [
  { value: 'application', label: 'Application', hint: 'Microservices and the clusters they run in' },
  { value: 'infrastructure', label: 'Infrastructure', hint: 'Nodes (VMs / machines) grouped by cluster' },
  { value: 'map', label: 'Map', hint: 'Where your sites are in the world' },
]

function Toggle({ checked, onChange, label, disabled, title }: { checked: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean; title?: string }) {
  return (
    <button
      role="switch"
      aria-checked={checked}
      aria-disabled={disabled}
      disabled={disabled}
      title={title}
      onClick={() => onChange(!checked)}
      className="flex w-full items-center gap-2.5 whitespace-nowrap rounded-md px-2 py-1.5 text-left text-sm text-nb-300 hover:bg-nb-930 disabled:opacity-50 disabled:hover:bg-transparent"
    >
      <span className={clsx('relative h-4 w-7 shrink-0 rounded-full transition-colors', checked ? 'bg-accent' : 'bg-nb-800')}>
        <span className={clsx('absolute top-0.5 size-3 rounded-full bg-white transition-all', checked ? 'left-3.5' : 'left-0.5')} />
      </span>
      {label}
    </button>
  )
}

type FormState =
  | { type: 'cluster'; id?: string }
  | { type: 'node'; id?: string }
  | { type: 'service'; id?: string }
  | { type: 'device'; id?: string }
  | null

/** The toolbar's popovers (filter, saved views, options, add) all hang off the same row: at most one may be
 * open at a time, so opening one always closes any other that was already open, instead of both fighting over
 * their own click-outside backdrop. */
type MenuKey = 'filter' | 'views' | 'options' | 'add' | 'scope'

function Canvas() {
  const topology = useTopology()
  const { fitView } = useReactFlow()
  const [sp, setSp] = useSearchParams()
  const connect = useConnectFlow()
  const telemetry = useTelemetryFlow()
  const started = useGettingStarted('topology')
  // The canvas is where a cluster's placement is actually seen, so it's a fair place to also resolve
  // a missing one silently (see Layout.tsx's comment for why this no longer runs on every route).
  useAutoPlaceClusters()

  // Regional operators aren't part of the central topology store (see RegionalOperatorsPage's own note) -
  // a plain fetch-on-mount, same admin-gated pattern that page already uses, is all the canvas needs; no
  // continuous polling, since a missed edit here just means "refresh to see a brand new operator's arrow".
  const conn = useServer((s) => s.conn)
  const isAdmin = useServer((s) => s.isAdmin)
  const admin = isAdmin()
  const [operators, setOperators] = useState<RegionalOperator[]>([])
  useEffect(() => {
    const c = conn()
    if (!c || !admin) return
    let cancelled = false
    void api.listOperators(c).then(
      (ops) => { if (!cancelled) setOperators(ops) },
      () => { /* silently skipped - operator arrows are a bonus, not core to the canvas */ },
    )
    return () => { cancelled = true }
  }, [conn, admin])

  const mode: Mode = sp.get('view') === 'infrastructure' ? 'infrastructure' : sp.get('view') === 'map' ? 'map' : 'application'
  const isMap = mode === 'map'
  const view: ViewKind = isMap ? 'application' : mode
  const groupBy: GroupBy = sp.get('group') === 'tier' ? 'tier' : 'cluster'
  const servicesOnNodes = sp.get('services') === '1'
  const links = sp.get('links') !== '0'
  const showDevices = sp.get('devices') !== '0'
  const showLabels = sp.get('labels') === '1'
  const showNoise = sp.get('noise') === '1'
  const hasMesh = anyMesh(topology.clusters)
  // Asked for in the URL, but only means something when a cluster runs a mesh.
  const showMesh = sp.get('mesh') === '1' && hasMesh
  // Only means something in the application view, grouped by cluster (a tier box already mixes clusters together).
  const showNamespaces = sp.get('namespaces') === '1' && groupBy === 'cluster'
  // Chain lays every service out in one flat left-to-right order across every cluster, so it replaces the
  // cluster/tier boxes and namespace sub-boxes rather than combining with them.
  const showChain = sp.get('chain') === '1' && mode === 'application'
  // How many options differ from the defaults, so a hidden option is never a mystery.
  const changedOptions = [!showDevices, showNoise, servicesOnNodes, !links, showLabels, groupBy === 'tier', showMesh, showNamespaces, showChain].filter(Boolean).length
  const setParam = (k: string, v: string | null) =>
    setSp((p) => {
      const n = new URLSearchParams(p)
      if (v === null) n.delete(k)
      else n.set(k, v)
      return n
    }, { replace: true })

  // The Application view defaults to "real services" (Deployment) rather than "everything": a fresh visit
  // with no filter chosen yet is the common case, and starting there with just Deployment makes the canvas
  // read as actual services (not DaemonSets/StatefulSets/Jobs) and gives a topology selection fewer, more
  // relevant cards to choose from. A one-time URL rewrite, not a render-time default - deriving it at render
  // time instead would break the moment someone unchecks the only active kind, since the resulting empty
  // selection writes the exact same "no kinds param" URL the default itself would read, so the default would
  // silently reassert itself on the very next render. Running once, on mount, means it can never re-fire
  // after that no matter what the person does afterward (including clearing it right back to "everything").
  useEffect(() => {
    if (isFreshApplicationView(sp)) setParam('kinds', encodeList(['Deployment']))
    // only meant to run once, at first mount
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const [selection, setSelection] = useState<Selection>(null)

  // Arriving from search (or a shared link) with ?sel=service:w-gw opens that thing in the inspector.
  const selParam = sp.get('sel')
  const [form, setForm] = useState<FormState>(null)
  const inPast = useHistoryView((s) => s.at !== null)
  const [openMenu, setOpenMenu] = useState<MenuKey | null>(null)
  const toggleMenu = (key: MenuKey) => setOpenMenu((cur) => (cur === key ? null : key))
  const [hoverEdge, setHoverEdge] = useState<string | null>(null)
  // Escape closes whichever one of the toolbar's popovers is open.
  useEffect(() => {
    if (!openMenu) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpenMenu(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [openMenu])

  const { clusters, nodes: machines, namespaces, services, devices, dependencies, applications, sites, siteLinks, externalEndpoints, agents } = topology
  // Discovered records come from the server with the first refresh, after the workspace loads: a link to one waits for them.
  const observedReady = useServer((s) => s.status === 'disconnected' || s.state !== undefined)

  // Local operators: cluster id → the summary the canvas badge needs (nodes.tsx's antenna badge on the
  // cluster's own group box - see graph.ts's `localTelemetry`). A local operator is just an already-approved
  // agent with telemetry signals turned on, so this is a view over `agents`, not a fetch of its own; mirrors
  // RegionalOperatorsPage's own `localRows` derivation. Only the first approved agent per cluster counts -
  // today's model is one discovery agent per cluster, so this never has to merge two operators' worth of
  // signals into one badge.
  const rawAgents = useServer((s) => s.state?.agents)
  // `rawAgents` is a fresh array on every poll - useServer.refresh() just replaces `state` wholesale
  // (see server.ts), unlike useRawTopology's merge, nothing dedupes it when the response is unchanged. Using
  // it as a useMemo dependency directly would give `graph` below a new `localOperators` identity every
  // 2-5s forever, forcing a full canvas re-layout on every poll tick even when no agent's telemetry actually
  // changed - exactly the "no continuous polling" the operators fetch above was meant to avoid, and by
  // itself enough to make the canvas feel less and less responsive the longer this page stays open. This
  // signature is the cheap part (strings only, same idea as `shape` further down), so it's fine to
  // recompute every poll; the Map below only rebuilds - and only then hands `graph` a new reference - when
  // the signature's value actually changes.
  const localOperatorsSig = useMemo(() => {
    const parts: string[] = []
    for (const a of agents) {
      if (a.status !== 'approved' || !a.clusterId) continue
      const installed = extrasOf(rawAgents, a.id).diagnostics?.installedTelemetry ?? []
      if (installed.length) parts.push(`${a.clusterId}:${a.id}:${[...installed].sort().join(',')}`)
    }
    return parts.sort().join('|')
  }, [agents, rawAgents])
  const localOperatorByCluster = useMemo(() => {
    const layerOf = new Map(TELEMETRY_SIGNALS.map((sig) => [sig.id, sig.layer]))
    const m = new Map<string, { layers: string[]; agentId: string }>()
    for (const a of agents) {
      if (a.status !== 'approved' || !a.clusterId || m.has(a.clusterId)) continue
      const installed = extrasOf(rawAgents, a.id).diagnostics?.installedTelemetry ?? []
      if (!installed.length) continue
      const layers = [...new Set(installed.map((id) => layerOf.get(id)).filter((l): l is 'infrastructure' | 'application' => !!l))]
      m.set(a.clusterId, { layers, agentId: a.id })
    }
    return m
    // only localOperatorsSig should force a rebuild - see its own comment above
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [localOperatorsSig])
  useEffect(() => {
    if (!selParam || !observedReady) return
    const want = parseSel(selParam)
    const pool: Record<string, { id: string }[]> = { cluster: clusters, node: machines, service: services, device: devices, site: sites, external: externalEndpoints }
    if (want && pool[want.kind].some((x) => x.id === want.id)) setSelection(want)
    setSp((p) => {
      const n = new URLSearchParams(p)
      n.delete('sel')
      return n
    }, { replace: true })
    // only the URL option triggers this; the lists are read when it arrives
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selParam, observedReady])
  const paths = usePaths()
  // Placement advice, shown as a small marker on the services it would move.
  const { plan, world } = usePlan()
  const hints = useMemo(() => new Map(plan.recommendations.map((r) => [r.serviceId, world.byCluster.get(r.to)?.name ?? r.to])), [plan, world])

  // Only some clusters or applications, when asked (?clusters=a,b&apps=x). Ids that no longer exist are ignored.
  const rawFilter = useMemo(() => parseFilter(sp), [sp])
  const filter = useMemo(() => knownOnly(rawFilter, { clusters, applications }), [rawFilter, clusters, applications])
  const filtering = filterActive(filter)
  const shown = useMemo(
    () => applyFilter({ clusters, nodes: machines, namespaces, services, devices, dependencies, applications, sites, siteLinks, externalEndpoints }, filter),
    [clusters, machines, namespaces, services, devices, dependencies, applications, sites, siteLinks, externalEndpoints, filter],
  )
  const graph = useMemo(
    () =>
      buildGraph(
        { ...shown, operators },
        { view, groupBy, servicesOnNodes, links, devices: showDevices, noise: showNoise, mesh: showMesh, namespaces: showNamespaces, chain: showChain, paths, hints, localOperators: localOperatorByCluster },
      ),
    [shown, operators, view, groupBy, servicesOnNodes, links, showDevices, showNoise, showMesh, showNamespaces, showChain, paths, hints, localOperatorByCluster],
  )
  const nothingMatches = filtering && shown.clusters.length === 0 && shown.devices.length === 0

  const [nodes, setNodes, onNodesChange] = useNodesState<TopoNode>(graph.nodes)

  // React Flow's own multi-select (shift/ctrl/cmd-click, or a box-drag - see multiSelectionKeyCode below)
  // writes here via onSelectionChange, entirely separately from `selection` below (the single-click
  // Inspector state) - the two are merged into one `highlightedIds` set just below instead of one
  // overwriting the other, which is what used to make them "fight": a plain click used to reset every
  // node's `.selected` flag down to just the last-clicked id, silently erasing a multi-selection's own
  // highlight a moment after React Flow had just set it.
  const [multiSelectedIds, setMultiSelectedIds] = useState<string[]>([])

  // React Flow id of the current single-click Inspector selection (if it is visible in this plane).
  const selectedRfId = useMemo(() => {
    if (!selection) return null
    if (selection.kind === 'cluster') return groupBy === 'cluster' ? groupId(selection.id) : null
    if (selection.kind === 'tier') return groupId(selection.id)
    if (selection.kind === 'site') return groupId(`dev:${selection.id}`)
    return cardId(selection.id)
  }, [selection, groupBy])

  // The full set of node ids that should currently show the accent halo and count toward a telemetry scope:
  // the single-click Inspector selection and the multi-select set above, merged - so a scope built up across
  // several clicks (or a single click, or a box-drag over a whole cluster) all read as one consistent,
  // highlighted group instead of only the last-clicked item mattering.
  const highlightedIds = useMemo(() => {
    const s = new Set(multiSelectedIds)
    if (selectedRfId) s.add(selectedRfId)
    return s
  }, [selectedRfId, multiSelectedIds])

  const selectedServices = useMemo(() => {
    const entityIds = new Set(selectedServiceIds(nodes, [...highlightedIds]))
    return services.filter((s) => entityIds.has(s.id))
  }, [highlightedIds, nodes, services])

  // Re-sync when the model / plane changes (keeps selection highlight). A node that was already on the
  // canvas keeps the position it has there (a manual drag, or a prior layout pass) instead of jumping back
  // to the graph's freshly computed one - which would otherwise happen on every poll, even one that changed
  // nothing about this node, because `graph` gets a new identity whenever any upstream data is refreshed.
  // That old position only still means what it used to when the node is still positioned relative to the
  // same parent (React Flow positions are parent-relative, or canvas-relative with no parent at all) -
  // toggling namespace sub-boxes, for instance, re-parents every card in a cluster from the cluster box
  // straight to a namespace box without changing the card's id, and its old, cluster-relative position
  // would otherwise land it in the wrong spot (often overlapping another card) inside the new, smaller
  // namespace box until something else - like leaving the page and coming back - forced a fresh layout.
  // See `resyncNodes` (graph.ts) for why this merges instead of replacing outright - in short, a node
  // React Flow is actively dragging must come back untouched, or a poll landing mid-gesture (this page
  // polls every 2-5s) can desync React Flow's own drag tracking and leave the canvas unresponsive until a
  // reload; every other node keeps its on-screen position across polls unless its parent actually changed.
  useEffect(() => {
    setNodes((prev) => resyncNodes(prev, graph.nodes, highlightedIds))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [graph, setNodes])
  useEffect(() => {
    setNodes((ns) => ns.map((n) => (n.selected === highlightedIds.has(n.id) ? n : { ...n, selected: highlightedIds.has(n.id) })))
  }, [highlightedIds, setNodes])

  // Re-fit the viewport whenever the *shape* of the graph changes (not on every edit).
  const shape = useMemo(() => graph.nodes.map((n) => `${n.id}:${n.style?.width}x${n.style?.height}`).join('|'), [graph])
  useEffect(() => {
    const t = setTimeout(() => fitView({ padding: FIT_PADDING, duration: 300 }), 60)
    return () => clearTimeout(t)
  }, [shape, fitView])

  // Edge styling for the current selection (color, width, dashing, opacity, and whether the label is shown
  // for a reason other than hover - selection/showLabels). Deliberately NOT keyed on `hoverEdge`: see `edges`
  // below for why.
  const styledEdges = useMemo<TopoEdge[]>(() => {
    const focus =
      selection?.kind === 'service' || selection?.kind === 'device' || selection?.kind === 'external'
        ? selection.id
        : selection?.kind === 'cluster' && groupBy === 'cluster'
          ? selection.id
          : selection?.kind === 'tier'
            ? selection.id
            : null
    const pickedEdge = selection?.kind === 'dependency' ? selection.id : null
    const related = (e: TopoEdge) => (!!focus && (e.data?.from === focus || e.data?.to === focus)) || e.id === pickedEdge
    const anyRelated = (!!focus || !!pickedEdge) && graph.edges.some(related)
    return graph.edges.map((e) => {
      const hot = related(e)
      const dim = anyRelated && !hot
      const showLabel = showLabels || hot
      const q = e.data?.quality
      const band = q ? lossBand(q.lossPct) : 'ok'
      // A link that loses connection attempts is coloured by how badly; otherwise grey, or orange when it is the focus.
      const mv = e.data?.mesh
      const stroke = hot ? '#f68330' : mv ? VERDICT_COLOR[mv.state] : band === 'hot' ? '#f87171' : band === 'warn' ? '#fbbf24' : e.data?.crossGroup ? '#98a4ae' : '#6f7b85'
      // Seen in traffic: solid, and a touch thicker the busier it is. Only declared (or gone quiet): dotted and
      // thin. Kept close to the declared baseline (1.2) rather than scaling up hard - a busy link should read as
      // "more traffic" without out-weighing the 2.4px used for the current selection/focus.
      const seen = !!e.data?.observed && !e.data?.stale
      const width = hot ? 2.4 : e.data?.aggregated ? 2 : seen ? 1.2 + 1.0 * (e.data?.weight ?? 0.15) : 1.2
      return {
        ...e,
        label: showLabel ? e.label : undefined,
        style: {
          stroke,
          strokeWidth: width,
          opacity: dim ? 0.15 : e.data?.stale ? 0.55 : 1,
          strokeDasharray: !e.data?.aggregated && !seen ? '2 5' : undefined,
          // Busier links run their dashes faster (a quiet one takes 2.4 s for a period, the busiest 0.7 s).
          animationDuration: e.className === 'edge-animated' ? `${(2.4 - 1.7 * (e.data?.weight ?? 0)).toFixed(2)}s` : undefined,
        },
        labelStyle: { fill: hot ? '#f68330' : '#a7b1b9', fontSize: 10.5, opacity: dim ? 0.3 : 1 },
        labelBgStyle: { fill: 'var(--color-nb-910)', fillOpacity: 0.95 },
        labelBgPadding: [6, 3] as [number, number],
        labelBgBorderRadius: 4,
        markerEnd: e.markerEnd && typeof e.markerEnd === 'object' ? { ...e.markerEnd, color: stroke } : e.markerEnd,
      }
    })
  }, [graph.edges, selection, groupBy, showLabels])

  // The raw (un-hidden) label text for every edge, so the hover-only reveal below can put one back without
  // needing to keep the whole graph.edges array around.
  const rawLabelById = useMemo(() => new Map(graph.edges.map((e) => [e.id, e.label])), [graph.edges])

  // Hovering only ever reveals ONE edge's label. Re-deriving the whole styled array (recomputing color/width/
  // dash/opacity for every edge, and - critically - handing React Flow a brand new object for every edge) on
  // every hover transition would force every OffsetEdge to re-render just because a mouse crossed the canvas
  // (it calls useInternalNode twice per edge - the most common canvas interaction there is, made needlessly
  // heavy). So this only ever swaps in a fresh object for the one edge whose shown-ness actually changed;
  // every other edge keeps the exact same object reference it already had.
  const edges = useMemo<TopoEdge[]>(() => {
    if (!hoverEdge) return styledEdges
    return styledEdges.map((e) => (e.id === hoverEdge && !e.label ? { ...e, label: rawLabelById.get(e.id) } : e))
  }, [styledEdges, hoverEdge, rawLabelById])

  const select = useCallback((s: Selection) => setSelection(s), [])

  const fromNode = (n: TopoNode): Selection => {
    const d = n.data
    if (d.kind === 'group') {
      if (d.extra === 'devices') return { kind: 'site', id: d.entityId }
      if (d.extra) return null
      return { kind: d.groupBy === 'cluster' ? 'cluster' : 'tier', id: d.entityId }
    }
    if (d.kind === 'namespace') return null // a visual grouping only, nothing to inspect on its own
    return { kind: d.kind === 'machine' ? 'node' : d.kind, id: d.entityId }
  }

  const editSelection = (s: NonNullable<Selection>) => {
    if (s.kind === 'cluster') setForm({ type: 'cluster', id: s.id })
    else if (s.kind === 'node') setForm({ type: 'node', id: s.id })
    else if (s.kind === 'service') setForm({ type: 'service', id: s.id })
    else if (s.kind === 'device') setForm({ type: 'device', id: s.id })
  }

  const miniColor = (n: { data?: unknown }) => {
    const d = n.data as TopoNode['data'] | undefined
    return d ? TIER_COLOR[d.tier] : '#3f444b'
  }

  const empty = clusters.length === 0
  const closeForm = () => setForm(null)

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/*
        Deliberately not PageHeader here, unlike every other page: PageHeader's spacious title + description
        + simple action-button row is built for scrolling content, and its `mb-6` alone would eat a chunk of
        the vertical space this page instead spends on the canvas filling the rest of the viewport. This
        toolbar's own content - a view-mode tablist, several dropdown menus (filter/views/options/add), and
        inline toggles - also doesn't fit PageHeader's title/description/actions shape to begin with. A
        compact, dense single-row bar is the right tool for a canvas page; PageHeader stays for pages that
        are actually a page of content.
      */}
      {/* Toolbar */}
      <div className="flex min-h-14 shrink-0 flex-wrap items-center gap-x-4 gap-y-2 border-b border-nb-850 bg-nb-920 px-4 py-2 sm:px-5">
        <h1 className="text-base font-medium text-nb-300">Topology</h1>
        <div className="flex rounded-lg border border-nb-800 bg-nb-925 p-0.5" role="tablist" aria-label="View">
          {VIEWS.map((v) => (
            <button
              key={v.value}
              role="tab"
              aria-selected={mode === v.value}
              title={v.hint}
              onClick={() => {
                setParam('view', v.value === 'application' ? null : v.value)
                setSelection(null)
              }}
              className={clsx(
                'rounded-md px-3.5 py-1.5 text-sm',
                PRESS_CLASS,
                mode === v.value ? 'bg-nb-850 text-nb-300' : 'text-nb-400 hover:text-nb-300',
              )}
            >
              {v.label}
            </button>
          ))}
        </div>

        <div className="ml-auto flex flex-wrap items-center gap-2 sm:gap-3">
          <LiveStatus />
          <FilterMenu
            open={openMenu === 'filter'}
            onOpenChange={(o) => setOpenMenu(o ? 'filter' : null)}
            filter={filter}
            clusters={clusters.filter((c) => !c.deletedAt)}
            applications={applications.filter((a) => !a.deletedAt)}
            onChange={(f) => {
              setSp((p) => {
                const n = new URLSearchParams(p)
                for (const [k, v] of [['clusters', encodeList(f.clusters)], ['apps', encodeList(f.apps)], ['kinds', encodeList(f.kinds)]] as const) {
                  if (v === null) n.delete(k)
                  else n.set(k, v)
                }
                return n
              }, { replace: true })
              setSelection(null)
            }}
          />
          <ViewsMenu
            open={openMenu === 'views'}
            onOpenChange={(o) => setOpenMenu(o ? 'views' : null)}
            sp={sp}
            onApply={(params) => {
              setSp(new URLSearchParams(params), { replace: true })
              setSelection(null)
            }}
          />
          {!isMap && (
            <div className="relative">
              <Button onClick={() => toggleMenu('options')} aria-expanded={openMenu === 'options'} aria-haspopup="true" data-testid="view-options">
                <SlidersHorizontal size={15} /> <span className="hidden sm:inline">Options</span>
                {changedOptions > 0 && <span className="rounded-full bg-accent-soft px-1.5 text-[11px] font-medium text-accent">{changedOptions}</span>}
              </Button>
              <MenuPanel open={openMenu === 'options'} onClose={() => setOpenMenu(null)} className="w-72 p-2" role="group" aria-label="View options">
                {mode === 'application' && (
                  <Toggle
                    checked={showDevices}
                    onChange={(v) => {
                      setParam('devices', v ? null : '0')
                      // Hiding devices removes the selected one from the canvas; Inspector would otherwise
                      // keep showing (and let you edit) something no longer drawn anywhere.
                      if (!v && (selection?.kind === 'device' || selection?.kind === 'external')) setSelection(null)
                    }}
                    label="Devices and external endpoints"
                  />
                )}
                {mode === 'application' && dependencies.some((d) => d.noise) && (
                  <Toggle checked={showNoise} onChange={(v) => setParam('noise', v ? '1' : null)} label="DNS & system traffic" />
                )}
                {mode === 'infrastructure' && (
                  <>
                    <Toggle checked={servicesOnNodes} onChange={(v) => setParam('services', v ? '1' : null)} label="Services on nodes" />
                    <Toggle checked={links} onChange={(v) => setParam('links', v ? null : '0')} label="Cross-cluster links" />
                  </>
                )}
                {mode === 'application' && (
                  <Toggle
                    checked={showMesh}
                    disabled={!hasMesh}
                    onChange={(v) => setParam('mesh', v ? '1' : null)}
                    label="Service mesh"
                    title={hasMesh ? 'Show the mesh, which services are in it, and what it does to each connection' : 'No service mesh was found in the connected clusters'}
                  />
                )}
                {mode === 'application' && !hasMesh && <p className="-mt-0.5 px-2 pb-1 pl-[46px] text-[11px] text-nb-500">No mesh found in your clusters</p>}
                <Toggle checked={showLabels} onChange={(v) => setParam('labels', v ? '1' : null)} label="Edge labels" />
                {mode === 'application' && (
                  <Toggle
                    checked={showNamespaces}
                    disabled={groupBy !== 'cluster' || showChain}
                    onChange={(v) => setParam('namespaces', v ? '1' : null)}
                    label="Namespace sub-boxes"
                    title={showChain ? 'Not available in chain layout' : groupBy === 'cluster' ? 'Draw a box per namespace inside each cluster' : 'Only available grouped by cluster'}
                  />
                )}
                {mode === 'application' && (
                  <Toggle
                    checked={showChain}
                    onChange={(v) => setParam('chain', v ? '1' : null)}
                    label="Chain layout"
                    title="Lay every service out left to right by who calls whom, across every cluster, instead of grouping them into boxes"
                  />
                )}
                <div className={clsx('mt-1 flex items-center justify-between gap-3 border-t border-nb-850 px-2 pb-1 pt-2.5 text-sm', showChain ? 'text-nb-600' : 'text-nb-400')}>
                  Group by
                  <Select
                    className="h-8 w-32"
                    value={groupBy}
                    disabled={showChain}
                    title={showChain ? 'Not available in chain layout' : undefined}
                    onChange={(e) => {
                      setParam('group', e.target.value === 'tier' ? 'tier' : null)
                      setSelection(null)
                    }}
                  >
                    <option value="cluster">Cluster</option>
                    <option value="tier">Tier</option>
                  </Select>
                </div>
              </MenuPanel>
            </div>
          )}

          <div className="relative">
            <Button variant="primary" onClick={() => toggleMenu('add')} disabled={inPast} title={inPast ? 'Return to now to add or change things' : undefined}>
              <Plus size={16} /> Add <ChevronDown size={14} />
            </Button>
            <MenuPanel open={openMenu === 'add'} onClose={() => setOpenMenu(null)} className="w-48 overflow-hidden p-1">
              {[
                { t: 'cluster', label: 'Cluster', icon: Boxes, disabled: false },
                { t: 'node', label: 'Node', icon: Server, disabled: empty },
                { t: 'service', label: 'Service', icon: Package, disabled: empty },
                { t: 'device', label: 'Device', icon: Radio, disabled: false },
              ].map(({ t, label, icon: Icon, disabled }) => (
                <button
                  key={t}
                  disabled={disabled}
                  onClick={() => {
                    setOpenMenu(null)
                    setForm({ type: t as 'cluster' | 'node' | 'service' | 'device' })
                  }}
                  className="flex w-full items-center gap-2.5 rounded-md px-3 py-2 text-sm text-nb-300 hover:bg-nb-940 disabled:opacity-40 disabled:hover:bg-transparent"
                >
                  <Icon size={15} className="text-nb-500" /> {label}
                </button>
              ))}
            </MenuPanel>
          </div>
        </div>
      </div>

      <div className="flex min-h-0 flex-1">
        <div className="relative min-w-0 flex-1">
          {!empty && nothingMatches ? (
            <div className="grid h-full place-items-center p-8">
              <EmptyState
                title="Nothing matches this filter"
                description="None of the chosen clusters run the chosen applications. Widen the filter, or clear it to see everything again."
                action={<Button onClick={() => setSp((p) => { const n = new URLSearchParams(p); n.delete('clusters'); n.delete('apps'); return n }, { replace: true })} data-testid="clear-filter"><FilterIcon size={15} /> Clear the filter</Button>}
              />
            </div>
          ) : empty ? (
            <div className="grid h-full place-items-center overflow-y-auto p-6 sm:p-8">
              {started && !started.dismissed ? (
                <div className="flex w-full flex-col items-center gap-5 py-4" data-testid="empty-topology">
                  <div className="text-center">
                    <h2 className="text-base font-medium text-nb-300">Your topology is empty</h2>
                    <p className="mx-auto mt-1 max-w-md text-sm text-nb-500">Nothing is connected yet. These steps take a cluster from nothing to live; Continuum then discovers its nodes, services and traffic itself.</p>
                  </div>
                  <GettingStarted variant="hero" checklist={started.checklist} onConnect={connect.start} />
                  <div className="flex flex-wrap items-center justify-center gap-2 text-sm text-nb-500">
                    or describe a cluster yourself
                    <Button size="sm" onClick={() => setForm({ type: 'cluster' })}><Plus size={14} /> Add manually</Button>
                  </div>
                </div>
              ) : (
                <EmptyState
                  title="Your topology is empty"
                  description="Connect a cluster and let Continuum discover its nodes, services and traffic, or describe one by hand. You can also load the sample under Settings → Import / Export."
                  action={
                    <div className="flex flex-wrap justify-center gap-2">
                      <Button variant="primary" onClick={connect.start} data-testid="empty-connect">
                        <Plug size={16} /> Connect a cluster
                      </Button>
                      <Button onClick={() => setForm({ type: 'cluster' })}><Plus size={16} /> Add manually</Button>
                    </div>
                  }
                />
              )}
            </div>
          ) : isMap ? (
            <MapView selection={selection} onSelect={select} filter={filter} />
          ) : (
            <ReactFlow<TopoNode, Edge>
              nodes={nodes}
              edges={edges}
              nodeTypes={nodeTypes}
              edgeTypes={edgeTypes}
              onNodesChange={onNodesChange}
              onSelectionChange={({ nodes: sel }) => setMultiSelectedIds(sel.map((n) => n.id))}
              onNodeClick={(e, n) => {
                // The local-telemetry antenna badge (nodes.tsx) sits inside a cluster's group box, so a
                // click on it also reaches this handler - check for it first and open that agent's
                // telemetry wizard instead of the normal group-select behaviour.
                const badge = (e.target as HTMLElement).closest?.('[data-local-telemetry-cluster]')
                const clusterId = badge?.getAttribute('data-local-telemetry-cluster')
                const agentId = clusterId ? localOperatorByCluster.get(clusterId)?.agentId : undefined
                if (agentId) { telemetry.start(agentId); return }
                // A multi-select click (shift/ctrl/cmd, matching multiSelectionKeyCode below) has already
                // been folded into React Flow's own selection at the library level by the time this fires,
                // which onSelectionChange picks up into multiSelectedIds - leave it at that. Driving the
                // single-click Inspector `selection` for it too would change selectedRfId and, through it,
                // highlightedIds, but only ever to a single id - which would fight the very multi-selection
                // this click just added to a moment later.
                if (e.shiftKey || e.metaKey || e.ctrlKey) return
                select(fromNode(n))
              }}
              onPaneClick={() => select(null)}
              onEdgeClick={(_, e) => { if (!e.data?.aggregated) select({ kind: 'dependency', id: e.id }) }}
              onEdgeMouseEnter={(_, e) => setHoverEdge(e.id)}
              onEdgeMouseLeave={() => setHoverEdge(null)}
              nodesConnectable={false}
              multiSelectionKeyCode={['Shift', 'Meta', 'Control']}
              minZoom={0.15}
              maxZoom={1.75}
              fitView
              fitViewOptions={{ padding: FIT_PADDING }}
              proOptions={{ hideAttribution: true }}
              colorMode="dark"
            >
              <Background variant={BackgroundVariant.Dots} gap={22} size={1.2} color="var(--color-nb-850)" />
              <Controls showInteractive={false} />
              <MiniMap className="!hidden sm:!block" pannable zoomable nodeColor={miniColor} nodeStrokeWidth={0} maskColor="rgba(22,24,26,0.7)" />
              {/* Follows the current selection instead of sitting in the fixed toolbar: the accent halo
                  (nodes.tsx, driven by highlightedIds above) marks *what* is selected, this sits right next
                  to it as the *action* for it - one click or box-drag, then the thing to do about it is right
                  there, rather than a person having to look away to a toolbar button that may not even be
                  visible depending on scroll/viewport. NodeToolbar accepts an array of node ids and centers
                  itself over their combined bounding box, so this works the same for one clicked service, a
                  shift/ctrl-click group, or a whole box-selected cluster. Hidden on the map plane, same as
                  the toolbar button it replaces was. */}
              <NodeToolbar nodeId={[...highlightedIds]} isVisible={!isMap && selectedServices.length > 0} position={Position.Top} offset={14}>
                <ScopeFromSelection
                  selected={selectedServices}
                  clusters={clusters.filter((c) => !c.deletedAt)}
                  agents={agents}
                  open={openMenu === 'scope'}
                  onOpenChange={(o) => setOpenMenu(o ? 'scope' : null)}
                  onScope={telemetry.start}
                />
              </NodeToolbar>
              <Panel position="bottom-left" className="!mb-3 !ml-16 hidden sm:block">
                <div className="flex items-center gap-4 whitespace-nowrap rounded-lg border border-nb-850 bg-nb-925/95 px-3.5 py-2 text-xs text-nb-400">
                  {TIERS.map((t) => (
                    <span key={t.value} className="flex items-center gap-1.5">
                      <span className="size-2 rounded-full" style={{ background: TIER_COLOR[t.value] }} />
                      {t.label}
                    </span>
                  ))}
                  <span className="h-3 w-px bg-nb-800" />
                  <span className="flex items-center gap-1.5">
                    <svg width="18" height="6"><line x1="0" y1="3" x2="18" y2="3" stroke="#8a96a0" strokeWidth="1.5" strokeDasharray="4 3" /></svg>
                    Cross-{groupBy}
                  </span>
                  {mode === 'application' && showMesh && (
                    <>
                      <span className="h-3 w-px bg-nb-800" />
                      {([['encrypted', 'mTLS', 'Both ends are in the mesh and its policy is strict (or always on)'], ['permissive', 'permissive / partial', 'Policy accepts plaintext, or only one end is in the mesh'], ['bypassed', 'bypass / off', 'A port skips the proxy, or the mesh has mutual TLS switched off']] as const).map(([k, label, hint]) => (
                        <span key={k} className="flex items-center gap-1.5" title={`${hint}. Inferred from configuration, not measured.`}>
                          <svg width="18" height="6"><line x1="0" y1="3" x2="18" y2="3" stroke={VERDICT_COLOR[k]} strokeWidth="2.4" /></svg>
                          {label}
                        </span>
                      ))}
                    </>
                  )}
                  {mode === 'application' && dependencies.length > 0 && (
                    <>
                      <span className="h-3 w-px bg-nb-800" />
                      <span className="flex items-center gap-1.5" title="Traffic was seen on this link; the thicker, the busier">
                        <svg width="18" height="6"><line x1="0" y1="3" x2="18" y2="3" stroke="#8a96a0" strokeWidth="2.4" /></svg>
                        Seen in traffic
                      </span>
                      <span className="flex items-center gap-1.5" title="Declared or entered by a person, but no traffic seen">
                        <svg width="18" height="6"><line x1="0" y1="3" x2="18" y2="3" stroke="#8a96a0" strokeWidth="1.2" strokeDasharray="2 5" /></svg>
                        Not seen
                      </span>
                    </>
                  )}
                </div>
              </Panel>
            </ReactFlow>
          )}
        </div>

        <Inspector selection={selection} onSelect={select} onEdit={editSelection} onClose={() => setSelection(null)} />
      </div>

      {form?.type === 'cluster' && <ClusterForm initial={clusters.find((c) => c.id === form.id) ?? null} onClose={closeForm} />}
      {form?.type === 'node' && (
        <NodeForm
          initial={machines.find((n) => n.id === form.id) ?? null}
          defaultClusterId={selection?.kind === 'cluster' ? selection.id : undefined}
          onClose={closeForm}
        />
      )}
      {form?.type === 'device' && <DeviceForm initial={devices.find((d) => d.id === form.id) ?? null} onClose={closeForm} />}
      {form?.type === 'service' && (
        <ServiceForm
          initial={services.find((w) => w.id === form.id) ?? null}
          defaultClusterId={selection?.kind === 'cluster' ? selection.id : undefined}
          onClose={closeForm}
        />
      )}
      {connect.dialogs}
      {telemetry.dialogs}
    </div>
  )
}

/** Leaves room under the graph for the legend. */
const FIT_PADDING = { top: '4%', left: '4%', right: '4%', bottom: '72px' } as const

export default function TopologyPage() {
  return (
    <ReactFlowProvider>
      <Canvas />
    </ReactFlowProvider>
  )
}
