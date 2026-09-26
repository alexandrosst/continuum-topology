import {
  Background,
  BackgroundVariant,
  Controls,
  MiniMap,
  Panel,
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
import { ClusterForm, DeviceForm, NodeForm, ServiceForm } from '@/components/forms'
import GettingStarted, { useGettingStarted } from '@/components/GettingStarted'
import Inspector, { type Selection } from '@/components/topology/Inspector'
import MapView from '@/components/topology/MapView'
import ViewsMenu from '@/components/topology/ViewsMenu'
import LiveStatus from '@/components/LiveStatus'
import { nodeTypes } from '@/components/topology/nodes'
import { edgeTypes } from '@/components/topology/OffsetEdge'
import { Button, EmptyState, MenuPanel, Select } from '@/components/ui/primitives'
import { PRESS_CLASS } from '@/components/ui/buttonClass'
import FilterMenu from '@/components/topology/FilterMenu'
import { applyFilter, encodeList, filterActive, knownOnly, parseFilter } from '@/lib/filter'
import { buildGraph, cardId, groupId, type TopoEdge, type TopoNode } from '@/lib/graph'
import { lossBand } from '@/lib/metrics'
import { anyMesh, VERDICT_COLOR } from '@/lib/mesh'
import { useAutoPlaceClusters } from '@/lib/usePlacement'
import { usePlan } from '@/lib/placement/usePlacement'
import { parseSel } from '@/lib/search'
import { TIER_COLOR, TIERS, type GroupBy, type ViewKind } from '@/lib/types'
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
type MenuKey = 'filter' | 'views' | 'options' | 'add'

function Canvas() {
  const topology = useTopology()
  const { fitView } = useReactFlow()
  const [sp, setSp] = useSearchParams()
  const connect = useConnectFlow()
  const started = useGettingStarted('topology')
  // The canvas is where a cluster's placement is actually seen, so it's a fair place to also resolve
  // a missing one silently (see Layout.tsx's comment for why this no longer runs on every route).
  useAutoPlaceClusters()

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
  // How many options differ from the defaults, so a hidden option is never a mystery.
  const changedOptions = [!showDevices, showNoise, servicesOnNodes, !links, showLabels, groupBy === 'tier', showMesh, showNamespaces].filter(Boolean).length
  const setParam = (k: string, v: string | null) =>
    setSp((p) => {
      const n = new URLSearchParams(p)
      if (v === null) n.delete(k)
      else n.set(k, v)
      return n
    }, { replace: true })

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

  const { clusters, nodes: machines, namespaces, services, devices, dependencies, applications, sites, siteLinks, externalEndpoints } = topology
  // Discovered records come from the server with the first refresh, after the workspace loads: a link to one waits for them.
  const observedReady = useServer((s) => s.status === 'disconnected' || s.state !== undefined)
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
    () => buildGraph(shown, { view, groupBy, servicesOnNodes, links, devices: showDevices, noise: showNoise, mesh: showMesh, namespaces: showNamespaces, paths, hints }),
    [shown, view, groupBy, servicesOnNodes, links, showDevices, showNoise, showMesh, showNamespaces, paths, hints],
  )
  const nothingMatches = filtering && shown.clusters.length === 0 && shown.devices.length === 0

  const [nodes, setNodes, onNodesChange] = useNodesState<TopoNode>(graph.nodes)

  // React Flow id of the current selection (if it is visible in this plane).
  const selectedRfId = useMemo(() => {
    if (!selection) return null
    if (selection.kind === 'cluster') return groupBy === 'cluster' ? groupId(selection.id) : null
    if (selection.kind === 'tier') return groupId(selection.id)
    if (selection.kind === 'site') return groupId(`dev:${selection.id}`)
    return cardId(selection.id)
  }, [selection, groupBy])

  // Re-sync when the model / plane changes (keeps selection highlight). A node that was already on the
  // canvas keeps the position it has there (a manual drag, or a prior layout pass) instead of jumping back
  // to the graph's freshly computed one - which would otherwise happen on every poll, even one that changed
  // nothing about this node, because `graph` gets a new identity whenever any upstream data is refreshed.
  useEffect(() => {
    setNodes((prev) => {
      const prevById = new Map(prev.map((n) => [n.id, n]))
      return graph.nodes.map((n) => {
        const old = prevById.get(n.id)
        return { ...n, position: old ? old.position : n.position, selected: n.id === selectedRfId }
      })
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [graph, setNodes])
  useEffect(() => {
    setNodes((ns) => ns.map((n) => (n.selected === (n.id === selectedRfId) ? n : { ...n, selected: n.id === selectedRfId })))
  }, [selectedRfId, setNodes])

  // Re-fit the viewport whenever the *shape* of the graph changes (not on every edit).
  const shape = useMemo(() => graph.nodes.map((n) => `${n.id}:${n.style?.width}x${n.style?.height}`).join('|'), [graph])
  useEffect(() => {
    const t = setTimeout(() => fitView({ padding: FIT_PADDING, duration: 300 }), 60)
    return () => clearTimeout(t)
  }, [shape, fitView])

  // Edge highlighting for the current selection.
  const edges = useMemo<TopoEdge[]>(() => {
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
      const showLabel = showLabels || hot || hoverEdge === e.id
      const q = e.data?.quality
      const band = q ? lossBand(q.lossPct) : 'ok'
      // A link that loses connection attempts is coloured by how badly; otherwise grey, or orange when it is the focus.
      const mv = e.data?.mesh
      const stroke = hot ? '#f68330' : mv ? VERDICT_COLOR[mv.state] : band === 'hot' ? '#f87171' : band === 'warn' ? '#fbbf24' : e.data?.crossGroup ? '#98a4ae' : '#6f7b85'
      // Seen in traffic: solid, and thicker the busier it is. Only declared (or gone quiet): dotted and thin.
      const seen = !!e.data?.observed && !e.data?.stale
      const width = hot ? 2.4 : e.data?.aggregated ? 2 : seen ? 1.4 + 2.2 * (e.data?.weight ?? 0.15) : 1.2
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
        labelBgStyle: { fill: '#16181a', fillOpacity: 0.95 },
        labelBgPadding: [6, 3] as [number, number],
        labelBgBorderRadius: 4,
        markerEnd: e.markerEnd && typeof e.markerEnd === 'object' ? { ...e.markerEnd, color: stroke } : e.markerEnd,
      }
    })
  }, [graph.edges, selection, groupBy, showLabels, hoverEdge])

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
      {/* Toolbar */}
      <div className="flex min-h-14 shrink-0 flex-wrap items-center gap-x-4 gap-y-2 border-b border-nb-850 bg-nb-920 px-4 py-2 sm:px-5">
        <h1 className="text-base font-medium text-white">Topology</h1>
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
                mode === v.value ? 'bg-nb-850 text-white' : 'text-nb-400 hover:text-nb-300',
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
                for (const [k, v] of [['clusters', encodeList(f.clusters)], ['apps', encodeList(f.apps)]] as const) {
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
                    disabled={groupBy !== 'cluster'}
                    onChange={(v) => setParam('namespaces', v ? '1' : null)}
                    label="Namespace sub-boxes"
                    title={groupBy === 'cluster' ? 'Draw a box per namespace inside each cluster' : 'Only available grouped by cluster'}
                  />
                )}
                <div className="mt-1 flex items-center justify-between gap-3 border-t border-nb-850 px-2 pb-1 pt-2.5 text-sm text-nb-400">
                  Group by
                  <Select
                    className="h-8 w-32"
                    value={groupBy}
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
                    <h2 className="text-base font-medium text-white">Your topology is empty</h2>
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
              onNodeClick={(_, n) => select(fromNode(n))}
              onPaneClick={() => select(null)}
              onEdgeClick={(_, e) => { if (!e.data?.aggregated) select({ kind: 'dependency', id: e.id }) }}
              onEdgeMouseEnter={(_, e) => setHoverEdge(e.id)}
              onEdgeMouseLeave={() => setHoverEdge(null)}
              nodesConnectable={false}
              minZoom={0.15}
              maxZoom={1.75}
              fitView
              fitViewOptions={{ padding: FIT_PADDING }}
              proOptions={{ hideAttribution: true }}
              colorMode="dark"
            >
              <Background variant={BackgroundVariant.Dots} gap={22} size={1.2} color="#2b2f33" />
              <Controls showInteractive={false} />
              <MiniMap className="!hidden sm:!block" pannable zoomable nodeColor={miniColor} nodeStrokeWidth={0} maskColor="rgba(22,24,26,0.7)" />
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
