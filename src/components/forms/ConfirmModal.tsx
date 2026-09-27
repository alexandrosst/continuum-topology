import { Button, Modal } from '@/components/ui/primitives'

/* ---------- Confirm ---------- */
export function ConfirmModal({ title, message, onConfirm, onClose }: { title: string; message: string; onConfirm: () => void; onClose: () => void }) {
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
            Delete
          </Button>
        </>
      }
    >
      <p className="text-sm text-nb-400">{message}</p>
    </Modal>
  )
}
