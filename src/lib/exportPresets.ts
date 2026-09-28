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
 */
export interface ExportPreset {
  id: string
  label: string
  endpointPattern: string
  headerName: string
  protocol: 'grpc' | 'http'
  httpOnly?: boolean
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
    note: 'Traces only - leave the other signals off, or point them somewhere else.',
    docsUrl: 'https://www.jaegertracing.io/docs/latest/apis/#opentelemetry-otlp',
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
    id: 'self-hosted',
    label: 'Self-hosted OTel Collector',
    endpointPattern: 'otel-gateway.example.com:4317',
    headerName: '',
    protocol: 'grpc',
    docsUrl: 'https://opentelemetry.io/docs/collector/',
  },
]

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
