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

{{/* Values that were added after 0.1.0 may be missing when `helm upgrade --reuse-values` carries the old release's values
     over an older chart. These read them without failing. */}}
{{- define "agent.healthEnabled" -}}{{- if (dig "health" "enabled" true .Values.AsMap) -}}true{{- end -}}{{- end -}}
{{- define "agent.healthPort" -}}{{- dig "health" "port" 8082 .Values.AsMap | int -}}{{- end -}}
