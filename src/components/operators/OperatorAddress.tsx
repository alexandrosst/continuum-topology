import { useState } from 'react'
import { CopyCommand } from '@/components/agents/AgentInsight'
import { Button, ErrorBanner, Field, Input, Modal } from '@/components/ui/primitives'
import { api, ApiError, type OperatorExposure } from '@/lib/api'
import { shArg } from '@/lib/install'
import { operatorServiceName } from '@/lib/operatorsView'
import type { AddressCandidates, RegionalOperator } from '@/lib/types'
import { candidateSub, useAddressCandidates, visibleHints } from '@/lib/useAddressCandidates'
import ServerClusterCard from './ServerClusterCard'
import { useServer } from '@/store/server'

/** What a person picks when creating an operator: whether clusters other than its own must be able to reach it.
 *  It only decides the Service type in the install command - the address that results is recorded afterwards. */
export const EXPOSURE_OPTIONS: { id: OperatorExposure; label: string; hint: string; recommended?: boolean }[] = [
  { id: 'nodeport', label: 'Node port', recommended: true, hint: 'Works on any cluster. Other clusters dial any node on one port, over its LAN, mesh or public address.' },
  { id: 'loadbalancer', label: 'Load balancer', hint: 'The cluster gives it one address of its own. Needs a load balancer provider; without one it waits for an address forever.' },
  { id: 'cluster', label: 'This cluster only', hint: 'Only workloads in the same cluster can send to it. Nothing outside can.' },
]

/** The kubectl line that reads the address a Service ended up with. */
export function addressCommands(id: string, svc?: { service: string; namespace: string }): { loadBalancer: string; nodePort: string } {
  // Names the server reported (or built from an operator's id): quoted all the same, since each lands in a line a person pastes.
  const name = shArg(svc?.service || operatorServiceName(id))
  const ns = shArg(svc?.namespace || 'continuum-system')
  return {
    loadBalancer: `kubectl get svc ${name} --namespace ${ns} -o jsonpath='{.status.loadBalancer.ingress[0].hostname}{.status.loadBalancer.ingress[0].ip}{"\\n"}'`,
    nodePort: `kubectl get svc ${name} --namespace ${ns} -o jsonpath='{.spec.ports[?(@.name=="otlp-grpc")].nodePort}{"\\n"}'`,
  }
}

/** The address with the receiver's port when none was given, as the server stores it. Display only: the server decides. */
export function withDefaultPort(value: string): string {
  const v = value.trim()
  if (!v) return ''
  const bracketed = v.startsWith('[')
  if (bracketed ? v.endsWith(']') : !v.includes(':')) return `${v}:4317`
  return v
}

/** Run from a machine in the cluster that will send: prints the names the certificate at that address carries, which
 *  must include the operator's stable one. Nothing printed means the address is not reachable from there. */
export function connectionCheckCommand(address: string, id: string): string {
  // The address is typed text: quoted, so a `;` or `$(...)` in it is part of the address and not a second command.
  return `openssl s_client -connect ${shArg(address)} -servername ${shArg(`${id}.continuum-system.svc`)} </dev/null 2>/dev/null | openssl x509 -noout -ext subjectAltName`
}

/** How to find the address, in the order a person does it. Shown after creating an exposed operator and in the
 *  "Reachable at" dialog, so nobody has to know which Service field to read. */
export function FindTheAddress({ id, exposure, service, testId }: { id: string; exposure?: OperatorExposure; /** Where the Service is, when it is not the operator's own (the central operator's, in the server's namespace). */ service?: { service: string; namespace: string }; testId: string }) {
  const cmds = addressCommands(id, service)
  // 'cluster' means the Service is not exposed by the install: there is nothing for kubectl to read, only an Ingress, a
  // DNS name or a mesh address of the person's own. Unknown (an operator from before it was asked) shows both.
  const showLb = exposure !== 'nodeport' && exposure !== 'cluster'
  const showNp = exposure !== 'loadbalancer' && exposure !== 'cluster'
  return (
    <div className="space-y-2" data-testid={testId}>
      {showLb && (
        <div>
          <div className="mb-1 text-xs text-nb-500">Load balancer: once the cloud has given it an address (this prints nothing until it has), read it. The port is 4317 unless you say otherwise</div>
          <CopyCommand text={cmds.loadBalancer} testId={`${testId}-lb`} />
        </div>
      )}
      {showNp && (
        <div>
          <div className="mb-1 text-xs text-nb-500">Node port: read the port, then use the address of any node of that cluster with it, such as <span className="font-mono">10.0.0.5:31317</span></div>
          <CopyCommand text={cmds.nodePort} testId={`${testId}-np`} />
        </div>
      )}
      <p className="text-xs leading-relaxed text-nb-500">
        {exposure === 'cluster' ? 'This operator was installed for its own cluster only. If you put your own Ingress, a DNS name or a mesh address in front of it, use that as ' : 'Behind your own Ingress or a DNS name? Use that instead, as '}
        <span className="font-mono">host:port</span>.{' '}
        <span data-testid={`${testId}-passthrough`}>TCP/TLS passthrough only: the receiver checks each client certificate itself, so an Ingress that ends TLS or speaks plain HTTP cannot sit in front of it.</span>
      </p>
    </div>
  )
}

/** Why the central operator's gateway lists no address, and the command that fixes it. The Service is changed in place - no pod restarts and
 *  its cluster address stays - and a later `helm upgrade` puts the type back unless the chart value is set to match, which the card says. */
export function ExposeCard({ expose, testId }: { expose: NonNullable<AddressCandidates['expose']>; testId: string }) {
  const [kind, setKind] = useState<'nodePort' | 'loadBalancer'>('nodePort')
  const value = kind === 'nodePort' ? 'NodePort' : 'LoadBalancer'
  return (
    <div role="note" className="space-y-2.5 rounded-lg border border-warn/30 bg-warn/10 px-3.5 py-3" data-testid={testId}>
      <div className="text-sm font-medium text-warn">FUSION&apos;s gateway is not reachable from other clusters yet</div>
      <p className="text-xs leading-relaxed text-nb-300">
        Its Service is only open inside its own cluster, so there is no node or load balancer address to list. Run this against the server&apos;s cluster: the nodes&apos; addresses, mesh ones included, appear here by themselves within a few seconds.
      </p>
      <div className="flex gap-1.5" role="group" aria-label="How to expose it">
        {([['nodePort', 'Node port'], ['loadBalancer', 'Load balancer']] as const).map(([id, label]) => (
          <button
            key={id}
            type="button"
            aria-pressed={kind === id}
            onClick={() => setKind(id)}
            className={`h-7 rounded-full border px-3 text-xs ${kind === id ? 'border-accent bg-accent/15 text-accent' : 'border-nb-800 text-nb-400 hover:text-nb-200'}`}
            data-testid={`${testId}-${id}`}
          >
            {label}
          </button>
        ))}
      </div>
      <CopyCommand text={expose.commands[kind]} testId={`${testId}-command`} />
      <p className="text-[11px] leading-relaxed text-nb-500">
        Takes effect at once and restarts nothing. It opens port 4317 on {kind === 'nodePort' ? 'every node of the server’s cluster' : 'a load balancer address of its own'}; senders still need a certificate from this server. Also set <span className="font-mono">{expose.value}={value}</span> in your Helm values, or the next upgrade puts it back.
      </p>
    </div>
  )
}

/** The addresses Ikhnos found for an operator from what its cluster's agent reports, to pick from instead of typing.
 *  Picking one only fills the field below; nothing is saved until Save. A failed lookup shows nothing: typing still works. */
function AddressCandidatesList({ operatorId, value, onPick }: { operatorId: string; value: string; onPick: (address: string) => void }) {
  const [refresh, setRefresh] = useState(0)
  const found = useAddressCandidates(operatorId, refresh)
  if (!found) return null
  const discovered = found.candidates.filter((x) => x.kind !== 'recorded')
  const sc = found.serverCluster
  const needsCluster = !!sc && sc.state !== 'known'
  return (
    <div data-testid="operator-address-candidates">
      <div className="mb-1 flex items-center gap-2 text-xs font-medium uppercase tracking-wide text-nb-500">
        Addresses found{found.hostClusterName ? ` in ${found.hostClusterName}` : ''}
        {sc?.state === 'known' && <span className="rounded-full bg-ok/10 px-2 py-px text-[11px] font-medium normal-case tracking-normal text-ok" data-testid="operator-address-server-cluster-pill">Where the server runs</span>}
      </div>
      {needsCluster && <div className="mb-2"><ServerClusterCard found={found} operatorId={operatorId} onChanged={() => setRefresh((n) => n + 1)} testId="operator-address-server-cluster" /></div>}
      {found.expose && <div className="mb-2"><ExposeCard expose={found.expose} testId="operator-address-expose" /></div>}
      {discovered.length === 0 ? (
        found.expose || needsCluster ? null : <p className="text-sm leading-relaxed text-nb-400" data-testid="operator-address-candidates-none">
          {found.found
            ? 'Its Service has no address outside the cluster yet. Install it as a load balancer or a node port, then it appears here.'
            : 'No agent has reported this operator in a cluster yet. Once one does, the addresses it can be reached at appear here.'}
        </p>
      ) : (
        <ul className="space-y-1.5">
          {discovered.map((x) => {
            const chosen = x.address === value.trim() || x.address === withDefaultPort(value)
            return (
              <li key={x.id} className="flex items-center justify-between gap-3 rounded-md border border-nb-800 px-3 py-2" data-testid="operator-address-candidate">
                <div className="min-w-0">
                  <div className="truncate font-mono text-sm text-nb-100">{x.address}</div>
                  <div className="text-xs text-nb-500">{candidateSub(x)}{x.recorded ? ' In use.' : ''}</div>
                </div>
                <Button type="button" onClick={() => onPick(x.address)} disabled={chosen} aria-label={`Use ${x.address}`}>{chosen ? 'Selected' : 'Use'}</Button>
              </li>
            )
          })}
        </ul>
      )}
      {sc?.state === 'known' && <div className="mt-2"><ServerClusterCard found={found} operatorId={operatorId} onChanged={() => setRefresh((n) => n + 1)} testId="operator-address-server-cluster" /></div>}
      {discovered.length > 0 && visibleHints(found).map((h) => <p key={h} className="mt-2 text-xs leading-relaxed text-nb-500" data-testid="operator-address-hint">{h}</p>)}
    </div>
  )
}

/** Records where other clusters reach an operator. Nothing on the operator changes - no certificate is reissued and
 *  nothing is restarted: callers dial the address and verify the operator by its stable name. */
export function OperatorAddressModal({ operator, central, onClose, onDone }: { operator: RegionalOperator; /** Set for the central operator (FUSION's gateway): the Service its address is read from. */ central?: { service: string; namespace: string }; onClose: () => void; onDone: () => void }) {
  const conn = useServer((s) => s.conn)
  const [value, setValue] = useState(operator.address ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const save = async () => {
    const c = conn()
    if (!c || busy) return
    setBusy(true)
    setError('')
    try {
      await api.setOperatorAddress(c, operator.id, value.trim())
      onDone()
      onClose()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Could not save the address.')
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title={`Where other clusters reach ${operator.name}`}
      width="max-w-xl"
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" onClick={() => void save()} disabled={busy} data-testid="operator-address-save">{busy ? 'Saving…' : 'Save'}</Button></>}
    >
      <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); void save() }}>
        <p className="text-sm leading-relaxed text-nb-400" data-testid="operator-address-explain">
          Once this is set, every command that points a cluster or another operator at this one uses it, so nobody has to work the address out. It changes nothing on the operator itself: no certificate is reissued and nothing restarts. A cluster that already points at this operator keeps the address it was given until you run its command again. Leave it empty if only this operator&apos;s own cluster sends to it.
        </p>
        <AddressCandidatesList operatorId={operator.id} value={value} onPick={setValue} />
        <Field label="Reachable at" hint="A DNS name or an IP address, with a port if it is not 4317: otlp.eu.example.com, otlp.eu.example.com:4317 or 203.0.113.7:4317.">
          <Input value={value} onChange={(e) => setValue(e.target.value)} placeholder="otlp.eu.example.com:4317" spellCheck={false} data-testid="operator-address-input" />
        </Field>
        {central && (
          <p className="text-sm leading-relaxed text-nb-400" data-testid="operator-address-central">
            FUSION&apos;s central operator is exposed by the server&apos;s own install: set <span className="font-mono">fusion.central.service.type</span> to LoadBalancer or NodePort in its Helm values (or put your own Ingress in front of the Service), then record the address it gets here. Nothing about the server needs to be restarted.
          </p>
        )}
        <FindTheAddress id={operator.id} exposure={operator.exposure} service={central} testId="operator-address-find" />
        {withDefaultPort(value) && (
          <div data-testid="operator-address-check">
            <div className="mb-1 text-xs text-nb-500">
              Check it before you save: run this from a machine in the cluster that will send. It should list <span className="font-mono">DNS:{operator.id}.continuum-system.svc</span>; no output means that machine cannot reach the address.
            </div>
            <CopyCommand text={connectionCheckCommand(withDefaultPort(value), operator.id)} testId="operator-address-check-command" />
          </div>
        )}
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </form>
    </Modal>
  )
}
