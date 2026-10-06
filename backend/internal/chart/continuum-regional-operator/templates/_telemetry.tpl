{{/* OTel Collector config generation for this chart's one standalone collector - processor/exporter/
     extension YAML fragments and resource sizing. Split out of _helpers.tpl for the same reason as
     continuum-agent's own _telemetry.tpl: this chart's one real self-contained concern, kept apart from
     basic chart identity. Several of these mirror continuum-agent's _telemetry.tpl almost exactly - see
     the per-define comments below for which ones, and why they are duplicated rather than shared (each
     chart must still be fully self-contained and independently installable - see chart.go). */}}

{{- define "operator.memoryLimiterYAML" -}}
memory_limiter:
  check_interval: {{ .Values.processors.memoryLimiter.checkInterval | quote }}
  limit_percentage: {{ .Values.processors.memoryLimiter.limitPercentage }}
  spike_limit_percentage: {{ .Values.processors.memoryLimiter.spikeLimitPercentage }}
{{- end -}}

{{- define "operator.resourceDetectionYAML" -}}
{{- if .Values.processors.resourceDetection.enabled }}
resourcedetection:
  detectors: {{ .Values.processors.resourceDetection.detectors | toJson }}
  timeout: {{ .Values.processors.resourceDetection.timeout | quote }}
  override: true
{{- end }}
{{- end -}}

{{- define "operator.redactionYAML" -}}
{{- if .Values.processors.redaction.enabled }}
redaction:
  allow_all_keys: true
  blocked_key_patterns: {{ .Values.processors.redaction.blockedKeyPatterns | toJson }}
  summary: info
{{- end }}
{{- end -}}

{{/* The operator's own provenance on everything it forwards: continuum.operator.id / .name, then each label
     as its own attribute. All upsert, so they overwrite whatever an upstream agent or a user's own processor
     set under the same key. Renders nothing when no `operator` value was given. */}}
{{- define "operator.resourceOperatorYAML" -}}
{{- $op := .Values.operator }}
{{- if or $op.id $op.name $op.labels }}
resource/operator:
  attributes:
    {{- if $op.id }}
    - {key: continuum.operator.id, value: {{ $op.id | quote }}, action: upsert}
    {{- end }}
    {{- if $op.name }}
    - {key: continuum.operator.name, value: {{ $op.name | quote }}, action: upsert}
    {{- end }}
    {{- range $op.labels }}
    - {key: {{ .key | quote }}, value: {{ .value | quote }}, action: upsert}
    {{- end }}
{{- end }}
{{- end -}}

{{- define "operator.probabilisticSamplerYAML" -}}
{{- if lt (int .Values.processors.tracesSampling.percentage) 100 }}
probabilistic_sampler:
  sampling_percentage: {{ .Values.processors.tracesSampling.percentage }}
{{- end }}
{{- end -}}

{{/* GOMEMLIMIT (~80% of the resolved memory limit), same algorithm as agent.telemetryGomemlimit - pass
     .Values.resources through fromYaml first if it were YAML text; here it is already a map, so callers
     pass .Values.resources directly. */}}
{{- define "operator.gomemlimit" -}}
{{- $raw := "" -}}
{{- if and .limits .limits.memory -}}{{- $raw = .limits.memory -}}{{- end -}}
{{- if $raw -}}
{{- if kindIs "string" $raw -}}
{{- if regexMatch "^[0-9]+(\\.[0-9]+)?(Ki|Mi|Gi|Ti|k|M|G|T)?$" $raw -}}
{{- $n := regexFind "^[0-9]+(\\.[0-9]+)?" $raw -}}
{{- $suf := regexFind "(Ki|Mi|Gi|Ti|k|M|G|T)$" $raw -}}
{{- $scale := dict "Ki" 1024.0 "Mi" 1048576.0 "Gi" 1073741824.0 "Ti" 1099511627776.0 "k" 1000.0 "M" 1000000.0 "G" 1000000000.0 "T" 1000000000000.0 -}}
{{- $factor := 1.0 -}}
{{- if $suf -}}{{- $factor = get $scale $suf -}}{{- end -}}
{{- printf "%.0f" (mulf (mulf (float64 $n) $factor) 0.8) -}}
{{- end -}}
{{- else -}}
{{- printf "%.0f" (mulf (float64 $raw) 0.8) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* health_check extension: always on, unlike bearertokenauth above - this is what
     livenessProbe/readinessProbe in deployment.yaml point at (see operator.healthPort below), and the
     upstream otel/opentelemetry-collector-contrib image ships it for free. Serves plain GET / on its own
     port, 200 once the collector's pipelines have started. */}}
{{- define "operator.healthPort" -}}{{- .Values.health.port | default 13133 | int -}}{{- end -}}
{{- define "operator.healthCheckExtensionYAML" -}}
health_check:
  endpoint: "0.0.0.0:{{ include "operator.healthPort" . }}"
{{- end -}}

{{/* Self-metrics: the collector's own standard service.telemetry.metrics stanza (queue depth, dropped
     items, process memory - about the collector itself, not whatever it is relaying). A plain Prometheus
     pull reader, same shape the OTel Collector docs show. Off by default (see selfMetrics.enabled in
     values.yaml) - callers turn it on once they actually have something to scrape it. */}}
{{- define "operator.selfMetricsYAML" -}}
{{- if .Values.selfMetrics.enabled }}
telemetry:
  metrics:
    readers:
      - pull:
          exporter:
            prometheus:
              host: "0.0.0.0"
              port: {{ .Values.selfMetrics.port }}
{{- end }}
{{- end -}}

{{/* The bearertokenauth extension and its one env var, same convention as
     agent.telemetryReceiverAuthExtensionYAML / agent.telemetryReceiverAuthEnv. Both no-ops when
     receiver.auth is off, so callers can always include them unconditionally. */}}
{{- define "operator.receiverAuthExtensionYAML" -}}
{{- if .Values.receiver.auth.enabled }}
bearertokenauth:
  token: "${env:CONTINUUM_OPERATOR_RECEIVER_AUTH}"
{{- end }}
{{- end -}}
{{- define "operator.receiverAuthEnv" -}}
{{- if .Values.receiver.auth.enabled }}
- name: CONTINUUM_OPERATOR_RECEIVER_AUTH
  valueFrom:
    secretKeyRef:
      name: {{ .Values.receiver.auth.secretName }}
      key: {{ .Values.receiver.auth.secretKey }}
{{- end }}
{{- end -}}

{{/* Destinations. `export.otlp` is the default; `export.routes.<metrics|logs|traces>` sends one signal type
     somewhere else instead (FUSION: metrics to Prometheus, logs to Loki, traces to Tempo). Each route is its own
     exporter with its own protocol and credential, and nothing is shared between them. The same design as
     continuum-agent's telemetry.export.routes; duplicated rather than shared because each chart must install on
     its own (see chart.go). */}}
{{- define "operator.modalities" -}}["metrics","logs","traces"]{{- end -}}

{{/* The collector exporter type a protocol is spoken by. Before routes existed this chart rendered the gRPC
     exporter whatever export.otlp.protocol said, so protocol=http pointed a gRPC client at an HTTP endpoint. */}}
{{- define "operator.exporterType" -}}
{{- if eq . "http" -}}otlphttp{{- else -}}otlp{{- end -}}
{{- end -}}

{{/* Non-empty when signal type .m (.root is the chart context) has a destination of its own. */}}
{{- define "operator.hasRoute" -}}
{{- $r := get .root.Values.export.routes .m -}}
{{- if $r.endpoint -}}true{{- end -}}
{{- end -}}

{{/* Non-empty when at least one signal type goes to the default destination. */}}
{{- define "operator.defaultUsed" -}}
{{- $root := . -}}
{{- range (include "operator.modalities" . | fromJsonArray) -}}
{{- if not (include "operator.hasRoute" (dict "root" $root "m" .)) -}}true{{- end -}}
{{- end -}}
{{- end -}}

{{- define "operator.exporterName" -}}
{{- include "operator.exporterType" .Values.export.otlp.protocol -}}
{{- end -}}

{{/* The exporter signal type .m is sent through: its route's, named "<type>/<signal>" (otlphttp/metrics), or the
     default's. */}}
{{- define "operator.exporterFor" -}}
{{- if include "operator.hasRoute" . -}}
{{- $r := get .root.Values.export.routes .m -}}
{{- printf "%s/%s" (include "operator.exporterType" $r.protocol) .m -}}
{{- else -}}
{{- include "operator.exporterName" .root -}}
{{- end -}}
{{- end -}}

{{/* One exporter block: .key its name in the collector config, .c the destination (the export.otlp shape), .env
     the variable its credential header is read from. Emits at column 0. The header's value is never written here,
     only a reference to the environment variable the container fills from a Secret. An http endpoint takes the
     scheme tls.insecure picks unless it already carries one; the collector appends /v1/<signal> to it. */}}
{{- define "operator.exporterBlock" -}}
{{- $c := .c -}}
{{ .key }}:
{{- if eq $c.protocol "http" }}
{{- $e := $c.endpoint }}
{{- if not (regexMatch "^https?://" $e) }}{{ $e = printf "%s://%s" (ternary "http" "https" $c.tls.insecure) $e }}{{ end }}
  endpoint: {{ $e | quote }}
  {{- if or $c.tls.mtls.enabled $c.tls.caFile }}
  tls:
    {{- if $c.tls.mtls.enabled }}
    ca_file: {{ .mtls }}/ca.crt
    cert_file: {{ .mtls }}/tls.crt
    key_file: {{ .mtls }}/tls.key
    {{- else }}
    ca_file: {{ $c.tls.caFile | quote }}
    {{- end }}
  {{- end }}
{{- else }}
  endpoint: {{ $c.endpoint | quote }}
  tls:
    insecure: {{ $c.tls.insecure }}
    {{- if $c.tls.mtls.enabled }}
    ca_file: {{ .mtls }}/ca.crt
    cert_file: {{ .mtls }}/tls.crt
    key_file: {{ .mtls }}/tls.key
    {{- else if $c.tls.caFile }}
    ca_file: {{ $c.tls.caFile | quote }}
    {{- end }}
{{- end }}
{{- if $c.auth.secretName }}
  headers:
    {{ $c.auth.headerName }}: {{ printf "${env:%s}" .env | quote }}
{{- end }}
{{- end -}}

{{/* The "exporters" stanza: the default destination when any signal type uses it, then one exporter per route. */}}
{{- define "operator.exporterYAML" -}}
{{- $root := . -}}
{{- $blocks := list -}}
{{- if include "operator.defaultUsed" . -}}
{{- $blocks = append $blocks (include "operator.exporterBlock" (dict "key" (include "operator.exporterName" $root) "c" $root.Values.export.otlp "env" "CONTINUUM_OPERATOR_EXPORT_AUTH" "mtls" "/export-mtls")) -}}
{{- end -}}
{{- range (include "operator.modalities" . | fromJsonArray) -}}
{{- if include "operator.hasRoute" (dict "root" $root "m" .) -}}
{{- $blocks = append $blocks (include "operator.exporterBlock" (dict "key" (include "operator.exporterFor" (dict "root" $root "m" .)) "c" (get $root.Values.export.routes .) "env" (printf "CONTINUUM_OPERATOR_EXPORT_AUTH_%s" (upper .)) "mtls" (printf "/export-mtls-%s" .))) -}}
{{- end -}}
{{- end -}}
{{- join "\n" $blocks -}}
{{- end -}}

{{/* One env entry per destination in use that has a credential: CONTINUUM_OPERATOR_EXPORT_AUTH for the default,
     CONTINUUM_OPERATOR_EXPORT_AUTH_<SIGNAL> for each route. Empty when none does. */}}
{{- define "operator.exporterEnv" -}}
{{- $root := . -}}
{{- $entries := list -}}
{{- $a := .Values.export.otlp.auth -}}
{{- if and (include "operator.defaultUsed" .) $a.secretName -}}
{{- $entries = append $entries (printf "- name: CONTINUUM_OPERATOR_EXPORT_AUTH\n  valueFrom:\n    secretKeyRef:\n      name: %s\n      key: %s" $a.secretName $a.secretKey) -}}
{{- end -}}
{{- range (include "operator.modalities" . | fromJsonArray) -}}
{{- if include "operator.hasRoute" (dict "root" $root "m" .) -}}
{{- $ra := (get $root.Values.export.routes .).auth -}}
{{- if $ra.secretName -}}
{{- $entries = append $entries (printf "- name: CONTINUUM_OPERATOR_EXPORT_AUTH_%s\n  valueFrom:\n    secretKeyRef:\n      name: %s\n      key: %s" (upper .) $ra.secretName $ra.secretKey) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- join "\n" $entries -}}
{{- end -}}

{{/* Client-certificate Secrets for destinations in use with ...tls.mtls.enabled: the default at /export-mtls,
     each route at /export-mtls-<signal>. Two lists of entries (mounts, volumes), empty when none is on. */}}
{{- define "operator.exporterMtlsMounts" -}}
{{- $root := . -}}
{{- $l := list -}}
{{- if and (include "operator.defaultUsed" .) $root.Values.export.otlp.tls.mtls.enabled -}}
{{- $l = append $l "- {name: export-mtls, mountPath: /export-mtls, readOnly: true}" -}}
{{- end -}}
{{- range (include "operator.modalities" . | fromJsonArray) -}}
{{- if and (include "operator.hasRoute" (dict "root" $root "m" .)) (get $root.Values.export.routes .).tls.mtls.enabled -}}
{{- $l = append $l (printf "- {name: export-mtls-%s, mountPath: /export-mtls-%s, readOnly: true}" . .) -}}
{{- end -}}
{{- end -}}
{{- join "\n" $l -}}
{{- end -}}
{{- define "operator.exporterMtlsVolumes" -}}
{{- $root := . -}}
{{- $l := list -}}
{{- if and (include "operator.defaultUsed" .) $root.Values.export.otlp.tls.mtls.enabled -}}
{{- $l = append $l (printf "- name: export-mtls\n  secret: {secretName: %s}" $root.Values.export.otlp.tls.mtls.secretName) -}}
{{- end -}}
{{- range (include "operator.modalities" . | fromJsonArray) -}}
{{- if and (include "operator.hasRoute" (dict "root" $root "m" .)) (get $root.Values.export.routes .).tls.mtls.enabled -}}
{{- $l = append $l (printf "- name: export-mtls-%s\n  secret: {secretName: %s}" . (get $root.Values.export.routes .).tls.mtls.secretName) -}}
{{- end -}}
{{- end -}}
{{- join "\n" $l -}}
{{- end -}}

{{/* Heartbeat (opt-in, see values.yaml's heartbeat block): ONE extra, self-contained metrics pipeline,
     httpcheck/heartbeat -> filter/heartbeat -> otlphttp/heartbeat, that is fed by nothing but the
     collector probing its own health_check endpoint. It shares no receiver, processor or exporter with the
     relaying pipelines, so it cannot carry anything they carry and cannot change what they do. All of these
     defines are only ever included when heartbeat.enabled. */}}
{{- define "operator.heartbeatReceiverYAML" -}}
httpcheck/heartbeat:
  collection_interval: "{{ .Values.heartbeat.intervalSeconds | default 60 | int }}s"
  targets:
    - endpoint: "http://127.0.0.1:{{ include "operator.healthPort" . }}/"
      method: GET
{{- end -}}

{{/* Belt and braces: even though only httpcheck feeds this pipeline, drop every metric but the one result
     the heartbeat is for. */}}
{{- define "operator.heartbeatProcessorYAML" -}}
filter/heartbeat:
  error_mode: ignore
  metrics:
    metric:
      - 'name != "httpcheck.status"'
{{- end -}}

{{- define "operator.heartbeatExporterYAML" -}}
otlphttp/heartbeat:
  metrics_endpoint: {{ .Values.heartbeat.url | quote }}
  encoding: proto
  timeout: 10s
  headers:
    Authorization: "Bearer ${env:CONTINUUM_OPERATOR_HEARTBEAT_AUTH}"
  {{- if .Values.heartbeat.tls.caSecretName }}
  tls:
    ca_file: {{ printf "/heartbeat-ca/%s" (.Values.heartbeat.tls.caSecretKey | default "ca.crt") | quote }}
  {{- end }}
  # A heartbeat is only worth sending while it is fresh: a short, small retry window and queue, so a server
  # outage never builds up a backlog of stale "alive" messages to deliver afterwards.
  retry_on_failure:
    enabled: true
    initial_interval: 5s
    max_interval: 30s
    max_elapsed_time: 90s
  sending_queue:
    enabled: true
    num_consumers: 1
    queue_size: 3
{{- end -}}

{{- define "operator.heartbeatEnv" -}}
- name: CONTINUUM_OPERATOR_HEARTBEAT_AUTH
  valueFrom:
    secretKeyRef:
      name: {{ .Values.heartbeat.auth.secretName }}
      key: {{ .Values.heartbeat.auth.secretKey | default "token" }}
{{- end -}}
