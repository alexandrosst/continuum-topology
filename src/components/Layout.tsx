import clsx from 'clsx'
import { BookOpen, Boxes, Cable, Database, Folders, HeartPulse, History, Keyboard, Layers, MapPin, Menu, Network, Package, Radar, Radio, Route, Search, Server, Settings2, Waypoints } from 'lucide-react'
import { Suspense, useEffect, useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { BrandMark, Copyright } from '@/components/ui/brand'
import { ICON_MD, ICON_SM, PageSkeleton, SkeletonBlock } from '@/components/ui/primitives'
import AccountMenu from '@/components/auth/AccountMenu'
import AuthGate from '@/components/auth/AuthGate'
import SyncNotices from '@/components/auth/SyncNotices'
import CommandPalette from '@/components/CommandPalette'
import ErrorBoundary from '@/components/ErrorBoundary'
import HistoryBanner from '@/components/HistoryBanner'
import KeyboardShortcutsModal from '@/components/KeyboardShortcutsModal'
import SampleBanner from '@/components/SampleBanner'
import { prefetchRoute } from '@/lib/routeLoaders'
import { resumeServer, useServer } from '@/store/server'
import { useRawTopology } from '@/store/topology'

// Published by .github/workflows/docs.yml from docs-site/ — see its "Release process" page for the one-time
// GitHub Pages setting that publishing depends on.
const DOCS_URL = 'https://alexandrosst.github.io/continuum-topology/'

type NavEntry = { to: string; label: string; icon: typeof Network }

// Grouped so the sidebar reads as three questions instead of one flat list: what am I looking at,
// what's it made of, and what needs my attention. Order within a group is the same as before.
const NAV_GROUPS: { label: string; items: NavEntry[] }[] = [
  {
    label: 'Explore',
    items: [
      { to: '/topology', label: 'Topology', icon: Network },
      { to: '/clusters', label: 'Clusters', icon: Boxes },
      { to: '/applications', label: 'Applications', icon: Layers },
      { to: '/sites', label: 'Sites', icon: MapPin },
    ],
  },
  {
    label: 'Infrastructure',
    items: [
      { to: '/nodes', label: 'Nodes', icon: Server },
      { to: '/namespaces', label: 'Namespaces', icon: Folders },
      { to: '/services', label: 'Services', icon: Package },
      { to: '/devices', label: 'Devices', icon: Radio },
    ],
  },
  {
    label: 'Telemetry',
    items: [
      { to: '/pipeline', label: 'Pipeline', icon: Waypoints },
      { to: '/fusion', label: 'FUSION', icon: Database },
    ],
  },
  {
    label: 'Manage',
    items: [
      { to: '/discovery', label: 'Discovery', icon: Radar },
      { to: '/agents', label: 'Agents', icon: Cable },
      { to: '/placement', label: 'Placement', icon: Route },
      { to: '/history', label: 'History', icon: History },
      { to: '/system-health', label: 'System Health', icon: HeartPulse },
    ],
  },
]

/**
 * `slid`: an ancestor already renders a single sliding accent bar (`NavIndicator`, below) that tracks
 * whichever of its items is active, so this item skips drawing its own - a `slid` group and a `NavIndicator`
 * always come as a pair. Items outside that group (Settings, in the footer) keep the plain static bar.
 */
function NavItem({ to, label, icon: Icon, badge, slid }: NavEntry & { badge?: number; slid?: boolean }) {
  return (
    <NavLink
      to={to}
      data-nav-to={slid ? to : undefined}
      onMouseEnter={() => prefetchRoute(to)}
      onFocus={() => prefetchRoute(to)}
      className={({ isActive }) =>
        clsx(
          'group relative flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors',
          isActive ? 'bg-nb-940 text-nb-300' : 'text-nb-400 hover:bg-nb-930 hover:text-nb-300',
        )
      }
    >
      {({ isActive }) => (
        <>
          {isActive && !slid && <span className="absolute -left-3 h-5 w-0.5 rounded-r bg-accent" />}
          <Icon size={ICON_SM} className={isActive ? 'text-accent' : ''} />
          {label}
          {!!badge && (
            <span className="ml-auto rounded-full bg-warn-soft px-1.5 text-[11px] font-medium text-warn" aria-label={`${badge} waiting`}>
              {badge}
            </span>
          )}
        </>
      )}
    </NavLink>
  )
}

/** True where NavLink's own default matching would call `to` active: an exact match, or a path under it. */
const isNavActive = (to: string, pathname: string) => pathname === to || pathname.startsWith(`${to}/`)

/**
 * The current page's accent bar, but one element that slides to the active item instead of one bar vanishing
 * as another appears. No animation library here, so this measures the active NavLink's own position with
 * getBoundingClientRect - the same hand-rolled approach `Select` uses to place its dropdown - and lets CSS
 * (`transition-[transform,height]`) ease the move.
 */
const NAV_INDICATOR_H = 20 // matches the old static bar's h-5, so switching to a sliding one changes no other sizing

function NavIndicator({ containerRef, activeTo }: { containerRef: RefObject<HTMLElement | null>; activeTo: string | null }) {
  const [top, setTop] = useState<number | null>(null)

  useLayoutEffect(() => {
    const container = containerRef.current
    if (!container || !activeTo) {
      setTop(null)
      return
    }
    const measure = () => {
      const item = container.querySelector<HTMLElement>(`[data-nav-to="${activeTo}"]`)
      if (!item) return setTop(null)
      const c = container.getBoundingClientRect()
      const r = item.getBoundingClientRect()
      setTop(r.top - c.top + (r.height - NAV_INDICATOR_H) / 2)
    }
    measure()
    // The sidebar can reflow under the browser's own resize (drawer breakpoint) without a route change.
    window.addEventListener('resize', measure)
    return () => window.removeEventListener('resize', measure)
  }, [containerRef, activeTo])

  if (top === null) return null
  return (
    <span
      className="pointer-events-none absolute -left-3 w-0.5 rounded-r bg-accent transition-transform duration-200 ease-out"
      style={{ height: NAV_INDICATOR_H, transform: `translateY(${top}px)` }}
      aria-hidden
    />
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
  // Whether anyone is already signed in is itself found out over the network (see useServer's `connect`,
  // which resolves `checked`), so it is never instant - the same "not actually free" latency task #383
  // already found for a few data-heavy pages. Before this, that whole window rendered nothing but a bare
  // colored div (AuthGate's own `if (!checked)` branch, still there for the states below): no sidebar, no
  // hint the app is even running, on every single page load. The sidebar's own contents (NAV_GROUPS, the
  // logo, the search button) need no auth at all to draw, so LoadingShell below renders them immediately;
  // only the content pane - and AuthGate's own real sign-in/2FA/no-organisation screens, which stay exactly
  // as they were for a person who genuinely isn't signed in - waits on `checked`.
  const checked = useServer((s) => s.checked)
  if (!checked) return <LoadingShell />
  // Auto-placing a siteless cluster needs the ~1.6MB city/country tables (see lib/places-data.ts); it used to
  // run here, globally, on every route, downloading that data on first paint whenever any cluster lacked a
  // site - an ordinary state (e.g. right after connecting one) - even for a person who never opens the map.
  // It now runs only from the pages where placement is actually surfaced: ClustersPage and TopologyPage.
  return (
    <AuthGate>
      <Shell />
    </AuthGate>
  )
}

const IS_MAC = typeof navigator !== 'undefined' && /mac|iphone|ipad/i.test(navigator.platform || navigator.userAgent)
const SHORTCUT = IS_MAC ? '⌘K' : 'Ctrl K'

/**
 * The sidebar's own chrome: static nav (NAV_GROUPS needs no data), plus whatever goes in its account-menu
 * slot - the real AccountMenu once signed in, or a quiet placeholder while that isn't known yet (see
 * LoadingShell below - AccountMenu reads `user.username` with no null-guard, relying on AuthGate having
 * always gated it behind a real signed-in user before this component existed; rendering it any earlier
 * would throw, not just look wrong).
 */
function SidebarNav({
  menu,
  setMenu,
  navRef,
  activeTo,
  approvals,
  found,
  accountSlot,
  onSearch,
  onShortcuts,
}: {
  menu: boolean
  setMenu: (v: boolean) => void
  navRef: RefObject<HTMLElement | null>
  activeTo: string | null
  approvals?: number
  found?: number
  accountSlot: ReactNode
  onSearch: () => void
  onShortcuts: () => void
}) {
  return (
    <>
      {menu && <div className="fixed inset-0 z-40 bg-black/60 xl:hidden" onClick={() => setMenu(false)} aria-hidden />}
      <aside
        id="main-menu"
        className={clsx(
          'fixed inset-y-0 left-0 z-50 flex w-60 shrink-0 flex-col overflow-y-auto border-r border-nb-850 bg-nb-920 px-3 py-4 transition-transform duration-200 xl:static xl:z-auto xl:min-h-0 xl:translate-x-0',
          menu ? 'translate-x-0' : '-translate-x-full',
        )}
      >
        <div className="mb-6 flex items-center gap-2.5 px-3">
          <BrandMark />
          <div className="text-base font-semibold tracking-tight text-nb-300">Ikhnos</div>
        </div>
        <button
          onClick={onSearch}
          className="mb-3 flex items-center gap-2.5 rounded-md border border-nb-850 bg-nb-925 px-3 py-2 text-left text-sm text-nb-500 hover:border-nb-800 hover:text-nb-400"
          aria-label={`Search (${SHORTCUT})`}
          data-testid="search-button"
        >
          <Search size={ICON_SM} aria-hidden /> Search
          <kbd className="ml-auto rounded border border-nb-800 px-1.5 text-[11px]">{SHORTCUT}</kbd>
        </button>
        <nav ref={navRef} className="relative flex flex-col gap-3">
          <NavIndicator containerRef={navRef} activeTo={activeTo} />
          {NAV_GROUPS.map((group) => (
            <div key={group.label} className="flex flex-col gap-1">
              <div className="px-3 text-[11px] font-medium uppercase tracking-wide text-nb-600">{group.label}</div>
              {group.items.map((n) => (
                <NavItem key={n.to} {...n} slid badge={n.to === '/discovery' ? found : n.to === '/agents' ? approvals : undefined} />
              ))}
            </div>
          ))}
        </nav>
        <div className="mt-auto border-t border-nb-850 pt-3">
          {accountSlot}
          <a
            href={DOCS_URL}
            target="_blank"
            rel="noreferrer"
            className="group relative flex items-center gap-3 rounded-md px-3 py-2 text-sm text-nb-400 transition-colors hover:bg-nb-930 hover:text-nb-300"
          >
            <BookOpen size={ICON_SM} /> Documentation
          </a>
          <NavItem to="/settings" label="Settings" icon={Settings2} />
          <button
            onClick={onShortcuts}
            className="flex w-full items-center gap-3 rounded-md px-3 py-2 text-left text-sm text-nb-400 transition-colors hover:bg-nb-930 hover:text-nb-300"
            data-testid="keyboard-shortcuts-open"
          >
            <Keyboard size={ICON_SM} /> Keyboard shortcuts
          </button>
          <Copyright className="mt-3 px-3" />
        </div>
      </aside>
    </>
  )
}

/**
 * What a page load shows before `checked` resolves (see Layout above): the same sidebar - full nav, no
 * badges (their counts need data this early), a skeleton where AccountMenu will mount - next to a
 * generic content skeleton, instead of the blank screen this replaced. No mobile top bar/hamburger here
 * (menu stays permanently closed): this state is normally a single network round-trip long, so trading a
 * moment of "can't open the drawer on a phone" for not having to wire up real interactivity for a state
 * that is about to be replaced entirely is the right side of that trade.
 */
function LoadingShell() {
  const navRef = useRef<HTMLElement>(null)
  return (
    <div className="flex h-full">
      <SidebarNav
        menu={false}
        setMenu={() => {}}
        navRef={navRef}
        activeTo={null}
        accountSlot={<SkeletonBlock className="mb-1 h-11 w-full rounded-md" />}
        onSearch={() => {}}
        onShortcuts={() => {}}
      />
      <div className="flex min-w-0 flex-1 flex-col">
        <main className="min-h-0 min-w-0 flex-1 overflow-y-auto">
          <PageSkeleton />
        </main>
      </div>
    </div>
  )
}

function Shell() {
  // Approving is done on Agents; Discovery holds what agents found for a person to decide.
  const approvals = useRawTopology((s) => s.agents.filter((a) => a.status === 'pending' && a.requestedAt).length)
  const found = useRawTopology((s) => s.suggestions.filter((x) => x.status === 'open').length)
  const { pathname } = useLocation()
  // The canvas page wants the full viewport; table pages get a padded container.
  const full = pathname.startsWith('/topology')
  const [searching, setSearching] = useState(false)
  const [shortcuts, setShortcuts] = useState(false)
  const navRef = useRef<HTMLElement>(null)
  const activeTo = NAV_GROUPS.flatMap((g) => g.items).find((n) => isNavActive(n.to, pathname))?.to ?? null
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
      // "?" opens the shortcuts panel, the same convention Gmail/GitHub/Slack use - but only away from any
      // text input (including the search palette's own), so it stays typeable as an ordinary character
      // everywhere a person might actually want to type a literal "?".
      if (e.key === '?' && !searching) {
        const el = document.activeElement
        const typing = el instanceof HTMLElement && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable)
        if (!typing) {
          e.preventDefault()
          setShortcuts((o) => !o)
        }
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [searching])

  return (
    <div className="flex h-full">
      <SidebarNav
        menu={menu}
        setMenu={setMenu}
        navRef={navRef}
        activeTo={activeTo}
        approvals={approvals}
        found={found}
        accountSlot={<AccountMenu />}
        onSearch={() => setSearching(true)}
        onShortcuts={() => setShortcuts(true)}
      />

      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex h-12 shrink-0 items-center gap-3 border-b border-nb-850 bg-nb-920 px-3 xl:hidden">
          <button onClick={() => setMenu(true)} aria-label="Open menu" aria-controls="main-menu" aria-expanded={menu} className="grid size-9 place-items-center rounded-md text-nb-400 hover:bg-nb-930 hover:text-nb-300">
            <Menu size={ICON_MD} />
          </button>
          <span className="text-sm font-semibold text-nb-300">Ikhnos</span>
          <button onClick={() => setSearching(true)} aria-label="Search" className="ml-auto grid size-9 place-items-center rounded-md text-nb-400 hover:bg-nb-930 hover:text-nb-300">
            <Search size={ICON_MD} />
          </button>
        </div>
        <SyncNotices />
        <SampleBanner />
        <HistoryBanner />
        <main className={clsx('min-h-0 min-w-0 flex-1', full ? 'flex flex-col' : 'overflow-y-auto')}>
          <ErrorBoundary key={pathname}>
            {/* Scoped to just this content pane, not the whole page (see App.tsx's own comment on why it
             * moved here): a lazy page chunk that hasn't loaded yet suspends only this <Outlet/>, so the
             * sidebar above stays mounted and interactive instead of vanishing along with it.
             *
             * The non-full-bleed wrapper below also carries `fade-in` (the same 0.15s fade already used
             * app-wide for small content swaps, see index.css) - keyed on pathname via the ErrorBoundary
             * above, so it replays on every navigation, not just the very first paint. Left off the `full`
             * (topology canvas) branch deliberately: that content is an interactive canvas, not something
             * read top-to-bottom, and a fresh wrapper div there risks fighting the canvas's own h-full
             * sizing for no real benefit. */}
            <Suspense fallback={<PageSkeleton />}>
              {full ? (
                <Outlet />
              ) : (
                <div className="fade-in mx-auto max-w-[1680px] px-4 py-6 sm:px-8 sm:py-8">
                  <Outlet />
                </div>
              )}
            </Suspense>
          </ErrorBoundary>
        </main>
      </div>
      <CommandPalette open={searching} onClose={() => setSearching(false)} />
      {shortcuts && <KeyboardShortcutsModal onClose={() => setShortcuts(false)} />}
    </div>
  )
}
