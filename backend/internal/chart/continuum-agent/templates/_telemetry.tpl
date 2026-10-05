{{/* OTel Collector config generation for this agent's two telemetry workloads (host DaemonSet, cluster
     Deployment) - processor/exporter/extension YAML fragments, resource sizing, and the signal-name list the
     agent container itself reports. Split out of _helpers.tpl because this is the single biggest, most
     self-contained concern in this chart (see continuum-regional-operator's own _helpers.tpl for the same
     generation logic applied to that chart's one standalone collector - duplicated, not shared, since each
     chart must still be a fully self-contained, independently installable Helm chart; see that file's own
     comments for the cross-references between the two). */}}

{{/* Telemetry: which of the two collector workloads (if either) this release needs. A signal counts as
     "host" when it can only be observed per-node (resource usage, kubelet-sourced metrics, node/container
     logs) and must therefore run on every node as a DaemonSet; everything else - cluster-wide object
     watching, and anything applications push directly - runs once as a Deployment. */}}
{{- define "agent.telemetryHostEnabled" -}}
{{- $t := .Values.telemetry -}}
{{- if or $t.resourceUsage.metrics.enabled $t.nodeRuntime.metrics.enabled $t.systemLogs.logs.enabled -}}true{{- end -}}
{{- end -}}
{{- define "agent.telemetryClusterEnabled" -}}
{{- $t := .Values.telemetry -}}
{{- if or $t.energy.metrics.enabled $t.accelerators.metrics.enabled $t.kubernetesState.metrics.enabled $t.kubernetesEvents.logs.enabled $t.applicationMetrics.metrics.enabled $t.applicationLogs.logs.enabled $t.traces.traces.enabled $t.networkLatency.metrics.enabled -}}true{{- end -}}
{{- end -}}
{{- define "agent.telemetryEnabled" -}}
{{- if or (include "agent.telemetryHostEnabled" .) (include "agent.telemetryClusterEnabled" .) -}}true{{- end -}}
{{- end -}}

{{/* Whether the k8sattributes processor (pod/namespace/node metadata enrichment) is needed - true for every
     telemetry signal, so its RBAC is granted whenever telemetry is on at all rather than per-signal. */}}
{{- define "agent.telemetryK8sAttrsEnabled" -}}{{- include "agent.telemetryEnabled" . -}}{{- end -}}

{{- define "agent.telemetryOtlpReceiverEnabled" -}}
{{- $t := .Values.telemetry -}}
{{- if or $t.applicationMetrics.metrics.enabled $t.applicationLogs.logs.enabled $t.traces.traces.enabled $t.networkLatency.metrics.enabled -}}true{{- end -}}
{{- end -}}

{{/* Validates the parts of telemetry that cross signals: an export endpoint is required once anything
     is enabled, and networkLatency has nothing to re-emit unless the underlying measurement is itself on. */}}
{{- define "agent.telemetryValidate" -}}
{{- if include "agent.telemetryEnabled" . -}}
{{- if not .Values.telemetry.export.otlp.endpoint -}}{{- fail "telemetry.export.otlp.endpoint is required once any telemetry.* signal is enabled" -}}{{- end -}}
{{- end -}}
{{- if and .Values.telemetry.networkLatency.metrics.enabled (not .Values.measurements.enabled) -}}
{{- fail "telemetry.networkLatency.metrics.enabled requires measurements.enabled: true - there is nothing to re-emit otherwise" -}}
{{- end -}}
{{- if and .Values.telemetry.energy.metrics.enabled (eq .Values.telemetry.energy.metrics.source "existing") (not .Values.telemetry.energy.metrics.existing.prometheusEndpoint) -}}
{{- fail "telemetry.energy.metrics.source=existing requires telemetry.energy.metrics.existing.prometheusEndpoint" -}}
{{- end -}}
{{- if and .Values.telemetry.accelerators.metrics.enabled (eq .Values.telemetry.accelerators.metrics.source "existing") (not .Values.telemetry.accelerators.metrics.existing.prometheusEndpoint) -}}
{{- fail "telemetry.accelerators.metrics.source=existing requires telemetry.accelerators.metrics.existing.prometheusEndpoint" -}}
{{- end -}}
{{- if .Values.telemetry.scope.selector -}}
{{- fail "telemetry.scope.selector is not implemented: filtering already-received telemetry by an arbitrary Kubernetes label selector isn't something the collector can do cleanly after the fact. Use telemetry.scope.namespaces / telemetry.scope.exclude instead." -}}
{{- end -}}
{{- if and .Values.telemetry.opamp.enabled (not .Values.telemetry.opamp.server.endpoint) -}}
{{- fail "telemetry.opamp.enabled requires telemetry.opamp.server.endpoint" -}}
{{- end -}}
{{- if gt (len .Values.telemetry.resource.attributes) 10 -}}
{{- fail "telemetry.resource.attributes takes at most 10 tags: every one is stamped on every record, so each costs storage and cardinality downstream" -}}
{{- end -}}
{{- $seenTags := dict -}}
{{- range .Values.telemetry.resource.attributes -}}
{{- if hasPrefix "continuum." (lower .key) -}}
{{- fail (printf "telemetry.resource.attributes: %q uses the continuum. prefix, which is reserved for the provenance this chart stamps itself (org, cluster, intent, scope)" .key) -}}
{{- end -}}
{{- if hasKey $seenTags .key -}}
{{- fail (printf "telemetry.resource.attributes: %q is listed twice" .key) -}}
{{- end -}}
{{- $_ := set $seenTags .key true -}}
{{- end -}}
{{- if and .Values.telemetry.debug.verbosity (not (has .Values.telemetry.debug.verbosity (list "basic" "detailed"))) -}}
{{- fail "telemetry.debug.verbosity must be empty (off), basic or detailed" -}}
{{- end -}}
{{- if and .Values.telemetry.receiver.auth.enabled (not .Values.telemetry.receiver.auth.secretName) -}}
{{- fail "telemetry.receiver.auth.enabled requires telemetry.receiver.auth.secretName" -}}
{{- end -}}
{{- if and .Values.telemetry.export.otlp.tls.mtls.enabled (not .Values.telemetry.export.otlp.tls.mtls.secretName) -}}
{{- fail "telemetry.export.otlp.tls.mtls.enabled requires telemetry.export.otlp.tls.mtls.secretName (a Secret holding tls.crt, tls.key, and ca.crt)" -}}
{{- end -}}
{{- end -}}

{{/* Turns a scope (namespaces to keep, namespaces to drop, workloads to keep inside a namespace) into the
     OTTL conditions a filterprocessor DROPS on, as a JSON list - callers `| fromJsonArray` it. Takes a dict:
       namespaces, exclude  lists of namespace names (Kubernetes namespace names are DNS-1123 labels, so a regex
                            alternation built from them needs no escaping)
       workloads            list of {namespace, names}. A namespace listed here keeps only those
                            workloads (matched against the deployment, statefulset, daemonset, job or cronjob
                            name k8sattributes put on the record); listed with no names keeps nothing of it.
                            Workload names are restricted to [a-z0-9-] by values.schema.json for the same
                            no-escaping reason.
       exempt               appended to every condition (networkLatency's standing exemption)
       guard                true for infrastructure signals: a record carrying no namespace at all (a node, the
                            host's own CPU) is never dropped, only one whose namespace is out of scope.
                            Application signals keep the old rule, where no namespace means dropped.
       nsExprs              optional: where to read the namespace from. Defaults to the resource attribute. */}}
{{- define "agent.telemetryScopeConditions" -}}
{{- $out := list -}}
{{- $exempt := .exempt | default "" -}}
{{- $nsExprs := .nsExprs | default (list "resource.attributes[\"k8s.namespace.name\"]") -}}
{{- range $nsExpr := $nsExprs -}}
{{- if $.namespaces -}}
{{- $m := printf "not IsMatch(%s, \"^(%s)$\")" $nsExpr (join "|" $.namespaces) -}}
{{- if $.guard }}{{ $m = printf "%s != nil and %s" $nsExpr $m }}{{ end -}}
{{- $out = append $out (printf "(%s)%s" $m $exempt) -}}
{{- end -}}
{{- if $.exclude -}}
{{- $out = append $out (printf "(IsMatch(%s, \"^(%s)$\"))%s" $nsExpr (join "|" $.exclude) $exempt) -}}
{{- end -}}
{{- end -}}
{{- $nsAttr := "resource.attributes[\"k8s.namespace.name\"]" -}}
{{- range $w := (.workloads | default list) -}}
{{- $ns := $w.namespace -}}
{{- $names := $w.names -}}
{{- if $names -}}
{{- $any := list -}}
{{- range $a := list "k8s.deployment.name" "k8s.statefulset.name" "k8s.daemonset.name" "k8s.job.name" "k8s.cronjob.name" -}}
{{- $any = append $any (printf "IsMatch(resource.attributes[\"%s\"], \"^(%s)$\")" $a (join "|" $names)) -}}
{{- end -}}
{{- $out = append $out (printf "(%s == \"%s\" and not (%s))%s" $nsAttr $ns (join " or " $any) $exempt) -}}
{{- else -}}
{{- $out = append $out (printf "(%s == \"%s\")%s" $nsAttr $ns $exempt) -}}
{{- end -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{/* Whether any scope in this release narrows by workload - the one case where k8sattributes has to extract
     the workload names (a few more attributes on every record, so only when something filters on them). */}}
{{- define "agent.telemetryWorkloadScopeEnabled" -}}
{{- $t := .Values.telemetry -}}
{{- if or $t.scope.workloads $t.scope.infra.workloads $t.applicationMetrics.metrics.scope.workloads $t.applicationLogs.logs.scope.workloads $t.traces.traces.scope.workloads -}}true{{- end -}}
{{- end -}}

{{/* The metadata list k8sattributes extracts, in both collector configs. */}}
{{- define "agent.telemetryK8sAttrsMetadata" -}}
{{- $m := list "k8s.namespace.name" "k8s.pod.name" "k8s.pod.uid" "k8s.node.name" "k8s.deployment.name" -}}
{{- if include "agent.telemetryWorkloadScopeEnabled" . }}{{ $m = concat $m (list "k8s.statefulset.name" "k8s.daemonset.name" "k8s.job.name" "k8s.cronjob.name") }}{{ end -}}
{{- toJson $m -}}
{{- end -}}

{{/* The scope infrastructure signals follow (telemetry.scope.infra): only what carries a namespace is
     narrowed; nodes and the host's own metrics pass untouched. */}}
{{- define "agent.telemetryInfraScopeConditions" -}}
{{- $i := .Values.telemetry.scope.infra -}}
{{- include "agent.telemetryScopeConditions" (dict "namespaces" $i.namespaces "exclude" $i.exclude "workloads" $i.workloads "guard" true) -}}
{{- end -}}

{{/* True when either half of telemetry.scope is set - gates whether the filter/scope_* processors and the
     split app/infra pipelines are emitted in telemetry-cluster-config.yaml at all. */}}
{{- define "agent.telemetryScopeFilterEnabled" -}}
{{- if or .Values.telemetry.scope.namespaces .Values.telemetry.scope.exclude .Values.telemetry.scope.workloads -}}true{{- end -}}
{{- end -}}

{{/* Per-workload resource requests/limits: the override if the operator set one, else the shared default.
     Sprig has no clean "is this map non-empty" beyond truthiness, which works fine here since an empty map
     ({}) and an unset field both render as Go's nil/zero value and are falsy in a template {{- if }}. */}}
{{- define "agent.telemetryHostCollectorResources" -}}
{{- if .Values.telemetry.hostCollector.resources -}}{{- toYaml .Values.telemetry.hostCollector.resources -}}
{{- else -}}{{- toYaml .Values.telemetry.resources -}}{{- end -}}
{{- end -}}
{{- define "agent.telemetryClusterCollectorResources" -}}
{{- if .Values.telemetry.clusterCollector.resources -}}{{- toYaml .Values.telemetry.clusterCollector.resources -}}
{{- else -}}{{- toYaml .Values.telemetry.resources -}}{{- end -}}
{{- end -}}
{{- define "agent.telemetryKeplerResources" -}}
{{- if .Values.telemetry.energy.metrics.resources -}}{{- toYaml .Values.telemetry.energy.metrics.resources -}}
{{- else -}}{{- toYaml .Values.telemetry.resources -}}{{- end -}}
{{- end -}}
{{- define "agent.telemetryAcceleratorsResources" -}}
{{- if .Values.telemetry.accelerators.metrics.resources -}}{{- toYaml .Values.telemetry.accelerators.metrics.resources -}}
{{- else -}}{{- toYaml .Values.telemetry.resources -}}{{- end -}}
{{- end -}}

{{/* Baseline collector hygiene processor: always rendered, first in every pipeline. Protects the
     collector itself from being OOM-killed under unexpected load - not a user on/off toggle, only its
     thresholds are configurable (telemetry.processors.memoryLimiter). Paired with agent.telemetryGomemlimit
     below, which computes a GOMEMLIMIT env var for the same containers so Go's own GC backs off in step
     with this. */}}
{{- define "agent.telemetryMemoryLimiterYAML" -}}
memory_limiter:
  check_interval: {{ .Values.telemetry.processors.memoryLimiter.checkInterval | quote }}
  limit_percentage: {{ .Values.telemetry.processors.memoryLimiter.limitPercentage }}
  spike_limit_percentage: {{ .Values.telemetry.processors.memoryLimiter.spikeLimitPercentage }}
{{- end -}}

{{/* Off by default (telemetry.processors.resourceDetection.enabled) - emits nothing when disabled. The
     pipeline processor lists in telemetry-host-config.yaml/telemetry-cluster-config.yaml only reference
     this by name when it's actually enabled, so a disabled call site never needs to guard this itself.
     timeout guards against an unreachable cloud metadata endpoint blocking the pipeline indefinitely - see
     the detectors list in values.yaml for why cloud detectors aren't in the default set. */}}
{{- define "agent.telemetryResourceDetectionYAML" -}}
{{- if .Values.telemetry.processors.resourceDetection.enabled }}
resourcedetection:
  detectors: {{ .Values.telemetry.processors.resourceDetection.detectors | toJson }}
  timeout: {{ .Values.telemetry.processors.resourceDetection.timeout | quote }}
  override: true
{{- end }}
{{- end -}}

{{/* On by default (telemetry.processors.redaction.enabled) - see the field's own comment in values.yaml
     for exactly what allow_all_keys: true + blocked_key_patterns does and does not do (masks matching
     values, never drops anything else). */}}
{{- define "agent.telemetryRedactionYAML" -}}
{{- if .Values.telemetry.processors.redaction.enabled }}
redaction:
  allow_all_keys: true
  blocked_key_patterns: {{ .Values.telemetry.processors.redaction.blockedKeyPatterns | toJson }}
  summary: info
{{- end }}
{{- end -}}

{{/* Continuum provenance: stamps this release's org/cluster/telemetry-grant identity onto every signal
     this agent emits, as plain resource attributes - continuum.org.id, continuum.cluster.id,
     continuum.intent.id. Unlike every other processor above, this one is never a user on/off toggle and
     is not meant to be hand-edited in values.yaml: telemetry.resource.* is filled in by the server's own
     generated `helm upgrade` command when a telemetry grant is created or changed, the same way
     access.tier and server.caPin are server-composed rather than user-edited. It must win over anything a
     user's own telemetry.processors.extraProcessors sets on these same keys, so the host/cluster config
     templates append "resource/continuum" to every pipeline's processor list after extraProcessorNames
     (and, for traces, extraTracesProcessorNames) and right before the final "batch" - action: upsert plus
     running last beats whatever an earlier, user-supplied processor set first. */}}
{{- define "agent.telemetryContinuumProvenanceYAML" -}}
resource/continuum:
  attributes:
    - {key: continuum.org.id, value: {{ .Values.telemetry.resource.orgId | quote }}, action: upsert}
    - {key: continuum.cluster.id, value: {{ .Values.telemetry.resource.clusterId | quote }}, action: upsert}
    {{- if .Values.telemetry.resource.intentId }}
    - {key: continuum.intent.id, value: {{ .Values.telemetry.resource.intentId | quote }}, action: upsert}
    {{- end }}
    {{- if .Values.telemetry.resource.scope }}
    {{- /* What this telemetry covers ("shop; payments: api+worker"), so a backend can tell narrowed from whole-cluster data. */}}
    - {key: continuum.scope, value: {{ .Values.telemetry.resource.scope | quote }}, action: upsert}
    {{- end }}
{{- end -}}

{{/* Tags the person chose to put on everything this release emits (telemetry.resource.attributes, a list of
     {key, value} - a list rather than a map so that setting it in a `helm upgrade --reuse-values` replaces the
     whole set, where a map would keep every key an earlier command put there), as plain resource attributes. Two rules keep them from ever misleading a reader: they are INSERTED, never
     upserted, so a key an application already set on its own telemetry keeps the application's value; and
     the continuum.* names belong to resource/continuum above - validation (agent.telemetryValidate) refuses
     a tag using that prefix, and resource/continuum runs after this anyway. Only rendered, and only listed in
     the pipelines, when at least one tag is set. */}}
{{- define "agent.telemetryTagsYAML" -}}
{{- if .Values.telemetry.resource.attributes }}
resource/tags:
  attributes:
    {{- range .Values.telemetry.resource.attributes }}
    - {key: {{ .key | quote }}, value: {{ .value | quote }}, action: insert}
    {{- end }}
{{- end }}
{{- end -}}

{{/* The processors every pipeline ends with, in order: the tags (when any), provenance, then batch. */}}
{{- define "agent.telemetryTailProcessors" -}}
{{- $tail := list -}}
{{- if .Values.telemetry.resource.attributes }}{{ $tail = append $tail "resource/tags" }}{{ end -}}
{{- $tail = concat $tail (list "resource/continuum" "batch") -}}
{{- toJson $tail -}}
{{- end -}}

{{/* Off unless telemetry.debug.verbosity is "basic" (a count of what passed through, per batch, in the
     collector's own log - no content) or "detailed" (every record's content in that log: it can include
     whatever the telemetry carries, so it is for short, deliberate troubleshooting). */}}
{{- define "agent.telemetryDebugExporterYAML" -}}
{{- if .Values.telemetry.debug.verbosity }}
debug:
  verbosity: {{ .Values.telemetry.debug.verbosity }}
{{- end }}
{{- end -}}

{{/* The exporters list every pipeline names: the real destination, plus the debug exporter when it is on. */}}
{{- define "agent.telemetryExporterList" -}}
{{- $l := list (include "agent.telemetryExporterName" .) -}}
{{- if .Values.telemetry.debug.verbosity }}{{ $l = append $l "debug" }}{{ end -}}
{{- toJson $l -}}
{{- end -}}

{{/* Traces only, referenced only from the traces pipeline in telemetry-cluster-config.yaml. 100 (default)
     means no sampling; the pipeline only references this by name when the percentage is below 100, so a
     default install renders no probabilistic_sampler block or pipeline entry at all. */}}
{{- define "agent.telemetryProbabilisticSamplerYAML" -}}
{{- if lt (int .Values.telemetry.processors.tracesSampling.percentage) 100 }}
probabilistic_sampler:
  sampling_percentage: {{ .Values.telemetry.processors.tracesSampling.percentage }}
{{- end }}
{{- end -}}

{{/* Computes a GOMEMLIMIT value (~80% of a workload's resolved memory *limit*) from its resolved
     resources dict - pass the YAML text of agent.telemetryHostCollectorResources / ClusterCollectorResources
     / KeplerResources through `fromYaml` first, e.g.
     {{ include "agent.telemetryGomemlimit" (include "agent.telemetryHostCollectorResources" . | fromYaml) }}.
     Returns empty when there's no limits.memory to read, or it isn't in a Kubernetes quantity format this
     recognizes - GOMEMLIMIT is a Go-runtime nicety paired with memory_limiter above, not something worth
     failing a release over. Kubernetes' binary suffixes (Ki/Mi/Gi/Ti) map directly to GOMEMLIMIT's own IEC
     suffixes (KiB/MiB/GiB/TiB, same magnitude); Kubernetes' decimal SI suffixes (k/M/G/T) and a bare
     number both convert to a plain byte count instead, since GOMEMLIMIT itself has no decimal-suffix
     form. */}}
{{- define "agent.telemetryGomemlimit" -}}
{{- $raw := "" -}}
{{- if and .limits .limits.memory -}}{{- $raw = .limits.memory -}}{{- end -}}
{{- if $raw -}}
{{- if kindIs "string" $raw -}}
{{/* A Kubernetes quantity with a unit suffix always arrives as a YAML/JSON string (the suffix letters
     make it non-numeric) - e.g. "256Mi", "500M". Parsed here rather than trusted as pre-normalized. */}}
{{- if regexMatch "^[0-9]+(\\.[0-9]+)?(Ki|Mi|Gi|Ti|k|M|G|T)?$" $raw -}}
{{- $n := regexFind "^[0-9]+(\\.[0-9]+)?" $raw -}}
{{- $suf := regexFind "(Ki|Mi|Gi|Ti|k|M|G|T)$" $raw -}}
{{- $scale := dict "Ki" 1024.0 "Mi" 1048576.0 "Gi" 1073741824.0 "Ti" 1099511627776.0 "k" 1000.0 "M" 1000000.0 "G" 1000000000.0 "T" 1000000000000.0 -}}
{{- $factor := 1.0 -}}
{{- if $suf -}}{{- $factor = get $scale $suf -}}{{- end -}}
{{/* Always emitted as a bare byte count, never with the original unit suffix rescaled: GOMEMLIMIT
     supports a bare number of bytes directly, and re-expressing "80% of 1Gi" in Gi/Ti loses precision at
     small integer magnitudes (0.8 rounds right back to 1) - bytes has none of that problem at any size. */}}
{{- printf "%.0f" (mulf (mulf (float64 $n) $factor) 0.8) -}}
{{- end -}}
{{- else -}}
{{/* A bare byte count with no suffix at all (e.g. `memory: 1000000` written directly in a values file)
     decodes as an actual YAML/JSON number, not a string - use it directly rather than routing it through
     the string/regex path above, which a Go %v-formatted float (e.g. "1e+06") would silently fail to
     match. */}}
{{- printf "%.0f" (mulf (float64 $raw) 0.8) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* The bearertokenauth extension (server-side use - confighttp/configgrpc's per-protocol "auth:
     authenticator: bearertokenauth" field references this by name), and the one extra env entry its token
     needs, only when telemetry.receiver.auth.enabled. Both no-ops otherwise, so callers can always include
     them unconditionally. Cluster collector only - see telemetry-cluster-config.yaml. */}}
{{- define "agent.telemetryReceiverAuthExtensionYAML" -}}
{{- if .Values.telemetry.receiver.auth.enabled }}
bearertokenauth:
  token: "${env:CONTINUUM_TELEMETRY_RECEIVER_AUTH}"
{{- end }}
{{- end -}}
{{- define "agent.telemetryReceiverAuthEnv" -}}
{{- if .Values.telemetry.receiver.auth.enabled }}
- name: CONTINUUM_TELEMETRY_RECEIVER_AUTH
  valueFrom:
    secretKeyRef:
      name: {{ .Values.telemetry.receiver.auth.secretName }}
      key: {{ .Values.telemetry.receiver.auth.secretKey }}
{{- end }}
{{- end -}}

{{/* Comma-separated list of enabled telemetry signal names, for the agent container's own
     CONTINUUM_TELEMETRY_SIGNALS env var (see deployment.yaml): the agent self-reports this in its
     Diagnostics, the same way it already self-reports its installed RBAC tier, so the UI can show what
     telemetry is actually installed without needing a live control-plane channel for it. Purely
     informational - the agent never interprets or enforces these names itself. */}}
{{- define "agent.telemetrySignalNames" -}}
{{- $t := .Values.telemetry -}}
{{- $n := list -}}
{{- if $t.resourceUsage.metrics.enabled }}{{ $n = append $n "resourceUsage" }}{{ end -}}
{{- if $t.energy.metrics.enabled }}{{ $n = append $n "energy" }}{{ end -}}
{{- if $t.accelerators.metrics.enabled }}{{ $n = append $n "accelerators" }}{{ end -}}
{{- if $t.kubernetesState.metrics.enabled }}{{ $n = append $n "kubernetesState" }}{{ end -}}
{{- if $t.nodeRuntime.metrics.enabled }}{{ $n = append $n "nodeRuntime" }}{{ end -}}
{{- if $t.networkLatency.metrics.enabled }}{{ $n = append $n "networkLatency" }}{{ end -}}
{{- if $t.applicationMetrics.metrics.enabled }}{{ $n = append $n "applicationMetrics" }}{{ end -}}
{{- if $t.systemLogs.logs.enabled }}{{ $n = append $n "systemLogs" }}{{ end -}}
{{- if $t.kubernetesEvents.logs.enabled }}{{ $n = append $n "kubernetesEvents" }}{{ end -}}
{{- if $t.applicationLogs.logs.enabled }}{{ $n = append $n "applicationLogs" }}{{ end -}}
{{- if $t.traces.traces.enabled }}{{ $n = append $n "traces" }}{{ end -}}
{{- join "," $n -}}
{{- end -}}

{{/* The effective-configuration counterparts to agent.telemetrySignalNames above, for the agent container's
     own CONTINUUM_TELEMETRY_* env vars (see deployment.yaml): not just which signals are on, but how each is
     actually configured - the destination, the processor settings telemetry-intent.md names as mattering
     most for "safe by default" (redaction, resourcedetection, traces sampling), and which source backs
     energy/accelerators when either is on. Same self-report mechanism installed_tier already uses, extended
     from a flat ceiling to a small config shape - see Diagnostics.installed_telemetry_config's own doc
     comment in agent.proto. Every one of these is only ever read, during `helm template`/`helm upgrade`, by
     deployment.yaml below gating the whole block on agent.telemetryEnabled, so none of them needs its own
     "is telemetry even on" guard. */}}
{{- define "agent.telemetryEffectiveDestination" -}}{{- .Values.telemetry.export.otlp.endpoint -}}{{- end -}}
{{- define "agent.telemetryEffectiveRedactionEnabled" -}}{{- .Values.telemetry.processors.redaction.enabled -}}{{- end -}}
{{- define "agent.telemetryEffectiveResourceDetectionEnabled" -}}{{- .Values.telemetry.processors.resourceDetection.enabled -}}{{- end -}}

{{/* The OpAMP extension block (ALPHA status upstream), and its entry in service.extensions - both emit
     nothing when telemetry.opamp.enabled is false, so callers can always include them unconditionally.
     Report-only: reports_effective_config/reports_health/reports_available_components default to true in
     the extension itself and are left at their defaults here rather than restated. */}}
{{- define "agent.telemetryOpampExtensionYAML" -}}
{{- if .Values.telemetry.opamp.enabled -}}
opamp:
  server:
    ws:
      endpoint: {{ .Values.telemetry.opamp.server.endpoint | quote }}
      {{- if or .Values.telemetry.opamp.server.tls.insecure .Values.telemetry.opamp.server.tls.caFile }}
      tls:
        insecure: {{ .Values.telemetry.opamp.server.tls.insecure }}
        {{- if .Values.telemetry.opamp.server.tls.caFile }}
        ca_file: {{ .Values.telemetry.opamp.server.tls.caFile | quote }}
        {{- end }}
      {{- end }}
      {{- if .Values.telemetry.opamp.server.headers }}
      headers:
        {{- range $k, $v := .Values.telemetry.opamp.server.headers }}
        {{ $k }}: {{ $v | quote }}
        {{- end }}
      {{- end }}
{{- end -}}
{{- end -}}

{{- define "agent.telemetryName" -}}continuum-telemetry{{- end -}}

{{/* "otlp" (configgrpc) or "otlphttp" (confighttp) - whichever telemetry.export.otlp.protocol asks for.
     Every exporters:/pipelines: reference below uses this instead of a literal "otlp", so the two stay in
     sync - see the bug this fixed: protocol=http rendered an httpOnly destination (Grafana Cloud, Datadog)
     under the gRPC-only exporter, which those backends simply do not speak. */}}
{{- define "agent.telemetryExporterName" -}}
{{- if eq .Values.telemetry.export.otlp.protocol "http" -}}otlphttp{{- else -}}otlp{{- end -}}
{{- end -}}

{{/* The "exporters" stanza shared by both collector ConfigMaps. Emits at column 0; the caller nindents it
     into place. The auth header's value is never written here - only a reference to the environment
     variable the container injects it into from a Secret at start (see telemetryExporterEnv below). */}}
{{- define "agent.telemetryExporterYAML" -}}
{{- if eq .Values.telemetry.export.otlp.protocol "http" }}
{{/* confighttp's otlphttp exporter has no configgrpc-style "insecure" toggle - the endpoint's own scheme
     IS that choice, and the collector appends /v1/<signal> to whatever is given here itself (so a path
     already in the endpoint, like Grafana Cloud's "…/otlp", still gets that suffix added on top - this is
     the backend's own documented shape, not something to strip). */}}
otlphttp:
  endpoint: {{ printf "%s://%s" (ternary "http" "https" .Values.telemetry.export.otlp.tls.insecure) .Values.telemetry.export.otlp.endpoint | quote }}
  {{- if or .Values.telemetry.export.otlp.tls.mtls.enabled .Values.telemetry.export.otlp.tls.caFile }}
  tls:
    {{- if .Values.telemetry.export.otlp.tls.mtls.enabled }}
    ca_file: /export-mtls/ca.crt
    cert_file: /export-mtls/tls.crt
    key_file: /export-mtls/tls.key
    {{- else }}
    ca_file: {{ .Values.telemetry.export.otlp.tls.caFile | quote }}
    {{- end }}
  {{- end }}
  {{- if .Values.telemetry.export.otlp.auth.secretName }}
  headers:
    {{ .Values.telemetry.export.otlp.auth.headerName }}: "${env:CONTINUUM_TELEMETRY_AUTH}"
  {{- end }}
{{- else }}
otlp:
  endpoint: {{ .Values.telemetry.export.otlp.endpoint | quote }}
  tls:
    insecure: {{ .Values.telemetry.export.otlp.tls.insecure }}
    {{- if .Values.telemetry.export.otlp.tls.mtls.enabled }}
    {{/* mtls.secretName is mounted at /export-mtls (see telemetry.yaml) - its ca.crt takes priority over
         a plain caFile below, since the same Secret already carries the one this destination actually
         trusts (whatever minted the client certificate also minted the server certificate to verify). */}}
    ca_file: /export-mtls/ca.crt
    cert_file: /export-mtls/tls.crt
    key_file: /export-mtls/tls.key
    {{- else if .Values.telemetry.export.otlp.tls.caFile }}
    ca_file: {{ .Values.telemetry.export.otlp.tls.caFile | quote }}
    {{- end }}
  {{- if .Values.telemetry.export.otlp.auth.secretName }}
  headers:
    {{ .Values.telemetry.export.otlp.auth.headerName }}: "${env:CONTINUUM_TELEMETRY_AUTH}"
  {{- end }}
{{- end }}
{{- end -}}

{{/* The one extra env entry a telemetry collector container needs beyond NODE_NAME, only when an auth
     header is configured. A no-op (empty) otherwise, so callers can always include it unconditionally. */}}
{{- define "agent.telemetryExporterEnv" -}}
{{- if .Values.telemetry.export.otlp.auth.secretName }}
- name: CONTINUUM_TELEMETRY_AUTH
  valueFrom:
    secretKeyRef:
      name: {{ .Values.telemetry.export.otlp.auth.secretName }}
      key: {{ .Values.telemetry.export.otlp.auth.secretKey }}
{{- end }}
{{- end -}}

{{/* Telemetry's own images: the OTel Collector Contrib distribution, and (only when bundled) Kepler.
     Digest-vs-tag resolution mirrors agent.image/agent.imagePullPolicy above exactly. */}}
{{- define "agent.telemetryCollectorImage" -}}
{{- $i := .Values.telemetry.collectorImage -}}
{{- if $i.digest -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" (toString $i.digest)) -}}{{- fail (printf "telemetry.collectorImage.digest must look like sha256:<64 hex characters>, got %q" (toString $i.digest)) -}}{{- end -}}
{{- printf "%s@%s" $i.repository $i.digest -}}
{{- else -}}
{{- printf "%s:%s" $i.repository (toString $i.tag) -}}
{{- end -}}
{{- end -}}
{{- define "agent.telemetryCollectorImagePullPolicy" -}}{{- .Values.telemetry.collectorImage.pullPolicy | default "IfNotPresent" -}}{{- end -}}

{{- define "agent.telemetryKeplerImage" -}}
{{- $i := .Values.telemetry.energy.metrics.keplerImage -}}
{{- if $i.digest -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" (toString $i.digest)) -}}{{- fail (printf "telemetry.energy.metrics.keplerImage.digest must look like sha256:<64 hex characters>, got %q" (toString $i.digest)) -}}{{- end -}}
{{- printf "%s@%s" $i.repository $i.digest -}}
{{- else -}}
{{- printf "%s:%s" $i.repository (toString $i.tag) -}}
{{- end -}}
{{- end -}}
{{- define "agent.telemetryKeplerImagePullPolicy" -}}{{- .Values.telemetry.energy.metrics.keplerImage.pullPolicy | default "IfNotPresent" -}}{{- end -}}

{{- define "agent.telemetryAcceleratorsImage" -}}
{{- $i := .Values.telemetry.accelerators.metrics.dcgmImage -}}
{{- if $i.digest -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" (toString $i.digest)) -}}{{- fail (printf "telemetry.accelerators.metrics.dcgmImage.digest must look like sha256:<64 hex characters>, got %q" (toString $i.digest)) -}}{{- end -}}
{{- printf "%s@%s" $i.repository $i.digest -}}
{{- else -}}
{{- printf "%s:%s" $i.repository (toString $i.tag) -}}
{{- end -}}
{{- end -}}
{{- define "agent.telemetryAcceleratorsImagePullPolicy" -}}{{- .Values.telemetry.accelerators.metrics.dcgmImage.pullPolicy | default "IfNotPresent" -}}{{- end -}}
