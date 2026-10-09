import type { ComponentType } from 'react'

// Every lazily-loaded page's import(), keyed by the route path it's mounted at. App.tsx builds its lazy()
// components from this same map instead of inlining `lazy(() => import(...))` per route, so Layout's nav
// can reuse the identical loader to start fetching a page's chunk on hover/focus - before the click that
// actually navigates there. Calling a loader more than once is safe and free: dynamic import() is cached
// by the module system, so a hover followed by a real navigation just resolves the same in-flight or
// already-settled promise the hover started; a hover that's never followed by a click just leaves that one
// chunk cached for whenever it's needed.
//
// This matters now that navigation is a normal-priority update (see App.tsx's BrowserRouter comment):
// without a startTransition to keep the previous page visible while a new chunk loads, the shared
// <Suspense> fallback shows for however long that fetch takes. Most of these chunks are a few KB to a few
// dozen KB (a page's own code plus whichever of its own lazy sub-dependencies, e.g. ClustersPage pulls in
// usePlacement), so a hover's head start - typically a couple hundred ms before the click lands - is often
// enough to make the fallback a non-event. Deliberately NOT eagerly prefetched for everyone on load: a few
// pages (Sites' map view in particular) pull in multi-hundred-KB map-data chunks that most sessions never
// touch, and downloading those unasked would cost real bandwidth for no benefit.
export const routeLoaders: Record<string, () => Promise<{ default: ComponentType }>> = {
  '/topology': () => import('@/pages/TopologyPage'),
  '/clusters': () => import('@/pages/ClustersPage'),
  '/nodes': () => import('@/pages/NodesPage'),
  '/namespaces': () => import('@/pages/NamespacesPage'),
  '/services': () => import('@/pages/ServicesPage'),
  '/devices': () => import('@/pages/DevicesPage'),
  '/applications': () => import('@/pages/ApplicationsPage'),
  '/sites': () => import('@/pages/SitesPage'),
  '/discovery': () => import('@/pages/DiscoveryPage'),
  '/agents': () => import('@/pages/AgentsPage'),
  '/pipeline': () => import('@/pages/PipelinePage'),
  '/fusion': () => import('@/pages/FusionPage'),
  '/history': () => import('@/pages/HistoryPage'),
  '/system-health': () => import('@/pages/SystemHealthPage'),
  '/placement': () => import('@/pages/PlacementPage'),
  '/settings': () => import('@/pages/SettingsPage'),
  '/activity': () => import('@/pages/ActivityPage'),
  '/team': () => import('@/pages/TeamPage'),
}

/** Starts fetching a route's chunk ahead of navigation. Safe to call for any string, including redirects
 *  like `/workloads` or `/users` that have no entry here - those are no-ops. */
export function prefetchRoute(to: string) {
  void routeLoaders[to]?.()
}

/** Starts fetching every route's own chunk, once the browser is idle after first load. This is safe to
 *  add on top of the hover/focus prefetch above (import() caching means a later hover or click just
 *  resolves the same settled promise this starts) and closes the gap hover/focus prefetch doesn't: a click
 *  that lands before a hover's head start resolves - fast pointer movement, a keyboard/tab-driven
 *  navigation, or the very first click of a session, none of which ever fire a hover/focus event first.
 *  Deliberately still NOT eager for the multi-hundred-KB map-data chunks (`cities`/`countries-*`, see
 *  MapView.tsx's own lazy `loadCoarse`/`loadFine`): those are a separate, further lazy import triggered
 *  only once a map view actually renders, not part of any page's own chunk here, so looping over every
 *  entry in routeLoaders never touches them - only each page's own, much smaller code chunk (a few KB to
 *  ~120KB for the heaviest, Topology) - in a production build.
 *
 *  Skipped entirely in dev (`import.meta.env.DEV`): the "a few KB to ~120KB" cost above only holds for a
 *  built, minified, per-route chunk. Vite's dev server serves each module of a route's own dependency graph
 *  unbundled and unminified - Placement's own transitive deps alone (d3-geo, topojson-client, its
 *  world-map data) run to several hundred KB of extra requests that would otherwise fire right after the
 *  very first paint of an unrelated page like Topology, in dev mode's own most bandwidth- and
 *  main-thread-constrained moment. Dev mode doesn't need this mechanism anyway: Vite's own module graph
 *  already keeps every previously-visited route's modules cached for instant reload, and a hard navigation
 *  during active development is rare enough that hover/focus prefetch alone is plenty. */
export function prefetchAllRoutesWhenIdle() {
  if (import.meta.env.DEV) return
  const run = () => {
    for (const to of Object.keys(routeLoaders)) prefetchRoute(to)
  }
  if (typeof requestIdleCallback === 'function') {
    requestIdleCallback(run, { timeout: 3000 })
  } else {
    setTimeout(run, 1000)
  }
}
