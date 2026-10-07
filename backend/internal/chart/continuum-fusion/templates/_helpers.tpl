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
{{- if and .Values.prometheus.enabled (include "fusion.retentionSizeSetting" .) (not (regexMatch "^(0|[0-9]+(B|KB|MB|GB|TB|PB))$" (include "fusion.retentionSizeSetting" .))) -}}
{{- fail (printf "prometheus.retentionSize must be 0 (no size limit) or a number and a unit (B, KB, MB, GB, TB, PB; powers of 1024) like 8GB; leave it empty to take 85%% of prometheus.storage. Got %q" (include "fusion.retentionSizeSetting" .)) -}}
{{- end -}}
{{- if and .Values.central.enabled (lt (int .Values.central.batch.sendBatchMaxSize) (int .Values.central.batch.sendBatchSize)) -}}
{{- fail "central.batch.sendBatchMaxSize must be at least central.batch.sendBatchSize" -}}
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
{{/* Derived, never a value: the Ikhnos server writes the certificates into <name>-central-receiver-tls and the Role it
     is granted names that Secret, so the chart cannot let it be renamed. */}}
{{- define "fusion.centralTLSSecret" -}}{{- printf "%s-receiver-tls" (include "fusion.central" .) -}}{{- end -}}

{{/* GOMEMLIMIT (80% of a container's memory limit) as a byte count, so the Go runtime collects harder before the
     container is OOM-killed; memory_limiter alone reacts only once the heap is already large. Takes a resources
     map; empty when there is no limits.memory or it is not a quantity this recognises (a nicety, never a reason to
     fail a release). Same algorithm as continuum-regional-operator's operator.gomemlimit. */}}
{{- define "fusion.gomemlimit" -}}
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

{{/* prometheus.retentionSize as text, empty when unset. Not a plain truthiness test: `--set prometheus.retentionSize=0`
     arrives as the number 0, which a Go template treats as empty, and "no size limit" would silently become the
     85% default instead. (The schema lets that one number through for exactly this reason.) */}}
{{- define "fusion.retentionSizeSetting" -}}
{{- if not (kindIs "invalid" .Values.prometheus.retentionSize) }}{{ toString .Values.prometheus.retentionSize | trim }}{{ end -}}
{{- end -}}

{{/* The value of Prometheus's --storage.tsdb.retention.size. With prometheus.retentionSize set it is used as
     written ("0" turns the size limit off). Otherwise, with a durable volume, it is 85% of prometheus.storage: the
     time-based retention alone never stops a busy install from filling its volume, and a full volume stops Prometheus
     ingesting at all. The 15% left over is headroom for the write-ahead log and the head, which are written before
     they are compacted into blocks, and for the compaction itself, which needs room for the block it is writing
     next to the ones it is merging. Prometheus reads the size in binary units, so the answer is whole MB (1 MB =
     1 MiB). Empty with no durable volume (an emptyDir has no size of its own to take a share of). The quantity is
     parsed here, not by Kubernetes: Ki/Mi/Gi/Ti are powers of 1024, K/M/G/T of 1000, no suffix is bytes. */}}
{{- define "fusion.prometheusRetentionSize" -}}
{{- $p := .Values.prometheus -}}
{{- $set := include "fusion.retentionSizeSetting" . -}}
{{- if $set -}}
{{- $set -}}
{{- else if .Values.persistence.enabled -}}
{{- $q := toString $p.storage -}}
{{- $n := float64 (regexFind "^[0-9]+(\\.[0-9]+)?" $q) -}}
{{- $suf := regexFind "(Ki|Mi|Gi|Ti|K|M|G|T)$" $q -}}
{{- $scale := dict "" 1.0 "Ki" 1024.0 "Mi" 1048576.0 "Gi" 1073741824.0 "Ti" 1099511627776.0 "K" 1000.0 "M" 1000000.0 "G" 1000000000.0 "T" 1000000000000.0 -}}
{{- $mib := int64 (floor (divf (mulf $n (get $scale $suf)) 1048576.0)) -}}
{{- printf "%dMB" (max 1 (div (mul $mib 85) 100)) -}}
{{- end -}}
{{- end -}}

{{/* What each of the gateway's exporters does while its store is down (a restart of Prometheus, Loki or Tempo, a
     volume that is not yet bound): retry for central.queue.retryMaxElapsedTime and hold up to central.queue.size
     batches meanwhile, instead of the collector's defaults (retry for 5 minutes, then drop). When the queue is full
     the exporter refuses, memory_limiter pushes back through the receiver and the senders' own queues take over, so
     nothing here grows without bound. With central.queue.persistent.enabled the queue is also on an emptyDir
     (file_storage/queue), which survives a container restart but not a rescheduled pod. Emits at column 0. */}}
{{- define "fusion.centralExporterResilience" -}}
{{- $q := .Values.central.queue -}}
retry_on_failure:
  enabled: true
  initial_interval: 5s
  max_interval: 30s
  max_elapsed_time: {{ $q.retryMaxElapsedTime | quote }}
sending_queue:
  enabled: true
  queue_size: {{ $q.size }}
  {{- if $q.persistent.enabled }}
  storage: file_storage/queue
  {{- end }}
{{- end -}}
