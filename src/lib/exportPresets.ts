// From install.ts, not consent.ts: this is a plain data file and must not depend on consent.ts, which
// itself depends on install.ts (install.ts's telemetryProblems reads EXPORT_PRESETS from here too).
import type { ExportProtocol, Modality } from './install'

/**
 * Recommended OTLP-native export destinations, shown as a pick-from-list option in the telemetry
 * destination field (see ComboField in primitives.tsx and its use in TelemetryFields.tsx) instead of
 * asking every person to already know their backend's exact endpoint pattern, port and credential
 * header by heart. Every entry here resolves to the chart's existing, unchanged `telemetry.export.otlp.*`
 * shape (see install.ts's withTelemetry) - this is UI sugar over that one generic exporter, not a new
 * export mode, so a destination not listed here is never unsupported, just not auto-filled.
 *
 * `endpointPattern` is shown as a hint, not parsed - regions/instance IDs/tenants are copied in by hand,
 * since they're account-specific and this app has no way to know them. `httpOnly` marks a backend whose
 * OTLP ingestion does not support gRPC at all (only 'http' would work as `exportProtocol`); presets
 * without it work with either.
 *
 * `modalities`, when set, is every modality this destination can actually receive - there is only ever one
 * `exportEndpoint` for every signal together (see TelemetryInput), so a preset that only speaks one
 * modality (Jaeger: traces) has to say so, rather than silently dropping whatever else was turned on.
 * Omitted means "a generic OTLP backend, anything goes" - true for every preset below except Jaeger.
 */
export interface ExportPreset {
  id: string
  label: string
  endpointPattern: string
  headerName: string
  protocol: ExportProtocol
  httpOnly?: boolean
  modalities?: Modality[]
  /** 'self-hosted' for something the person runs themselves (shown in its own group); absent means a
   *  hosted/cloud service. */
  group?: 'self-hosted'
  /** An in-cluster receiver that speaks plain text (no TLS) by default: picking it turns "skip TLS
   *  verification" on, which for this chart's exporter is what makes it use http:// / a plaintext gRPC channel. */
  plain?: boolean
  /** Extra words the destination search matches on (a Grafana stack's pieces all answer to "grafana"). */
  keywords?: string[]
  note?: string
  docsUrl: string
}

export const EXPORT_PRESETS: ExportPreset[] = [
  {
    id: 'honeycomb',
    label: 'Honeycomb',
    endpointPattern: 'api.honeycomb.io:443',
    headerName: 'x-honeycomb-team',
    protocol: 'grpc',
    docsUrl: 'https://docs.honeycomb.io/send-data/opentelemetry/',
  },
  {
    id: 'new-relic',
    label: 'New Relic',
    endpointPattern: 'otlp.nr-data.net:4317',
    headerName: 'api-key',
    protocol: 'grpc',
    note: 'Uses the header "api-key", not "Authorization".',
    docsUrl: 'https://docs.newrelic.com/docs/opentelemetry/best-practices/opentelemetry-otlp/',
  },
  {
    id: 'signoz-cloud',
    label: 'SigNoz Cloud',
    endpointPattern: 'ingest.<region>.signoz.cloud:443',
    headerName: 'signoz-ingestion-key',
    protocol: 'grpc',
    docsUrl: 'https://signoz.io/docs/instrumentation/opentelemetry-collector/',
  },
  {
    id: 'splunk-o11y',
    label: 'Splunk Observability Cloud',
    endpointPattern: 'ingest.<realm>.observability.splunkcloud.com:443',
    headerName: 'X-SF-Token',
    protocol: 'grpc',
    docsUrl: 'https://docs.splunk.com/observability/en/gdi/opentelemetry/opentelemetry.html',
  },
  {
    id: 'chronosphere',
    label: 'Chronosphere',
    endpointPattern: '<tenant>.chronosphere.io:443',
    headerName: 'API-Token',
    protocol: 'grpc',
    docsUrl: 'https://docs.chronosphere.io/ingest/otlp',
  },
  {
    id: 'jaeger',
    label: 'Jaeger (traces only)',
    endpointPattern: 'jaeger-collector.observability:4317',
    headerName: '',
    protocol: 'grpc',
    modalities: ['traces'],
    group: 'self-hosted',
    plain: true,
    note: 'Traces only - leave the other signals off, or point them somewhere else.',
    docsUrl: 'https://www.jaegertracing.io/docs/latest/apis/#opentelemetry-otlp',
  },
  {
    id: 'zipkin',
    label: 'Zipkin (traces only)',
    endpointPattern: 'zipkin.observability:9411',
    headerName: '',
    protocol: 'zipkin',
    modalities: ['traces'],
    group: 'self-hosted',
    plain: true,
    keywords: ['zipkin', 'b3'],
    note: 'Traces only. Spans are posted to the Zipkin API (/api/v2/spans) - a bare host:port gets that path added; a full URL is used as written.',
    docsUrl: 'https://zipkin.io/zipkin-api/',
  },
  {
    id: 'grafana-cloud',
    label: 'Grafana Cloud',
    endpointPattern: 'otlp-gateway-<region>.grafana.net/otlp',
    headerName: 'Authorization',
    protocol: 'http',
    httpOnly: true,
    note: 'HTTP only, no gRPC. Auth is Basic (base64 of instance-id:token) - put that whole value in the Secret this header reads from.',
    docsUrl: 'https://grafana.com/docs/grafana-cloud/send-data/otlp/',
  },
  {
    id: 'datadog',
    label: 'Datadog',
    endpointPattern: '<your-datadog-endpoint>:4318',
    headerName: 'dd-api-key',
    protocol: 'http',
    httpOnly: true,
    note: 'HTTP only, no gRPC. The hostname is account/site-specific - copy it from your Datadog org.',
    docsUrl: 'https://docs.datadoghq.com/opentelemetry/setup/otlp_ingest_in_the_agent/',
  },
  {
    id: 'elastic-cloud',
    label: 'Elastic Cloud',
    endpointPattern: '<your-deployment>.apm.<region>.cloud.es.io:443',
    headerName: 'Authorization',
    protocol: 'grpc',
    note: 'The endpoint is deployment-specific - copy it from your Elastic Cloud deployment. Header value is "ApiKey <key>".',
    docsUrl: 'https://www.elastic.co/guide/en/apm/guide/current/open-telemetry-elastic.html',
  },
  {
    id: 'prometheus',
    label: 'Prometheus',
    endpointPattern: 'prometheus.<namespace>.svc:9090/api/v1/otlp',
    headerName: '',
    protocol: 'http',
    httpOnly: true,
    modalities: ['metrics'],
    group: 'self-hosted',
    plain: true,
    note: 'Metrics only. Prometheus must run with --web.enable-otlp-receiver for this to work.',
    docsUrl: 'https://prometheus.io/docs/guides/opentelemetry/',
  },
  {
    id: 'loki',
    label: 'Grafana Loki',
    endpointPattern: 'loki.<namespace>.svc:3100/otlp',
    headerName: '',
    protocol: 'http',
    httpOnly: true,
    modalities: ['logs'],
    group: 'self-hosted',
    plain: true,
    keywords: ['grafana'],
    note: 'Logs only.',
    docsUrl: 'https://grafana.com/docs/loki/latest/send-data/otel/',
  },
  {
    id: 'tempo',
    label: 'Grafana Tempo',
    endpointPattern: 'tempo.<namespace>.svc:4317',
    headerName: '',
    protocol: 'grpc',
    modalities: ['traces'],
    group: 'self-hosted',
    plain: true,
    keywords: ['grafana'],
    note: 'Traces only.',
    docsUrl: 'https://grafana.com/docs/tempo/latest/configuration/',
  },
  {
    id: 'mimir',
    label: 'Grafana Mimir',
    endpointPattern: 'mimir-nginx.<namespace>.svc/otlp',
    headerName: '',
    protocol: 'http',
    httpOnly: true,
    modalities: ['metrics'],
    group: 'self-hosted',
    plain: true,
    keywords: ['grafana'],
    note: 'Metrics only.',
    docsUrl: 'https://grafana.com/docs/mimir/latest/configure/configure-otel-collector/',
  },
  {
    id: 'victoria-metrics',
    label: 'VictoriaMetrics',
    endpointPattern: 'victoria-metrics.<namespace>.svc:8428/opentelemetry',
    headerName: '',
    protocol: 'http',
    httpOnly: true,
    modalities: ['metrics'],
    group: 'self-hosted',
    plain: true,
    note: 'Metrics only.',
    docsUrl: 'https://docs.victoriametrics.com/victoriametrics/data-ingestion/opentelemetry-collector/',
  },
  {
    id: 'self-hosted',
    label: 'Self-hosted OTel Collector',
    endpointPattern: 'otel-gateway.example.com:4317',
    headerName: '',
    protocol: 'grpc',
    group: 'self-hosted',
    keywords: ['collector', 'gateway', 'otlp'],
    docsUrl: 'https://opentelemetry.io/docs/collector/',
  },
]

/**
 * Whether `preset` can carry every modality in `enabled` - true for any preset that doesn't restrict
 * modalities at all (a generic OTLP backend), false the moment one enabled modality isn't in its list.
 * Used to filter the destination picker so the compatible backend is the one that's easy to choose, per
 * whatever signals are already turned on, rather than something to notice has gone wrong after the fact.
 */
export function presetSupportsModalities(preset: ExportPreset, enabled: Set<Modality>): boolean {
  if (!preset.modalities) return true
  return [...enabled].every((m) => preset.modalities!.includes(m))
}

/**
 * Backends whose OTLP ingestion does not fit this chart's generic exporter (each needs its own dedicated
 * exporter component with cloud-IAM-style auth this chart does not implement, not a static header/Secret)
 * - checked against free text typed into the destination field so picking one of these says why, instead
 * of silently doing nothing.
 */
export const UNSUPPORTED_DESTINATIONS: { match: RegExp; label: string; why: string }[] = [
  { match: /aws|cloudwatch|x-ray|xray|amp\b/i, label: 'AWS (X-Ray / CloudWatch / AMP)', why: 'needs SigV4-signed IAM auth via a dedicated AWS exporter, not a static header - out of reach for this generic OTLP exporter.' },
  { match: /azure/i, label: 'Azure Monitor', why: "needs a dedicated exporter with connection-string/Entra ID auth; Microsoft's own docs mark generic OTLP as not officially supported for it." },
  { match: /google|gcp|cloud trace|stackdriver/i, label: 'Google Cloud (Trace/Monitoring)', why: 'accepts OTLP but only with short-lived OAuth2 tokens, which a static Secret-sourced header cannot provide.' },
]

export function unsupportedDestinationNote(text: string): string | undefined {
  const hit = UNSUPPORTED_DESTINATIONS.find((d) => d.match.test(text))
  return hit ? `${hit.label} ${hit.why}` : undefined
}
