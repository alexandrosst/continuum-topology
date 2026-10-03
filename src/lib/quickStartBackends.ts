import type { QuickStartKind } from './history'

/**
 * Specs for the "don't have a backend yet?" quick-start option next to the telemetry destination field
 * (see QuickStartBackends.tsx). Each one is a plain `helm install` against a well-known upstream chart -
 * the same "mechanism only, here's a command" shape every other install in this app already uses
 * (ConnectClusterWizard, regional operators): this app never deploys or dials a user's cluster itself, so
 * "quick-start" means generating a ready-to-run command, not an automatic install. Scoped to the two
 * modalities most people ask for first (traces, metrics) - logs is a reasonable fast-follow, not covered
 * here (see the Task 6 writeup: "we don't need to cover everything for now").
 *
 * Both commands below were checked against the chart's actual current templates/values (not just
 * remembered from an older major version) after a review caught the previous drafts pointing at a
 * values shape neither chart's latest release still has - see each command's own comment.
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
    // The jaegertracing/jaeger chart moved to the OTel-collector-based "Jaeger v2" engine (chart 4.x):
    // there is no more allInOne/agent/collector/query split, and the chart renders exactly one Service
    // named after the release itself (jaeger.fullname - the release name, since it already contains
    // "jaeger"), carrying every port (otlp-grpc 4317, http-query 16686, ...) together. Getting a working
    // deployment (not just one that installs without error) needs a full collector config - a bare "turn
    // on badger" flag doesn't exist anymore - passed as `userconfig` (see the chart's own
    // cmd/jaeger/config-badger.yaml for this exact shape), which the chart writes to a ConfigMap and
    // passes as --config on the binary. The liveness/readiness probes hit :13133/status unconditionally,
    // so healthcheckv2 is not optional - without it the pod never becomes ready.
    exportEndpoint: (ns) => `jaeger-quickstart.${ns}.svc:4317`,
    exportProtocol: 'grpc',
    command: (ns, retention) => {
      const userconfig = JSON.stringify({
        service: {
          extensions: ['healthcheckv2', 'jaeger_storage', 'jaeger_query'],
          pipelines: { traces: { receivers: ['otlp'], processors: ['batch'], exporters: ['jaeger_storage_exporter'] } },
        },
        extensions: {
          healthcheckv2: { use_v2: true, http: null },
          jaeger_query: { storage: { traces: 'store' } },
          jaeger_storage: { backends: { store: { badger: { directories: { keys: '/tmp/jaeger/', values: '/tmp/jaeger/' }, ephemeral: false, ttl: { spans: retention } } } } },
        },
        receivers: { otlp: { protocols: { grpc: null, http: null } } },
        processors: { batch: null },
        exporters: { jaeger_storage_exporter: { trace_storage: 'store' } },
      })
      return (
        `helm repo add jaegertracing https://jaegertracing.github.io/helm-charts\n` +
        `helm repo update jaegertracing\n` +
        `helm upgrade --install jaeger-quickstart jaegertracing/jaeger --namespace ${ns} --create-namespace \\\n` +
        `  --set-json 'userconfig=${userconfig}'`
      )
    },
    openHint: 'The Jaeger UI (query service), once reachable.',
    portForward: (ns) => `kubectl -n ${ns} port-forward svc/jaeger-quickstart 16686:16686`,
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
    // Port 80, not 9090: the server Service's declared port is 80 (proxying to the container's 9090) -
    // a ClusterIP Service only answers on the port it declares, in-cluster callers included.
    exportEndpoint: (ns) => `prometheus-quickstart-server.${ns}.svc:80/api/v1/otlp`,
    exportProtocol: 'http',
    command: (ns, retention) =>
      `helm repo add prometheus-community https://prometheus-community.github.io/helm-charts\n` +
      `helm repo update prometheus-community\n` +
      `helm upgrade --install prometheus-quickstart prometheus-community/prometheus --namespace ${ns} --create-namespace \\\n` +
      `  --set server.retention=${retention} \\\n` +
      `  --set-json 'server.extraFlags=["web.enable-lifecycle","web.enable-otlp-receiver"]' \\\n` +
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
