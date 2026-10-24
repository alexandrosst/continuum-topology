/**
 * The Grafana dashboards that come with FUSION, for the screens that point at one. The uid is what Grafana knows it by (the chart's
 * dashboards/*.json); tests-ui/fusionDashboards.test.ts holds this list to those files, so a dashboard cannot be renamed or dropped from the
 * chart while a button here still asks for it.
 */
export interface ShippedDashboard {
  uid: string
  title: string
  about: string
  group: 'Fleet' | 'Links' | 'Kubernetes' | 'Telemetry' | 'Ikhnos'
  /** Whether its cluster picker can start on one cluster (the dashboard has a `cluster` variable that takes a cluster id). */
  clusterPicker?: boolean
}

export const DASHBOARDS: readonly ShippedDashboard[] = [
  { uid: 'ikhnos-fleet', title: 'Fleet overview', group: 'Fleet', about: 'All clusters side by side, by name: what is enrolled and sending, nodes and pods and whether they are healthy.' },
  { uid: 'ikhnos-clusters', title: 'Clusters and nodes', group: 'Kubernetes', about: 'Readiness, CPU, memory, disk, network and pod health of each cluster and node.', clusterPicker: true },
  { uid: 'ikhnos-workloads', title: 'Namespaces and workloads', group: 'Kubernetes', about: 'Pods, CPU, memory, network, restarts and replicas of a namespace or workload, with its logs.' },
  { uid: 'ikhnos-applications', title: 'Applications', group: 'Kubernetes', about: 'One application at a glance: its services, their pods, traces and logs.' },
  { uid: 'ikhnos-links', title: 'Cluster links', group: 'Links', about: 'Loss and delay between clusters measured as pairs, in each direction, with the route and traffic.' },
  { uid: 'ikhnos-link-matrix', title: 'Link quality matrix', group: 'Links', about: 'Every cluster against every other at a glance: loss, delay, jitter, throughput and freshness.' },
  { uid: 'ikhnos-delivery', title: 'Delivery health', group: 'Telemetry', about: 'Is the data arriving: freshness per cluster, through which operator, which signals, and the collectors themselves.' },
  { uid: 'ikhnos-categories', title: 'Telemetry by category', group: 'Telemetry', about: 'What FUSION holds, grouped by what it is about.' },
  { uid: 'ikhnos-self', title: 'Ikhnos self', group: 'Ikhnos', about: 'What running Ikhnos costs and whether it is healthy: the server process and every cluster’s agent.', clusterPicker: true },
]

export const DASHBOARD_GROUPS = ['Fleet', 'Kubernetes', 'Links', 'Telemetry', 'Ikhnos'] as const
