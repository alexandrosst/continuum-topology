import { useState } from 'react'
import { GroupingPicker } from '@/components/GroupingPicker'
import { Field, Input, Modal } from '@/components/ui/primitives'
import { groupingAlternativesFor } from '@/lib/suggestions'
import { uid, useTopology } from '@/store/topology'
import { DEFAULT_ORG, type Application } from '@/lib/types'
import { FormFooter } from './shared'

/* ---------- Application ---------- */
export function ApplicationForm({ initial, onClose }: { initial: Application | null; onClose: () => void }) {
  const upsert = useTopology((s) => s.upsertApplication)
  const regroup = useTopology((s) => s.regroupApplication)
  const suggestions = useTopology((s) => s.suggestions)
  const [f, setF] = useState<Application>(
    initial ?? { id: uid('app'), orgId: DEFAULT_ORG, name: '', description: '', origin: 'explicit', confidence: 'high', source: 'manual' },
  )
  const alternatives = initial ? groupingAlternativesFor(initial, suggestions) : []
  return (
    <Modal
      open
      onClose={onClose}
      title={initial ? 'Edit application' : 'Add application'}
      description="A group of services and devices that together deliver something. Assign them from the Services and Devices pages."
      footer={
        <FormFooter
          isNew={!initial}
          disabled={!f.name.trim()}
          onCancel={onClose}
          onSave={() => {
            upsert({ ...f, name: f.name.trim() })
            onClose()
          }}
        />
      }
    >
      <div className="grid gap-4">
        <Field label="Name">
          <Input autoFocus value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="e.g. Sensor ingestion" />
          <GroupingPicker
            label="Group by a different label instead"
            alternatives={alternatives}
            onPick={(alt) => {
              regroup(initial!.id, alt)
              onClose()
            }}
          />
        </Field>
        <Field label="Description">
          <Input value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} />
        </Field>
      </div>
    </Modal>
  )
}
