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
{{/* The collector's ConfigMap. Named in two places (the ConfigMap itself and the Deployment volume that mounts it), so
     both take it from here: the suffix has to be added before the 63-character cut, or a long release name gets a
     ConfigMap whose name is truncated while the Deployment asks for the untruncated one and never starts. */}}
{{- define "operator.configMapName" -}}{{- printf "%s-config" (include "operator.name" .) | trunc 63 | trimSuffix "-" -}}{{- end -}}
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
{{- printf "%s@%s" (include "operator.imageRepo" (dict "root" . "repo" $i.repository)) $i.digest -}}
{{- else -}}
{{- printf "%s:%s" (include "operator.imageRepo" (dict "root" . "repo" $i.repository)) (toString ($i.tag | default .Chart.AppVersion)) -}}
{{- end -}}
{{- end -}}

{{/* Platform layer, the same as continuum-agent's templates/_platform.tpl (duplicated, not shared: each chart is installable on
     its own). Values are read with `dig` and a default so that `helm upgrade --reuse-values` over an older release, which
     lacks these blocks, still renders. See that file for the reasoning behind each one. */}}
{{- define "operator.podSecurityContext" -}}
runAsNonRoot: true
{{- $uid := int (dig "podSecurity" "runAsUser" 65532 .Values.AsMap) }}
runAsUser: {{ $uid }}
runAsGroup: {{ $uid }}
seccompProfile: {type: RuntimeDefault}
{{- end -}}
{{/* global.imageRegistry replaces the registry host of the image repository and keeps the rest of its path
     (otel/opentelemetry-collector-contrib -> <registry>/otel/opentelemetry-collector-contrib). */}}
{{- define "operator.imageRepo" -}}
{{- $reg := trimSuffix "/" (toString (dig "global" "imageRegistry" "" .root.Values.AsMap)) -}}
{{- if $reg -}}
{{- $parts := splitList "/" (toString .repo) -}}
{{- $first := index $parts 0 -}}
{{- if and (gt (len $parts) 1) (or (contains "." $first) (contains ":" $first) (eq $first "localhost")) -}}
{{- printf "%s/%s" $reg (join "/" (rest $parts)) -}}
{{- else -}}
{{- printf "%s/%s" $reg (toString .repo) -}}
{{- end -}}
{{- else -}}
{{- .repo -}}
{{- end -}}
{{- end -}}
{{/* Keeps a service mesh's sidecar out of the pod (mesh.injection: disabled, the default; inherit changes nothing). Istio:
     pod label sidecar.istio.io/inject="false"; Linkerd: annotation linkerd.io/inject: disabled. A sidecar would be refused in a
     namespace enforcing Pod Security restricted (its init container needs NET_ADMIN), and the receiver does its own TLS/mTLS
     (receiver.tls), which a proxy in front of it would have to be told to pass through. */}}
{{- define "operator.meshOptOutLabels" -}}
{{- if and (ne (toString (dig "mesh" "injection" "disabled" .Values.AsMap)) "inherit") (not (hasKey (.Values.podLabels | default dict) "sidecar.istio.io/inject")) -}}
sidecar.istio.io/inject: "false"
{{- end -}}
{{- end -}}
{{- define "operator.meshOptOutAnnotations" -}}
{{- if and (ne (toString (dig "mesh" "injection" "disabled" .Values.AsMap)) "inherit") (not (hasKey (.Values.podAnnotations | default dict) "linkerd.io/inject")) -}}
linkerd.io/inject: disabled
{{- end -}}
{{- end -}}
{{/* The DNS egress rule: kube-dns/CoreDNS (kube-system) and NodeLocal
     DNSCache (169.254.20.10); see continuum-agent's agent.dnsEgressRule. Takes a dict: dnsCIDRs. */}}
{{- define "operator.dnsEgressRule" -}}
- to:
    - namespaceSelector:
        matchLabels: {kubernetes.io/metadata.name: kube-system}
      podSelector:
        matchLabels: {k8s-app: kube-dns}
    {{- range (concat (list "169.254.20.10/32") (.dnsCIDRs | default list) | uniq) }}
    - ipBlock: {cidr: {{ . | quote }}}
    {{- end }}
  ports:
    - {port: 53, protocol: UDP}
    - {port: 53, protocol: TCP}
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
     client certificate, each destination's CA bundle, and the heartbeat's CA. Kubernetes refreshes a mounted Secret in place when it changes, but
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
{{- if and (include "operator.hasRoute" (dict "root" $root "m" .)) $r.tls.caSecretName (not $r.tls.mtls.enabled) }}{{ $l = append $l $r.tls.caSecretName }}{{ end -}}
{{- end -}}
{{- if and (include "operator.defaultUsed" .) .Values.export.otlp.tls.caSecretName (not .Values.export.otlp.tls.mtls.enabled) }}{{ $l = append $l .Values.export.otlp.tls.caSecretName }}{{ end -}}
{{- if and .Values.heartbeat.enabled .Values.heartbeat.tls.caSecretName }}{{ $l = append $l .Values.heartbeat.tls.caSecretName }}{{ end -}}
{{- toJson (uniq $l) -}}
{{- end -}}

{{/* Every Secret whose content the pod reads once, at start, and never again: the TLS ones above, plus the bearer
     token of the receiver, the credential header of each destination in use and the heartbeat secret. They reach the
     collector as environment variables, which a running container never sees change, so a rotated token needs the pod
     restarted: with these in the digest below, running the install command again does it. */}}
{{- define "operator.rolloutSecretNames" -}}
{{- $root := . -}}
{{- $l := include "operator.tlsSecretNames" . | fromJsonArray -}}
{{- if .Values.receiver.auth.enabled }}{{ $l = append $l .Values.receiver.auth.secretName }}{{ end -}}
{{- if and (include "operator.defaultUsed" .) .Values.export.otlp.auth.secretName }}{{ $l = append $l .Values.export.otlp.auth.secretName }}{{ end -}}
{{- range (include "operator.modalities" . | fromJsonArray) -}}
{{- $r := get $root.Values.export.routes . -}}
{{- if and (include "operator.hasRoute" (dict "root" $root "m" .)) $r.auth.secretName }}{{ $l = append $l $r.auth.secretName }}{{ end -}}
{{- end -}}
{{- if .Values.heartbeat.enabled }}{{ $l = append $l .Values.heartbeat.auth.secretName }}{{ end -}}
{{- /* The proxy URL is an environment variable too: read once, at start. */ -}}
{{- if .Values.export.proxy.secretName }}{{ $l = append $l .Values.export.proxy.secretName }}{{ end -}}
{{- toJson (uniq $l) -}}
{{- end -}}

{{/* A digest of what those Secrets hold right now, so that renewing a certificate or rotating a token and running `helm upgrade`
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
{{- range (include "operator.rolloutSecretNames" . | fromJsonArray) -}}
{{- $s := lookup "v1" "Secret" $ns . -}}
{{- if and $s $s.data }}{{ $parts = append $parts (printf "%s=%s" . (toJson $s.data | sha256sum)) }}{{ end -}}
{{- end -}}
{{- if $parts }}{{ join "," $parts | sha256sum }}{{ end -}}
{{- end -}}
{{- end -}}

{{/* Required: an operator with no destination for a signal type has nothing to do with it. */}}
{{- define "operator.validate" -}}
{{- include "operator.exportValidate" . -}}
{{- /* retry_on_failure.max_interval is 30s (operator.exporterResilienceYAML) and the collector refuses to start when
       max_elapsed_time is shorter than it, which the schema's duration pattern cannot say: the pod would crash-loop
       on a value that looked fine. 0 is "never stop retrying". */ -}}
{{- $retry := toString .Values.export.queue.retryMaxElapsedTime -}}
{{- if regexMatch "^[0-9]+(ms|s|m|h)$" $retry -}}
{{- $unit := dict "ms" 0.001 "s" 1.0 "m" 60.0 "h" 3600.0 -}}
{{- $secs := mulf (float64 (regexFind "^[0-9]+" $retry)) (get $unit (regexFind "(ms|s|m|h)$" $retry)) -}}
{{- if and (gt $secs 0.0) (lt $secs 30.0) -}}{{- fail (printf "export.queue.retryMaxElapsedTime is %s: it must be at least 30s (the longest wait between two retries, which the collector will not let it be shorter than) or 0s to retry until the destination answers" $retry) -}}{{- end -}}
{{- end -}}
{{- if ge (int .Values.processors.memoryLimiter.spikeLimitPercentage) (int .Values.processors.memoryLimiter.limitPercentage) -}}{{- fail "processors.memoryLimiter.spikeLimitPercentage must be less than processors.memoryLimiter.limitPercentage (the collector refuses to start otherwise)" -}}{{- end -}}
{{- $ports := dict "4317" "the OTLP/gRPC receiver" "4318" "the OTLP/HTTP receiver" -}}
{{- $hp := toString (include "operator.healthPort" .) -}}
{{- if hasKey $ports $hp -}}{{- fail (printf "health.port %s is already used by %s" $hp (get $ports $hp)) -}}{{- end -}}
{{- if .Values.selfMetrics.enabled -}}
{{- $mp := toString (int .Values.selfMetrics.port) -}}
{{- if hasKey $ports $mp -}}{{- fail (printf "selfMetrics.port %s is already used by %s" $mp (get $ports $mp)) -}}{{- end -}}
{{- if eq $mp $hp -}}{{- fail "selfMetrics.port and health.port must differ" -}}{{- end -}}
{{- end -}}
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
