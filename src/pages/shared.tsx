import { Database, Pencil, Scaling, Search, ShieldCheck, Trash2 } from 'lucide-react'
import { Button, Input, Trait } from '@/components/ui/primitives'
import { autoscalerRange, disruptionLabel, totalVolumeGb, volumeSize } from '@/lib/present'
import type { Service } from '@/lib/types'

export function SearchBox({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder: string }) {
  return (
    <div className="relative w-80">
      <Search size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-nb-500" />
      <Input className="pl-9" aria-label={placeholder} value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} />
    </div>
  )
}

export function RowActions({ onEdit, onDelete }: { onEdit: () => void; onDelete: () => void }) {
  return (
    <div className="flex justify-end gap-1">
      <Button variant="ghost" size="sm" aria-label="Edit" onClick={onEdit}>
        <Pencil size={14} />
      </Button>
      <Button variant="ghost" size="sm" aria-label="Delete" onClick={onDelete}>
        <Trash2 size={14} />
      </Button>
    </div>
  )
}

export const matches = (q: string, ...fields: (string | undefined)[]) => {
  const s = q.trim().toLowerCase()
  return !s || fields.some((f) => f?.toLowerCase().includes(s))
}

/** Storage, autoscaling and disruption rules of a service as small chips: what an orchestrator must respect. */
export function ServiceTraits({ w, nodeName }: { w: Service; nodeName: (id: string) => string }) {
  const vols = w.volumes ?? []
  const pinned = vols.filter((v) => v.pinnedNodeIds?.length)
  if (!vols.length && !w.autoscaler && !w.disruption) return null
  return (
    <div className="mt-1 flex flex-wrap gap-1">
      {vols.length > 0 && (
        <Trait
          icon={<Database size={11} />}
          warn={pinned.length > 0}
          title={vols.map((v) => `${v.name}: ${volumeSize(v.sizeGb)}${v.storageClass ? ` on ${v.storageClass}` : ''}${v.pinnedNodeIds?.length ? `, data only on ${v.pinnedNodeIds.map(nodeName).join(', ')}` : ''}`).join('\n')}
        >
          {volumeSize(totalVolumeGb(vols))}
          {pinned.length > 0 && ' · pinned'}
        </Trait>
      )}
      {w.autoscaler && (
        <Trait icon={<Scaling size={11} />} title={`Autoscaled ${autoscalerRange(w.autoscaler)}, now ${w.autoscaler.current}${w.autoscaler.targets?.length ? `; target ${w.autoscaler.targets.join(', ')}` : ''}`}>
          {w.autoscaler.min}–{w.autoscaler.max}
        </Trait>
      )}
      {w.disruption && (
        <Trait icon={<ShieldCheck size={11} />} title={`Disruption budget: ${disruptionLabel(w.disruption)}; ${w.disruption.allowed} pod(s) may be evicted right now`}>
          {disruptionLabel(w.disruption)}
        </Trait>
      )}
    </div>
  )
}
