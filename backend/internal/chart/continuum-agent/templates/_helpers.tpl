{{- define "agent.name" -}}continuum-agent{{- end -}}
{{- define "agent.labels" -}}
app.kubernetes.io/name: continuum-agent
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: continuum
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}
{{- define "agent.identitySecret" -}}continuum-agent-identity{{- end -}}
{{- define "agent.tier" -}}
{{- $t := int .Values.access.tier -}}
{{- if or (lt $t 0) (gt $t 2) -}}{{- fail "access.tier must be 0, 1 or 2 in this release" -}}{{- end -}}
{{- $t -}}
{{- end -}}

{{/* The one image for every role. A digest wins over the tag: repository@sha256:... */}}
{{- define "agent.image" -}}
{{- $i := .Values.image -}}
{{- if $i.digest -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" (toString $i.digest)) -}}{{- fail (printf "image.digest must look like sha256:<64 hex characters>, got %q" (toString $i.digest)) -}}{{- end -}}
{{- printf "%s@%s" $i.repository $i.digest -}}
{{- else -}}
{{- printf "%s:%s" $i.repository (toString ($i.tag | default .Chart.AppVersion)) -}}
{{- end -}}
{{- end -}}

{{/* Effective imagePullPolicy: an explicit value always wins; otherwise Always when the resolved tag is the
     moving `edge` tag (a node that already cached anything named edge won't re-pull under IfNotPresent even
     after the registry moves on), else IfNotPresent. */}}
{{- define "agent.imagePullPolicy" -}}
{{- $i := .Values.image -}}
{{- if $i.pullPolicy -}}
{{- $i.pullPolicy -}}
{{- else if eq (toString ($i.tag | default .Chart.AppVersion)) "edge" -}}
Always
{{- else -}}
IfNotPresent
{{- end -}}
{{- end -}}

{{/* Extra pod labels from values, one per line, refusing the labels the chart's selectors depend on. Indent with nindent. */}}
{{- define "agent.podLabels" -}}
{{- range $k, $v := .Values.podLabels }}
{{- if or (hasPrefix "app.kubernetes.io/" $k) (hasPrefix "helm.sh/" $k) }}{{ fail (printf "podLabels: %q is set by the chart and cannot be overridden" $k) }}{{ end }}
{{ $k }}: {{ $v | quote }}
{{- end }}
{{- end -}}

{{- define "agent.probeSecretName" -}}continuum-agent-probe{{- end -}}
{{- define "agent.probeSecret" -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace (include "agent.probeSecretName" .) -}}
{{- if .Values.nodeProbe.secret -}}{{- .Values.nodeProbe.secret -}}
{{- else if and $existing $existing.data (hasKey $existing.data "secret") -}}{{- index $existing.data "secret" | b64dec -}}
{{- else -}}{{- randAlphaNum 48 -}}
{{- end -}}
{{- end -}}
{{- define "agent.flowSecretName" -}}continuum-agent-flows{{- end -}}
{{- define "agent.flowSecret" -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace (include "agent.flowSecretName" .) -}}
{{- if .Values.flowObserver.secret -}}{{- .Values.flowObserver.secret -}}
{{- else if and $existing $existing.data (hasKey $existing.data "secret") -}}{{- index $existing.data "secret" | b64dec -}}
{{- else -}}{{- randAlphaNum 48 -}}
{{- end -}}
{{- end -}}

{{/*
  What goes into a pod's "checksum/..." annotation, so that changing a secret rolls the pods that read it at start.
  The probe and flow secrets are generated (randAlphaNum) unless given, and every render draws a fresh random value on a first
  install, so hashing the rendered Secret would make the annotation differ from one render to the next and roll the pods on
  every upgrade. This hashes only what the operator controls: the value they gave, or the fixed word "generated" (a
  generated secret is created once and then kept by `lookup`, so it never changes underneath the pods).
*/}}
{{- define "agent.probeSecretRevision" -}}{{ .Values.nodeProbe.secret | default "generated" | sha256sum }}{{- end -}}
{{- define "agent.flowSecretRevision" -}}{{ .Values.flowObserver.secret | default "generated" | sha256sum }}{{- end -}}

{{- define "agent.receiver" -}}{{- if or .Values.nodeProbe.enabled .Values.flowObserver.enabled -}}true{{- end -}}{{- end -}}

{{/* The port of server.address (host:port; the last colon wins, so [::1]:8443 works too). */}}
{{- define "agent.serverPort" -}}{{- regexReplaceAll "^.*:" (toString .Values.server.address) "" -}}{{- end -}}

{{/* enrollment.key is "<caPin>.<token>": the install command's one convenience flag for the two values below. It is
     split here, once, at render time - server.caPin / enrollment.token set directly always win when both are given. */}}
{{- define "agent.caPin" -}}
{{- if .Values.server.caPin -}}{{- .Values.server.caPin -}}
{{- else if .Values.enrollment.key -}}
{{- $p := splitList "." (toString .Values.enrollment.key) -}}{{- if eq (len $p) 2 -}}{{- index $p 0 -}}{{- end -}}
{{- end -}}
{{- end -}}
{{- define "agent.enrollToken" -}}
{{- if .Values.enrollment.token -}}{{- .Values.enrollment.token -}}
{{- else if .Values.enrollment.key -}}
{{- $p := splitList "." (toString .Values.enrollment.key) -}}{{- if eq (len $p) 2 -}}{{- index $p 1 -}}{{- end -}}
{{- end -}}
{{- end -}}

{{/* Values that were added after 0.1.0 may be missing when `helm upgrade --reuse-values` carries the old release's values
     over an older chart. These read them without failing. */}}
{{- define "agent.healthEnabled" -}}{{- if (dig "health" "enabled" true .Values.AsMap) -}}true{{- end -}}{{- end -}}
{{- define "agent.healthPort" -}}{{- dig "health" "port" 8082 .Values.AsMap | int -}}{{- end -}}

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
{{- if or $t.energy.metrics.enabled $t.kubernetesState.metrics.enabled $t.kubernetesEvents.logs.enabled $t.applicationMetrics.metrics.enabled $t.applicationLogs.logs.enabled $t.traces.traces.enabled $t.networkLatency.metrics.enabled -}}true{{- end -}}
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

{{- define "agent.telemetryPrometheusReceiverEnabled" -}}
{{- $t := .Values.telemetry -}}
{{- if or (and $t.energy.metrics.enabled (eq $t.energy.metrics.source "bundle-kepler")) (and $t.energy.metrics.enabled (eq $t.energy.metrics.source "existing")) $t.applicationMetrics.metrics.enabled -}}true{{- end -}}
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
{{- end -}}

{{- define "agent.telemetryName" -}}continuum-telemetry{{- end -}}

{{/* The "exporters" stanza shared by both collector ConfigMaps. Emits at column 0; the caller nindents it
     into place. The auth header's value is never written here - only a reference to the environment
     variable the container injects it into from a Secret at start (see telemetryExporterEnv below). */}}
{{- define "agent.telemetryExporterYAML" -}}
otlp:
  endpoint: {{ .Values.telemetry.export.otlp.endpoint | quote }}
  tls:
    insecure: {{ .Values.telemetry.export.otlp.tls.insecure }}
    {{- if .Values.telemetry.export.otlp.tls.caFile }}
    ca_file: {{ .Values.telemetry.export.otlp.tls.caFile | quote }}
    {{- end }}
  {{- if .Values.telemetry.export.otlp.auth.secretName }}
  headers:
    {{ .Values.telemetry.export.otlp.auth.headerName }}: "${env:CONTINUUM_TELEMETRY_AUTH}"
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
