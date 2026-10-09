import { lazy, useEffect } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import Layout from '@/components/Layout'
import { prefetchAllRoutesWhenIdle, routeLoaders } from '@/lib/routeLoaders'

// Pages load on demand: the map, wizard and tables are big and most visits touch only one or two of them.
// Built from routeLoaders (not `lazy(() => import(...))` inlined here) so Layout's nav can reuse the exact
// same loader to prefetch a page's chunk on hover/focus - see routeLoaders.ts for why.
const AgentsPage = lazy(routeLoaders['/agents'])
const PipelinePage = lazy(routeLoaders['/pipeline'])
const FusionPage = lazy(routeLoaders['/fusion'])
const ActivityPage = lazy(routeLoaders['/activity'])
const ApplicationsPage = lazy(routeLoaders['/applications'])
const ClustersPage = lazy(routeLoaders['/clusters'])
const DevicesPage = lazy(routeLoaders['/devices'])
const DiscoveryPage = lazy(routeLoaders['/discovery'])
const HistoryPage = lazy(routeLoaders['/history'])
const SystemHealthPage = lazy(routeLoaders['/system-health'])
const NamespacesPage = lazy(routeLoaders['/namespaces'])
const NodesPage = lazy(routeLoaders['/nodes'])
const PlacementPage = lazy(routeLoaders['/placement'])
const SettingsPage = lazy(routeLoaders['/settings'])
const SitesPage = lazy(routeLoaders['/sites'])
const TopologyPage = lazy(routeLoaders['/topology'])
const TeamPage = lazy(routeLoaders['/team'])
const ServicesPage = lazy(routeLoaders['/services'])

/** The old /operators address keeps working: the page is now the Pipeline, and ?cat= carries over. */
function OperatorsRedirect() {
  const { search } = useLocation()
  return <Navigate to={`/pipeline${search}`} replace />
}

export default function App() {
  // Hover/focus prefetch (Layout's NavItem) covers most navigations, but not a click that lands before
  // the hover head start resolves - a fast pointer, a keyboard-driven nav, or the session's very first
  // click. Idle-time prefetch closes that gap once, after whatever the initial page itself needed to load
  // has settled, without competing with it for bandwidth. See routeLoaders.ts for what this does and does
  // not fetch.
  useEffect(() => {
    prefetchAllRoutesWhenIdle()
  }, [])

  return (
    // React Router v7 wraps every navigation - a <Link>/<NavLink> click, useNavigate(), useSearchParams()'s
    // setter, all of it - in React.startTransition() unless told not to (BrowserRouter's own `useTransitions`
    // prop, default true - see node_modules/react-router/dist/.../chunk-HQO5H5CC.js's BrowserRouter). A
    // transition is *low priority*: Layout's server poll (useServerPolling, every 2-5s) dispatches a
    // normal-priority store update on the same page, and a normal-priority update interrupts and restarts
    // an in-progress low-priority one. On a heavy page like Topology, if a render triggered by navigation
    // doesn't finish inside one poll window, the next poll interrupts it before it commits - and if that
    // keeps happening, the navigation never gets an uninterrupted pass to complete. Nothing throws (a
    // starved transition isn't an error) and nothing here reads useTransition()'s `isPending` to show it's
    // stuck, so this is silent: the URL updates (history changes synchronously, outside React) but the page
    // never repaints to match - indistinguishable from "broken" until a reload clears it. useSearchParams()
    // goes through the exact same navigate() path, so Topology's own filters are just as exposed. Turning
    // transitions off makes every navigation a normal-priority update like everything else in the app - the
    // trade-off (no automatic "keep old content visible while the next page suspends" smoothing) is one
    // this app already doesn't rely on: nothing reads isPending, and Layout's own <Suspense> (wrapping only
    // its content pane's <Outlet/>, not the whole page - see Layout.tsx) already has an explicit fallback
    // skeleton for a lazy page's first load. That Suspense boundary deliberately sits inside Layout, not
    // here: a boundary here would catch a suspending lazy page and unmount everything above it too,
    // including Layout's own sidebar - the exact "the whole page goes blank, sidebar included" flash this
    // was written to stop happening.
    <BrowserRouter useTransitions={false}>
      <Routes>
        <Route element={<Layout />}>
          <Route index element={<Navigate to="/topology" replace />} />
          <Route path="/topology" element={<TopologyPage />} />
          <Route path="/clusters" element={<ClustersPage />} />
          <Route path="/nodes" element={<NodesPage />} />
          <Route path="/namespaces" element={<NamespacesPage />} />
          <Route path="/services" element={<ServicesPage />} />
          <Route path="/devices" element={<DevicesPage />} />
          <Route path="/applications" element={<ApplicationsPage />} />
          <Route path="/sites" element={<SitesPage />} />
          <Route path="/discovery" element={<DiscoveryPage />} />
          <Route path="/agents" element={<AgentsPage />} />
          <Route path="/pipeline" element={<PipelinePage />} />
          <Route path="/fusion/*" element={<FusionPage />} />
          <Route path="/operators" element={<OperatorsRedirect />} />
          <Route path="/history" element={<HistoryPage />} />
          <Route path="/system-health" element={<SystemHealthPage />} />
          <Route path="/placement" element={<PlacementPage />} />
          <Route path="/workloads" element={<Navigate to="/services" replace />} />
          <Route path="/settings" element={<SettingsPage />} />
          <Route path="/activity" element={<ActivityPage />} />
          <Route path="/team" element={<TeamPage />} />
          <Route path="/users" element={<Navigate to="/team" replace />} />
          <Route path="*" element={<Navigate to="/topology" replace />} />
        </Route>
      </Routes>
    </BrowserRouter>
  )
}
