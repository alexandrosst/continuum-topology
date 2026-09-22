import { useState } from 'react'
import { CopyCommand } from '@/components/agents/AgentInsight'
import { Field, Input } from '@/components/ui/primitives'
import { exposureHelp, exposureIsAddressSwap, exposureLabel, serverAddressUpgradeCommand } from '@/lib/exposure'
import { useServer } from '@/store/server'

/**
 * What every agent dials to reach this server, and how it's exposed. Read-only - this page cannot run helm for you -
 * but the point is that you no longer have to reconstruct the upgrade command yourself from three different docs
 * pages: paste the new address and copy the exact command.
 */
export default function ServerAddressSettings() {
  const info = useServer((s) => s.info)
  const [next, setNext] = useState('')
  const address = info?.agentAddress ?? ''
  const kind = info?.agentExposure ?? ''

  return (
    <section id="server-address" aria-labelledby="server-address-title" className="mb-8 scroll-mt-6" data-testid="server-address-settings">
      <h2 id="server-address-title" className="mb-1 text-sm font-medium text-white">Server address</h2>
      <p className="mb-3 max-w-2xl text-sm text-nb-500">What every agent dials to reach this server. Changing it re-issues the server's TLS certificate, not its identity - agents trust the CA, not the certificate, so they keep working once they can reach the new address.</p>
      <div className="rounded-xl border border-nb-850 bg-nb-925 p-5">
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          <dt className="text-nb-500">Address</dt>
          <dd className="break-all font-mono text-nb-300" data-testid="server-address">{address || '(not set)'}</dd>
          <dt className="text-nb-500">Exposed via</dt>
          <dd className="text-nb-300" data-testid="server-exposure">{exposureLabel(kind)}</dd>
        </dl>
        <p className="mt-3 text-xs text-nb-500">{exposureHelp(kind)}</p>
        {exposureIsAddressSwap(kind) && (
          <div className="mt-3">
            <Field label="New address" hint="host:port. Nothing changes until you run the command this fills in.">
              <Input
                value={next}
                onChange={(e) => setNext(e.target.value)}
                placeholder={kind === 'nodeport' ? 'NODE_IP:30443' : 'EXTERNAL_IP:8443'}
                spellCheck={false}
                autoCapitalize="none"
                autoCorrect="off"
                autoComplete="off"
                className="font-mono text-[13px]"
                data-testid="server-address-input"
              />
            </Field>
            <CopyCommand text={serverAddressUpgradeCommand(info, next.trim())} />
          </div>
        )}
      </div>
    </section>
  )
}
