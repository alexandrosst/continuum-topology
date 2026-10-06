import { useState } from 'react'
import { CopyCommand } from '@/components/agents/AgentInsight'
import { Button, ErrorBanner, Field, Input, Modal } from '@/components/ui/primitives'
import { api, ApiError, type OperatorExposure } from '@/lib/api'
import type { RegionalOperator } from '@/lib/types'
import { useServer } from '@/store/server'

/** What a person picks when creating an operator: whether clusters other than its own must be able to reach it.
 *  It only decides the Service type in the install command - the address that results is recorded afterwards. */
export const EXPOSURE_OPTIONS: { id: OperatorExposure; label: string; hint: string }[] = [
  { id: 'cluster', label: 'This cluster only', hint: 'Only workloads in the same cluster can send to it (the default).' },
  { id: 'loadbalancer', label: 'Other clusters, through a load balancer', hint: 'The install command makes its Service a LoadBalancer; the cloud gives it an address.' },
  { id: 'nodeport', label: 'Other clusters, through a node port', hint: 'The install command makes its Service a NodePort; other clusters dial any node on that port.' },
]

/** The Service the regional-operator chart creates for a release named after the operator (what the install command
 *  does): the chart's own naming rule - the release name plus "-regional-operator", unless it already says so, cut to 63
 *  characters. The server applies the same rule (operatorServiceName), so the two agree on what to dial and to read. */
export function operatorServiceName(id: string): string {
  const name = id.includes('regional-operator') ? id : `${id}-regional-operator`
  return name.slice(0, 63).replace(/-+$/, '')
}

/** The kubectl line that reads the address a Service ended up with. */
export function addressCommands(id: string, svc?: { service: string; namespace: string }): { loadBalancer: string; nodePort: string } {
  const name = svc?.service || operatorServiceName(id)
  const ns = svc?.namespace || 'continuum-system'
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
  return `openssl s_client -connect ${address} -servername ${id}.continuum-system.svc </dev/null 2>/dev/null | openssl x509 -noout -ext subjectAltName`
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
        <span className="font-mono">host:port</span>.
      </p>
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
