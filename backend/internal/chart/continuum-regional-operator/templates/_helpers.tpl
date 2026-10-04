{{/* Object name for everything this chart templates - NOT the same as the fixed app.kubernetes.io/name
     label below (that stays "continuum-regional-operator" for every release, it is the chart identity).
     Derived from .Release.Name so two operators installed into the same namespace never collide on
     object names (ServiceAccount/ConfigMap/Service/Deployment/NetworkPolicy) - this chart is explicitly
     designed to run many instances per namespace, unlike continuum-agent, which is 1:1 with a cluster and
     so can safely hardcode its own name. Same dedup convention as continuum-server's continuum.fullname:
     a release already named with "regional-operator" in it is not repeated. */}}
{{- define "operator.name" -}}
{{- $suffix := "regional-operator" -}}
{{- if contains $suffix .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $suffix | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- define "operator.labels" -}}
app.kubernetes.io/name: continuum-regional-operator
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: continuum
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{/* Digest-vs-tag resolution and pull policy, the same convention continuum-agent's own helpers use
     (agent.image / agent.imagePullPolicy) - duplicated here rather than shared, since Helm has no
     cross-chart template include without a library chart and Chart.lock, which this package's
     embed-per-chart model does not support. See chart.go's own comment. */}}
{{- define "operator.image" -}}
{{- $i := .Values.image -}}
{{- if $i.digest -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" (toString $i.digest)) -}}{{- fail (printf "image.digest must look like sha256:<64 hex characters>, got %q" (toString $i.digest)) -}}{{- end -}}
{{- printf "%s@%s" $i.repository $i.digest -}}
{{- else -}}
{{- printf "%s:%s" $i.repository (toString ($i.tag | default .Chart.AppVersion)) -}}
{{- end -}}
{{- end -}}
{{- define "operator.imagePullPolicy" -}}{{- .Values.image.pullPolicy | default "IfNotPresent" -}}{{- end -}}

{{/* Extra pod labels from values, refusing the labels the chart's own selectors depend on - same
     convention as agent.podLabels. */}}
{{- define "operator.podLabels" -}}
{{- range $k, $v := .Values.podLabels }}
{{- if or (hasPrefix "app.kubernetes.io/" $k) (hasPrefix "helm.sh/" $k) }}{{ fail (printf "podLabels: %q is set by the chart and cannot be overridden" $k) }}{{ end }}
{{ $k }}: {{ $v | quote }}
{{- end }}
{{- end -}}

{{/* Required: an operator with no destination has nothing to do. */}}
{{- define "operator.validate" -}}
{{- if not .Values.export.otlp.endpoint -}}{{- fail "export.otlp.endpoint is required" -}}{{- end -}}
{{- if and .Values.receiver.auth.enabled (not .Values.receiver.auth.secretName) -}}
{{- fail "receiver.auth.enabled requires receiver.auth.secretName" -}}
{{- end -}}
{{- if and .Values.receiver.tls.enabled (not .Values.receiver.tls.secretName) -}}
{{- fail "receiver.tls.enabled requires receiver.tls.secretName (a Secret holding tls.crt, tls.key, and - if receiver.tls.mtls is also on - ca.crt)" -}}
{{- end -}}
{{- end -}}
