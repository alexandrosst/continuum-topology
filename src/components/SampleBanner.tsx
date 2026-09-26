import { FlaskConical, X } from 'lucide-react'
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button } from '@/components/ui/primitives'
import { isSampleData } from '@/lib/seed'
import { useServer } from '@/store/server'
import { useRawTopology } from '@/store/topology'

const KEY = 'continuum-sample-banner/dismissed'
const read = () => {
  try {
    return sessionStorage.getItem(KEY) === '1'
  } catch {
    return false
  }
}

/**
 * The first thing a new visitor sees is the built-in example. Without a word it looks like their own
 * infrastructure, so it says what it is and how to replace it with the real thing.
 */
export default function SampleBanner() {
  const clusters = useRawTopology((s) => s.clusters)
  const clear = useRawTopology((s) => s.clear)
  const connected = useServer((s) => s.status === 'connected')
  const [gone, setGone] = useState(read)
  const navigate = useNavigate()
  if (connected || gone || !isSampleData(clusters)) return null
  const dismiss = () => {
    setGone(true)
    try {
      sessionStorage.setItem(KEY, '1')
    } catch {
      /* the banner simply comes back next visit */
    }
  }
  return (
    <div role="status" className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-accent/25 bg-accent-soft px-4 py-2.5 text-sm text-nb-300 sm:px-6" data-testid="sample-banner">
      <FlaskConical size={16} className="shrink-0 text-accent" aria-hidden />
      <span className="min-w-[60%] flex-1 basis-56">
        <strong className="font-medium text-nb-300">Sample data.</strong> This is a made-up cloud → edge → far-edge example, not your infrastructure. Connect a cluster to see the real thing.
      </span>
      <span className="flex items-center gap-2">
        <Button size="sm" variant="primary" onClick={() => navigate('/discovery?connect=1')}>Connect a cluster</Button>
        <Button size="sm" onClick={() => { clear(); dismiss() }}>Start empty</Button>
        <button onClick={dismiss} aria-label="Dismiss" className="rounded p-1 text-nb-500 hover:bg-nb-930 hover:text-nb-300"><X size={14} /></button>
      </span>
    </div>
  )
}
