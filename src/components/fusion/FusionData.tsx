import { useEffect, useState } from 'react'
import { FusionSection } from '@/components/fusion/FusionSection'
import { ErrorBanner, Pill, SkeletonBlock, Table, Td, Th } from '@/components/ui/primitives'
import { api, ApiError, type FusionApplications, type FusionServices, type FusionSignal } from '@/lib/api'
import { useServer } from '@/store/server'

const SIGNAL_WORD: Record<FusionSignal, string> = { metrics: 'Metrics', logs: 'Logs', traces: 'Traces' }
const COLS = ['w-56', 'w-20', '', 'w-72']
const SHOWN_SERVICES = 25

/** The signals FUSION holds for something, or that it has none yet. */
function Signals({ list }: { list: FusionSignal[] }) {
  if (list.length === 0) return <span className="text-xs text-nb-500">No data yet</span>
  return <span className="inline-flex flex-wrap gap-1">{list.map((s) => <Pill key={s}>{SIGNAL_WORD[s]}</Pill>)}</span>
}

/** What FUSION holds: for each Ikhnos application the services in it and which signals arrived for them over the last day, and the services
 *  with data that belong to none (an exporter, a meter). Read from the same data API an API token reads. */
export function FusionData() {
  const conn = useServer((s) => s.conn)
  const [apps, setApps] = useState<FusionApplications | null>(null)
  const [services, setServices] = useState<FusionServices | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    const c = conn()
    if (!c) return
    let alive = true
    Promise.all([api.getFusionApplications(c), api.getFusionServices(c)]).then(
      ([a, s]) => { if (alive) { setApps(a); setServices(s) } },
      (e) => { if (alive) setError(e instanceof ApiError ? e.message : 'Could not read what FUSION holds.') },
    )
    return () => { alive = false }
  }, [conn])

  if (error) return <ErrorBanner>{error}</ErrorBanner>
  if (!apps || !services) return <SkeletonBlock className="h-40 w-full" />
  const warnings = [...new Set([...apps.warnings, ...services.warnings])]
  const loose = services.services.filter((s) => s.applications.length === 0)
  return (
    <>
      {warnings.map((w) => <ErrorBanner key={w} className="mb-4">{w}</ErrorBanner>)}
      <FusionSection title="Applications" description="The applications Ikhnos knows, and which signals FUSION received for their services in the last 24 hours." testId="fusion-applications">
        <Table cols={COLS}>
          <thead><tr><Th>Application</Th><Th>Services</Th><Th>Clusters</Th><Th>Data</Th></tr></thead>
          <tbody>
            {apps.applications.map((a) => (
              <tr key={a.id} data-testid={`fusion-app-${a.name}`}>
                <Td valign="top"><div className="truncate" title={a.name}>{a.name}</div>{a.unresolvedMembers ? <div className="mt-0.5 text-xs text-nb-500">{a.unresolvedMembers} not running</div> : null}</Td>
                <Td valign="top" className="tabular-nums text-nb-500">{a.services.length}</Td>
                <Td valign="top" className="text-nb-500">{a.clusters.join(', ') || '-'}</Td>
                <Td valign="top"><Signals list={a.signals} /></Td>
              </tr>
            ))}
            {apps.applications.length === 0 && <tr><Td colSpan={4} className="text-nb-500">No application has been declared in Ikhnos yet.</Td></tr>}
          </tbody>
        </Table>
      </FusionSection>

      {loose.length > 0 && (
        <FusionSection title="Other services" description="Services with data that are in no application: infrastructure that reports under a name of its own, or a service nobody has grouped yet." testId="fusion-services">
          <Table cols={['', 'w-72']}>
            <thead><tr><Th>Service</Th><Th>Data</Th></tr></thead>
            <tbody>
              {loose.slice(0, SHOWN_SERVICES).map((s) => (
                <tr key={s.name}><Td><div className="truncate" title={s.name}>{s.name}</div></Td><Td><Signals list={s.signals} /></Td></tr>
              ))}
            </tbody>
          </Table>
          {loose.length > SHOWN_SERVICES && <p className="mt-2 text-xs text-nb-500">and {loose.length - SHOWN_SERVICES} more, which the data API lists.</p>}
        </FusionSection>
      )}

      <p className="max-w-3xl text-xs leading-relaxed text-nb-500">
        Telemetry is sorted into three categories, which the data API takes as a filter: <span className="text-nb-400">system</span> (hosts and the pipeline itself),{' '}
        <span className="text-nb-400">kubernetes</span> (the cluster&apos;s own objects) and <span className="text-nb-400">application</span> (what an application reports about itself).
      </p>
    </>
  )
}
