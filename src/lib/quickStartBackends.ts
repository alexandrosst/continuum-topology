import type { QuickStartKind } from './history'

/**
 * Specs for the "don't have a backend yet?" quick-start option next to the telemetry destination field
 * (see QuickStartBackends.tsx). Each one is a plain `helm install` against a well-known upstream chart -
 * the same "mechanism only, here's a command" shape every other install in this app already uses
 * (ConnectClusterWizard, regional operators): this app never deploys or dials a user's cluster itself, so
 * "quick-start" means generating a ready-to-run command, not an automatic install. Scoped to the two
 * modalities most people ask for first (traces, metrics) - logs is a reasonable fast-follow, not covered
 * here (see the Task 6 writeup: "we don't need to cover everything for now").
 */
export interface QuickStartSpec {
  kind: QuickStartKind
  label: string
  modality: 'traces' | 'metrics'
  defaultRetention: string
  retentionHint: string
  defaultNamespace: string
  /** The exportEndpoint value to fill in once this is installed in the SAME cluster an agent release runs
   *  in - a bare host:port (or host:port/path for the http-only case) the chart's own generic OTLP
   *  exporter can point at directly. Cross-cluster telemetry still needs a real, externally reachable
   *  destination; this one-line in-cluster install is for getting something up fast, not fleet-wide
   *  collection - said plainly in the UI, not just implied by what's offered. */
  exportEndpoint: (namespace: string) => string
  exportProtocol: 'grpc' | 'http'
  command: (namespace: string, retention: string) => string
  openHint: string
  portForward: (namespace: string) => string
  docsUrl: string
}

export const QUICK_START_BACKENDS: QuickStartSpec[] = [
  {
    kind: 'jaeger',
    label: 'Jaeger (traces)',
    modality: 'traces',
    defaultRetention: '72h',
    retentionHint: 'How long a trace stays queryable (Badger storage TTL). Example: 24h, 72h, 168h.',
    defaultNamespace: 'observability',
    exportEndpoint: (ns) => `jaeger-quickstart-collector.${ns}.svc:4317`,
    exportProtocol: 'grpc',
    command: (ns, retention) =>
      `helm repo add jaegertracing https://jaegertracing.github.io/helm-charts\n` +
      `helm repo update jaegertracing\n` +
      `helm upgrade --install jaeger-quickstart jaegertracing/jaeger --namespace ${ns} --create-namespace \\\n` +
      `  --set provisionDataStore.cassandra=false --set storage.type=badger \\\n` +
      `  --set allInOne.enabled=true --set agent.enabled=false --set collector.enabled=false --set query.enabled=false \\\n` +
      `  --set-json 'allInOne.args=["--badger.ephemeral=false","--badger.directory-value=/badger/data","--badger.directory-key=/badger/key","--badger.span-store-ttl=${retention}"]'`,
    openHint: 'The Jaeger UI (query service), once reachable.',
    portForward: (ns) => `kubectl -n ${ns} port-forward svc/jaeger-quickstart-query 16686:16686`,
    docsUrl: 'https://www.jaegertracing.io/docs/latest/getting-started/',
  },
  {
    kind: 'prometheus',
    label: 'Prometheus (metrics)',
    modality: 'metrics',
    defaultRetention: '15d',
    retentionHint: 'How long metrics are kept on disk (--storage.tsdb.retention.time). Example: 15d, 30d, 90d.',
    defaultNamespace: 'observability',
    // Prometheus's OTLP receiver is HTTP-only, under /api/v1/otlp - the otlphttp exporter appends
    // /v1/metrics to whatever path is already here itself, so this is the full base path it needs.
    exportEndpoint: (ns) => `prometheus-quickstart-server.${ns}.svc:9090/api/v1/otlp`,
    exportProtocol: 'http',
    command: (ns, retention) =>
      `helm repo add prometheus-community https://prometheus-community.github.io/helm-charts\n` +
      `helm repo update prometheus-community\n` +
      `helm upgrade --install prometheus-quickstart prometheus-community/prometheus --namespace ${ns} --create-namespace \\\n` +
      `  --set server.retention=${retention} \\\n` +
      `  --set-json 'server.extraFlags=["web.enable-otlp-receiver"]' \\\n` +
      `  --set alertmanager.enabled=false --set prometheus-pushgateway.enabled=false --set prometheus-node-exporter.enabled=false`,
    openHint: 'The Prometheus web UI, once reachable.',
    portForward: (ns) => `kubectl -n ${ns} port-forward svc/prometheus-quickstart-server 9090:80`,
    docsUrl: 'https://prometheus.io/docs/prometheus/latest/feature_flags/#otlp-receiver',
  },
]

export function quickStartSpec(kind: QuickStartKind): QuickStartSpec {
  const s = QUICK_START_BACKENDS.find((q) => q.kind === kind)
  if (!s) throw new Error(`unknown quick-start kind ${kind}`)
  return s
}
