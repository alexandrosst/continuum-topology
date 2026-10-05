import type { ExportProtocol, Modality } from './install'
import type { Service } from './types'

/**
 * Telemetry receivers that discovery has already seen running in a cluster - the "Found in your cluster"
 * group of the destination step. Matching is deliberately modest: the image's repository name (never its
 * tag), against a short list of products whose OTLP ingestion is well known. A match is a hint, not a
 * fact - discovery sees workloads, not Kubernetes Services, so the address built from a workload's name is
 * a best guess the step says to check.
 */
export interface DetectedKind {
  id: string
  label: string
  /** Matches the image repository path, e.g. "grafana/loki" or "otel/opentelemetry-collector-contrib". */
  image: RegExp
  modalities: Modality[]
  /** The container port this receiver listens for OTLP on, and what follows it in the endpoint. */
  port: number
  path: string
  protocol: ExportProtocol
  note?: string
}

const ALL: Modality[] = ['metrics', 'logs', 'traces']

export const DETECTED_KINDS: DetectedKind[] = [
  { id: 'otel-collector', label: 'OpenTelemetry Collector', image: /(^|\/)(opentelemetry-collector(-contrib|-k8s|-otlp)?|otelcol(-contrib|-k8s)?)$/, modalities: ALL, port: 4317, path: '', protocol: 'grpc', note: 'Needs an OTLP receiver in its own configuration.' },
  { id: 'prometheus', label: 'Prometheus', image: /(^|\/)prometheus$/, modalities: ['metrics'], port: 9090, path: '/api/v1/otlp', protocol: 'http', note: 'Must run with --web.enable-otlp-receiver.' },
  { id: 'loki', label: 'Grafana Loki', image: /(^|\/)loki$/, modalities: ['logs'], port: 3100, path: '/otlp', protocol: 'http' },
  { id: 'tempo', label: 'Grafana Tempo', image: /(^|\/)tempo$/, modalities: ['traces'], port: 4317, path: '', protocol: 'grpc' },
  { id: 'mimir', label: 'Grafana Mimir', image: /(^|\/)mimir$/, modalities: ['metrics'], port: 80, path: '/otlp', protocol: 'http' },
  { id: 'victoria-metrics', label: 'VictoriaMetrics', image: /(^|\/)(victoria-metrics|victoriametrics)$/, modalities: ['metrics'], port: 8428, path: '/opentelemetry', protocol: 'http' },
  { id: 'zipkin', label: 'Zipkin', image: /(^|\/)openzipkin\/zipkin(-slim)?$|(^|\/)zipkin(-slim)?$/, modalities: ['traces'], port: 9411, path: '', protocol: 'zipkin', note: 'Spans are posted to its /api/v2/spans endpoint.' },
  { id: 'jaeger', label: 'Jaeger', image: /(^|\/)(jaeger|all-in-one|jaeger-collector|jaeger-all-in-one)$/, modalities: ['traces'], port: 4317, path: '', protocol: 'grpc' },
]

/** The namespace this platform's own components run in: its collectors are not somewhere to send to. */
const OWN_NAMESPACE = 'continuum-system'

/** "docker.io/grafana/loki:3.1@sha256:…" -> "grafana/loki" (registry host, tag and digest dropped). */
export function imageRepository(image: string): string {
  const noDigest = image.split('@')[0]
  const noTag = noDigest.replace(/:[^/:]*$/, '')
  const parts = noTag.split('/')
  // A first segment with a dot, a colon or "localhost" is a registry host, not part of the repository path.
  if (parts.length > 1 && /[.:]|^localhost$/.test(parts[0])) parts.shift()
  return parts.join('/').toLowerCase()
}

export interface DetectedBackend {
  /** Stable per workload (the discovered service's own id). */
  id: string
  kind: DetectedKind
  service: Pick<Service, 'id' | 'name' | 'namespace' | 'image' | 'clusterId'>
  /** host:port plus path, in-cluster. */
  endpoint: string
}

/**
 * What in `services` (one cluster's, or every cluster's) looks like a telemetry receiver. Anything in the
 * platform's own namespace is left out, and so is anything on a port discovery lists that is not the
 * receiver's - when discovery reports ports at all and the expected one is not among them, the workload is
 * probably a different component of the same product (a query frontend, say), and is skipped rather than
 * offered with a wrong address.
 */
export function detectBackends(services: Service[], opts: { clusterId?: string } = {}): DetectedBackend[] {
  const out: DetectedBackend[] = []
  for (const s of services) {
    if (opts.clusterId && s.clusterId !== opts.clusterId) continue
    if (!s.image || s.namespace === OWN_NAMESPACE) continue
    const repo = imageRepository(s.image)
    const kind = DETECTED_KINDS.find((k) => k.image.test(repo))
    if (!kind) continue
    if (s.ports && s.ports.length > 0 && !s.ports.includes(kind.port)) continue
    out.push({
      id: s.id,
      kind,
      service: { id: s.id, name: s.name, namespace: s.namespace, image: s.image, clusterId: s.clusterId },
      endpoint: `${s.name}.${s.namespace}.svc:${kind.port}${kind.path}`,
    })
  }
  return out.sort((a, b) => a.kind.label.localeCompare(b.kind.label) || a.endpoint.localeCompare(b.endpoint))
}
