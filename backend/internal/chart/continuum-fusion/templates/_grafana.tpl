{{/* The one starter dashboard: what is arriving, from where. Built from the stores that are on, so a panel never points
     at a data source that does not exist. Only what the three stores promise on their own (labels the chart promotes,
     Loki's default OTLP index labels) - nothing that depends on which signals a sender chose. */}}
{{- define "fusion.arrivingDashboard" -}}
{{- $panels := list -}}
{{- $y := 0 -}}
{{- if .Values.prometheus.enabled -}}
{{- $panels = append $panels (dict "id" 1 "type" "stat" "title" "Clusters reporting" "datasource" (dict "type" "prometheus" "uid" "fusion-metrics") "gridPos" (dict "h" 5 "w" 6 "x" 0 "y" $y) "targets" (list (dict "refId" "A" "expr" "count(count by (continuum_cluster_id) ({continuum_cluster_id!=\"\", __name__!=\"ikhnos_application_info\"})) or vector(0)" "instant" true)) "options" (dict "reduceOptions" (dict "calcs" (list "lastNotNull")))) -}}
{{- $panels = append $panels (dict "id" 2 "type" "timeseries" "title" "Series by cluster" "datasource" (dict "type" "prometheus" "uid" "fusion-metrics") "gridPos" (dict "h" 5 "w" 18 "x" 6 "y" $y) "targets" (list (dict "refId" "A" "expr" "count by (continuum_cluster_id) ({continuum_cluster_id!=\"\", __name__!=\"ikhnos_application_info\"})" "legendFormat" "{{continuum_cluster_id}}" "interval" "5m"))) -}}
{{- $y = add $y 5 -}}
{{- end -}}
{{- if .Values.loki.enabled -}}
{{- $panels = append $panels (dict "id" 3 "type" "timeseries" "title" "Log lines by namespace" "datasource" (dict "type" "loki" "uid" "fusion-logs") "gridPos" (dict "h" 7 "w" 24 "x" 0 "y" $y) "targets" (list (dict "refId" "A" "expr" "sum by (k8s_namespace_name) (count_over_time({k8s_namespace_name=~\".+\"}[$__interval]))" "legendFormat" "{{k8s_namespace_name}}"))) -}}
{{- $y = add $y 7 -}}
{{- $panels = append $panels (dict "id" 4 "type" "logs" "title" "Latest logs" "datasource" (dict "type" "loki" "uid" "fusion-logs") "gridPos" (dict "h" 9 "w" 24 "x" 0 "y" $y) "targets" (list (dict "refId" "A" "expr" "{k8s_namespace_name=~\".+\"}")) "options" (dict "enableLogDetails" true "sortOrder" "Descending")) -}}
{{- $y = add $y 9 -}}
{{- end -}}
{{- if .Values.tempo.enabled -}}
{{- $panels = append $panels (dict "id" 5 "type" "table" "title" "Recent traces" "datasource" (dict "type" "tempo" "uid" "fusion-traces") "gridPos" (dict "h" 8 "w" 24 "x" 0 "y" $y) "targets" (list (dict "refId" "A" "queryType" "traceql" "query" "{}" "limit" 20 "tableType" "traces"))) -}}
{{- end -}}
{{- dict "uid" "fusion-arriving" "title" "What is arriving" "tags" (list "ikhnos") "schemaVersion" 39 "version" 1 "editable" false "time" (dict "from" "now-3h" "to" "now") "refresh" "30s" "timepicker" (dict "refresh_intervals" (list "10s" "30s" "1m" "5m" "15m" "1h")) "panels" $panels | toPrettyJson -}}
{{- end -}}

{{/* The Ikhnos dashboards (files/dashboards/*.json: clusters and nodes, namespaces and workloads with logs, delivery health). They
     are written against the three provisioned data sources, so a panel whose store is off is dropped here and a dashboard
     never points at a data source that does not exist. Every query in them was run against series that really arrive from the
     collectors' own output; nothing depends on a signal a sender may have left out beyond the metrics named in the query. */}}
{{- define "fusion.dashboardFile" -}}
{{- $root := .root -}}
{{- $d := $root.Files.Get .file | fromJson -}}
{{- $keep := list -}}
{{- range $d.panels -}}
{{- $uid := dig "datasource" "uid" "" . -}}
{{- if or (eq .type "row") (and (eq $uid "fusion-metrics") $root.Values.prometheus.enabled) (and (eq $uid "fusion-logs") $root.Values.loki.enabled) (and (eq $uid "fusion-traces") $root.Values.tempo.enabled) -}}
{{- $keep = append $keep . -}}
{{- end -}}
{{- end -}}
{{- $_ := set $d "panels" $keep -}}
{{- $d | toPrettyJson -}}
{{- end -}}

{{/* Which dashboard files there are, for the ConfigMap and its checksum. They all read metrics (their variables do), so none
     is rendered without Prometheus. */}}
{{- define "fusion.dashboardNames" -}}
{{- if .Values.prometheus.enabled -}}clusters workloads delivery applications categories{{- end -}}
{{- end -}}
