import { useState } from 'react'
import { Button, ErrorBanner, Field, Input, Modal } from '@/components/ui/primitives'
import { useServer } from '@/store/server'

export default function ServerConnect({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { url, status, error, connect } = useServer()
  const [addr, setAddr] = useState(url || 'http://127.0.0.1:8080')
  const busy = status === 'connecting'

  const submit = async () => {
    if (await connect(addr)) onClose()
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Connect to a Continuum server"
      description="The server enrolls clusters, receives what their agents discover and keeps the shared topology. You will be asked to sign in."
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={submit} disabled={busy}>
            {busy ? 'Connecting…' : 'Connect'}
          </Button>
        </>
      }
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <Field label="Server address" hint="Where the admin API listens. Leave empty to use the address this page was loaded from.">
          <Input value={addr} onChange={(e) => setAddr(e.target.value)} placeholder="https://continuum.example.com" spellCheck={false} />
        </Field>
        {status === 'error' && error && <ErrorBanner>{error}</ErrorBanner>}
      </form>
    </Modal>
  )
}
