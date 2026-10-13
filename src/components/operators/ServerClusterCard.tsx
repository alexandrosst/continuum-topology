import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button, ErrorBanner, Select } from '@/components/ui/primitives'
import { api, ApiError } from '@/lib/api'
import type { AddressCandidates } from '@/lib/types'
import { useServer } from '@/store/server'

const SOURCE: Record<NonNullable<NonNullable<AddressCandidates['serverCluster']>['source']>, string> = {
  install: 'from its install',
  chosen: 'because you chose it',
  matched: 'matched from what the agents report',
}

/** Which cluster the server, and so FUSION's gateway, runs in. The nodes of that cluster are the ones to list, so the card says
 *  when it is known and how, asks for it when nothing could say, and points at connecting it when it has no agent. */
export default function ServerClusterCard({ found, operatorId, onChanged, testId }: { found: AddressCandidates; operatorId: string; onChanged: () => void; testId: string }) {
  const sc = found.serverCluster
  const conn = useServer((s) => s.conn)
  const navigate = useNavigate()
  const [changing, setChanging] = useState(false)
  const [pick, setPick] = useState(sc?.clusterId ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  if (!sc) return null
  const options = sc.options ?? []
  const save = async (id: string) => {
    const c = conn()
    if (!c || busy) return
    setBusy(true)
    setError('')
    try {
      await api.setServerCluster(c, operatorId, id)
      setChanging(false)
      onChanged()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Could not save the cluster.')
    } finally {
      setBusy(false)
    }
  }
  const chooser = (
    <div className="space-y-2">
      <div className="flex gap-2">
        <div className="min-w-0 flex-1">
          <Select value={pick} onChange={(e) => setPick(e.target.value)} aria-label="Cluster" data-testid={`${testId}-select`}>
            <option value="">{sc.state === 'known' ? 'Let the server work it out' : 'Choose a cluster'}</option>
            {options.map((o) => <option key={o.id} value={o.id}>{o.name}</option>)}
          </Select>
        </div>
        <Button type="button" variant="primary" onClick={() => void save(pick)} disabled={busy || (sc.state !== 'known' && pick === '')} data-testid={`${testId}-save`}>{busy ? 'Saving…' : 'Save'}</Button>
      </div>
      {error && <ErrorBanner>{error}</ErrorBanner>}
    </div>
  )

  if (sc.state === 'known') {
    const name = found.hostClusterName ?? sc.clusterId
    return (
      <div className="space-y-2" data-testid={testId} data-state="known">
        <p className="text-[11px] leading-relaxed text-nb-500">
          The server knows it runs in {name} {sc.source ? SOURCE[sc.source] : ''}.{' '}
          <button type="button" className="text-accent underline" onClick={() => { setPick(sc.source === 'chosen' ? sc.clusterId ?? '' : ''); setChanging((v) => !v) }} data-testid={`${testId}-change`}>Change</button>
        </p>
        {changing && chooser}
      </div>
    )
  }

  if (sc.state === 'noAgent') {
    return (
      <div role="note" className="space-y-2.5 rounded-lg border border-warn/30 bg-warn/10 px-3.5 py-3" data-testid={testId} data-state="noAgent">
        <div className="text-sm font-medium text-warn">The server&apos;s cluster has no agent</div>
        <p className="text-xs leading-relaxed text-nb-300">
          FUSION runs in the same cluster as this server, and only an agent can read that cluster&apos;s nodes and their network interfaces. Connect it with the node probe on, and its nodes&apos; addresses, mesh ones included, appear here.
        </p>
        <div>
          <Button type="button" variant="primary" onClick={() => navigate('/agents?connect=1&probe=1')} data-testid={`${testId}-connect`}>Connect the server&apos;s cluster</Button>
        </div>
        <p className="text-[11px] leading-relaxed text-nb-500">
          Opens the usual connect dialog with the node probe already on.{sc.source === 'chosen' ? ' You chose this cluster; ' : ' '}
          {sc.source === 'chosen' ? <button type="button" className="text-accent underline" onClick={() => void save('')} data-testid={`${testId}-clear`}>Take the choice back</button> : null}
        </p>
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </div>
    )
  }

  return (
    <div role="group" aria-label="Which cluster is the server in" className="space-y-2.5 rounded-lg border border-nb-800 bg-nb-900/60 px-3.5 py-3" data-testid={testId} data-state="unknown">
      <div className="text-sm font-medium text-nb-100">Which cluster is the server in?</div>
      <p className="text-xs leading-relaxed text-nb-400">
        FUSION runs next to this server, so its nodes are the ones to list. The install normally tells the server this by itself; it could not here (a chart rendered with <span className="font-mono">helm template</span> or applied from GitOps), and what the agents report did not say either.
      </p>
      {options.length > 0 ? chooser : (
        <div>
          <Button type="button" variant="primary" onClick={() => navigate('/agents?connect=1&probe=1')} data-testid={`${testId}-connect`}>Connect the server&apos;s cluster</Button>
        </div>
      )}
      <p className="text-[11px] leading-relaxed text-nb-500">To skip this step, set <span className="font-mono">clusterId</span> in the server&apos;s Helm values.</p>
    </div>
  )
}
