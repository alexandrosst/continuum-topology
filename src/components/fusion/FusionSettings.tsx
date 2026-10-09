import { useState } from 'react'
import { ConfirmModal } from '@/components/forms'
import { FusionRetentionCard, type useFusionRetention } from '@/components/fusion/FusionRetention'
import { FusionSection } from '@/components/fusion/FusionSection'
import { type useFusion } from '@/components/fusion/useFusion'
import { Button } from '@/components/ui/primitives'

/** How long data is kept and on what volumes, and the switch: the settings that change what FUSION holds. */
export function FusionSettings({ fusion, retention }: { fusion: ReturnType<typeof useFusion>; retention: ReturnType<typeof useFusionRetention> }) {
  const { status, busy } = fusion
  const [confirmOff, setConfirmOff] = useState(false)
  const on = !!status && status.state !== 'off'
  return (
    <>
      {status?.data && <FusionRetentionCard retention={retention} />}
      {status?.available && (
        <FusionSection
          title={on ? 'Turn FUSION off' : 'Turn FUSION on'}
          description={on ? 'Stops the central operator and the three stores. What they saved stays on their volumes.' : 'Starts the three stores and the central operator. What was saved before comes back with them.'}
          actions={
            on ? (
              <Button onClick={() => setConfirmOff(true)} disabled={busy} data-testid="fusion-disable">Turn off</Button>
            ) : (
              <Button variant="primary" onClick={() => void fusion.enable().catch(() => undefined)} disabled={busy} data-testid="fusion-enable">{busy ? 'Starting…' : 'Turn on FUSION'}</Button>
            )
          }
        />
      )}
      {confirmOff && (
        <ConfirmModal
          title="Turn FUSION off?"
          message="The central operator and the three stores stop. What they saved stays on their volumes and comes back when FUSION is turned on again. Regional operators sending to it keep what they cannot deliver queued for a while, then drop it, until it is back."
          confirmLabel="Turn off"
          onConfirm={() => void fusion.disable().catch(() => undefined)}
          onClose={() => setConfirmOff(false)}
        />
      )}
    </>
  )
}
