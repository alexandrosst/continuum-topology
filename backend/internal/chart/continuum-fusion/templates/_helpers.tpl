{{/* Object name for everything this chart templates, and the prefix of every Service a regional operator is pointed at:
     the release name, with "-fusion" added unless it already says so (a release called "continuum-fusion" is not
     "continuum-fusion-fusion"). The Service names the server prints for an operator are built from this, so it is
     a contract: change it and every operator pointed at a FUSION install loses it. */}}
{{- define "fusion.name" -}}
{{- if contains "fusion" .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-fusion" .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "fusion.labels" -}}
app.kubernetes.io/name: continuum-fusion
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: continuum
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{/* The labels that select ONE store's pods: used by the StatefulSet, its Service and the NetworkPolicy. */}}
{{- define "fusion.selector" -}}
app.kubernetes.io/name: continuum-fusion
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/* digest wins over tag, as in the other charts */}}
{{- define "fusion.image" -}}
{{- $i := .image -}}
{{- if $i.digest -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" (toString $i.digest)) -}}{{- fail (printf "%s.image.digest must look like sha256:<64 hex characters>, got %q" .component (toString $i.digest)) -}}{{- end -}}
{{- printf "%s@%s" $i.repository $i.digest -}}
{{- else -}}
{{- printf "%s:%s" $i.repository (toString $i.tag) -}}
{{- end -}}
{{- end -}}

{{- define "fusion.podLabels" -}}
{{- range $k, $v := .Values.podLabels }}
{{- if or (hasPrefix "app.kubernetes.io/" $k) (hasPrefix "helm.sh/" $k) }}{{ fail (printf "podLabels: %q is set by the chart and cannot be overridden" $k) }}{{ end }}
{{ $k }}: {{ $v | quote }}
{{- end }}
{{- end -}}

{{/* Fail early, in words, instead of letting a store crash-loop on a config it cannot read. */}}
{{- define "fusion.validate" -}}
{{- if not (or .Values.prometheus.enabled .Values.loki.enabled .Values.tempo.enabled) -}}
{{- fail "nothing to deploy: enable at least one of prometheus, loki, tempo" -}}
{{- end -}}
{{- if and .Values.prometheus.enabled (not (regexMatch "^[0-9]+(y|w|d|h|m|s|ms)$" (toString .Values.prometheus.retention))) -}}
{{- fail (printf "prometheus.retention must be a number and a unit (y, w, d, h, m, s), like 15d; got %q" (toString .Values.prometheus.retention)) -}}
{{- end -}}
{{- if .Values.loki.enabled -}}
{{- $r := toString .Values.loki.retention -}}
{{- if not (regexMatch "^[0-9]+h$" $r) -}}{{- fail (printf "loki.retention must be hours, like 168h; got %q" $r) -}}{{- end -}}
{{- if or (lt (int (trimSuffix "h" $r)) 24) (ne (mod (int (trimSuffix "h" $r)) 24) 0) -}}
{{- fail (printf "loki.retention must be a whole number of days written in hours (24h, 48h, 168h, ...); got %q" $r) -}}
{{- end -}}
{{- end -}}
{{- if and .Values.tempo.enabled (not (regexMatch "^[0-9]+h$" (toString .Values.tempo.retention))) -}}
{{- fail (printf "tempo.retention must be hours, like 72h; got %q" (toString .Values.tempo.retention)) -}}
{{- end -}}
{{- end -}}

{{/* Scheduling and identity common to every store's pod. */}}
{{- define "fusion.podCommon" -}}
{{- with .Values.priorityClassName }}
priorityClassName: {{ . | quote }}
{{- end }}
{{- with .Values.imagePullSecrets }}
imagePullSecrets: {{- toYaml . | nindent 2 }}
{{- end }}
serviceAccountName: {{ include "fusion.name" . }}
automountServiceAccountToken: false
{{- with .Values.nodeSelector }}
nodeSelector: {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.tolerations }}
tolerations: {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.affinity }}
affinity: {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/* How many replicas a workload starts with. With switch.managed off it is always 1 - a plain install. With it
     on, the server owns the switch (it scales these workloads between 0 and 1 itself), so a `helm upgrade` must
     not undo its last decision: the live replica count is kept when the workload already exists, and
     switch.initialReplicas (0 = off until the server turns it on) only applies the first time. Called with
     (dict "root" . "kind" "StatefulSet" "name" ...). `lookup` is empty under `helm template`, which is
     exactly the first-install case. */}}
{{- define "fusion.replicas" -}}
{{- if .root.Values.switch.managed -}}
{{- $o := lookup "apps/v1" .kind .root.Release.Namespace .name -}}
{{- if and $o $o.spec -}}{{- $o.spec.replicas | int -}}{{- else -}}{{- .root.Values.switch.initialReplicas | int -}}{{- end -}}
{{- else -}}1{{- end -}}
{{- end -}}

{{/* The central gateway: the one door into FUSION. Named <name>-central like the stores are <name>-<store>. */}}
{{- define "fusion.central" -}}{{- printf "%s-central" (include "fusion.name" .) -}}{{- end -}}
{{- define "fusion.centralTLSSecret" -}}{{- .Values.central.receiver.tlsSecretName | default (printf "%s-receiver-tls" (include "fusion.central" .)) -}}{{- end -}}
