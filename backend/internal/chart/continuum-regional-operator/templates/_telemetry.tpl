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

{{/* The "exporters" stanza - same shape as agent.telemetryExporterYAML, reading from .Values.export.otlp
     instead of .Values.telemetry.export.otlp. */}}
{{- define "operator.exporterYAML" -}}
otlp:
  endpoint: {{ .Values.export.otlp.endpoint | quote }}
  tls:
    insecure: {{ .Values.export.otlp.tls.insecure }}
    {{- if .Values.export.otlp.tls.caFile }}
    ca_file: {{ .Values.export.otlp.tls.caFile | quote }}
    {{- end }}
  {{- if .Values.export.otlp.auth.secretName }}
  headers:
    {{ .Values.export.otlp.auth.headerName }}: "${env:CONTINUUM_OPERATOR_EXPORT_AUTH}"
  {{- end }}
{{- end -}}
{{- define "operator.exporterEnv" -}}
{{- if .Values.export.otlp.auth.secretName }}
- name: CONTINUUM_OPERATOR_EXPORT_AUTH
  valueFrom:
    secretKeyRef:
      name: {{ .Values.export.otlp.auth.secretName }}
      key: {{ .Values.export.otlp.auth.secretKey }}
{{- end }}
{{- end -}}
