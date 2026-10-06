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

/** The kubectl line that reads the address a Service ended up with. */
export function addressCommands(id: string): { loadBalancer: string; nodePort: string } {
  return {
    loadBalancer: `kubectl get svc ${id} --namespace continuum-system -o jsonpath='{.status.loadBalancer.ingress[0].hostname}{.status.loadBalancer.ingress[0].ip}{"\\n"}'`,
    nodePort: `kubectl get svc ${id} --namespace continuum-system -o jsonpath='{.spec.ports[?(@.name=="otlp-grpc")].nodePort}{"\\n"}'`,
  }
}

/** How to find the address, in the order a person does it. Shown after creating an exposed operator and in the
 *  "Reachable at" dialog, so nobody has to know which Service field to read. */
export function FindTheAddress({ id, exposure, testId }: { id: string; exposure?: OperatorExposure; testId: string }) {
  const cmds = addressCommands(id)
  const showLb = exposure !== 'nodeport'
  const showNp = exposure !== 'loadbalancer'
  return (
    <div className="space-y-2" data-testid={testId}>
      {showLb && (
        <div>
          <div className="mb-1 text-xs text-nb-500">Load balancer: once the cloud has given it an address (this prints nothing until it has), read it, then add <span className="font-mono">:4317</span></div>
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
        Behind your own Ingress or a DNS name? Use that instead, as <span className="font-mono">host:port</span>.
      </p>
    </div>
  )
}

/** Records where other clusters reach an operator. Nothing on the operator changes - no certificate is reissued and
 *  nothing is restarted: callers dial the address and verify the operator by its stable name. */
export function OperatorAddressModal({ operator, onClose, onDone }: { operator: RegionalOperator; onClose: () => void; onDone: () => void }) {
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
          Once this is set, every command that points a cluster or another operator at this one uses it, so nobody has to work the address out. It changes nothing on the operator itself: no certificate is reissued and nothing restarts. Leave it empty if only this operator&apos;s own cluster sends to it.
        </p>
        <Field label="Reachable at" hint="A host and a port: a DNS name or an IP address, for example otlp.eu.example.com:4317 or 203.0.113.7:4317.">
          <Input value={value} onChange={(e) => setValue(e.target.value)} placeholder="otlp.eu.example.com:4317" spellCheck={false} data-testid="operator-address-input" />
        </Field>
        <FindTheAddress id={operator.id} testId="operator-address-find" />
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </form>
    </Modal>
  )
}
