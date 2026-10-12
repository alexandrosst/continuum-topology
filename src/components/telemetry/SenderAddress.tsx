import { Input } from '@/components/ui/primitives'
import { withDefaultPort } from '@/components/operators/OperatorAddress'
import type { RegionalOperator } from '@/lib/types'
import { CANDIDATE_KIND, useAddressCandidates } from '@/lib/useAddressCandidates'

/**
 * Which address of a regional operator THIS cluster dials.
 *
 * The sending side chooses, because only it knows which of the operator's addresses it can reach: a node address on the operator's own
 * network answers from a cluster on that network and from nowhere else. The choice is stored with the destination (nothing about the
 * operator changes, and another cluster sending to it keeps its own), and the commands dial it; the operator is still verified by its
 * stable name, whatever address is dialled. Empty means the address recorded for the operator.
 */
export default function SenderAddress({ operator, value, onChange, testId }: { operator: RegionalOperator; value: string; onChange: (address: string) => void; testId: string }) {
  const found = useAddressCandidates(operator.id)
  const recorded = operator.address ?? ''
  const chosen = withDefaultPort(value)
  const discovered = (found?.candidates ?? []).filter((x) => x.kind !== 'recorded' && x.address !== recorded)
  const onList = chosen === '' || chosen === recorded || discovered.some((x) => x.address === chosen)
  const row = (key: string, selected: boolean, pick: () => void, main: string, sub: string) => (
    <li key={key}>
      <label className="flex cursor-pointer items-start gap-2 rounded-md border border-nb-800 px-3 py-2 text-sm has-[:checked]:border-accent/60" data-testid={`${testId}-option`}>
        <input type="radio" name={`${testId}-pick`} className="mt-1 size-4 accent-[var(--color-accent)]" checked={selected} onChange={pick} aria-label={main} />
        <span className="min-w-0">
          <span className="block truncate font-mono text-xs text-nb-100">{main}</span>
          <span className="block text-xs text-nb-500">{sub}</span>
        </span>
      </label>
    </li>
  )
  return (
    <div className="space-y-2" data-testid={testId}>
      <div className="text-xs font-medium uppercase tracking-wide text-nb-500">Address this cluster dials</div>
      <p className="text-xs text-nb-400">Pick one this cluster can reach: an address on the operator&apos;s own network answers only from clusters on that network.</p>
      <ul className="space-y-1.5">
        {row('recorded', chosen === '', () => onChange(''), recorded || 'The operator’s recorded address', recorded ? 'The address recorded for this operator. Every cluster without a choice of its own uses it.' : 'No address is recorded for this operator yet, so commands dial its name inside its own cluster.')}
        {discovered.map((x) => row(x.id, chosen === x.address, () => onChange(x.address), x.address, `${CANDIDATE_KIND[x.kind]} · ${x.source}`))}
      </ul>
      <label className="block text-xs text-nb-400">
        Another address (host:port)
        <Input
          value={onList ? '' : value}
          onChange={(e) => onChange(e.target.value)}
          placeholder="10.20.0.14:31317"
          className="mt-1 font-mono text-xs"
          data-testid={`${testId}-other`}
        />
      </label>
    </div>
  )
}
