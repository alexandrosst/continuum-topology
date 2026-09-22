import { useEffect, useState } from 'react'
import { useServer } from '@/store/server'

/** Whether what is on screen is current: the server's answer is polled every few seconds, and this says how old it is. */
export default function LiveStatus() {
  const status = useServer((s) => s.status)
  const at = useServer((s) => s.state?.generatedAt)
  const trouble = useServer((s) => !!s.error)
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 5000)
    return () => clearInterval(id)
  }, [])
  if (status !== 'connected' || !at) return null
  const age = Math.max(0, Math.round((now - new Date(at).getTime()) / 1000))
  const stale = trouble || age > 30
  const text = trouble ? 'Connection trouble' : age < 15 ? 'Live' : age < 90 ? `Updated ${age} s ago` : `Updated ${Math.round(age / 60)} min ago`
  return (
    <span className="hidden items-center gap-1.5 whitespace-nowrap text-xs text-nb-500 sm:inline-flex" title={`Data from your Continuum server, as of ${new Date(at).toLocaleTimeString()}`} data-testid="live-status">
      <span className="relative inline-flex size-1.5">
        {!stale && <span className="absolute inline-flex size-full animate-ping rounded-full bg-emerald-400 opacity-75" />}
        <span className={`relative inline-flex size-1.5 rounded-full ${stale ? 'bg-amber-400' : 'bg-emerald-400'}`} />
      </span>
      {text}
    </span>
  )
}
