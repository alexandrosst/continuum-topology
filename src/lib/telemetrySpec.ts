import { isKept, KEPT_GROUPS, type ExportTarget, type ScopeOverrideInput, type TelemetryInput } from './install'

// What the server is asked to write a telemetry command from: backend/internal/install's Telemetry, as JSON. The web page keeps the
// draft a person is editing (including which groups are still "as installed", which only the draft can tell); this is that draft said
// in the words the renderer takes, so the page no longer builds the command itself.

const destination = (d: ExportTarget) => ({
  endpoint: d.exportEndpoint, protocol: d.exportProtocol, insecure: d.exportInsecure,
  authHeader: d.exportAuthHeaderName, authSecret: d.exportAuthSecretName, authKey: d.exportAuthSecretKey, operator: d.exportOperatorId,
})

const scope = (s: ScopeOverrideInput) => ({ namespaces: s.namespaces, exclude: s.exclude, workloads: s.workloads })

export function telemetrySpec(t: TelemetryInput, measurementsOn?: boolean) {
  return {
    signals: {
      resourceUsage: t.resourceUsage, energy: t.energy, kubernetesState: t.kubernetesState, nodeRuntime: t.nodeRuntime, networkLatency: t.networkLatency,
      clusterLinks: t.clusterLinks, applicationMetrics: t.applicationMetrics, systemLogs: t.systemLogs, kubernetesEvents: t.kubernetesEvents,
      applicationLogs: t.applicationLogs, traces: t.traces, accelerators: t.accelerators,
    },
    energy: { kind: t.energySource, endpoint: t.energyExistingEndpoint },
    accelerators: { kind: t.acceleratorsSource, endpoint: t.acceleratorsExistingEndpoint, applyScope: t.acceleratorsApplyScope },
    clusterLinks: { probe: t.clusterLinksProbe, throughput: t.clusterLinksThroughput, authSecret: t.clusterLinksAuthSecret },
    scopes: { applicationMetrics: scope(t.applicationMetricsScope), applicationLogs: scope(t.applicationLogsScope), traces: scope(t.tracesScope), infrastructure: t.scopeInfrastructure },
    destination: destination(t),
    routes: {
      split: t.exportSplit, metrics: destination(t.exportLanes.metrics), logs: destination(t.exportLanes.logs), traces: destination(t.exportLanes.traces),
      installed: t.exportRoutesInstalled, kept: t.exportLanesKept,
    },
    processing: {
      resourceDetection: t.resourceDetection, redaction: t.redaction,
      // An emptied field is NaN, which JSON writes as null: a number that is not one, which the renderer refuses.
      tracesSamplingPercent: Number.isFinite(t.tracesSamplingPercent) ? t.tracesSamplingPercent : null,
      debug: t.debugVerbosity, tags: t.tags,
      extra: t.extraProcessors.map((e) => ({ id: e.id, kind: e.kind, name: e.name, config: e.config, raw: e.raw })),
    },
    owner: { orgId: t.resourceOrgId, clusterId: t.resourceClusterId },
    kept: KEPT_GROUPS.filter((g) => isKept(t, g)),
    hadTelemetry: t.hadTelemetry,
    ...(measurementsOn === undefined ? {} : { measurementsOn }),
  }
}
