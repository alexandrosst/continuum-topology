import {
  Background,
  BackgroundVariant,
  Controls,
  getNodesBounds,
  getViewportForBounds,
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
import { toPng } from 'html-to-image'
import { Antenna, Boxes, ChevronDown, Download, Filter as FilterIcon, Package, Plug, Plus, Radio, RotateCcw, ScanEye, Server, SlidersHorizontal, Target, X } from 'lucide-react'
import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useConnectFlow } from '@/components/discovery/ConnectFlow'
import { useTelemetryFlow } from '@/components/telemetry/TelemetryFlow'
import { ClusterForm, DeviceForm, NodeForm, ServiceForm } from '@/components/forms'
import GettingStarted, { useGettingStarted } from '@/components/GettingStarted'
import Inspector, { type Selection } from '@/components/topology/Inspector'
// Lazily loaded, not a plain top-level import: MapView pulls in d3-geo, topojson-client and the
// placement-suggestion engine (usePlacementSuggestions) at its own module top level - real weight
// (~45KB gzipped) that a static import would put on every single visit to this page, Topology being
// the app's default landing route, even though most sessions render the Application or Infrastructure
// view and never open the Map tab at all. The raw map DATA (cities/countries topojson) was already its
// own further-lazy import inside MapView itself (see MapView.tsx's own loadCoarse/loadFine) - this just
// extends the same reasoning to MapView's own code, the one part of that story the module boundary
// here didn't previously cover.
const MapView = lazy(() => import('@/components/topology/MapView'))
import EdgeHoverCard, { type EdgeHoverPos } from '@/components/topology/EdgeHoverCard'
import ScopeFromSelection from '@/components/topology/ScopeFromSelection'
import ViewsMenu from '@/components/topology/ViewsMenu'
import LiveStatus from '@/components/LiveStatus'
import { nodeTypes } from '@/components/topology/nodes'
import { edgeTypes, EdgeStyleContext } from '@/components/topology/OffsetEdge'
import { Button, EmptyState, ICON_MD, ICON_SM, MenuPanel, Select, SkeletonBlock } from '@/components/ui/primitives'
import { PRESS_CLASS } from '@/components/ui/buttonClass'
import FilterMenu from '@/components/topology/FilterMenu'
import { extrasOf, TELEMETRY_SIGNALS } from '@/lib/consent'
import { applyFilter, encodeList, filterActive, hopNeighborhood, isFreshApplicationView, knownOnly, parseFilter } from '@/lib/filter'
import { applyGraphUpdate, buildGraph, cardId, groupId, selectedServiceIds, syncPickEligibility, syncSelected, type TopoEdge, type TopoNode } from '@/lib/graph'
import { lossBand } from '@/lib/metrics'
import { anyMesh, VERDICT_COLOR } from '@/lib/mesh'
import { useAutoPlaceClusters } from '@/lib/usePlacement'
import { usePlan } from '@/lib/placement/usePlacement'
import { parseSel } from '@/lib/search'
import { TIER_COLOR, TIERS, type ClusterLink, type GroupBy, type ViewKind } from '@/lib/types'
import { useOperators } from '@/lib/useOperators'
import { useServer } from '@/store/server'
import { useHistoryView } from '@/store/history'
import { useClusterLinks, useDiscoveryAgents, usePaths, useTopology } from '@/store/topology'

/** Overlay (joined through a tunnel) vs. subnet (same flat network, no tunnel) - a cluster link's own
 *  two-colour palette, independent of the mesh/loss colours above it in precedence (see styledEdges).
 *  Deliberately NOT green/amber/red (those mean healthy/degraded/down elsewhere on this canvas - a
 *  cluster link is a category, not a health signal) and deliberately not the same hex as any TIER_COLOR,
 *  MESH_TONE.control (violet-400, mesh's own control-plane chip) or --color-info (the "detected" badge) -
 *  this exact pair collided with both of those before, and got picked to still have nothing else reuse. */
const CLUSTER_LINK_COLOR: Record<ClusterLink['kind'], string> = {
  overlay: '#e879f9',
  subnet: '#a3e635',
}

type Mode = ViewKind | 'map'
const VIEWS: { value: Mode; label: string; hint: string }[] = [
  { value: 'application', label: 'Application', hint: 'Microservices and the clusters they run in' },
  { value: 'infrastructure', label: 'Infrastructure', hint: 'Nodes (VMs / machines) grouped by cluster' },
  { value: 'map', label: 'Map', hint: 'Where your sites are in the world' },
]

// `lens`, when true, marks a control that changes what the canvas is actually interpreting or
// computing (recolouring edges by a different live signal, e.g.) rather than just drawing or hiding
// something already decided - see the "Lenses" subsection below, where every lens-type Toggle in this
// menu lives together under its own label, the same way this file already splits "Show" from "Layout".
// It renders the small ScanEye glyph DetailRow-style controls elsewhere use for "not just a plain fact"
// (never a second, differently-shaped control: a lens is still switched the same way any other toggle
// here is, it's what it DOES that differs) and keeps the switch itself the same accent colour on, so the
// distinction reads at a glance without the page growing a second control shape to learn.
function Toggle({ checked, onChange, label, disabled, title, lens }: { checked: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean; title?: string; lens?: boolean }) {
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
      {lens && <ScanEye size={ICON_SM} className="shrink-0 text-accent" aria-hidden />}
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
  const { fitView, getNodes } = useReactFlow()
  const [sp, setSp] = useSearchParams()
  const connect = useConnectFlow()
  const telemetry = useTelemetryFlow()
  const started = useGettingStarted('topology')
  // The canvas is where a cluster's placement is actually seen, so it's a fair place to also resolve
  // a missing one silently (see Layout.tsx's comment for why this no longer runs on every route).
  useAutoPlaceClusters()

  // Regional operators aren't part of the central topology store (see PipelinePage's own note): read through the shared, polled
  // hook, so an operator made (or revoked) while the canvas is open gets its arrow without a refresh. An unchanged answer keeps the
  // same array, so a poll that found nothing new does not lay the canvas out again.
  const isAdmin = useServer((s) => s.isAdmin)
  const { operators } = useOperators(isAdmin())

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
  // The default 'curved' bow, or the opt-in rounded-orthogonal 'elbow' style (OffsetEdge.tsx) - a pure
  // rendering preference for every edge on the canvas, not tied to any one of them, so it lives in the same
  // URL-param "view option" family as the toggles above rather than on graph/edge data.
  const edgeStyle: 'curved' | 'elbow' = sp.get('edges') === 'elbow' ? 'elbow' : 'curved'
  // Confirmed overlay/same-subnet facts, drawn in every view/groupBy combination (see graph.ts) - on by
  // default, same as every other "Show" toggle in this menu.
  const showClusterLinks = sp.get('clusterLinks') !== '0'
  // Off by default (unlike showClusterLinks itself): recolors a confirmed cluster link by its live
  // flow-rollup health (ClusterLink.avgLossPct, see tunnels.go's correlateClusterLinks) instead of its
  // fixed overlay/subnet category color - a different question ("is this link actually healthy right
  // now") than the category swatch answers ("what kind of link is this"), so it's an opt-in lens over
  // the always-on category coloring rather than a replacement for it. Meaningless with cluster links
  // hidden altogether, so it only ever applies alongside showClusterLinks.
  const showHealthLens = sp.get('health') === '1' && showClusterLinks
  // Discovery agent boxes and the regional-operator boxes, shown or hidden together as one "system"
  // group - off by default, same as every other extra-detail toggle here (noise/mesh/namespaces/chain),
  // since nothing else on the canvas depends on these being visible (unlike devices/cluster links, which
  // default on).
  const showSystem = sp.get('system') === '1'
  // How many options differ from the defaults, so a hidden option is never a mystery.
  const changedOptions = [!showDevices, showNoise, servicesOnNodes, !links, showLabels, groupBy === 'tier', showMesh, showNamespaces, showChain, edgeStyle === 'elbow', !showClusterLinks, showHealthLens, showSystem].filter(Boolean).length
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
  // Cursor position for EdgeHoverCard (the throughput/latency popover a hovered edge shows - same anchoring
  // pattern as MapView's own LinkCard). Kept separate from `hoverEdge` itself: the id alone is enough to
  // drive the on-edge label reveal above, but the popover also needs to track the mouse to stay near it.
  const [hoverPos, setHoverPos] = useState<EdgeHoverPos | null>(null)
  // The canvas's own bounding box, for EdgeHoverCard to position itself against - set once the ReactFlow
  // wrapper mounts, same ref-callback pattern MapView uses for its own hover cards' `host`.
  const [host, setHost] = useState<HTMLElement | null>(null)
  const [exportingPng, setExportingPng] = useState(false)
  // Renders the graph's own `.react-flow__viewport` element (the panned/zoomed layer that actually holds
  // every card and edge - Controls/MiniMap/Background are separate siblings under `host`, not part of it,
  // so capturing this one element already excludes them without a filter) into a detached, full-bounds PNG:
  // a fixed width/height and an explicit transform temporarily replace whatever pan/zoom is on screen,
  // rather than asking someone to zoom-to-fit and hope nothing is cropped first. html-to-image is the same
  // library React Flow's own "Download Image" example uses for exactly this - canvas-based alternatives
  // (html2canvas and similar) don't handle this library's own CSS custom properties and transforms as
  // reliably.
  const exportPng = useCallback(async () => {
    const viewportEl = host?.querySelector<HTMLElement>('.react-flow__viewport')
    if (!viewportEl) return
    setExportingPng(true)
    try {
      const bounds = getNodesBounds(getNodes())
      if (bounds.width <= 0 || bounds.height <= 0) return
      const maxDim = 1600
      const aspect = bounds.width / bounds.height
      const imageWidth = aspect >= 1 ? maxDim : Math.round(maxDim * aspect)
      const imageHeight = aspect >= 1 ? Math.round(maxDim / aspect) : maxDim
      const viewport = getViewportForBounds(bounds, imageWidth, imageHeight, 0.1, 4, 0.06)
      // The canvas's own dark/light background token (index.css's --color-nb-910, what .react-flow itself
      // paints behind everything) - read live rather than hardcoded so the export matches whichever theme
      // is actually on screen. The literal fallback is that same token's default (dark) value, for the
      // vanishingly unlikely case the variable isn't resolvable at all.
      const bg = getComputedStyle(document.documentElement).getPropertyValue('--color-nb-910').trim() || '#16181a'
      const dataUrl = await toPng(viewportEl, {
        backgroundColor: bg,
        width: imageWidth,
        height: imageHeight,
        pixelRatio: 2,
        style: {
          width: `${imageWidth}px`,
          height: `${imageHeight}px`,
          transform: `translate(${viewport.x}px, ${viewport.y}px) scale(${viewport.zoom})`,
        },
      })
      const a = document.createElement('a')
      a.download = `topology-${mode}-${new Date().toISOString().slice(0, 10)}.png`
      a.href = dataUrl
      a.click()
    } catch {
      // Best-effort: a failed export just means no download happened, nothing else on the canvas is
      // affected (the real DOM/viewport were never touched - only a detached clone toPng renders from).
    } finally {
      setExportingPng(false)
    }
  }, [host, getNodes, mode])
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
  // operatorsView's joinLocal. Only the first approved agent per cluster counts -
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
  const clusterLinks = useClusterLinks()
  const discoveryAgents = useDiscoveryAgents()
  // Placement advice, shown as a small marker on the services it would move.
  const { plan, world } = usePlan()
  const hints = useMemo(() => new Map(plan.recommendations.map((r) => [r.serviceId, world.byCluster.get(r.to)?.name ?? r.to])), [plan, world])

  // Only some clusters or applications, when asked (?clusters=a,b&apps=x). Ids that no longer exist are ignored.
  const rawFilter = useMemo(() => parseFilter(sp), [sp])
  const filter = useMemo(() => knownOnly(rawFilter, { clusters, applications }), [rawFilter, clusters, applications])
  const filtering = filterActive(filter)
  const filteredByAttrs = useMemo(
    () => applyFilter({ clusters, nodes: machines, namespaces, services, devices, dependencies, applications, sites, siteLinks, externalEndpoints }, filter),
    [clusters, machines, namespaces, services, devices, dependencies, applications, sites, siteLinks, externalEndpoints, filter],
  )
  // Backstage-style "max depth": while a service is selected, ?hops=N further narrows the canvas to just
  // its neighborhood (what it calls and what calls it, N steps out) - independent of the cluster/app/kind
  // filter above, and only in effect while that service is actually the current selection.
  const hopsParam = sp.get('hops')
  const hops = hopsParam !== null && /^\d+$/.test(hopsParam) ? Number(hopsParam) : undefined
  const focusId = selection?.kind === 'service' ? selection.id : undefined
  const shown = useMemo(
    () => (hops !== undefined && focusId ? hopNeighborhood(filteredByAttrs, focusId, hops) : filteredByAttrs),
    [filteredByAttrs, hops, focusId],
  )
  const graph = useMemo(
    () =>
      buildGraph(
        { ...shown, operators, discoveryAgents },
        {
          view, groupBy, servicesOnNodes, links, devices: showDevices, noise: showNoise, mesh: showMesh, namespaces: showNamespaces, chain: showChain, paths, hints, localOperators: localOperatorByCluster,
          clusterLinks: showClusterLinks ? clusterLinks : [], showSystem,
        },
      ),
    [shown, operators, discoveryAgents, view, groupBy, servicesOnNodes, links, showDevices, showNoise, showMesh, showNamespaces, showChain, paths, hints, localOperatorByCluster, clusterLinks, showClusterLinks, showSystem],
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

  // React Flow's SelectionListener effect (inside <ReactFlow>) is keyed on this callback's own identity,
  // not just on the selection actually changing - an inline arrow function passed straight in the JSX below
  // gets a fresh identity every render, so it fires again after every single render regardless of whether
  // anything was actually (re)selected. Combined with a plain `setMultiSelectedIds(sel.map(...))` - a new
  // array even when the ids are identical to what's already there - that turns into a genuine unconditional
  // render loop: render -> a "new" onSelectionChange -> React Flow calls it again -> setMultiSelectedIds
  // with a fresh-but-equal array -> React (correctly) re-renders because the reference changed -> repeat,
  // forever, with nothing in between ever actually settling. That is the real "Maximum update depth
  // exceeded" (React error #185) reported on the topology page - not the dragging race fixed elsewhere in
  // this file and in graph.ts, which only applies to filter/view-toggle timing, not this.
  //
  // Fixed the same way every other node/selection sync in this file already is (see syncSelected's and
  // applyGraphUpdate's own doc comments in graph.ts): a stable callback identity via useCallback, and a
  // bail-out to the exact same array reference when the ids didn't actually change, so setMultiSelectedIds
  // is a true no-op - and therefore triggers zero re-renders - once the selection has settled.
  const onSelectionChange = useCallback(({ nodes: sel }: { nodes: TopoNode[] }) => {
    setMultiSelectedIds((prev) => {
      const ids = sel.map((n) => n.id)
      if (ids.length === prev.length && ids.every((id, i) => id === prev[i])) return prev
      return ids
    })
  }, [])

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

  // Re-sync when the model / plane changes (keeps selection highlight). On an ordinary background poll, a
  // node that was already on the canvas keeps the position it has there (a manual drag, or a prior layout
  // pass) instead of jumping back to the graph's freshly computed one, which would otherwise happen every
  // 2-5s even for a poll that changed nothing about this node - see resyncNodes (graph.ts) for the full
  // rule, including why a node React Flow is actively dragging is left completely untouched.
  //
  // An EXPLICIT interaction - a filter, or any other view toggle - gets a full fresh layout instead: see
  // applyGraphUpdate's own doc comment (graph.ts) for why the poll-time merge above leaves stale, messy
  // positions for a filter specifically (survivors keep the same parent, so the merge's own "parent changed"
  // escape hatch never fires for them, even though buildGraph repacked the whole group around them).
  // Every filter and view toggle on this page goes through the URL's search params, and a poll never
  // touches them, so "did `sp` itself change since last render" is exactly that distinction.
  const prevSpRef = useRef(sp)
  useEffect(() => {
    const explicit = prevSpRef.current !== sp
    prevSpRef.current = sp
    setNodes((prev) => applyGraphUpdate(prev, graph.nodes, highlightedIds, explicit))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [graph, setNodes])
  // See syncSelected's own doc comment (graph.ts) for why this needs to both skip a node mid-drag and bail
  // out to the exact same array when nothing changed - together, the fix for a real "Maximum update depth
  // exceeded" crash (React error #185) that a click or drag between two nodes could trigger.
  //
  // Keyed on `selectedRfId` alone, deliberately NOT on `multiSelectedIds` (or `highlightedIds` itself, which
  // mixes both). `selectedRfId` is the single-click Inspector selection, which can change from OUTSIDE the
  // canvas entirely (e.g. clicking a related entity inside the Inspector panel) - React Flow has no way to
  // know about that on its own, so re-applying it here is genuinely necessary. `multiSelectedIds`, on the
  // other hand, is populated FROM React Flow's own selection via onSelectionChange above: by the time a
  // multi-select change reaches this component, React Flow's own nodes already carry the right `.selected`
  // flags for it - that's the very state onSelectionChange just read. Re-running this effect (and therefore
  // re-writing `.selected` on every node) merely because `multiSelectedIds` changed was writing that same
  // information back a second time through a different effect - which React Flow then dutifully reports
  // again via onSelectionChange, changing multiSelectedIds again, re-firing this effect again, forever. A
  // live headless-browser repro caught this as a real, sustained content-level oscillation (not just a
  // referential-identity one, which onSelectionChange's own fix above already closes) between a node just
  // clicked, that plus a leftover multi-selected node, and nothing at all - under rapid view-toggling
  // interleaved with clicks, and it kept looping on its own with no further input needed once started. Only
  // `selectedRfId` actually needs an imposed write here; `highlightedIds` (read fresh from the closure
  // below, not listed as a dependency) still correctly includes both pieces for that write itself - only the
  // trigger is narrowed, closing the loop.
  useEffect(() => {
    setNodes((ns) => syncSelected(ns, highlightedIds))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedRfId, setNodes])

  // Re-fit the viewport whenever the *shape* of the graph changes (not on every edit).
  const shape = useMemo(() => graph.nodes.map((n) => `${n.id}:${n.style?.width}x${n.style?.height}`).join('|'), [graph])
  useEffect(() => {
    // Layout's ErrorBoundary remounts this whole page on every navigation (`key={pathname}` - see its own
    // comment for why that's load-bearing for error recovery, not something to remove here), so this effect
    // - and the animated pan/zoom below - reruns on every single visit to this tab, not just a cold one.
    // Animating that settle is worth it the first time a session sees the canvas (there's no prior viewport
    // to jump from), but repeating a 300ms pan on every revisit is exactly the kind of "not instant" tab
    // switch this is meant to avoid. hasEverFit is module state, not component state, on purpose: it must
    // survive this component's own remount, and only a full page reload should reset it.
    const animate = !hasEverFit
    hasEverFit = true
    const t = setTimeout(() => fitView({ padding: FIT_PADDING, duration: animate ? 300 : 0 }), animate ? 60 : 0)
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
      const cl = e.data?.clusterLink
      // Cluster links get their own two colours, independent of the loss/mesh/crossGroup palette above -
      // they are never a dependency (no quality/mesh verdict can coexist with them), so there's no
      // precedence to resolve, only `hot` (selection focus) still wins. Overlay (purple) vs. subnet (cyan)
      // mirrors the legend below. The health lens is opt-in and only ever applies when there's an actual
      // measured avgLossPct to show - a link with no matched flows yet falls straight back to its fixed
      // category colour rather than a fabricated "healthy" green.
      const clHealthBand = showHealthLens && cl?.avgLossPct !== undefined ? lossBand(cl.avgLossPct) : null
      const clHealthColor = clHealthBand === 'hot' ? '#f87171' : clHealthBand === 'warn' ? '#fbbf24' : clHealthBand === 'ok' ? '#34d399' : null
      const stroke = hot ? '#f68330' : clHealthColor ?? (cl ? CLUSTER_LINK_COLOR[cl.kind] : mv ? VERDICT_COLOR[mv.state] : band === 'hot' ? '#f87171' : band === 'warn' ? '#fbbf24' : e.data?.crossGroup ? '#98a4ae' : '#6f7b85')
      // Seen in traffic: solid, and a touch thicker the busier it is. Only declared (or gone quiet): dotted and
      // thin. Kept close to the declared baseline (1.2) rather than scaling up hard - a busy link should read as
      // "more traffic" without out-weighing the 2.4px used for the current selection/focus.
      const seen = !!e.data?.observed && !e.data?.stale
      const width = hot ? 2.4 : cl ? 1.8 : e.data?.aggregated ? 2 : seen ? 1.2 + 1.0 * (e.data?.weight ?? 0.15) : 1.2
      // A seen edge with no `via` at all can't happen (isObserved only ever sets true alongside via), so
      // this only ever fires for a real conntrack-only edge - one whose traffic numbers, if it shows any,
      // are connection counts only (see EdgeData.via's own comment): a long, open dash reads as "mostly
      // solid but not fully confirmed" without competing with the short, tight '2 5' used for "not seen".
      const conntrackOnly = seen && e.data?.via === 'conntrack'
      return {
        ...e,
        label: showLabel ? e.label : undefined,
        style: {
          stroke,
          strokeWidth: width,
          opacity: dim ? 0.15 : e.data?.stale ? 0.55 : 1,
          // A cluster link is solid for "subnet" (a direct, physical network fact - no tunnel in the way)
          // and dashed for "overlay" (traffic actually travels through a tunnel interface to get there) -
          // a deliberate, different dash from the traffic seen/not-seen convention below, since this was
          // never a question of whether anything was observed.
          strokeDasharray: cl ? (cl.kind === 'overlay' ? '6 4' : undefined) : !e.data?.aggregated && !seen ? '2 5' : conntrackOnly ? '8 4' : undefined,
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
  }, [graph.edges, selection, groupBy, showLabels, showHealthLens])

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

  // React-flow node id -> its own display title, for EdgeHoverCard: an edge's `source`/`target` ARE already
  // that id (cardId/groupId - see makeEdge in graph.ts), whether it's a service/machine/device card or a
  // cluster/tier/operator group box, so this one map names either end of any edge without needing to know
  // which kind of thing it points at.
  const nodeTitleById = useMemo(() => {
    const m = new Map<string, string>()
    for (const n of nodes) m.set(n.id, 'title' in n.data ? n.data.title : n.id)
    return m
  }, [nodes])
  const hoveredEdge = hoverEdge ? edges.find((e) => e.id === hoverEdge) : undefined

  const select = useCallback((s: Selection) => setSelection(s), [])

  const fromNode = (n: TopoNode): Selection => {
    const d = n.data
    if (d.kind === 'group') {
      if (d.extra === 'devices') return { kind: 'site', id: d.entityId }
      if (d.extra === 'agents') return { kind: 'agent', id: d.entityId }
      if (d.extra) return null
      return { kind: d.groupBy === 'cluster' ? 'cluster' : 'tier', id: d.entityId }
    }
    if (d.kind === 'namespace') return null // a visual grouping only, nothing to inspect on its own
    return { kind: d.kind === 'machine' ? 'node' : d.kind, id: d.entityId }
  }

  // "Pick from canvas": dims every entity and un-dims whatever's under the pointer (topology-pick-mode in
  // index.css does the dimming, driven only by this boolean), so a single hover-then-click goes straight to
  // that entity's telemetry wizard - a quicker, more direct alternative to box-selecting one or more service
  // cards first and then using the floating "Define scope" action (ScopeFromSelection below), which stays
  // for the multi-service case this can't cover (one click = one target).
  const [pickMode, setPickMode] = useState(false)

  // What clicking `n` while pickMode is on should do, or null if `n` isn't a valid scope target (a device,
  // an external endpoint, a namespace sub-box, a cluster/service with no connected+approved agent yet).
  // Resolved from the same Selection fromNode already computes, so this stays in sync with whatever a plain
  // click would have selected, rather than re-deriving node kinds a second way.
  const pickTarget = useCallback(
    (n: TopoNode): { agentId: string; scope?: { name: string; namespaces: string[] } } | null => {
      const sel = fromNode(n)
      if (!sel) return null
      if (sel.kind === 'service') {
        const svc = services.find((s) => s.id === sel.id)
        const agent = svc && agents.find((a) => a.clusterId === svc.clusterId && a.status === 'approved')
        return svc && agent ? { agentId: agent.id, scope: { name: `${svc.name} (from topology)`, namespaces: [svc.namespace] } } : null
      }
      if (sel.kind === 'cluster') {
        const agent = agents.find((a) => a.clusterId === sel.id && a.status === 'approved')
        return agent ? { agentId: agent.id } : null
      }
      return null
    },
    // fromNode is intentionally left out: it's a pure function of its argument alone (no closed-over
    // component state), so its identity changing every render never changes what this returns for the same
    // node - including it here would just make pickEligibleIds below recompute on every render for no
    // reason, the exact thing wrapping this in useCallback is meant to avoid.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [services, agents],
  )
  // Escape backs out of pick mode without picking anything - same "give up on this" affordance as every
  // other transient canvas mode (the toolbar popovers above, via their own effect).
  useEffect(() => {
    if (!pickMode) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setPickMode(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [pickMode])

  // Which nodes pickMode could actually do something with, recomputed whenever pick mode turns on or the
  // canvas' own nodes change under it - null while pick mode is off (syncPickEligibility below treats that
  // as "nothing to mark"). Kept separate from pickTarget's own per-click call so hover feedback (index.css's
  // pick-ineligible rule) can show BEFORE a click, not just explain a dead end after one.
  const pickEligibleIds = useMemo(() => {
    if (!pickMode) return null
    const ids = new Set<string>()
    for (const n of nodes) if (pickTarget(n)) ids.add(n.id)
    return ids
  }, [pickMode, nodes, pickTarget])

  useEffect(() => {
    setNodes((ns) => syncPickEligibility(ns, pickEligibleIds))
  }, [pickEligibleIds, setNodes])

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
          {mode === 'application' && selection?.kind === 'service' && (
            <div
              className="flex items-center gap-1 rounded-md border border-nb-800 bg-nb-925 px-1.5 py-1"
              title="Show only this service's neighborhood: what it calls and what calls it, this many steps out"
              data-testid="hops-control"
            >
              <Target size={ICON_SM} className="ml-0.5 shrink-0 text-nb-500" aria-hidden />
              {[1, 2, 3].map((n) => (
                <button
                  key={n}
                  type="button"
                  onClick={() => setParam('hops', hops === n ? null : String(n))}
                  className={clsx(
                    'rounded px-2 py-0.5 text-xs',
                    PRESS_CLASS,
                    hops === n ? 'bg-accent-soft text-accent' : 'text-nb-400 hover:text-nb-300',
                  )}
                  aria-pressed={hops === n}
                  data-testid={`hops-${n}`}
                >
                  {n}
                </button>
              ))}
              {hops !== undefined && (
                <button
                  type="button"
                  onClick={() => setParam('hops', null)}
                  className="rounded px-1 py-0.5 text-nb-500 hover:bg-nb-850 hover:text-nb-300"
                  aria-label="Clear neighborhood filter"
                  data-testid="hops-clear"
                >
                  <X size={ICON_MD} />
                </button>
              )}
            </div>
          )}
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
                <SlidersHorizontal size={ICON_SM} /> <span className="hidden sm:inline">Options</span>
                {changedOptions > 0 && <span className="rounded-full bg-accent-soft px-1.5 text-[11px] font-medium text-accent">{changedOptions}</span>}
              </Button>
              <MenuPanel open={openMenu === 'options'} onClose={() => setOpenMenu(null)} className="w-72 p-2" role="group" aria-label="View options">
                {/* Three subsections: what's drawn at all, what instead changes which signal the canvas
                    is reading (a "lens" - recolouring/reinterpreting what's already drawn rather than
                    adding or removing anything), then how it's all laid out. "Show"/"Layout" mirror
                    FilterMenu's own subsection labels (same classes) so the toolbar's two popovers read as
                    one family instead of two different menus; "Lenses" follows the identical pattern
                    rather than inventing a new one, since a grouped, labelled subsection was already this
                    page's own way of telling two kinds of option apart. */}
                <div className="px-2 pb-1 pt-1 text-xs uppercase tracking-wide text-nb-500">Show</div>
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
                <Toggle checked={showLabels} onChange={(v) => setParam('labels', v ? '1' : null)} label="Edge labels" />
                <Toggle
                  checked={showClusterLinks}
                  onChange={(v) => setParam('clusterLinks', v ? null : '0')}
                  label="Cluster links"
                  title="Clusters confirmed joined by an overlay/tunnel, or sitting on the same flat subnet"
                />
                <Toggle
                  checked={showSystem}
                  onChange={(v) => {
                    setParam('system', v ? '1' : null)
                    // Hiding system entities removes the selected one from the canvas, same reasoning
                    // as hiding devices just above.
                    if (!v && selection?.kind === 'agent') setSelection(null)
                  }}
                  label="System entities"
                  title="The discovery agent running in each cluster, and any regional operator it feeds"
                />

                {/* Lenses: unlike every toggle above (which only ever decides whether something already
                    computed gets drawn), each of these changes what the canvas itself is interpreting -
                    Service mesh swaps an edge's colour from the plain loss/quality read to its mTLS
                    verdict, and Network health swaps a cluster link's fixed overlay/subnet category
                    colour for its live measured loss% - so they get their own labelled group, and each
                    Toggle below also carries the small ScanEye glyph (see Toggle's own `lens` prop) as a
                    second, per-row cue for anyone who lands here without reading the section header.
                    Network health applies in both modes (a cluster-link tunnel is a network fact, not an
                    application-view one - same as Cluster links itself above), so only Service mesh's own
                    row is further gated to the application view. */}
                <div className="mt-1 border-t border-nb-850 px-2 pb-1 pt-2.5 text-xs uppercase tracking-wide text-nb-500">Lenses</div>
                {mode === 'application' && (
                  <>
                    <Toggle
                      lens
                      checked={showMesh}
                      disabled={!hasMesh}
                      onChange={(v) => setParam('mesh', v ? '1' : null)}
                      label="Service mesh"
                      title={hasMesh ? 'Recolour each connection by its mTLS verdict instead of loss/quality, and show which services are in the mesh' : 'No service mesh was found in the connected clusters'}
                    />
                    {!hasMesh && <p className="-mt-0.5 px-2 pb-1 pl-[46px] text-[11px] text-nb-500">No mesh found in your clusters</p>}
                  </>
                )}
                <Toggle
                  lens
                  checked={showHealthLens}
                  disabled={!showClusterLinks}
                  onChange={(v) => setParam('health', v ? '1' : null)}
                  label="Network health"
                  title={
                    showClusterLinks
                      ? "Colour cluster links by their live measured loss%, instead of overlay/subnet category"
                      : "Turn on Cluster links first - there's nothing to colour by health otherwise"
                  }
                />

                <div className="mt-1 border-t border-nb-850 px-2 pb-1 pt-2.5 text-xs uppercase tracking-wide text-nb-500">Layout</div>
                {mode === 'application' && (
                  <Toggle
                    checked={showNamespaces}
                    disabled={groupBy !== 'cluster' || showChain}
                    onChange={(v) => setParam('namespaces', v ? '1' : null)}
                    label="Namespace sub-boxes"
                    title={showChain ? 'Not available in chain layout' : groupBy === 'cluster' ? 'Draw a box per namespace inside each cluster' : 'Only available grouped by cluster'}
                  />
                )}
                {mode === 'application' && !showChain && groupBy !== 'cluster' && (
                  <p className="-mt-0.5 px-2 pb-1 pl-[46px] text-[11px] text-nb-500">Only available grouped by cluster</p>
                )}
                {mode === 'application' && (
                  <Toggle
                    checked={showChain}
                    onChange={(v) => setParam('chain', v ? '1' : null)}
                    label="Chain layout"
                    title="Lay every service out left to right by who calls whom, across every cluster, instead of grouping them into boxes"
                  />
                )}
                {mode === 'application' && showChain && (
                  <p className="-mt-0.5 px-2 pb-1 pl-[46px] text-[11px] text-nb-500">Namespace sub-boxes and Group by aren&apos;t available while this is on</p>
                )}
                <div className={clsx('mt-1 flex items-center justify-between gap-3 px-2 py-1.5 text-sm', showChain ? 'text-nb-600' : 'text-nb-400')}>
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
                <div className="flex items-center justify-between gap-3 px-2 py-1.5 text-sm text-nb-400">
                  Edge style
                  <Select
                    className="h-8 w-32"
                    value={edgeStyle}
                    title="Curved: a gentle bow between boxes. Squared: rounded right-angle routing, closer to a classic flowchart connector."
                    onChange={(e) => setParam('edges', e.target.value === 'elbow' ? 'elbow' : null)}
                  >
                    <option value="curved">Curved</option>
                    <option value="elbow">Squared</option>
                  </Select>
                </div>
              </MenuPanel>
            </div>
          )}

          {!isMap && (
            <Button
              onClick={() => {
                setNodes(graph.nodes)
                // The `shape` effect above only re-fits when node ids/sizes change, which a layout reset
                // never does (same nodes, new positions) - without this, a reset whose new positions happen
                // to land outside the current viewport looked like the button did nothing at all.
                fitView({ padding: FIT_PADDING, duration: 200 })
              }}
              title="Reset the canvas layout - snaps every entity back to its computed position. Your view options (filters, grouping, toggles) are untouched."
              data-testid="reset-layout"
            >
              <RotateCcw size={ICON_SM} /> <span className="hidden sm:inline">Reset layout</span>
            </Button>
          )}

          {!isMap && (
            <Button
              onClick={exportPng}
              disabled={exportingPng}
              title="Save the current canvas as a PNG image, at its full extent (not just what's on screen)"
              data-testid="export-png"
            >
              <Download size={ICON_SM} /> <span className="hidden sm:inline">{exportingPng ? 'Exporting…' : 'Export PNG'}</span>
            </Button>
          )}

          {!isMap && (
            <Button
              variant={pickMode ? 'primary' : undefined}
              onClick={() => setPickMode((v) => !v)}
              aria-pressed={pickMode}
              title={pickMode ? 'Cancel - click a service or cluster to scope it, or press Escape' : 'Pick a service or cluster on the canvas to configure its telemetry, without selecting it first'}
              data-testid="pick-scope"
            >
              <Target size={ICON_SM} /> <span className="hidden sm:inline">Pick from canvas</span>
            </Button>
          )}

          <div className="relative">
            <Button variant="primary" onClick={() => toggleMenu('add')} disabled={inPast} title={inPast ? 'Return to now to add or change things' : undefined}>
              <Plus size={ICON_SM} /> Add <ChevronDown size={ICON_SM} />
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
                  <Icon size={ICON_SM} className="text-nb-500" /> {label}
                </button>
              ))}
            </MenuPanel>
          </div>
        </div>
      </div>

      <div className="flex min-h-0 flex-1">
        <div className="relative min-w-0 flex-1" ref={setHost}>
          {!empty && nothingMatches ? (
            <div className="grid h-full place-items-center p-8">
              <EmptyState
                title="Nothing matches this filter"
                description="None of the chosen clusters run the chosen applications. Widen the filter, or clear it to see everything again."
                action={<Button onClick={() => setSp((p) => { const n = new URLSearchParams(p); n.delete('clusters'); n.delete('apps'); return n }, { replace: true })} data-testid="clear-filter"><FilterIcon size={ICON_SM} /> Clear the filter</Button>}
              />
            </div>
          ) : empty ? (
            <div className="grid h-full place-items-center overflow-y-auto p-6 sm:p-8">
              {started && !started.dismissed ? (
                <div className="flex w-full flex-col items-center gap-5 py-4" data-testid="empty-topology">
                  <div className="text-center">
                    <h2 className="text-base font-medium text-nb-300">Your topology is empty</h2>
                    <p className="mx-auto mt-1 max-w-md text-sm text-nb-500">Nothing is connected yet. These steps take a cluster from nothing to live; Ikhnos then discovers its nodes, services and traffic itself.</p>
                  </div>
                  <GettingStarted variant="hero" checklist={started.checklist} onConnect={connect.start} />
                  <div className="flex flex-wrap items-center justify-center gap-2 text-sm text-nb-500">
                    or describe a cluster yourself
                    <Button size="sm" onClick={() => setForm({ type: 'cluster' })}><Plus size={ICON_SM} /> Add manually</Button>
                  </div>
                </div>
              ) : (
                <EmptyState
                  title="Your topology is empty"
                  description="Connect a cluster and let Ikhnos discover its nodes, services and traffic, or describe one by hand. You can also load the sample under Settings → Import / Export."
                  action={
                    <div className="flex flex-wrap justify-center gap-2">
                      <Button variant="primary" onClick={connect.start} data-testid="empty-connect">
                        <Plug size={ICON_SM} /> Connect a cluster
                      </Button>
                      <Button onClick={() => setForm({ type: 'cluster' })}><Plus size={ICON_SM} /> Add manually</Button>
                    </div>
                  }
                />
              )}
            </div>
          ) : isMap ? (
            <Suspense fallback={<div className="h-full w-full p-4"><SkeletonBlock className="h-full w-full" /></div>}>
              <MapView selection={selection} onSelect={select} filter={filter} />
            </Suspense>
          ) : (
            <EdgeStyleContext.Provider value={edgeStyle}>
            <ReactFlow<TopoNode, Edge>
              className={pickMode ? 'topology-pick-mode' : undefined}
              nodes={nodes}
              edges={edges}
              nodeTypes={nodeTypes}
              edgeTypes={edgeTypes}
              onNodesChange={onNodesChange}
              onSelectionChange={onSelectionChange}
              onNodeClick={(e, n) => {
                // The local-telemetry antenna badge (nodes.tsx) sits inside a cluster's group box, so a
                // click on it also reaches this handler - check for it first and open that agent's
                // telemetry wizard instead of the normal group-select behaviour.
                const badge = (e.target as HTMLElement).closest?.('[data-local-telemetry-cluster]')
                const clusterId = badge?.getAttribute('data-local-telemetry-cluster')
                const agentId = clusterId ? localOperatorByCluster.get(clusterId)?.agentId : undefined
                // Exit pick mode here too, even though this branch doesn't go through pickTarget - the
                // badge is reachable while pick mode is on (it isn't excluded from the dim/hover CSS), and
                // leaving pickMode true after it opens the telemetry wizard used to strand the canvas dimmed
                // with no visible cause once the wizard closed.
                if (agentId) { setPickMode(false); telemetry.start(agentId); return }
                // Same technique, for the per-pod expand panel's own node-name buttons (nodes.tsx's Card):
                // those live inside a SERVICE card but need to select a DIFFERENT entity (the node that
                // pod happens to be scheduled on) - something only this handler, not the card component
                // itself, has the selection setters to do. data-select-node carries that node's id.
                const nodeBtn = (e.target as HTMLElement).closest?.('[data-select-node]')
                const selectNodeId = nodeBtn?.getAttribute('data-select-node')
                if (selectNodeId) {
                  setMultiSelectedIds((prev) => (prev.length === 0 ? prev : []))
                  select({ kind: 'node', id: selectNodeId })
                  // syncSelected checks React Flow node ids, which are cardId(...)-prefixed - not the
                  // raw MachineNode id data-select-node carries (see cardId's call sites in graph.ts).
                  setNodes((ns) => syncSelected(ns, new Set([cardId(selectNodeId)])))
                  return
                }
                // Pick mode takes over the click entirely - one click picks (or, for an ineligible node,
                // just cancels) rather than also falling through to a normal select.
                if (pickMode) {
                  setPickMode(false)
                  const picked = pickTarget(n)
                  if (picked) telemetry.start(picked.agentId, picked.scope)
                  return
                }
                // A multi-select click (shift/ctrl/cmd, matching multiSelectionKeyCode below) has already
                // been folded into React Flow's own selection at the library level by the time this fires,
                // which onSelectionChange picks up into multiSelectedIds - leave it at that. Driving the
                // single-click Inspector `selection` for it too would change selectedRfId and, through it,
                // highlightedIds, but only ever to a single id - which would fight the very multi-selection
                // this click just added to a moment later.
                if (e.shiftKey || e.metaKey || e.ctrlKey) return
                // A plain click replaces whatever was selected with just this one node - that's the whole
                // point of a plain click vs. a shift/ctrl one. But a *prior* shift/ctrl-click or box-drag
                // leaves multiSelectedIds populated, and nothing about changing selectedRfId below clears
                // it on its own: the effect that paints `.selected` onto the canvas is keyed on
                // selectedRfId alone (see its own doc comment for why), so a plain re-click of a node that
                // was ALREADY the single-click selection before the multi-select even started changes
                // nothing about selectedRfId's own VALUE - same string, so React correctly skips that
                // effect - and React Flow's own native click handling also does nothing in exactly this
                // case (it only adds-or-removes a node from the selection; a node that's already selected
                // with no multi-select key held gets neither). Nothing was left to paint `.selected`
                // correctly, a real, reproducible bug (a plain click on one card, then another - or even
                // the same one again - left every previously multi-selected card still highlighted).
                // Fixed directly here instead of widening the effect's own dependency array (which is
                // deliberately narrow - see its comment on the render loop that made it that way): clear
                // the stale multi-selection and paint exactly this one node's `.selected` eagerly, right in
                // the click handler itself, so the result never depends on whether some other effect
                // happens to re-run afterward. syncSelected itself still no-ops (same array back) when the
                // canvas already matches, so this costs nothing on the common case where nothing was stale.
                setMultiSelectedIds((prev) => (prev.length === 0 ? prev : []))
                select(fromNode(n))
                setNodes((ns) => syncSelected(ns, new Set([n.id])))
              }}
              onPaneClick={() => {
                if (pickMode) { setPickMode(false); return }
                // Closes the Inspector (the single-click `selection`) - but a prior shift/ctrl-click or
                // box-drag multi-selection is tracked entirely separately (`multiSelectedIds`, populated
                // from React Flow's own selection via onSelectionChange - see its doc comment) and
                // `select(null)` alone never touched it, so clicking empty canvas after multi-selecting a
                // few cards left them all still highlighted, the floating scope toolbar still open, and
                // React Flow's own `.selected` flags still set - a real, reproducible gap (confirmed via a
                // live-browser check, not just reasoning about the code), not just a hypothetical one.
                // Cleared directly here, on the node array itself, rather than only through
                // setMultiSelectedIds: the effect that would otherwise re-sync `.selected` from
                // highlightedIds is deliberately keyed on `selectedRfId` alone (see its own doc comment, on
                // why watching multiSelectedIds too would loop), so a multi-select-only clear (no Inspector
                // selection open at all) would never reach it.
                select(null)
                setMultiSelectedIds((prev) => (prev.length === 0 ? prev : []))
                setNodes((ns) => syncSelected(ns, new Set()))
              }}
              onEdgeClick={(_, e) => { if (!e.data?.aggregated) select({ kind: 'dependency', id: e.id }) }}
              onEdgeMouseEnter={(e, edge) => { setHoverEdge(edge.id); setHoverPos({ cx: e.clientX, cy: e.clientY }) }}
              onEdgeMouseMove={(e) => setHoverPos({ cx: e.clientX, cy: e.clientY })}
              onEdgeMouseLeave={() => { setHoverEdge(null); setHoverPos(null) }}
              nodesConnectable={false}
              nodesDraggable={!pickMode}
              multiSelectionKeyCode={['Shift', 'Meta', 'Control']}
              minZoom={0.15}
              maxZoom={1.75}
              fitView
              fitViewOptions={FIT_VIEW_OPTIONS}
              proOptions={PRO_OPTIONS}
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
                {/* flex-wrap (plus the max-w below) lets a long legend - everything explained can be on at
                    once: tiers, cross-group, mesh, traffic, telemetry, cluster links - wrap onto a second
                    line on a narrower viewport or with the Inspector open, rather than running off the
                    right edge of the canvas with no way to see the rest of it. whitespace-nowrap is kept on
                    the row so wrapping only ever happens between entries, never mid-label. */}
                <div className="flex max-w-[min(92vw,720px)] flex-wrap items-center gap-x-4 gap-y-1.5 whitespace-nowrap rounded-lg border border-nb-850 bg-nb-925/95 px-3.5 py-2 text-xs text-nb-400">
                  {TIERS.map((t) => (
                    <span key={t.value} className="flex items-center gap-1.5">
                      <span className="size-2 rounded-full" style={{ background: TIER_COLOR[t.value] }} />
                      {t.label}
                    </span>
                  ))}
                  <span className="h-3 w-px bg-nb-800" />
                  <span
                    className="flex items-center gap-1.5"
                    title="A lighter line marks a dependency that crosses a cluster boundary - a separate, independent signal from the solid/dashed traffic styles below. A cross-cluster edge can be either, depending on whether traffic has actually been seen on it."
                  >
                    {/* Solid, not dashed: crossGroup-ness is a colour-only signal (a lighter grey stroke, see
                        the edge style computation above) that combines independently with the seen/not-seen
                        dash pattern on the right - a cross-cluster edge that's also seen in traffic renders
                        solid, and one that isn't renders dotted, same as any other edge. A dashed swatch here
                        used to imply cross-cluster edges are always dashed, which isn't true and duplicated
                        what the "Not seen" swatch already means; the earlier version's colour (#8a96a0) is
                        also now the exact shade the edges themselves use (#98a4ae), not just an approximation. */}
                    <svg width="18" height="6"><line x1="0" y1="3" x2="18" y2="3" stroke="#98a4ae" strokeWidth="1.5" /></svg>
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
                      {graph.edges.some((e) => e.data?.via === 'conntrack' && e.data?.observed && !e.data?.stale) && (
                        <span className="flex items-center gap-1.5" title="Seen by conntrack only - no eBPF collector on that node, so there's no byte count or retransmit data behind it, connections only">
                          <svg width="18" height="6"><line x1="0" y1="3" x2="18" y2="3" stroke="#8a96a0" strokeWidth="1.2" strokeDasharray="8 4" /></svg>
                          Connections only
                        </span>
                      )}
                    </>
                  )}
                  {/* Both entries only appear once something on the canvas actually needs them explained -
                      same "don't explain what isn't there" rule the mesh/traffic entries above already
                      follow. Operator edges can appear in either mode (buildGraph draws them unconditionally
                      once an operator box is on the canvas), so this checks the nodes directly rather than
                      gating on `mode` the way the application-only entries above do. */}
                  {graph.nodes.some((n) => n.data.kind === 'group' && n.data.extra === 'operators') && (
                    <>
                      <span className="h-3 w-px bg-nb-800" />
                      <span className="flex items-center gap-1.5" title="A cluster feeding a regional operator - a declared relationship (its source clusters), not measured traffic">
                        <svg width="18" height="6"><line x1="0" y1="3" x2="18" y2="3" stroke="#8a96a0" strokeWidth="1.6" strokeDasharray="1 4" /></svg>
                        Telemetry
                      </span>
                    </>
                  )}
                  {/* Same "only explain what's actually on the canvas" rule as the operator entry above. */}
                  {graph.nodes.some((n) => n.data.kind === 'group' && n.data.extra === 'agents') && (
                    <>
                      <span className="h-3 w-px bg-nb-800" />
                      <span className="flex items-center gap-1.5" title="The discovery agent that serves this cluster - a declared relationship (one agent, one cluster), not measured traffic">
                        <svg width="18" height="6"><line x1="0" y1="3" x2="18" y2="3" stroke="#8a96a0" strokeWidth="1.6" strokeDasharray="1 4" /></svg>
                        Monitors
                      </span>
                    </>
                  )}
                  {/* Same "only explain what's actually on the canvas" rule as the operator entry just above.
                      Both swatches are confirmed from real kernel-reported routing/address data on both
                      sides (see ClusterLink's own doc) - never a guess from naming or a declared exposure
                      flag, which is worth saying here since every other colour on this canvas means either
                      "seen in traffic" or "inferred from configuration". */}
                  {graph.edges.some((e) => e.data?.clusterLink) && (
                    <>
                      <span className="h-3 w-px bg-nb-800" />
                      {showHealthLens ? (
                        <>
                          <span className="flex items-center gap-1.5" title="Average measured loss% across the flows matched onto this cluster link is under 1%">
                            <svg width="18" height="6"><line x1="0" y1="3" x2="18" y2="3" stroke="#34d399" strokeWidth="1.8" /></svg>
                            Healthy
                          </span>
                          <span className="flex items-center gap-1.5" title="Average measured loss% across the flows matched onto this cluster link is 1-5%">
                            <svg width="18" height="6"><line x1="0" y1="3" x2="18" y2="3" stroke="#fbbf24" strokeWidth="1.8" /></svg>
                            Degraded
                          </span>
                          <span className="flex items-center gap-1.5" title="Average measured loss% across the flows matched onto this cluster link is 5% or higher">
                            <svg width="18" height="6"><line x1="0" y1="3" x2="18" y2="3" stroke="#f87171" strokeWidth="1.8" /></svg>
                            Unhealthy
                          </span>
                          <span className="flex items-center gap-1.5 text-nb-500" title="No flows have been matched onto this cluster link's confirmed tunnel yet, so it keeps its overlay/subnet category colour until one is">
                            <svg width="18" height="6"><line x1="0" y1="3" x2="18" y2="3" stroke={CLUSTER_LINK_COLOR.overlay} strokeWidth="1.8" strokeDasharray="6 4" /></svg>
                            No data yet
                          </span>
                        </>
                      ) : (
                        <>
                          <span className="flex items-center gap-1.5" title="Clusters joined through an overlay/tunnel interface - confirmed from each side's own routing data, not a guess">
                            <svg width="18" height="6"><line x1="0" y1="3" x2="18" y2="3" stroke={CLUSTER_LINK_COLOR.overlay} strokeWidth="1.8" strokeDasharray="6 4" /></svg>
                            Overlay link
                          </span>
                          <span className="flex items-center gap-1.5" title="Clusters whose nodes sit on the very same flat network segment, with no tunnel at all - confirmed from each side's own address data, not a guess">
                            <svg width="18" height="6"><line x1="0" y1="3" x2="18" y2="3" stroke={CLUSTER_LINK_COLOR.subnet} strokeWidth="1.8" /></svg>
                            Same subnet
                          </span>
                        </>
                      )}
                    </>
                  )}
                  {localOperatorByCluster.size > 0 && (
                    <>
                      <span className="h-3 w-px bg-nb-800" />
                      <span className="flex items-center gap-1.5" title="This badge on a cluster box means a local operator (an approved agent with telemetry signals on) is running there - click it to configure">
                        <Antenna size={ICON_SM} className="text-accent" />
                        Local telemetry
                      </span>
                    </>
                  )}
                </div>
              </Panel>
            </ReactFlow>
            </EdgeStyleContext.Provider>
          )}
          {hoveredEdge && hoverPos && (
            <EdgeHoverCard
              edge={hoveredEdge}
              pos={hoverPos}
              host={host}
              fromName={nodeTitleById.get(hoveredEdge.source) ?? hoveredEdge.source}
              toName={nodeTitleById.get(hoveredEdge.target) ?? hoveredEdge.target}
            />
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
const FIT_VIEW_OPTIONS = { padding: FIT_PADDING }
const PRO_OPTIONS = { hideAttribution: true }
// Whether the canvas has already animated its initial fitView once this session - see the effect above.
let hasEverFit = false

export default function TopologyPage() {
  return (
    <ReactFlowProvider>
      <Canvas />
    </ReactFlowProvider>
  )
}
