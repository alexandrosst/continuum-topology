import { Crosshair } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { Button, MenuPanel } from '@/components/ui/primitives'
import { encodeList } from '@/lib/filter'
import type { Agent, Cluster, Service } from '@/lib/types'

/**
 * "Define scope from selection": box-select some service cards on the canvas (already works today via
 * React Flow's own shift-drag select - nothing here changes that), then jump straight into that cluster's
 * telemetry wizard with a scope draft pre-filled from the selection's distinct namespaces. Closes the loop
 * between *seeing* the topology and *instrumenting* it, instead of retyping the same namespace list by hand.
 *
 * Namespace-only, not cluster-aware, because that's exactly what a telemetry scope override already is
 * (`ScopeOverrideInput` in `src/lib/install.ts`) - a selection spanning several clusters still collects one
 * flat namespace set, the person just picks which cluster's install to apply it to first (namespace names
 * are commonly reused across clusters by convention, so applying the same draft to more than one, one at a
 * time, is a legitimate if manual path).
 *
 * Renders nothing when the selection has no service with a connected, approved agent to hand the scope to -
 * there is nowhere for the quick action to go yet.
 */
export default function ScopeFromSelection({
  selected,
  clusters,
  agents,
  open,
  onOpenChange,
}: {
  /** The services resolved from the current canvas selection (already filtered to service cards). */
  selected: Service[]
  clusters: Cluster[]
  agents: Agent[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const navigate = useNavigate()

  if (selected.length === 0) return null

  const namespaces = [...new Set(selected.map((s) => s.namespace))]
  const clusterIds = [...new Set(selected.map((s) => s.clusterId))]
  const targets = clusterIds
    .map((id) => ({ cluster: clusters.find((c) => c.id === id), agent: agents.find((a) => a.clusterId === id && a.status === 'approved') }))
    .filter((t): t is { cluster: Cluster; agent: Agent } => !!t.cluster && !!t.agent)
  if (targets.length === 0) return null

  const goTo = (clusterName: string, agentId: string) => {
    const name = targets.length > 1 ? `${clusterName} scope (from topology)` : 'Scope from topology selection'
    const params = new URLSearchParams()
    params.set('openAgent', agentId)
    params.set('scopeName', name)
    params.set('scopeNamespaces', encodeList(namespaces) ?? '')
    navigate(`/agents?${params.toString()}`)
    onOpenChange(false)
  }

  const label = `Define scope (${selected.length} service${selected.length === 1 ? '' : 's'})`

  if (targets.length === 1) {
    return (
      <Button onClick={() => goTo(targets[0].cluster.name, targets[0].agent.id)} data-testid="scope-from-selection">
        <Crosshair size={15} /> <span className="hidden sm:inline">{label}</span>
      </Button>
    )
  }

  return (
    <div className="relative">
      <Button onClick={() => onOpenChange(!open)} aria-haspopup="dialog" aria-expanded={open} data-testid="scope-from-selection">
        <Crosshair size={15} /> <span className="hidden sm:inline">{label}</span>
      </Button>
      <MenuPanel open={open} onClose={() => onOpenChange(false)} className="w-72 overflow-hidden" role="dialog" aria-label="Which cluster's telemetry to scope">
        <p className="border-b border-nb-850 px-3 py-2 text-xs text-nb-500">The selection spans {targets.length} clusters - pick one to start from. The same {namespaces.length} namespace{namespaces.length === 1 ? '' : 's'} carries over either way.</p>
        <div className="max-h-64 overflow-y-auto p-1">
          {targets.map((t) => (
            <button
              key={t.cluster.id}
              className="block w-full rounded-md px-3 py-2 text-left text-sm text-nb-300 hover:bg-nb-940"
              onClick={() => goTo(t.cluster.name, t.agent.id)}
              data-testid="scope-from-selection-target"
            >
              {t.cluster.name}
            </button>
          ))}
        </div>
      </MenuPanel>
    </div>
  )
}
