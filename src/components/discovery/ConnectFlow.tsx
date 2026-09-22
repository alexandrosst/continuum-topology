import { Loader2 } from 'lucide-react'
import { lazy, Suspense, useCallback, useEffect, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import ServerConnect from '@/components/discovery/ServerConnect'
import { useServer } from '@/store/server'

/** Covers the screen the instant the wizard is asked for, before its chunk has even arrived - so a click aimed
 * at "Add manually" (or anything else) while it loads lands here instead of opening a second dialog behind it. */
function WizardLoading() {
  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-black/60 backdrop-blur-[2px]" aria-hidden>
      <Loader2 size={28} className="animate-spin text-accent" />
    </div>
  )
}

// The wizard is large and most visits never open it.
const ConnectClusterWizard = lazy(() => import('@/components/discovery/ConnectClusterWizard'))

/**
 * "Connect a cluster" is one flow with one entry point, whichever page offers it (Discovery, Agents, the empty
 * Topology, the getting-started list). Call `start()` from a button; render `dialogs` once on the page.
 *
 * With a server in use it opens the wizard; without one it first asks for the server's address. A link that ends
 * in `?connect=1` (the sample-data banner, older bookmarks, `/discovery?connect=1`) starts it on arrival, once the
 * server has answered, so a signed-in person is not asked for an address they already gave.
 */
export function useConnectFlow(): { start: () => void; dialogs: ReactNode; canStart: boolean; wizardOpen: boolean } {
  const server = useServer()
  const [connecting, setConnecting] = useState(false)
  const [wizard, setWizard] = useState(false)
  const [sp, setSp] = useSearchParams()
  const connected = server.status === 'connected'
  // Viewers can read everything but the server refuses their changes, so the flow is not offered.
  const canStart = !connected || server.isAdmin()

  const start = useCallback(() => {
    if (useServer.getState().status === 'connected') setWizard(true)
    else setConnecting(true)
  }, [])

  const asked = sp.get('connect') === '1'
  useEffect(() => {
    if (!asked) return
    // A full page load starts before the server has answered: wait, or a signed-in person is asked for the address.
    if (!server.checked || server.status === 'connecting') return
    start()
    setSp((p) => { const n = new URLSearchParams(p); n.delete('connect'); return n }, { replace: true })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [asked, server.checked, server.status])

  const dialogs = (
    <>
      <ServerConnect open={connecting} onClose={() => setConnecting(false)} />
      {wizard && (
        <Suspense fallback={<WizardLoading />}>
          <ConnectClusterWizard open={wizard} onClose={() => setWizard(false)} />
        </Suspense>
      )}
    </>
  )
  return { start, dialogs, canStart, wizardOpen: wizard }
}
