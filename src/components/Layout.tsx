import clsx from 'clsx'
import { BookOpen, Boxes, Cable, Cpu, Folders, History, Layers, MapPin, Menu, Network, Package, Radar, Radio, Route, Search, Server, Settings2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { Copyright } from '@/components/ui/brand'
import AccountMenu from '@/components/auth/AccountMenu'
import AuthGate from '@/components/auth/AuthGate'
import SyncNotices from '@/components/auth/SyncNotices'
import CommandPalette from '@/components/CommandPalette'
import ErrorBoundary from '@/components/ErrorBoundary'
import HistoryBanner from '@/components/HistoryBanner'
import SampleBanner from '@/components/SampleBanner'
import { resumeServer, useServer } from '@/store/server'
import { useRawTopology } from '@/store/topology'

// Published by .github/workflows/docs.yml from docs-site/ — see its "Release process" page for the one-time
// GitHub Pages setting that publishing depends on.
const DOCS_URL = 'https://alexandrosst.github.io/continuum-topology/'

const NAV = [
  { to: '/topology', label: 'Topology', icon: Network },
  { to: '/clusters', label: 'Clusters', icon: Boxes },
  { to: '/nodes', label: 'Nodes', icon: Server },
  { to: '/namespaces', label: 'Namespaces', icon: Folders },
  { to: '/services', label: 'Services', icon: Package },
  { to: '/devices', label: 'Devices', icon: Radio },
  { to: '/applications', label: 'Applications', icon: Layers },
  { to: '/sites', label: 'Sites', icon: MapPin },
  { to: '/discovery', label: 'Discovery', icon: Radar },
  { to: '/agents', label: 'Agents', icon: Cable },
  { to: '/history', label: 'History', icon: History },
  { to: '/placement', label: 'Placement', icon: Route },
]

function NavItem({ to, label, icon: Icon, badge }: (typeof NAV)[number] & { badge?: number }) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        clsx(
          'group relative flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors',
          isActive ? 'bg-nb-940 text-white' : 'text-nb-400 hover:bg-nb-930 hover:text-nb-300',
        )
      }
    >
      {({ isActive }) => (
        <>
          {isActive && <span className="absolute -left-3 h-5 w-0.5 rounded-r bg-accent" />}
          <Icon size={16} className={isActive ? 'text-accent' : ''} />
          {label}
          {!!badge && (
            <span className="ml-auto rounded-full bg-amber-400/15 px-1.5 text-[11px] font-medium text-amber-300" aria-label={`${badge} waiting`}>
              {badge}
            </span>
          )}
        </>
      )}
    </NavLink>
  )
}

/** Keeps the topology in step with the server while a connection is open. Faster while something waits for approval. */
function useServerPolling() {
  const status = useServer((s) => s.status)
  const waiting = useServer((s) => s.state?.agents?.some((a) => a.status === 'pending') ?? false)
  useEffect(() => {
    void resumeServer()
  }, [])
  useEffect(() => {
    if (status !== 'connected') return
    const tick = () => document.visibilityState === 'visible' && void useServer.getState().refresh()
    const id = setInterval(tick, waiting ? 2000 : 5000)
    document.addEventListener('visibilitychange', tick)
    return () => {
      clearInterval(id)
      document.removeEventListener('visibilitychange', tick)
    }
  }, [status, waiting])
}

export default function Layout() {
  useServerPolling()
  return (
    <AuthGate>
      <Shell />
    </AuthGate>
  )
}

const IS_MAC = typeof navigator !== 'undefined' && /mac|iphone|ipad/i.test(navigator.platform || navigator.userAgent)
const SHORTCUT = IS_MAC ? '⌘K' : 'Ctrl K'

function Shell() {
  // Approving is done on Agents; Discovery holds what agents found for a person to decide.
  const approvals = useRawTopology((s) => s.agents.filter((a) => a.status === 'pending' && a.requestedAt).length)
  const found = useRawTopology((s) => s.suggestions.filter((x) => x.status === 'open').length)
  const { pathname } = useLocation()
  // The canvas page wants the full viewport; table pages get a padded container.
  const full = pathname.startsWith('/topology')
  const [searching, setSearching] = useState(false)
  // Below the desktop breakpoint the menu is a drawer, so the page gets the whole width.
  const [menu, setMenu] = useState(false)
  useEffect(() => setMenu(false), [pathname])
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setSearching((o) => !o)
      }
      if (e.key === 'Escape') setMenu(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <div className="flex h-full">
      {menu && <div className="fixed inset-0 z-40 bg-black/60 xl:hidden" onClick={() => setMenu(false)} aria-hidden />}
      <aside
        id="main-menu"
        className={clsx(
          'fixed inset-y-0 left-0 z-50 flex w-60 shrink-0 flex-col overflow-y-auto border-r border-nb-850 bg-nb-920 px-3 py-4 transition-transform duration-200 xl:static xl:z-auto xl:translate-x-0',
          menu ? 'translate-x-0' : '-translate-x-full',
        )}
      >
        <div className="mb-6 flex items-center gap-2.5 px-3">
          <div className="grid size-8 place-items-center rounded-lg bg-accent-soft text-accent">
            <Cpu size={18} />
          </div>
          <div className="leading-tight">
            <div className="text-sm font-semibold text-white">Continuum</div>
            <div className="text-[11px] text-nb-500">Topology Studio</div>
          </div>
        </div>
        <button
          onClick={() => setSearching(true)}
          className="mb-3 flex items-center gap-2.5 rounded-md border border-nb-850 bg-nb-925 px-3 py-2 text-left text-sm text-nb-500 hover:border-nb-800 hover:text-nb-400"
          aria-label={`Search (${SHORTCUT})`}
          data-testid="search-button"
        >
          <Search size={14} aria-hidden /> Search
          <kbd className="ml-auto rounded border border-nb-800 px-1.5 text-[11px]">{SHORTCUT}</kbd>
        </button>
        <nav className="flex flex-col gap-1">
          {NAV.map((n) => (
            <NavItem key={n.to} {...n} badge={n.to === '/discovery' ? found : n.to === '/agents' ? approvals : undefined} />
          ))}
        </nav>
        <div className="mt-auto border-t border-nb-850 pt-3">
          <AccountMenu />
          <a
            href={DOCS_URL}
            target="_blank"
            rel="noreferrer"
            className="group relative flex items-center gap-3 rounded-md px-3 py-2 text-sm text-nb-400 transition-colors hover:bg-nb-930 hover:text-nb-300"
          >
            <BookOpen size={16} /> Documentation
          </a>
          <NavItem to="/settings" label="Settings" icon={Settings2} />
          <Copyright className="mt-3 px-3" />
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex h-12 shrink-0 items-center gap-3 border-b border-nb-850 bg-nb-920 px-3 xl:hidden">
          <button onClick={() => setMenu(true)} aria-label="Open menu" aria-controls="main-menu" aria-expanded={menu} className="grid size-9 place-items-center rounded-md text-nb-400 hover:bg-nb-930 hover:text-white">
            <Menu size={18} />
          </button>
          <span className="text-sm font-semibold text-white">Continuum</span>
          <button onClick={() => setSearching(true)} aria-label="Search" className="ml-auto grid size-9 place-items-center rounded-md text-nb-400 hover:bg-nb-930 hover:text-white">
            <Search size={16} />
          </button>
        </div>
        <SyncNotices />
        <SampleBanner />
        <HistoryBanner />
        <main className={clsx('min-h-0 min-w-0 flex-1', full ? 'flex flex-col' : 'overflow-y-auto')}>
          <ErrorBoundary key={pathname}>
            {full ? (
              <Outlet />
            ) : (
              <div className="mx-auto max-w-[1680px] px-4 py-6 sm:px-8 sm:py-8">
                <Outlet />
              </div>
            )}
          </ErrorBoundary>
        </main>
      </div>
      <CommandPalette open={searching} onClose={() => setSearching(false)} />
    </div>
  )
}
