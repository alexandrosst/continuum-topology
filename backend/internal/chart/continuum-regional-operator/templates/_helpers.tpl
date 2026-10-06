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

{{/* The names of the Secrets this pod mounts for TLS, as a JSON list: the receiver's certificate, each destination's
     client certificate, and the heartbeat's CA. Kubernetes refreshes a mounted Secret in place when it changes, but
     a collector reads its certificate files only at start and then at every reload_interval (1h, see
     operator.exporterBlock and config.yaml). */}}
{{- define "operator.tlsSecretNames" -}}
{{- $root := . -}}
{{- $l := list -}}
{{- if .Values.receiver.tls.enabled }}{{ $l = append $l .Values.receiver.tls.secretName }}{{ end -}}
{{- if and (include "operator.defaultUsed" .) .Values.export.otlp.tls.mtls.enabled }}{{ $l = append $l .Values.export.otlp.tls.mtls.secretName }}{{ end -}}
{{- range (include "operator.modalities" . | fromJsonArray) -}}
{{- $r := get $root.Values.export.routes . -}}
{{- if and (include "operator.hasRoute" (dict "root" $root "m" .)) $r.tls.mtls.enabled }}{{ $l = append $l $r.tls.mtls.secretName }}{{ end -}}
{{- end -}}
{{- if and .Values.heartbeat.enabled .Values.heartbeat.tls.caSecretName }}{{ $l = append $l .Values.heartbeat.tls.caSecretName }}{{ end -}}
{{- toJson (uniq $l) -}}
{{- end -}}

{{/* A digest of what those Secrets hold right now, so that renewing a certificate and running `helm upgrade`
     (the install command again) restarts the pod instead of leaving it on the old certificate until
     reload_interval comes round. Read with `lookup`, which Helm answers with an empty map when the object does not
     exist or when it renders offline (`helm template`, and therefore Argo CD and Flux): then this is empty, the
     annotation is left out, and nothing here can fail the render. Two limits follow from that and are the
     reason reload_interval exists as well: a GitOps render never sees the cluster, and an account that may not
     `get` Secrets in this namespace makes Helm itself fail the lookup - set rolloutOnSecretChange=false there. */}}
{{- define "operator.tlsChecksum" -}}
{{- if .Values.rolloutOnSecretChange -}}
{{- $ns := .Release.Namespace -}}
{{- $parts := list -}}
{{- range (include "operator.tlsSecretNames" . | fromJsonArray) -}}
{{- $s := lookup "v1" "Secret" $ns . -}}
{{- if and $s $s.data }}{{ $parts = append $parts (printf "%s=%s" . (toJson $s.data | sha256sum)) }}{{ end -}}
{{- end -}}
{{- if $parts }}{{ join "," $parts | sha256sum }}{{ end -}}
{{- end -}}
{{- end -}}

{{/* Required: an operator with no destination for a signal type has nothing to do with it. */}}
{{- define "operator.validate" -}}
{{- if lt (int .Values.processors.batch.sendBatchMaxSize) (int .Values.processors.batch.sendBatchSize) -}}{{- fail "processors.batch.sendBatchMaxSize must be at least processors.batch.sendBatchSize" -}}{{- end -}}
{{- if and (include "operator.defaultUsed" .) (not .Values.export.otlp.endpoint) -}}{{- fail "export.otlp.endpoint is required, unless every signal type (metrics, logs, traces) has a destination of its own under export.routes" -}}{{- end -}}
{{- if and .Values.export.otlp.tls.mtls.enabled (not .Values.export.otlp.tls.mtls.secretName) -}}{{- fail "export.otlp.tls.mtls.enabled requires export.otlp.tls.mtls.secretName (a Secret holding tls.crt, tls.key and ca.crt)" -}}{{- end -}}
{{- range (include "operator.modalities" . | fromJsonArray) -}}
{{- $r := get $.Values.export.routes . -}}
{{- if and $r.tls.mtls.enabled (not $r.tls.mtls.secretName) -}}{{- fail (printf "export.routes.%s.tls.mtls.enabled requires export.routes.%s.tls.mtls.secretName (a Secret holding tls.crt, tls.key and ca.crt)" . .) -}}{{- end -}}
{{- end -}}
{{- if .Values.service.nodePort -}}
{{- if ne .Values.service.type "NodePort" -}}{{- fail "service.nodePort only applies to service.type=NodePort" -}}{{- end -}}
{{- end -}}
{{- if and .Values.receiver.auth.enabled (not .Values.receiver.auth.secretName) -}}
{{- fail "receiver.auth.enabled requires receiver.auth.secretName" -}}
{{- end -}}
{{- if and .Values.receiver.tls.enabled (not .Values.receiver.tls.secretName) -}}
{{- fail "receiver.tls.enabled requires receiver.tls.secretName (a Secret holding tls.crt, tls.key, and - if receiver.tls.mtls is also on - ca.crt)" -}}
{{- end -}}
{{- if and .Values.receiver.requireAuth (not .Values.receiver.auth.enabled) (not (and .Values.receiver.tls.enabled .Values.receiver.tls.mtls)) -}}
{{- fail "receiver.requireAuth is set, but nothing would authenticate the receiver: enable receiver.auth (a bearer token) or receiver.tls with receiver.tls.mtls (a required client certificate). Refusing to render an open receiver." -}}
{{- end -}}
{{- if .Values.heartbeat.enabled -}}
{{- if not .Values.heartbeat.url -}}{{- fail "heartbeat.enabled requires heartbeat.url (the Ikhnos server's heartbeat address, https://<server>/api/v1/operator-heartbeat)" -}}{{- end -}}
{{- if not (regexMatch "^https?://[^\\s/]+" (toString .Values.heartbeat.url)) -}}{{- fail (printf "heartbeat.url must be an http(s) URL, got %q" (toString .Values.heartbeat.url)) -}}{{- end -}}
{{- if and (not (hasPrefix "https://" (toString .Values.heartbeat.url))) (not .Values.heartbeat.allowPlainHTTP) -}}
{{- fail "heartbeat.url must be https:// - the heartbeat secret is sent with every request. Set heartbeat.allowPlainHTTP=true only for a throwaway test server on a trusted network." -}}
{{- end -}}
{{- if not .Values.heartbeat.auth.secretName -}}{{- fail "heartbeat.enabled requires heartbeat.auth.secretName (a Secret holding the heartbeat secret Ikhnos minted for this operator - not the receiver token)" -}}{{- end -}}
{{- if and .Values.heartbeat.tls.caSecretName (not (hasPrefix "https://" (toString .Values.heartbeat.url))) -}}
{{- fail "heartbeat.tls.caSecretName only applies to an https:// heartbeat.url" -}}
{{- end -}}
{{- end -}}
{{- end -}}
