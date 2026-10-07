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
     port, 200 once the collector's pipelines have started.
     It says the PROCESS is up and nothing about whether it can deliver. That is not a choice made here: in the
     pinned collector (0.160.0) check_collector_pipeline is accepted and ignored, and the component-status mode
     (use_v2 with the extension.healthcheck.useComponentStatus feature gate) stays StatusOK while the otlp and
     otlphttp exporters fail for every reason (destination down, wrong CA, bad token, 415): they never report a
     status. Measured, so neither is wired - a probe that claims to know and does not is worse than one that does not
     claim. Delivery is visible in the self metrics (selfMetrics, on by default): otelcol_exporter_queue_size above 0
     and otelcol_exporter_send_failed_* / enqueue_failed_* increasing. */}}
{{- define "operator.healthPort" -}}{{- .Values.health.port | default 13133 | int -}}{{- end -}}
{{- define "operator.healthCheckExtensionYAML" -}}
health_check:
  endpoint: "0.0.0.0:{{ include "operator.healthPort" . }}"
{{- end -}}

{{/* Self-metrics: the collector's own standard service.telemetry.metrics stanza (queue depth, send_failed and
     enqueue_failed counts, process memory - about the collector itself, not whatever it is relaying). A plain
     Prometheus pull reader on the pod's own address (POD_IP, the downward-API variable deployment.yaml sets), the same
     shape continuum-agent's telemetry.health uses. On by default (see selfMetrics.enabled in values.yaml): it is the
     only place a destination that cannot be reached shows up as a number, because the health check cannot see it. */}}
{{- define "operator.selfMetricsYAML" -}}
{{- $level := .Values.selfMetrics.logLevel | default "info" }}
{{- if or .Values.selfMetrics.enabled (ne $level "info") }}
telemetry:
  {{- if ne $level "info" }}
  logs:
    level: {{ $level }}
  {{- end }}
  {{- if .Values.selfMetrics.enabled }}
  metrics:
    readers:
      - pull:
          exporter:
            prometheus:
              host: ${env:POD_IP}
              port: {{ .Values.selfMetrics.port }}
  {{- end }}
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

{{/* The trust and identity lines of an exporter's tls block, at column 0, empty when it has none: the client
     certificate (and the CA that verifies the destination, from the same Secret) when ...tls.mtls is on; otherwise
     the CA bundle of caSecretName, mounted at .ca; otherwise the caFile path as written (nothing mounts it). */}}
{{- define "operator.exporterTrust" -}}
{{- $c := .c -}}
{{- if $c.tls.mtls.enabled -}}
ca_file: {{ .mtls }}/ca.crt
cert_file: {{ .mtls }}/tls.crt
key_file: {{ .mtls }}/tls.key
reload_interval: 1h
{{- else if $c.tls.caSecretName -}}
ca_file: {{ printf "%s/ca.crt" .ca | quote }}
{{- else if $c.tls.caFile -}}
ca_file: {{ $c.tls.caFile | quote }}
{{- end -}}
{{- end -}}

{{/* One exporter block: .key its name in the collector config, .c the destination (the export.otlp shape), .env
     the variable its credential header is read from, .mtls and .ca the directories its client-certificate and
     CA-bundle Secrets are mounted at. Emits at column 0. The header's value is never written here,
     only a reference to the environment variable the container fills from a Secret. An http endpoint takes the
     scheme tls.insecure picks unless it already carries one; the collector appends /v1/<signal> to it. */}}
{{- define "operator.exporterBlock" -}}
{{- $c := .c -}}
{{- $trust := include "operator.exporterTrust" . -}}
{{ .key }}:
{{- if eq $c.protocol "http" }}
{{- $e := $c.endpoint }}
{{- if not (regexMatch "^https?://" $e) }}{{ $e = printf "%s://%s" (ternary "http" "https" $c.tls.insecure) $e }}{{ end }}
  {{- if and .m (or $c.fullUrl (regexMatch (printf "/v1/%s/?$" .m) $e)) }}
  {{- /* A route whose URL is already the whole address of its signal: posted to as written, the collector appends nothing. */}}
  {{ .m }}_endpoint: {{ $e | quote }}
  {{- else }}
  endpoint: {{ $e | quote }}
  {{- end }}
  {{- if or $trust $c.tls.serverName }}
  tls:
    {{- if $trust }}
    {{- $trust | nindent 4 }}
    {{- end }}
    {{- if $c.tls.serverName }}
    server_name_override: {{ $c.tls.serverName | quote }}
    {{- end }}
  {{- end }}
{{- else }}
  endpoint: {{ $c.endpoint | quote }}
  tls:
    insecure: {{ $c.tls.insecure }}
    {{- if $trust }}
    {{- $trust | nindent 4 }}
    {{- end }}
    {{- if $c.tls.serverName }}
    server_name_override: {{ $c.tls.serverName | quote }}
    {{- end }}
{{- end }}
{{- with .root.Values.export.timeout }}
  timeout: {{ . | quote }}
{{- end }}
{{- if and (eq $c.protocol "grpc") .root.Values.export.keepalive.time }}
  keepalive:
    time: {{ .root.Values.export.keepalive.time | quote }}
    timeout: {{ .root.Values.export.keepalive.timeout | quote }}
    permit_without_stream: true
{{- end }}
{{- if $c.auth.secretName }}
  headers:
    {{ $c.auth.headerName }}: {{ printf "${env:%s}" .env | quote }}
{{- end }}
{{- include "operator.exporterResilienceYAML" .root | nindent 2 }}
{{- end -}}

{{/* What every exporter does when its destination is down, spelled out instead of left to the collector's
     defaults (which retry for 5 minutes and then drop): retry for export.queue.retryMaxElapsedTime, holding at
     most export.queue.size requests in memory meanwhile.
     block_on_overflow is what makes a full queue push back. Without it (the collector's default) a full queue REJECTS
     the batch and the batch processor in front of the exporter only logs "sending queue is full": the receiver has
     already answered 200 to the sender, so the data is lost and nobody upstream knows (measured: 15 requests of 3000
     records into a queue of 3, destination down: all 15 answered 200, 36000 records dropped, receiver refused 0).
     With it, the batch processor waits for room, the receiver stops answering, and the senders see timeouts and keep
     the data in their own queues.
     sending_queue.batch cuts a request into pieces of at most export.queue.maxRequestBytes serialized bytes (min_size
     1: nothing is held back to fill a batch, that is the batch processor's job). processors.batch caps a batch in ITEMS,
     and 4096 log records of 2 KiB are 8 MiB: past the 4 MiB the next hop accepts, so the whole batch was rejected with
     a permanent error and dropped, while its sender had been told 200. 0 turns the cut off.
     With export.queue.persistent.enabled the queue is also written to an emptyDir (file_storage/queue), so it
     survives a container restart (not a rescheduled pod). Emits at column 0; callers nindent it. */}}
{{- define "operator.exporterResilienceYAML" -}}
{{- $q := .Values.export.queue -}}
retry_on_failure:
  enabled: true
  initial_interval: 5s
  max_interval: 30s
  max_elapsed_time: {{ $q.retryMaxElapsedTime | quote }}
sending_queue:
  enabled: true
  queue_size: {{ $q.size }}
  block_on_overflow: true
  {{- if $q.maxRequestBytes }}
  batch:
    sizer: bytes
    min_size: 1
    max_size: {{ int $q.maxRequestBytes }}
    flush_timeout: 1s
  {{- end }}
  {{- if $q.persistent.enabled }}
  storage: file_storage/queue
  {{- end }}
{{- end -}}

{{/* The one file_storage extension every exporter queue shares when export.queue.persistent.enabled. The root
     filesystem is read-only, so it lives on the emptyDir deployment.yaml mounts at /queue. */}}
{{- define "operator.queueExtensionYAML" -}}
{{- if .Values.export.queue.persistent.enabled }}
file_storage/queue:
  directory: /queue
{{- end }}
{{- end -}}

{{/* Explicit batch sizes. The collector's own defaults leave the maximum unbounded, and a batch larger than the
     next hop's receive limit (4 MiB on a collector's OTLP/gRPC receiver, which is what the next hop usually is)
     is rejected as a whole and retried until it is dropped. */}}
{{- define "operator.batchYAML" -}}
batch:
  timeout: {{ .Values.processors.batch.timeout | quote }}
  send_batch_size: {{ .Values.processors.batch.sendBatchSize }}
  send_batch_max_size: {{ .Values.processors.batch.sendBatchMaxSize }}
{{- end -}}

{{/* The "exporters" stanza: the default destination when any signal type uses it, then one exporter per route. */}}
{{- define "operator.exporterYAML" -}}
{{- $root := . -}}
{{- $blocks := list -}}
{{- if include "operator.defaultUsed" . -}}
{{- $blocks = append $blocks (include "operator.exporterBlock" (dict "root" $root "key" (include "operator.exporterName" $root) "c" $root.Values.export.otlp "env" "CONTINUUM_OPERATOR_EXPORT_AUTH" "mtls" "/export-mtls" "ca" "/export-ca")) -}}
{{- end -}}
{{- range (include "operator.modalities" . | fromJsonArray) -}}
{{- if include "operator.hasRoute" (dict "root" $root "m" .) -}}
{{- $blocks = append $blocks (include "operator.exporterBlock" (dict "root" $root "key" (include "operator.exporterFor" (dict "root" $root "m" .)) "c" (get $root.Values.export.routes .) "m" . "env" (printf "CONTINUUM_OPERATOR_EXPORT_AUTH_%s" (upper .)) "mtls" (printf "/export-mtls-%s" .) "ca" (printf "/export-ca-%s" .))) -}}
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

{{/* CA-bundle Secrets (...tls.caSecretName, key ca.crt) for destinations in use: the default at /export-ca, each route
     at /export-ca-<signal>. Not mounted when that destination's mtls is on, because its own Secret's ca.crt is what is
     trusted then. Same shape as the client-certificate lists above. */}}
{{- define "operator.exporterCAMounts" -}}
{{- $root := . -}}
{{- $l := list -}}
{{- $d := $root.Values.export.otlp.tls -}}
{{- if and (include "operator.defaultUsed" .) $d.caSecretName (not $d.mtls.enabled) -}}
{{- $l = append $l "- {name: export-ca, mountPath: /export-ca, readOnly: true}" -}}
{{- end -}}
{{- range (include "operator.modalities" . | fromJsonArray) -}}
{{- $r := get $root.Values.export.routes . -}}
{{- if and (include "operator.hasRoute" (dict "root" $root "m" .)) $r.tls.caSecretName (not $r.tls.mtls.enabled) -}}
{{- $l = append $l (printf "- {name: export-ca-%s, mountPath: /export-ca-%s, readOnly: true}" . .) -}}
{{- end -}}
{{- end -}}
{{- join "\n" $l -}}
{{- end -}}
{{- define "operator.exporterCAVolumes" -}}
{{- $root := . -}}
{{- $l := list -}}
{{- $d := $root.Values.export.otlp.tls -}}
{{- if and (include "operator.defaultUsed" .) $d.caSecretName (not $d.mtls.enabled) -}}
{{- $l = append $l (printf "- name: export-ca\n  secret: {secretName: %s}" $d.caSecretName) -}}
{{- end -}}
{{- range (include "operator.modalities" . | fromJsonArray) -}}
{{- $r := get $root.Values.export.routes . -}}
{{- if and (include "operator.hasRoute" (dict "root" $root "m" .)) $r.tls.caSecretName (not $r.tls.mtls.enabled) -}}
{{- $l = append $l (printf "- name: export-ca-%s\n  secret: {secretName: %s}" . $r.tls.caSecretName) -}}
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

{{/* Fails the render for a destination that cannot work, with the values key to change. .c is the destination (the
     export.otlp shape), .what its values path, .m the signal type of a route ("" for the default destination),
     .root the chart context. Everything here is something the collector either refuses to start with (a gRPC endpoint with a
     path or without a port: "invalid configuration ... missing port in address", a crash loop) or accepts and then cannot
     deliver through (https:// with insecure: true is TLS, with a verification error for every batch; http:// with
     insecure: false is a TLS handshake against a plaintext port), measured with the collector this chart pins. Only the
     port-number guess is optional (export.checkPorts). */}}
{{- define "operator.destinationCheck" -}}
{{- $c := .c -}}
{{- $what := .what -}}
{{- $m := .m | default "" -}}
{{- $e := toString $c.endpoint -}}
{{- $insecure := $c.tls.insecure -}}
{{- $plain := false -}}
{{- if regexMatch "[\\s\"'\\\\<>]" $e -}}{{- fail (printf "%s.endpoint %q has whitespace, a quote, a backslash or an angle bracket in it" $what $e) -}}{{- end -}}
{{- if eq $c.protocol "grpc" -}}
{{- $rest := regexReplaceAll "^(https?://|dns:///|dns:|passthrough:///)" $e "" -}}
{{- if hasPrefix "unix:" $e -}}
{{- $plain = $insecure -}}
{{- else -}}
{{- if contains "/" $rest -}}{{- fail (printf "%s.endpoint %q has a path, but a gRPC endpoint is host:port only (the collector refuses to start with it). For a URL such as https://host/otlp use %s.protocol=http" $what $e $what) -}}{{- end -}}
{{- if not (regexMatch "^(\\[[0-9A-Fa-f:.]+\\]|[A-Za-z0-9_][A-Za-z0-9._-]*):[0-9]{1,5}$" $rest) -}}{{- fail (printf "%s.endpoint %q is not host:port (the collector refuses to start without the port; an IPv6 address goes in brackets, [fd00::5]:4317)" $what $e) -}}{{- end -}}
{{- if and (hasPrefix "https://" $e) $insecure -}}{{- fail (printf "%s.endpoint %q says https:// but %s.tls.insecure is true: the scheme wins, TLS is used and the certificate check fails. Set tls.insecure=false, or use a plaintext destination (no scheme or http://)" $what $e $what) -}}{{- end -}}
{{- if and (hasPrefix "http://" $e) (not $insecure) -}}{{- fail (printf "%s.endpoint %q says http:// (plaintext) but %s.tls.insecure is false, so the collector would start a TLS handshake against a plaintext port. Set tls.insecure=true to send without TLS, or use https://" $what $e $what) -}}{{- end -}}
{{- $plain = or $insecure (hasPrefix "http://" $e) -}}
{{- $port := regexFind "[0-9]+$" $rest -}}
{{- if and .root.Values.export.checkPorts (eq $port "4318") -}}{{- fail (printf "%s.endpoint %q is the conventional OTLP/HTTP port but %s.protocol is grpc: a gRPC client on an HTTP port is refused for good and everything sent is dropped. Set %s.protocol=http, or use port 4317. (A destination that really serves gRPC on 4318: set export.checkPorts=false.)" $what $e $what $what) -}}{{- end -}}
{{- end -}}
{{- else if eq $c.protocol "http" -}}
{{- $url := $e -}}
{{- if not (contains "://" $e) -}}{{- $url = printf "%s://%s" (ternary "http" "https" $insecure) $e -}}{{- end -}}
{{- if not (regexMatch "^https?://" $url) -}}{{- fail (printf "%s.endpoint %q: an OTLP/HTTP endpoint is http(s)://host[:port][/base]; for gRPC (host:port, dns:///host:port) set %s.protocol=grpc" $what $e $what) -}}{{- end -}}
{{- $hostpart := regexFind "^https?://[^/?#]*" $url -}}
{{- if not (regexMatch "^https?://(\\[[0-9A-Fa-f:.]+\\]|[A-Za-z0-9_][A-Za-z0-9._-]*)(:[0-9]{1,5})?$" $hostpart) -}}{{- fail (printf "%s.endpoint %q has no valid host (an IPv6 address goes in brackets: https://[fd00::5]:4318)" $what $e) -}}{{- end -}}
{{- $path := trimPrefix $hostpart $url -}}
{{- if regexMatch "[?#]" $path -}}{{- fail (printf "%s.endpoint %q has a query or fragment: the collector appends /v1/<signal> to the URL, which would land after it" $what $e) -}}{{- end -}}
{{- $plain = hasPrefix "http://" $url -}}
{{- if regexMatch "/v1/(metrics|logs|traces)/?$" $path -}}
{{- $sig := regexReplaceAll "^.*/v1/(metrics|logs|traces)/?$" $path "${1}" -}}
{{- if not $m -}}{{- fail (printf "%s.endpoint %q ends in /v1/%s, but this destination carries several signals and the collector appends /v1/<signal> itself (…/v1/%s/v1/metrics is a 404 and drops everything). Drop the suffix, or give each signal its own destination under telemetry.export.routes.<signal> (an endpoint ending in its own signal's /v1/<signal> is posted to as written there)" $what $e $sig $sig) -}}
{{- else if ne $sig $m -}}{{- fail (printf "%s.endpoint %q ends in /v1/%s but this route carries %s" $what $e $sig $m) -}}{{- end -}}
{{- end -}}
{{- if and .root.Values.export.checkPorts (eq (regexFind ":[0-9]+$" $hostpart) ":4317") -}}{{- fail (printf "%s.endpoint %q is the conventional OTLP/gRPC port but %s.protocol is http: an HTTP client on a gRPC port is refused for good and everything sent is dropped. Set %s.protocol=grpc, or use port 4318. (A destination that really serves HTTP on 4317: set export.checkPorts=false.)" $what $e $what $what) -}}{{- end -}}
{{- end -}}
{{- if and $c.fullUrl (ne $c.protocol "http") -}}{{- fail (printf "%s.fullUrl only applies to protocol=http" $what) -}}{{- end -}}
{{- if and $plain $c.tls.mtls.enabled -}}
{{- fail (printf "%s: TLS is off (%s) but a client certificate (tls.mtls) is configured; it would never be used, the destination would never see who is sending, and the data would go in plaintext. Either turn TLS on (tls.insecure=false and, for HTTP, an https:// endpoint) or turn tls.mtls off" $what (ternary "tls.insecure is true or the endpoint is http://" "the endpoint is http://, or has no scheme while tls.insecure is true" (eq $c.protocol "grpc"))) -}}
{{- end -}}
{{- end -}}

{{/* Runs the check above for every destination in use (see operator.destinationsInUse), then the proxy settings. Called
     from operator.validate. fullUrl means "this URL is the whole address of its signal", which a default destination
     (several signals) cannot have: refused here because the shared schema allows the key there. */}}
{{- define "operator.exportValidate" -}}
{{- $root := . -}}
{{- if and .Values.export.otlp.endpoint .Values.export.otlp.fullUrl -}}{{- fail "export.otlp.fullUrl does not apply to the default destination, which carries several signals: give the signal its own destination under export.routes.<signal> and set fullUrl there" -}}{{- end -}}
{{- /* Only destinations that something is sent to: a default destination every signal type has a route around is never
       rendered, and a stale value in it must not fail the install. */ -}}
{{- range (include "operator.destinationsInUse" . | fromJsonArray) -}}
{{- if .c.endpoint -}}
{{- include "operator.destinationCheck" (dict "root" $root "c" .c "what" .name "m" .m) -}}
{{- end -}}
{{- end -}}
{{- range $k, $v := .Values.export.proxy -}}
{{- if and (has $k (list "httpProxy" "httpsProxy")) $v (not (regexMatch "^((https?|socks5h?)://)?[^/@\\s]+@?[^/\\s]*/?$" $v)) -}}
{{- fail (printf "export.proxy.%s %q is not a proxy URL (http://host:3128, https://host:3129 or socks5://host:1080)" $k $v) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Whether a destination (.c, the export.otlp shape) is reached without TLS: a gRPC endpoint with tls.insecure
     (or http://), an HTTP endpoint that says http:// or says nothing while tls.insecure is true. "true" or empty. */}}
{{- define "operator.destinationPlain" -}}
{{- $c := .c -}}
{{- $e := toString $c.endpoint -}}
{{- if eq $c.protocol "http" -}}{{- if or (hasPrefix "http://" $e) (and (not (hasPrefix "https://" $e)) $c.tls.insecure) -}}true{{- end -}}
{{- else -}}{{- if or (hasPrefix "http://" $e) (and (not (hasPrefix "https://" $e)) $c.tls.insecure) -}}true{{- end -}}
{{- end -}}
{{- end -}}

{{/* The destinations in use (the default one when any signal type goes there, each route that has an endpoint), as a JSON
     list of {name, c}: .name is the values path for messages. */}}
{{- define "operator.destinationsInUse" -}}
{{- $root := . -}}
{{- $l := list -}}
{{- if include "operator.defaultUsed" . -}}{{- $l = append $l (dict "name" "export.otlp" "m" "" "c" .Values.export.otlp) -}}{{- end -}}
{{- range (include "operator.modalities" . | fromJsonArray) -}}
{{- if include "operator.hasRoute" (dict "root" $root "m" .) -}}{{- $l = append $l (dict "name" (printf "export.routes.%s" .) "m" . "c" (get $root.Values.export.routes .)) -}}{{- end -}}
{{- end -}}
{{- toJson $l -}}
{{- end -}}

{{/* One line per destination in use for NOTES: where, how, and whether TLS and a credential are involved. */}}
{{- define "operator.destinationLines" -}}
{{- $out := list -}}
{{- range (include "operator.destinationsInUse" . | fromJsonArray) -}}
{{- $c := .c -}}
{{- $tls := "TLS" -}}
{{- if include "operator.destinationPlain" (dict "c" $c) }}{{ $tls = "PLAINTEXT" }}{{ else if $c.tls.mtls.enabled }}{{ $tls = "mutual TLS" }}{{ end -}}
{{- $out = append $out (printf "%s -> %s (%s, %s%s)" (trimPrefix "export." .name) $c.endpoint $c.protocol $tls (ternary (printf ", header %s from Secret %s" $c.auth.headerName $c.auth.secretName) "" (ne (toString $c.auth.secretName) ""))) -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{/* The Secrets (with the keys read from each) the pod cannot start without, as a JSON list of strings. A missing Secret
     leaves the pod in ContainerCreating ("FailedMount": a volume) or CreateContainerConfigError (an environment variable):
     visible in `kubectl describe pod`, but only if someone looks, so NOTES names them. */}}
{{- define "operator.exportSecrets" -}}
{{- $l := list -}}
{{- if .Values.receiver.tls.enabled }}{{ $l = append $l (printf "%s (tls.crt, tls.key%s)" .Values.receiver.tls.secretName (ternary ", ca.crt" "" (default false .Values.receiver.tls.mtls))) }}{{ end -}}
{{- if .Values.receiver.auth.enabled }}{{ $l = append $l (printf "%s (%s)" .Values.receiver.auth.secretName .Values.receiver.auth.secretKey) }}{{ end -}}
{{- range (include "operator.destinationsInUse" . | fromJsonArray) -}}
{{- $c := .c -}}
{{- if $c.tls.mtls.enabled }}{{ $l = append $l (printf "%s (tls.crt, tls.key, ca.crt)" $c.tls.mtls.secretName) }}{{ else if $c.tls.caSecretName }}{{ $l = append $l (printf "%s (ca.crt)" $c.tls.caSecretName) }}{{ end -}}
{{- if $c.auth.secretName }}{{ $l = append $l (printf "%s (%s)" $c.auth.secretName $c.auth.secretKey) }}{{ end -}}
{{- end -}}
{{- if .Values.export.proxy.secretName }}{{ $l = append $l (printf "%s (HTTPS_PROXY%s)" .Values.export.proxy.secretName (ternary "" ", HTTP_PROXY optional" (ne (toString .Values.export.proxy.httpProxy) ""))) }}{{ end -}}
{{- if .Values.heartbeat.enabled }}{{ $l = append $l (printf "%s (%s)" .Values.heartbeat.auth.secretName (.Values.heartbeat.auth.secretKey | default "token")) }}{{ end -}}
{{- toJson (uniq $l) -}}
{{- end -}}

{{/* What NOTES warns about on the export path, as a JSON list of strings: settings that are accepted but will not do what
     the person most likely meant, or a limit hit later that then fails quietly. Not render failures: each has a legitimate use. */}}
{{- define "operator.exportWarnings" -}}
{{- $w := list -}}
{{- $p := .Values.export.proxy -}}
{{- $proxied := or $p.httpProxy $p.httpsProxy $p.secretName -}}
{{- $anyGrpc := false -}}
{{- range (include "operator.destinationsInUse" . | fromJsonArray) -}}
{{- if eq .c.protocol "grpc" }}{{ $anyGrpc = true }}{{ end -}}
{{- if and .c.auth.secretName (include "operator.destinationPlain" (dict "c" .c)) -}}
{{- $w = append $w (printf "%s sends its credential header (%s) over a connection without TLS: anyone on the path can read it. Use TLS (tls.insecure=false and, for HTTP, an https:// endpoint) or send it only inside a trusted network." .name .c.auth.headerName) -}}
{{- end -}}
{{- if and (or .c.tls.caSecretName .c.tls.caFile) (include "operator.destinationPlain" (dict "c" .c)) -}}
{{- $w = append $w (printf "%s names a CA (tls.caSecretName / tls.caFile) but TLS is off (tls.insecure=true or an http:// endpoint): the CA is never used and the data goes in plaintext. If you meant TLS, set tls.insecure=false (and use an https:// endpoint for HTTP)." .name) -}}
{{- end -}}
{{- end -}}
{{- if $proxied -}}
{{- if and $anyGrpc (not $p.httpsProxy) (not $p.secretName) -}}
{{- $w = append $w "export.proxy.httpProxy is set but a destination is gRPC, which only ever uses httpsProxy (a CONNECT tunnel): it goes direct, not through the proxy. Set export.proxy.httpsProxy." -}}
{{- end -}}
{{- if or (contains "@" (toString $p.httpProxy)) (contains "@" (toString $p.httpsProxy)) -}}
{{- $w = append $w "export.proxy carries credentials (user:password@) in clear text in the pod spec and in `helm get values`. Put the URL in a Secret under the key HTTPS_PROXY (and HTTP_PROXY) and name it in export.proxy.secretName." -}}
{{- end -}}
{{- if .Values.networkPolicy.egress.enabled -}}
{{- $w = append $w "networkPolicy.egress is on and a proxy is configured: the pod connects only to the proxy, so networkPolicy.egress.allowedEgress must allow the proxy's address and port (the destinations behind it need no rule)." -}}
{{- end -}}
{{- end -}}
{{- $q := .Values.export.queue -}}
{{- $mrb := int $q.maxRequestBytes -}}
{{- if and $anyGrpc (gt $mrb 4194304) -}}
{{- $w = append $w (printf "export.queue.maxRequestBytes is %d, above the 4194304 (4 MiB) a gRPC receiver accepts by default: a larger request is refused for good (\"received message larger than max\") and dropped. Keep it at 3145728 unless every gRPC destination was raised." $mrb) -}}
{{- end -}}
{{- toJson $w -}}
{{- end -}}

{{/* The proxy variables of the collector container: HTTPS_PROXY / HTTP_PROXY from the values or from the Secret named in
     export.proxy.secretName, and a NO_PROXY that always keeps in-cluster names and the pod's own loopback direct (a next
     hop written <name>.<ns>.svc is a cluster Service, which a corporate proxy cannot reach and a source cluster's agent
     uses to reach this very operator). Nothing at all when no proxy is configured. */}}
{{- define "operator.proxyEnv" -}}
{{- $p := .Values.export.proxy -}}
{{- if or $p.httpProxy $p.httpsProxy $p.secretName -}}
{{- if $p.httpsProxy }}
- {name: HTTPS_PROXY, value: {{ $p.httpsProxy | quote }}}
{{- else if $p.secretName }}
- name: HTTPS_PROXY
  valueFrom: {secretKeyRef: {name: {{ $p.secretName | quote }}, key: HTTPS_PROXY}}
{{- end }}
{{- if $p.httpProxy }}
- {name: HTTP_PROXY, value: {{ $p.httpProxy | quote }}}
{{- else if $p.secretName }}
- name: HTTP_PROXY
  valueFrom: {secretKeyRef: {name: {{ $p.secretName | quote }}, key: HTTP_PROXY, optional: true}}
{{- end }}
{{- $no := list "localhost" "127.0.0.1" "::1" ".svc" ".cluster.local" }}
{{- if $p.noProxy }}{{ $no = append $no $p.noProxy }}{{ end }}
- {name: NO_PROXY, value: {{ join "," $no | quote }}}
{{- end }}
{{- end -}}
