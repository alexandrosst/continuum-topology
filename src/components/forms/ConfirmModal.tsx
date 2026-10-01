import { Button, Modal } from '@/components/ui/primitives'

/* ---------- Confirm ---------- */
// confirmLabel defaults to "Delete" (its original, and still most common, use) but every call site whose
// action isn't actually a delete - revoking an agent, clearing or replacing the topology - must pass its
// own verb. A revoke/replace dialog whose only button says "Delete" doesn't match the title/message a
// person just read, right in the one kind of dialog meant to prevent exactly that kind of mistake.
export function ConfirmModal({ title, message, confirmLabel = 'Delete', onConfirm, onClose }: { title: string; message: string; confirmLabel?: string; onConfirm: () => void; onClose: () => void }) {
  return (
    <Modal
      open
      onClose={onClose}
      title={title}
      width="max-w-md"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="danger"
            onClick={() => {
              onConfirm()
              onClose()
            }}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      <p className="text-sm text-nb-400">{message}</p>
    </Modal>
  )
}
