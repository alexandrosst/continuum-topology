import { RotateCcw } from 'lucide-react'
import { Button, Input } from '@/components/ui/primitives'
import { DEFAULT_POLICY, type Policy } from '@/lib/placement/types'
import { usePolicy } from '@/store/placement'

const ROWS: { key: keyof Policy; label: string; unit: string; help: string; step: number; scale?: number }[] = [
  { key: 'latency', label: 'Round-trip time', unit: 'points per ms', step: 0.1, help: 'Cost of each millisecond of round trip on a connection that is in constant use. Quieter connections count for proportionally less.' },
  { key: 'traffic', label: 'Traffic between sites', unit: 'points per MB/s', step: 1, help: 'Cost of every MB/s that has to cross between two sites (bandwidth, egress fees, exposure).' },
  { key: 'migration', label: 'Copying data', unit: 'points per GB', step: 0.1, help: 'One-off cost of copying persistent volumes to the new place. Subtracted from what a move saves.' },
  { key: 'headroom', label: 'Filling a cluster', unit: 'points at 100 %', step: 1, help: 'Grows from 0 at 80 % of a cluster’s CPU requested to this much when it is full.' },
  { key: 'minBenefit', label: 'Smallest worthwhile gain', unit: '% of current cost', step: 5, scale: 100, help: 'A move must improve the service’s cost by at least this share.' },
  { key: 'minAbsolute', label: '…and at least', unit: 'points', step: 0.5, help: 'Ignore moves that save less than this, however large the share.' },
  { key: 'fallbackMs', label: 'Round trip when nothing is known', unit: 'ms', step: 5, help: 'Assumed for a path nobody measured, declared or could estimate from distance. Deliberately pessimistic.' },
]

/** The weights behind the advice, in words. They are yours to change; the advice re-computes at once. */
export default function PolicyPanel() {
  const { policy, set, reset } = usePolicy()
  const changed = (Object.keys(DEFAULT_POLICY) as (keyof Policy)[]).some((k) => policy[k] !== DEFAULT_POLICY[k])
  return (
    <details className="group rounded-xl border border-nb-850 bg-nb-925" data-testid="policy">
      <summary className="flex cursor-pointer list-none items-center gap-3 px-5 py-3 text-sm text-nb-300">
        <span className="font-medium text-white">Policy: how the advice is weighed</span>
        {changed && <span className="rounded-md bg-amber-400/10 px-2 py-0.5 text-xs text-amber-300">changed from the defaults</span>}
        <span className="ml-auto text-xs text-nb-500 group-open:hidden">Show</span>
      </summary>
      <div className="border-t border-nb-850 px-5 py-4">
        <p className="mb-4 max-w-3xl text-xs leading-5 text-nb-500">
          Everything is scored in points: one point is one millisecond of round trip on a connection in constant use. These weights are a starting point, not a truth; what matters most depends on your workloads. They are kept in this browser and change only the advice you read here. Hard limits (data residency, trust zones, capacity, node selectors, volumes) are never weighed against anything: a move that breaks one is not offered.
        </p>
        <div className="grid gap-x-8 gap-y-4 md:grid-cols-2">
          {ROWS.map((r) => (
            <label key={r.key} className="block">
              <span className="mb-1 block text-sm text-nb-300">{r.label}</span>
              <span className="flex items-center gap-2">
                <Input
                  type="number"
                  min={0}
                  step={r.step}
                  className="w-28"
                  value={Math.round(policy[r.key] * (r.scale ?? 1) * 100) / 100}
                  onChange={(e) => {
                    const n = Number(e.target.value)
                    if (Number.isFinite(n) && n >= 0) set({ [r.key]: n / (r.scale ?? 1) })
                  }}
                  data-testid={`policy-${r.key}`}
                />
                <span className="whitespace-nowrap text-xs text-nb-500">{r.unit}</span>
              </span>
              <span className="mt-1 block text-xs text-nb-500">{r.help}</span>
            </label>
          ))}
        </div>
        <div className="mt-4">
          <Button size="sm" onClick={reset} disabled={!changed}>
            <RotateCcw size={13} /> Back to the defaults
          </Button>
        </div>
      </div>
    </details>
  )
}
