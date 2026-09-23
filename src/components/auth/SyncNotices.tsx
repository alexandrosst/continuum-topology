import { useState } from 'react'
import { Button, Modal } from '@/components/ui/primitives'
import { useServer } from '@/store/server'
import { useWorkspace } from '@/store/workspace'

const when = (iso?: string) => (iso ? new Date(iso).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : '')

function Banner({ tone, children }: { tone: 'red' | 'amber' | 'neutral'; children: React.ReactNode }) {
  const c = tone === 'red' ? 'border-red-500/30 bg-red-500/10 text-red-200' : tone === 'amber' ? 'border-amber-400/30 bg-amber-400/10 text-amber-200' : 'border-nb-800 bg-nb-925 text-nb-300'
  return (
    <div role="alert" className={`flex flex-wrap items-center justify-between gap-3 border-b px-6 py-2.5 text-sm ${c}`}>
      {children}
    </div>
  )
}

/**
 * What the shared workspace needs from the person, shown above the page: a conflict with someone
 * else's save, a failed save, read-only access, and the one-time choice of what to do with the work
 * already in this browser when the server is empty.
 */
export default function SyncNotices() {
  const status = useServer((s) => s.status)
  const { status: sync, conflict, error, local, note, dismissNote, useTheirs, overwrite, adoptLocal, startEmpty } = useWorkspace()
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState<string | null>(null)
  if (status !== 'connected') return null
  const run = async (f: () => Promise<void>, fallback: string) => {
    setBusy(true)
    setFailed(null)
    try {
      await f()
    } catch (e) {
      setFailed(e instanceof Error ? e.message : fallback)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      {sync === 'conflict' && conflict && (
        <Banner tone="red">
          <span>
            <strong className="font-medium">{conflict.updatedBy || 'Someone'}</strong> saved a newer version{conflict.updatedAt ? ` (${when(conflict.updatedAt)})` : ''} while you had unsaved changes.
          </span>
          <span className="flex gap-2">
            <Button size="sm" disabled={busy} onClick={() => run(useTheirs, 'Could not load their version.')}>Load theirs (discard mine)</Button>
            <Button size="sm" variant="danger" disabled={busy} onClick={() => run(overwrite, 'Could not overwrite their version.')}>Keep mine (overwrite theirs)</Button>
          </span>
          {failed && <span className="w-full text-xs text-red-300">{failed}</span>}
        </Banner>
      )}
      {note && (
        <Banner tone="amber">
          <span data-testid="workspace-note">{note}</span>
          <Button size="sm" onClick={dismissNote}>Dismiss</Button>
        </Banner>
      )}
      {sync === 'error' && <Banner tone="red"><span>Could not save your changes: {error}. They are kept in this browser and saving is retried.</span></Banner>}
      {sync === 'readonly' && <Banner tone="neutral"><span>You have read-only access. Changes you make here stay in this browser and are not saved to the server.</span></Banner>}

      <Modal
        open={sync === 'choose'}
        onClose={() => {}}
        dismissible={false}
        title="This server has no workspace yet"
        description="This browser holds a topology of its own. Do you want to keep working on it here, on the server, or start clean?"
        width="max-w-lg"
        footer={
          <>
            <Button disabled={busy} onClick={() => run(startEmpty, 'Could not start an empty workspace.')}>Start with an empty workspace</Button>
            <Button variant="primary" disabled={busy} onClick={() => run(adoptLocal, 'Could not upload this browser’s topology.')}>Upload this browser&apos;s topology</Button>
          </>
        }
      >
        {local && (
          <p className="text-sm text-nb-300" data-testid="local-summary">
            {local.clusters} clusters, {local.nodes} nodes, {local.services} services, {local.devices} devices, {local.applications} applications. Uploading makes it the shared workspace everyone signs in to. Starting empty removes it from this browser.
          </p>
        )}
        {failed && <p className="mt-3 text-sm text-red-300" role="alert">{failed}</p>}
      </Modal>
    </>
  )
}
