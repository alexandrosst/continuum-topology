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
  '/operators': () => import('@/pages/RegionalOperatorsPage'),
  '/history': () => import('@/pages/HistoryPage'),
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
