{{- define "controller.name" -}}ikhnos-controller{{- end -}}
{{- define "controller.labels" -}}
app.kubernetes.io/name: ikhnos-controller
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: ikhnos
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}
{{- define "controller.image" -}}
{{- if .Values.image.digest -}}
{{ .Values.image.repository }}@{{ .Values.image.digest }}
{{- else -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end -}}
{{- define "controller.pullPolicy" -}}
{{- if .Values.image.pullPolicy -}}
{{- .Values.image.pullPolicy -}}
{{- else if eq (.Values.image.tag | default .Chart.AppVersion | toString) "edge" -}}
Always
{{- else -}}
IfNotPresent
{{- end -}}
{{- end -}}
{{/* The names of the agent chart's cluster-scoped objects, which are fixed by that chart: a role per tier and its binding in a namespace,
     and the roles of the telemetry's collectors and of Kepler (named whether or not allowTelemetry is on, so that the controller can still
     remove them when telemetry is taken off). */}}
{{- define "controller.agentClusterNames" -}}
{{- $ns := .Release.Namespace -}}
{{- range $t := until 3 }}
- ikhnos-agent-t{{ $t }}
- ikhnos-agent-t{{ $t }}-{{ $ns }}
{{- end }}
{{- range $r := list "telemetry" "kepler" }}
- ikhnos-agent-{{ $r }}
- ikhnos-agent-{{ $r }}-{{ $ns }}
{{- end }}
{{- end -}}
{{- define "controller.validate" -}}
{{- $t := .Values.maxAccessTier | int -}}
{{- if not (has $t (list 0 1 2)) -}}{{- fail "maxAccessTier must be 0, 1 or 2" -}}{{- end -}}
{{- end -}}
