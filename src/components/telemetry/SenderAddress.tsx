import { Button, Input } from '@/components/ui/primitives'
import { withDefaultPort } from '@/components/operators/OperatorAddress'
import type { RegionalOperator } from '@/lib/types'
import { CANDIDATE_KIND, useAddressCandidates } from '@/lib/useAddressCandidates'
import { useAddressTests, type AddressTestView } from '@/lib/useAddressTests'

/**
 * Which address of a regional operator THIS cluster dials.
 *
 * The sending side chooses, because only it knows which of the operator's addresses it can reach: a node address on the operator's own
 * network answers from a cluster on that network and from nowhere else. The choice is stored with the destination (nothing about the
 * operator changes, and another cluster sending to it keeps its own), and the commands dial it; the operator is still verified by its
 * stable name, whatever address is dialled. Empty means the address recorded for the operator.
 */
/** What a test found, in words about the cluster that made it. */
function TestResult({ test, from, testId }: { test: AddressTestView; from: string; testId: string }) {
  const who = from || 'this cluster'
  if (test.state === 'pending') {
    return test.timedOut ? (
      <span className="text-warn" data-testid={testId} data-state="timeout">No answer from {who} yet: its agent may be offline, or connection timing may be switched off there.</span>
    ) : (
      <span className="text-nb-400" data-testid={testId} data-state="pending">Testing from {who}…</span>
    )
  }
  if (test.state === 'reachable') {
    const ms = test.rttP50Ms ? `${Math.round(test.rttP50Ms * 10) / 10} ms` : 'under a millisecond'
    return (
      <span className="text-ok" data-testid={testId} data-state="reachable">
        Reachable from {who} ({ms}){test.failed ? `, but ${test.failed} of ${test.samples} attempts failed` : ''}.
      </span>
    )
  }
  return (
    <span className="text-bad" data-testid={testId} data-state="unreachable">
      No connection from {who}: all {test.samples} attempts failed. It is not reachable from there, or nothing listens on that port.
    </span>
  )
}

export default function SenderAddress({ operator, value, onChange, testId, agentId, clusterName }: { operator: RegionalOperator; value: string; onChange: (address: string) => void; testId: string; /** The agent of the cluster that will send: when known, an address can be tried from it. */ agentId?: string; clusterName?: string }) {
  const found = useAddressCandidates(operator.id)
  const t = useAddressTests(agentId)
  const recorded = operator.address ?? ''
  const chosen = withDefaultPort(value)
  const discovered = (found?.candidates ?? []).filter((x) => x.kind !== 'recorded' && x.address !== recorded)
  const onList = chosen === '' || chosen === recorded || discovered.some((x) => x.address === chosen)
  // Results are kept under the address as the server normalised it: the receiver's port when none was given.
  const result = (address: string) => (address && t.tests[withDefaultPort(address)] ? <TestResult test={t.tests[withDefaultPort(address)]} from={clusterName ?? ''} testId={`${testId}-result`} /> : null)
  const testButton = (address: string, label: string) =>
    t.available && address ? (
      <Button type="button" size="sm" onClick={() => void t.start([address])} disabled={t.busy} aria-label={`Test ${label} from this cluster`} data-testid={`${testId}-test`}>
        Test
      </Button>
    ) : null
  const row = (key: string, selected: boolean, pick: () => void, main: string, sub: string, address: string) => (
    <li key={key} className="flex items-start gap-2 rounded-md border border-nb-800 px-3 py-2 text-sm has-[:checked]:border-accent/60" data-testid={`${testId}-option`}>
      <label className="flex min-w-0 flex-1 cursor-pointer items-start gap-2">
        <input type="radio" name={`${testId}-pick`} className="mt-1 size-4 accent-[var(--color-accent)]" checked={selected} onChange={pick} aria-label={main} />
        <span className="min-w-0">
          <span className="block truncate font-mono text-xs text-nb-100">{main}</span>
          <span className="block text-xs text-nb-500">{sub}</span>
          <span className="block text-xs">{result(address)}</span>
        </span>
      </label>
      {testButton(address, main)}
    </li>
  )
  return (
    <div className="space-y-2" data-testid={testId}>
      <div className="text-xs font-medium uppercase tracking-wide text-nb-500">Address this cluster dials</div>
      <p className="text-xs text-nb-400">Pick one this cluster can reach: an address on the operator&apos;s own network answers only from clusters on that network.</p>
      <ul className="space-y-1.5">
        {row('recorded', chosen === '', () => onChange(''), recorded || 'The operator’s recorded address', recorded ? 'The address recorded for this operator. Every cluster without a choice of its own uses it.' : 'No address is recorded for this operator yet, so commands dial its name inside its own cluster.', recorded)}
        {discovered.map((x) => row(x.id, chosen === x.address, () => onChange(x.address), x.address, `${CANDIDATE_KIND[x.kind]} · ${x.source}`, x.address))}
      </ul>
      <label className="block text-xs text-nb-400">
        Another address (host:port)
        <span className="mt-1 flex items-center gap-2">
          <Input
            value={onList ? '' : value}
            onChange={(e) => onChange(e.target.value)}
            placeholder="10.20.0.14:31317"
            className="font-mono text-xs"
            data-testid={`${testId}-other`}
          />
          {!onList && testButton(chosen, chosen)}
        </span>
        {!onList && <span className="mt-1 block">{result(chosen)}</span>}
      </label>
      {t.available && (
        <p className="text-xs text-nb-500" data-testid={`${testId}-test-note`}>
          {t.error ? <span className="text-bad" role="alert">{t.error} </span> : null}
          Test connects from {clusterName || 'this cluster'} to the address and closes the connection: it shows the address answers on its port from there, not that the operator is what answers.
        </p>
      )}
    </div>
  )
}
