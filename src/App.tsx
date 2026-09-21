import { lazy, Suspense } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import Layout from '@/components/Layout'

// Pages load on demand: the map, wizard and tables are big and most visits
// touch only one or two of them.
const AgentsPage = lazy(() => import('@/pages/AgentsPage'))
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

function Loading() {
  return <div className="p-8 text-sm text-nb-500" role="status">Loading…</div>
}

export default function App() {
  return (
    <BrowserRouter>
      <Suspense fallback={<Loading />}>
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
