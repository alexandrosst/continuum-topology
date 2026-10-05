import type { QuickStartKind } from './history'

/**
 * Specs for the "don't have a backend yet?" quick-start option next to the telemetry destination field
 * (see QuickStartBackends.tsx). Each one is a plain `helm install` against a well-known upstream chart -
 * the same "mechanism only, here's a command" shape every other install in this app already uses
 * (ConnectClusterWizard, regional operators): this app never deploys or dials a user's cluster itself, so
 * "quick-start" means generating a ready-to-run command, not an automatic install.
 *
 * Every command below was checked against the chart's actual current templates/values (not just
 * remembered from an older major version, or from docs alone) - verified by actually running
 * `helm template` against each chart with the exact flags the command passes, after a review caught an
 * earlier Jaeger/Prometheus draft pointing at a values shape neither chart's latest release still has.
 */
export interface QuickStartSpec {
  kind: QuickStartKind
  label: string
  modality: 'traces' | 'metrics' | 'logs'
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
    kind: 'zipkin',
    label: 'Zipkin (traces)',
    modality: 'traces',
    defaultRetention: '500000',
    retentionHint: 'How many spans Zipkin keeps in memory (MEM_MAX_SPANS) before dropping the oldest. Digits only. Everything is lost when the pod restarts.',
    defaultNamespace: 'observability',
    // Zipkin cannot ingest OTLP (it takes its own v1/v2 span formats on :9411), so unlike the other three this
    // is two small Deployments: Zipkin itself, and an OpenTelemetry Collector in front of it that receives
    // OTLP and forwards to Zipkin's /api/v2/spans with the collector's own `zipkin` exporter. The endpoint
    // an agent exports to is therefore the collector's Service, not Zipkin's. Plain manifests rather than a
    // chart: there is no official Zipkin chart this app could pin, and the whole thing is two Deployments
    // and two Services. Zipkin uses its in-memory store, hence "retention" being a span count and the
    // "not for keeping data around" warning the wizard already shows.
    exportEndpoint: (ns) => `zipkin-quickstart-otlp.${ns}.svc:4317`,
    exportProtocol: 'grpc',
    command: (ns, retention) => {
      const maxSpans = retention.replace(/\D/g, '') || '500000'
      return (
        `kubectl apply -f - <<'EOF'\n` +
        `apiVersion: v1\n` +
        `kind: Namespace\n` +
        `metadata:\n` +
        `  name: ${ns}\n` +
        `---\n` +
        `apiVersion: apps/v1\n` +
        `kind: Deployment\n` +
        `metadata:\n` +
        `  name: zipkin-quickstart\n` +
        `  namespace: ${ns}\n` +
        `spec:\n` +
        `  replicas: 1\n` +
        `  selector:\n` +
        `    matchLabels: {app: zipkin-quickstart}\n` +
        `  template:\n` +
        `    metadata:\n` +
        `      labels: {app: zipkin-quickstart}\n` +
        `    spec:\n` +
        `      containers:\n` +
        `        - name: zipkin\n` +
        `          image: openzipkin/zipkin-slim:3\n` +
        `          ports: [{name: http, containerPort: 9411}]\n` +
        `          env:\n` +
        `            - {name: STORAGE_TYPE, value: mem}\n` +
        `            - {name: MEM_MAX_SPANS, value: "${maxSpans}"}\n` +
        `          readinessProbe:\n` +
        `            httpGet: {path: /health, port: http}\n` +
        `---\n` +
        `apiVersion: v1\n` +
        `kind: Service\n` +
        `metadata:\n` +
        `  name: zipkin-quickstart\n` +
        `  namespace: ${ns}\n` +
        `spec:\n` +
        `  selector: {app: zipkin-quickstart}\n` +
        `  ports: [{name: http, port: 9411, targetPort: http}]\n` +
        `---\n` +
        `apiVersion: v1\n` +
        `kind: ConfigMap\n` +
        `metadata:\n` +
        `  name: zipkin-quickstart-otlp\n` +
        `  namespace: ${ns}\n` +
        `data:\n` +
        `  config.yaml: |\n` +
        `    receivers:\n` +
        `      otlp:\n` +
        `        protocols:\n` +
        `          grpc: {endpoint: 0.0.0.0:4317}\n` +
        `          http: {endpoint: 0.0.0.0:4318}\n` +
        `    processors:\n` +
        `      batch: {}\n` +
        `    exporters:\n` +
        `      zipkin:\n` +
        `        endpoint: http://zipkin-quickstart.${ns}.svc:9411/api/v2/spans\n` +
        `        tls: {insecure: true}\n` +
        `    service:\n` +
        `      pipelines:\n` +
        `        traces: {receivers: [otlp], processors: [batch], exporters: [zipkin]}\n` +
        `---\n` +
        `apiVersion: apps/v1\n` +
        `kind: Deployment\n` +
        `metadata:\n` +
        `  name: zipkin-quickstart-otlp\n` +
        `  namespace: ${ns}\n` +
        `spec:\n` +
        `  replicas: 1\n` +
        `  selector:\n` +
        `    matchLabels: {app: zipkin-quickstart-otlp}\n` +
        `  template:\n` +
        `    metadata:\n` +
        `      labels: {app: zipkin-quickstart-otlp}\n` +
        `    spec:\n` +
        `      containers:\n` +
        `        - name: collector\n` +
        `          image: otel/opentelemetry-collector-contrib:0.160.0\n` +
        `          args: ["--config=/conf/config.yaml"]\n` +
        `          ports: [{name: otlp-grpc, containerPort: 4317}, {name: otlp-http, containerPort: 4318}]\n` +
        `          volumeMounts: [{name: conf, mountPath: /conf}]\n` +
        `      volumes:\n` +
        `        - name: conf\n` +
        `          configMap: {name: zipkin-quickstart-otlp}\n` +
        `---\n` +
        `apiVersion: v1\n` +
        `kind: Service\n` +
        `metadata:\n` +
        `  name: zipkin-quickstart-otlp\n` +
        `  namespace: ${ns}\n` +
        `spec:\n` +
        `  selector: {app: zipkin-quickstart-otlp}\n` +
        `  ports: [{name: otlp-grpc, port: 4317, targetPort: otlp-grpc}, {name: otlp-http, port: 4318, targetPort: otlp-http}]\n` +
        `EOF`
      )
    },
    openHint: 'The Zipkin UI, once reachable.',
    portForward: (ns) => `kubectl -n ${ns} port-forward svc/zipkin-quickstart 9411:9411`,
    docsUrl: 'https://zipkin.io/pages/quickstart.html',
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
  {
    kind: 'loki',
    label: 'Loki (logs)',
    modality: 'logs',
    defaultRetention: '168h',
    retentionHint: 'How long logs are kept before the compactor deletes them. Example: 168h (7d), 720h (30d).',
    defaultNamespace: 'observability',
    // grafana/loki's own "single binary, no cloud storage" reference values still bundle MinIO by
    // default (even SingleBinary mode normally wants an S3-shaped object store) - the chart's own escape
    // hatch for a genuinely local, zero-extra-component install is `useTestSchema: true` with
    // `storage.type: filesystem` (literally their own "for testing or playing around" values.yaml
    // comment). Disabling everything deploymentMode: SingleBinary doesn't already turn off itself
    // (gateway, the canary DaemonSet, the two memcached-backed caches) gets it down to exactly one pod -
    // but disabling the canary alone breaks a hard chart-level validation ("Helm test requires the Loki
    // Canary to be enabled") unless test.enabled is turned off too. OTLP logs land on the standard HTTP
    // port under /otlp, same "exporter appends /v1/logs itself" shape as the other two.
    exportEndpoint: (ns) => `loki-quickstart.${ns}.svc:3100/otlp`,
    exportProtocol: 'http',
    command: (ns, retention) =>
      `helm repo add grafana https://grafana.github.io/helm-charts
` +
      `helm repo update grafana
` +
      `helm upgrade --install loki-quickstart grafana/loki --namespace ${ns} --create-namespace \
` +
      `  --set deploymentMode=SingleBinary --set singleBinary.replicas=1 \
` +
      `  --set read.replicas=0 --set write.replicas=0 --set backend.replicas=0 \
` +
      `  --set gateway.enabled=false --set lokiCanary.enabled=false --set test.enabled=false \
` +
      `  --set chunksCache.enabled=false --set resultsCache.enabled=false \
` +
      `  --set loki.auth_enabled=false --set loki.commonConfig.replication_factor=1 \
` +
      `  --set loki.useTestSchema=true --set loki.storage.type=filesystem \
` +
      `  --set loki.limits_config.retention_period=${retention} \
` +
      `  --set loki.compactor.retention_enabled=true --set loki.compactor.delete_request_store=filesystem`,
    openHint: 'Grafana, pointed at this Loki as a data source, once reachable (Loki itself has no UI).',
    portForward: (ns) => `kubectl -n ${ns} port-forward svc/loki-quickstart 3100:3100`,
    docsUrl: 'https://grafana.com/docs/loki/latest/send-data/otel/',
  },
]

export function quickStartSpec(kind: QuickStartKind): QuickStartSpec {
  const s = QUICK_START_BACKENDS.find((q) => q.kind === kind)
  if (!s) throw new Error(`unknown quick-start kind ${kind}`)
  return s
}

/** Whether `kind` has a catalog entry above - true for jaeger/zipkin/prometheus/loki, false for "custom" (see
 * QuickStartBackend.kind in history.ts). A "custom" backend has no known upstream chart, so none of this
 * catalog's generated commands (install, port-forward) or the Part C gateway manifest (gatewaySpec in
 * quickStartGateway.ts) apply to it - only its own user-supplied label and tool URL do. */
export function hasQuickStartSpec(kind: QuickStartKind): boolean {
  return QUICK_START_BACKENDS.some((q) => q.kind === kind)
}
