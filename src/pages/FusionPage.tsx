import clsx from 'clsx'
import { Database, KeyRound, LayoutDashboard, Settings2 } from 'lucide-react'
import { type ReactNode, useMemo } from 'react'
import { Navigate, NavLink, Route, Routes } from 'react-router-dom'
import { FusionAccess } from '@/components/fusion/FusionAccess'
import { FusionData } from '@/components/fusion/FusionData'
import { FusionOverview } from '@/components/fusion/FusionOverview'
import { useFusionRetention } from '@/components/fusion/FusionRetention'
import { FusionSection } from '@/components/fusion/FusionSection'
import { FusionSettings } from '@/components/fusion/FusionSettings'
import { OpenError, OpenPages, useOpenPage } from '@/components/fusion/OpenPages'
import { StatusChip } from '@/components/fusion/StatusChip'
import { useFusion, useNow } from '@/components/fusion/useFusion'
import { Button, EmptyState, ErrorBanner, ICON_SM, PageHeader, SkeletonBlock } from '@/components/ui/primitives'
import { fusionHealth, fusionProblems, HEALTH_WORD, lastDataText } from '@/lib/fusionStatus'
import { useServer } from '@/store/server'

const TABS = [
  { to: '/fusion', end: true, label: 'Overview', icon: LayoutDashboard },
  { to: '/fusion/data', label: 'Data', icon: Database },
  { to: '/fusion/access', label: 'Access', icon: KeyRound },
  { to: '/fusion/settings', label: 'Settings', icon: Settings2 },
]

/** The sections of FUSION, as the app's segmented control (the Local / Regional switch of the Pipeline page): each is a route, so the
 *  address says which is open and Back goes to the one before. */
function Tabs() {
  return (
    <nav className="flex w-fit max-w-full overflow-hidden rounded-md border border-nb-850" aria-label="FUSION sections">
      {TABS.map(({ to, end, label, icon: Icon }) => (
        <NavLink key={to} to={to} end={end} className={({ isActive }) => clsx('flex items-center gap-1.5 px-2.5 py-1.5 text-sm sm:px-3', isActive ? 'bg-nb-940 text-nb-300' : 'text-nb-400 hover:text-nb-300')}>
          <Icon size={ICON_SM} className="hidden sm:block" aria-hidden /> {label}
        </NavLink>
      ))}
    </nav>
  )
}

/**
 * FUSION, the stores and API that come with Ikhnos, as a section of its own: Overview (what needs attention, then the parts), Data (what it
 * holds), Access (who may read it) and Settings (how long it keeps data, and the switch). The state and the retention are read once here, so
 * the status beside the tabs and the problems on the Overview come from the same answers.
 */
export default function FusionPage() {
  const isAdmin = useServer((s) => s.isAdmin)
  const admin = isAdmin()
  const fusion = useFusion(admin)
  const { status, error } = fusion
  const retention = useFusionRetention(status?.state ?? '', admin && !!status?.data)
  const now = useNow(10_000, status?.state === 'running')
  const problems = useMemo(() => fusionProblems(status, retention.doc, now), [status, retention.doc, now])
  const health = fusionHealth(status, problems)
  const page = useOpenPage()
  const on = !!status?.available && status.state !== 'off'

  if (!admin) {
    return (
      <>
        <PageHeader title="FUSION" />
        <EmptyState title="Administrators only" description="Only organisation administrators can see and manage FUSION." />
      </>
    )
  }

  // Overview and Data say something only while FUSION runs: off, or a server that cannot run it, is one clear message.
  const running = (content: ReactNode) =>
    !status?.available ? (
      <EmptyState title="FUSION is not available here" description={status?.message ?? 'This server cannot switch FUSION.'} />
    ) : status.state === 'off' ? (
      <EmptyState
        title="FUSION is off"
        description="Nothing is running or saved. Earlier data comes back when it is turned on."
        action={<Button variant="primary" onClick={() => void fusion.enable().catch(() => undefined)} disabled={fusion.busy} data-testid="fusion-enable">{fusion.busy ? 'Starting…' : 'Turn on FUSION'}</Button>}
      />
    ) : (
      content
    )

  return (
    <>
      <PageHeader
        title="FUSION"
        description="Prometheus, Loki and Tempo, with Grafana and a data API."
        actions={on ? <OpenPages links={status.links} opening={page.opening} onOpen={(p) => void page.open(p)} /> : undefined}
      />
      <div className="mb-6 flex flex-wrap items-center gap-x-4 gap-y-2">
        <Tabs />
        {status && (
          <span className="flex items-center gap-2 text-xs text-nb-500" data-testid="fusion-state">
            {/* Only the state's word, so that a change (Starting, then Healthy) is announced and "last data 12 s ago" counting up is not. */}
            <span className="sr-only" aria-live="polite" aria-atomic="true" data-testid="fusion-live">FUSION: {HEALTH_WORD[health]}</span>
            <StatusChip status={health} />
            {on && health !== 'starting' && <span>{lastDataText(status, now)}</span>}
          </span>
        )}
      </div>
      {error && <ErrorBanner className="mb-4">{error}</ErrorBanner>}
      <OpenError message={page.error} />

      {status === null ? (
        !error && <div className="space-y-3" role="status" aria-label="Checking FUSION"><SkeletonBlock className="h-16 w-full" /><SkeletonBlock className="h-40 w-full" /></div>
      ) : (
        <Routes>
          <Route index element={running(<FusionOverview fusion={fusion} problems={problems} retention={retention.doc} now={now} />)} />
          <Route path="data" element={running(status.data ? <FusionData /> : null)} />
          <Route
            path="access"
            element={
              status.data ? (
                <>
                  <FusionSection title="Grafana and Prometheus" description="They open through this server with your sign-in. Loki and Tempo have no page; Grafana reads them." testId="fusion-links-note" />
                  <FusionAccess />
                </>
              ) : (
                <EmptyState title="API tokens are managed elsewhere" description={status.message ?? 'FUSION is shared by everything that sends to this server, so it is managed from the server’s main organisation.'} />
              )
            }
          />
          <Route path="settings" element={<FusionSettings fusion={fusion} retention={retention} />} />
          <Route path="*" element={<Navigate to="/fusion" replace />} />
        </Routes>
      )}
    </>
  )
}
