import { lazy, Suspense } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import Layout from '@/components/Layout'
import { PageSkeleton } from '@/components/ui/primitives'

// Pages load on demand: the map, wizard and tables are big and most visits
// touch only one or two of them.
const AgentsPage = lazy(() => import('@/pages/AgentsPage'))
const RegionalOperatorsPage = lazy(() => import('@/pages/RegionalOperatorsPage'))
const ActivityPage = lazy(() => import('@/pages/ActivityPage'))
const ApplicationsPage = lazy(() => import('@/pages/ApplicationsPage'))
const ClustersPage = lazy(() => import('@/pages/ClustersPage'))
const DevicesPage = lazy(() => import('@/pages/DevicesPage'))
const DiscoveryPage = lazy(() => import('@/pages/DiscoveryPage'))
const HistoryPage = lazy(() => import('@/pages/HistoryPage'))
const NamespacesPage = lazy(() => import('@/pages/NamespacesPage'))
const NodesPage = lazy(() => import('@/pages/NodesPage'))
const PlacementPage = lazy(() => import('@/pages/PlacementPage'))
const SettingsPage = lazy(() => import('@/pages/SettingsPage'))
const SitesPage = lazy(() => import('@/pages/SitesPage'))
const TopologyPage = lazy(() => import('@/pages/TopologyPage'))
const TeamPage = lazy(() => import('@/pages/TeamPage'))
const ServicesPage = lazy(() => import('@/pages/ServicesPage'))

export default function App() {
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
    // this app already doesn't rely on: nothing reads isPending, and the one shared <Suspense> below already
    // has an explicit fallback skeleton for a lazy page's first load.
    <BrowserRouter useTransitions={false}>
      <Suspense fallback={<PageSkeleton />}>
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
            <Route path="/operators" element={<RegionalOperatorsPage />} />
            <Route path="/history" element={<HistoryPage />} />
            <Route path="/placement" element={<PlacementPage />} />
            <Route path="/workloads" element={<Navigate to="/services" replace />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="/activity" element={<ActivityPage />} />
            <Route path="/team" element={<TeamPage />} />
            <Route path="/users" element={<Navigate to="/team" replace />} />
            <Route path="*" element={<Navigate to="/topology" replace />} />
          </Route>
        </Routes>
      </Suspense>
    </BrowserRouter>
  )
}
