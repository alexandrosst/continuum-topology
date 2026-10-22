import clsx from 'clsx'
import { ExternalLink } from 'lucide-react'
import { useRef, useState } from 'react'
import { buttonClass } from '@/components/ui/buttonClass'
import { ErrorBanner, ICON_SM } from '@/components/ui/primitives'
import { api, ApiError, type FusionStatus } from '@/lib/api'
import { useServer } from '@/store/server'

/** Opens one of FUSION's own pages (Grafana, Prometheus) in a new tab. The tab is opened inside the click, which is what lets a pop-up
 *  blocker allow it; the address arrives with the server's answer (a link into a new tab carries no session cookie, so the server gives
 *  it a ticket). A page being opened is guarded against a second click, which would mint a second ticket and open a second tab: the ref is
 *  the guard (it changes at once, where state changes on the next render), the state only dims the buttons. */
export function useOpenPage() {
  const conn = useServer((s) => s.conn)
  const [error, setError] = useState('')
  const openingRef = useRef(false)
  const [opening, setOpening] = useState(false)
  const open = async (page: 'grafana' | 'prometheus') => {
    const c = conn()
    if (!c || openingRef.current) return
    setError('')
    const tab = window.open('', '_blank')
    if (!tab) {
      setError('Your browser blocked the new tab. Allow pop-ups for this address and try again.')
      return
    }
    tab.opener = null
    openingRef.current = true
    setOpening(true)
    try {
      const r = await api.openFusionPage(c, page)
      tab.location.href = `${c.url.replace(/\/$/, '')}${r.path}`
    } catch (e) {
      tab.close()
      setError(e instanceof ApiError ? e.message : 'Could not open the page.')
    } finally {
      openingRef.current = false
      setOpening(false)
    }
  }
  return { open, opening, error }
}

/** The two pages FUSION serves, as buttons. Before one is up (its part is still starting) it is shown, disabled, so the person knows it is
 *  coming rather than wondering where it is. */
export function OpenPages({ links, opening, onOpen }: { links: FusionStatus['links']; opening: boolean; onOpen: (page: 'grafana' | 'prometheus') => void }) {
  const button = (page: 'grafana' | 'prometheus', label: string) => {
    const cls = buttonClass('secondary')
    if (!links?.[page]) {
      return (
        <span className={clsx(cls, 'cursor-not-allowed opacity-45')} aria-disabled="true" title="Available when it has started" data-testid={`fusion-open-${page}`}>
          <ExternalLink size={ICON_SM} aria-hidden /> {label}
        </span>
      )
    }
    return (
      <button type="button" className={cls} onClick={() => onOpen(page)} disabled={opening} data-testid={`fusion-open-${page}`}>
        <ExternalLink size={ICON_SM} aria-hidden /> {label}
      </button>
    )
  }
  return <>{button('prometheus', 'Open Prometheus')}{button('grafana', 'Open Grafana')}</>
}

export const OpenError = ({ message }: { message: string }) => (message ? <ErrorBanner className="mb-4" data-testid="fusion-open-error">{message}</ErrorBanner> : null)
