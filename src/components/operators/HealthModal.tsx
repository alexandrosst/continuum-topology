import { useState } from 'react'
import { HEARTBEAT_WHAT, HeartbeatCommands } from '@/components/operators/HeartbeatCommands'
import { Button, ErrorBanner, Modal } from '@/components/ui/primitives'
import { api, ApiError, type OperatorHeartbeatEnabled } from '@/lib/api'
import { isReportingHealth } from '@/lib/operatorHealth'
import type { RegionalOperator } from '@/lib/types'
import { useHoldReload } from '@/lib/useHoldReload'
import { useServer } from '@/store/server'

/** Confirms, then mints, an operator's health credential - and only then shows it. Minting is the whole
 *  point of the confirmation: it creates a credential, replaces any existing one, and makes the operator
 *  start calling this server, so nothing is requested until the person has read what that means. */
export default function HealthModal({ operator, onClose, onDone }: { operator: RegionalOperator; onClose: () => void; onDone: () => void }) {
  const conn = useServer((s) => s.conn)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState<OperatorHeartbeatEnabled | null>(null)
  const rotating = isReportingHealth(operator)
  // The credential is shown once, in the result below: hold the page's own reload while it is on screen (see the Modal's `dismissible`).
  useHoldReload(result !== null)
  const verb = rotating ? 'Rotate health credential' : 'Enable health reporting'
  const confirm = async () => {
    const c = conn()
    if (!c || busy) return
    setBusy(true)
    setError('')
    try {
      setResult(await api.enableOperatorHeartbeat(c, operator.id))
      onDone()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Could not enable health reporting.')
    } finally {
      setBusy(false)
    }
  }
  if (result) {
    return (
      <Modal open onClose={onClose} dismissible={false} title={result.rotated ? `Health credential rotated for ${operator.name}` : `Health reporting enabled for ${operator.name}`} width="max-w-2xl" footer={<Button variant="primary" onClick={onClose} data-testid="operator-health-done">Done</Button>}>
        {result.rotated && (
          <p className="mb-3 text-xs leading-relaxed text-nb-400" data-testid="operator-health-rotated">
            The previous credential stopped working at once. The operator shows as offline until its Secret is replaced and the collector restarted.
          </p>
        )}
        <HeartbeatCommands
          testId="operator-health-commands"
          secretCommand={result.heartbeatSecretCommand}
          upgradeCommand={result.heartbeatUpgradeCommand}
          restartCommand={result.heartbeatRestartCommand}
          caSecretCommand={result.heartbeatCaSecretCommand}
          warning={result.heartbeatWarning}
          url={result.heartbeatUrl}
          intervalSeconds={result.heartbeatIntervalSeconds}
          rotated={result.rotated}
        />
      </Modal>
    )
  }
  return (
    <Modal
      open
      onClose={onClose}
      // Once the request is out the credential exists and is on its way to this dialog: closing now would lose it.
      dismissible={!busy}
      title={`${verb} for ${operator.name}?`}
      width="max-w-lg"
      footer={<><Button onClick={onClose} disabled={busy}>Cancel</Button><Button variant="primary" onClick={() => void confirm()} disabled={busy} data-testid="operator-health-confirm">{busy ? 'Working…' : verb}</Button></>}
    >
      <div className="space-y-2 text-sm text-nb-400" data-testid="operator-health-explain">
        <p>This mints a credential for the operator&apos;s heartbeat and shows it once. What the operator then sends is {HEARTBEAT_WHAT}</p>
        <p>The operator will start contacting this server - the only thing it sends here. It is opt-in: nothing changes until you run the commands that follow, and you can switch it off later with heartbeat.enabled=false on the release.</p>
        <p>{rotating ? 'Rotating invalidates the old credential at once: until you replace its Secret and restart the collector, the operator will show as offline.' : 'If this operator already has a health credential, this replaces it and the old one stops working at once.'}</p>
      </div>
      {error && <ErrorBanner className="mt-3">{error}</ErrorBanner>}
    </Modal>
  )
}
