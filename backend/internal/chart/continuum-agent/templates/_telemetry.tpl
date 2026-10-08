{{/* OTel Collector config generation for this agent's two telemetry workloads (host DaemonSet, cluster
     Deployment) - processor/exporter/extension YAML fragments, resource sizing, and the signal-name list the
     agent container itself reports. Split out of _helpers.tpl because this is the single biggest, most
     self-contained concern in this chart (see continuum-regional-operator's own _helpers.tpl for the same
     generation logic applied to that chart's one standalone collector - duplicated, not shared, since each
     chart must still be a fully self-contained, independently installable Helm chart; see that file's own
     comments for the cross-references between the two). */}}

{{/* Fills, in place, every telemetry value (and measurements, networkPolicy.telemetryEgress, which the telemetry templates
     read too) that this release's values lack, from files/telemetry-defaults.yaml. Normally nothing is lacking: Helm merges the
     chart's values.yaml under the release's. `helm upgrade --reuse-values` is the exception - it replaces this chart's defaults
     with the OLD chart's, so an option added since the install is absent and the first template to read it fails with "nil
     pointer evaluating interface {}" (even for a release that uses no telemetry at all). Only a key that is absent is added:
     a value the release has, false and empty included, is never touched, and neither is a key under a map the release
     deliberately fills itself (nodeSelector, headers). Every helper below that reads .Values.telemetry first, and every
     template that does not go through one, includes this; it runs once per render. */}}
{{- define "agent.telemetryDefaults" -}}
{{- if not (hasKey .Values "agentTelemetryDefaulted") -}}
{{- $defaults := .Files.Get "files/telemetry-defaults.yaml" | fromYaml -}}
{{- if not $defaults -}}{{- fail "files/telemetry-defaults.yaml is missing or empty: the chart is damaged" -}}{{- end -}}
{{- $_ := include "agent.fillDefaults" (dict "dst" .Values "src" $defaults) -}}
{{- $_ := set .Values "agentTelemetryDefaulted" true -}}
{{- end -}}
{{- end -}}
{{- define "agent.fillDefaults" -}}
{{- $dst := .dst -}}
{{- range $k, $v := .src -}}
{{- if or (not (hasKey $dst $k)) (kindIs "invalid" (get $dst $k)) -}}
{{- $_ := set $dst $k (deepCopy $v) -}}
{{- else if and (kindIs "map" $v) (kindIs "map" (get $dst $k)) (ne $k "nodeSelector") (ne $k "headers") -}}
{{- $_ := include "agent.fillDefaults" (dict "dst" (get $dst $k) "src" $v) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Telemetry: which of the two collector workloads (if either) this release needs. A signal counts as
     "host" when it can only be observed per-node (resource usage, kubelet-sourced metrics, node/container
     logs) and must therefore run on every node as a DaemonSet; everything else - cluster-wide object
     watching, and anything applications push directly - runs once as a Deployment. */}}
{{- define "agent.telemetryHostEnabled" -}}
{{- include "agent.telemetryDefaults" . -}}
{{- $t := .Values.telemetry -}}
{{- if or $t.resourceUsage.metrics.enabled $t.nodeRuntime.metrics.enabled $t.systemLogs.logs.enabled -}}true{{- end -}}
{{- end -}}
{{- define "agent.telemetryClusterEnabled" -}}
{{- include "agent.telemetryDefaults" . -}}
{{- $t := .Values.telemetry -}}
{{- if or $t.energy.metrics.enabled $t.accelerators.metrics.enabled $t.kubernetesState.metrics.enabled $t.kubernetesEvents.logs.enabled $t.applicationMetrics.metrics.enabled $t.applicationLogs.logs.enabled $t.traces.traces.enabled $t.networkLatency.metrics.enabled -}}true{{- end -}}
{{- end -}}
{{- define "agent.telemetryEnabled" -}}
{{- if or (include "agent.telemetryHostEnabled" .) (include "agent.telemetryClusterEnabled" .) -}}true{{- end -}}
{{- end -}}

{{/* Whether the k8sattributes processor (pod/namespace/node metadata enrichment) is needed - true for every
     telemetry signal, so its RBAC is granted whenever telemetry is on at all rather than per-signal. */}}
{{- define "agent.telemetryK8sAttrsEnabled" -}}{{- include "agent.telemetryEnabled" . -}}{{- end -}}

{{- define "agent.telemetryOtlpReceiverEnabled" -}}
{{- $t := .Values.telemetry -}}
{{- if or $t.applicationMetrics.metrics.enabled $t.applicationLogs.logs.enabled $t.traces.traces.enabled $t.networkLatency.metrics.enabled -}}true{{- end -}}
{{- end -}}

{{/* Validates the parts of telemetry that cross signals: an export endpoint is required once anything
     is enabled, and networkLatency has nothing to re-emit unless the underlying measurement is itself on. */}}
{{- define "agent.telemetryValidate" -}}
{{- include "agent.telemetryDefaults" . -}}
{{- include "agent.telemetryHostValidate" . -}}
{{- /* retry_on_failure.max_interval is 30s (agent.telemetryExporterResilienceYAML) and the collector refuses to start
       when max_elapsed_time is shorter than it, which the schema's duration pattern cannot say: the collectors would
       crash-loop on a value that looked fine. 0 is "never stop retrying". */ -}}
{{- $retry := toString .Values.telemetry.export.queue.retryMaxElapsedTime -}}
{{- if regexMatch "^[0-9]+(ms|s|m|h)$" $retry -}}
{{- $unit := dict "ms" 0.001 "s" 1.0 "m" 60.0 "h" 3600.0 -}}
{{- $secs := mulf (float64 (regexFind "^[0-9]+" $retry)) (get $unit (regexFind "(ms|s|m|h)$" $retry)) -}}
{{- if and (gt $secs 0.0) (lt $secs 30.0) -}}{{- fail (printf "telemetry.export.queue.retryMaxElapsedTime is %s: it must be at least 30s (the longest wait between two retries, which the collector will not let it be shorter than) or 0s to retry until the destination answers" $retry) -}}{{- end -}}
{{- end -}}
{{- if lt (int .Values.telemetry.processors.batch.sendBatchMaxSize) (int .Values.telemetry.processors.batch.sendBatchSize) -}}{{- fail "telemetry.processors.batch.sendBatchMaxSize must be at least telemetry.processors.batch.sendBatchSize" -}}{{- end -}}
{{- if ge (int .Values.telemetry.processors.memoryLimiter.spikeLimitPercentage) (int .Values.telemetry.processors.memoryLimiter.limitPercentage) -}}{{- fail "telemetry.processors.memoryLimiter.spikeLimitPercentage must be lower than telemetry.processors.memoryLimiter.limitPercentage (the collector refuses to start otherwise)" -}}{{- end -}}
{{- if has (int .Values.telemetry.health.port) (list 4317 4318 (int (include "agent.telemetryProbePort" .))) -}}{{- fail (printf "telemetry.health.port %d is already a port of the collector (4317 and 4318 are OTLP, %s is its readiness probe): pick another" (int .Values.telemetry.health.port) (include "agent.telemetryProbePort" .)) -}}{{- end -}}
{{- if include "agent.telemetryEnabled" . -}}
{{- if and (include "agent.telemetryDefaultUsed" (dict "root" . "scope" "all")) (not .Values.telemetry.export.otlp.endpoint) -}}{{- fail "telemetry.export.otlp.endpoint is required once any telemetry.* signal is enabled that has no route of its own (telemetry.export.routes.<metrics|logs|traces>.endpoint)" -}}{{- end -}}
{{- end -}}
{{- if and (include "agent.telemetryDefaultUsed" (dict "root" . "scope" "all")) (eq .Values.telemetry.export.otlp.protocol "zipkin") -}}
{{- $root := . -}}
{{- range (include "agent.telemetryModalities" (dict "root" . "scope" "all") | fromJsonArray) -}}
{{- if and (ne . "traces") (not (include "agent.telemetryHasRoute" (dict "root" $root "m" .))) -}}
{{- fail (printf "telemetry.export.otlp.protocol=zipkin carries traces only: Zipkin has no way to receive %s. Turn those signals off, give them their own destination under telemetry.export.routes.%s, or send everything to an OTLP destination instead." . .) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $vroot := . -}}
{{- range $m := (list "metrics" "logs" "traces") -}}
{{- $r := get $vroot.Values.telemetry.export.routes $m -}}
{{- if and (kindIs "map" $r) $r.endpoint -}}
{{- if and (eq $r.protocol "zipkin") (ne $m "traces") -}}
{{- fail (printf "telemetry.export.routes.%s.protocol=zipkin: Zipkin carries traces only" $m) -}}
{{- end -}}
{{- if and $r.tls.mtls.enabled (not $r.tls.mtls.secretName) -}}
{{- fail (printf "telemetry.export.routes.%s.tls.mtls.enabled requires telemetry.export.routes.%s.tls.mtls.secretName (a Secret holding tls.crt, tls.key, and ca.crt)" $m $m) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if and .Values.telemetry.networkLatency.metrics.enabled (not .Values.measurements.enabled) -}}
{{- fail "telemetry.networkLatency.metrics.enabled requires measurements.enabled: true - there is nothing to re-emit otherwise" -}}
{{- end -}}
{{- if and .Values.telemetry.energy.metrics.enabled (eq .Values.telemetry.energy.metrics.source "existing") (not .Values.telemetry.energy.metrics.existing.prometheusEndpoint) -}}
{{- fail "telemetry.energy.metrics.source=existing requires telemetry.energy.metrics.existing.prometheusEndpoint" -}}
{{- end -}}
{{- /* An existing exporter is found either by one address or by the label of its pods (a DaemonSet of per-node exporters, the
       NVIDIA GPU Operator's, cannot be scraped through its Service: each scrape would reach one random node). Exactly one. */ -}}
{{- $acc := .Values.telemetry.accelerators.metrics -}}
{{- if and $acc.enabled (eq $acc.source "existing") (not $acc.existing.prometheusEndpoint) (not $acc.existing.pods.labelSelector) -}}
{{- fail "telemetry.accelerators.metrics.source=existing requires telemetry.accelerators.metrics.existing.prometheusEndpoint (one address) or telemetry.accelerators.metrics.existing.pods.labelSelector (every exporter pod, e.g. app=nvidia-dcgm-exporter for the NVIDIA GPU Operator)" -}}
{{- end -}}
{{- if and $acc.enabled (eq $acc.source "existing") $acc.existing.prometheusEndpoint $acc.existing.pods.labelSelector -}}
{{- fail "telemetry.accelerators.metrics.existing.prometheusEndpoint and existing.pods.labelSelector are two ways of finding the same exporter: set only one" -}}
{{- end -}}
{{- if and $acc.enabled (eq $acc.source "bundle-dcgm") -}}
{{- range $i, $m := $acc.hostMounts -}}
{{- if or (not (hasPrefix "/" (toString $m.hostPath))) (not (hasPrefix "/" (toString $m.mountPath))) -}}{{- fail (printf "telemetry.accelerators.metrics.hostMounts[%d]: hostPath and mountPath must both be absolute paths" $i) -}}{{- end -}}
{{- end -}}
{{- range $i, $e := $acc.extraEnv -}}
{{- if has $e.name (list "DCGM_EXPORTER_LISTEN" "NODE_NAME") -}}{{- fail (printf "telemetry.accelerators.metrics.extraEnv: %s is set by this chart (the Service, the scrape job and the probes depend on it) and cannot be overridden" $e.name) -}}{{- end -}}
{{- if and (include "agent.telemetryAccApplyScope" $) (eq $e.name "DCGM_EXPORTER_KUBERNETES") -}}{{- fail "telemetry.accelerators.metrics.extraEnv: DCGM_EXPORTER_KUBERNETES is set by this chart when the accelerators follow the scope" -}}{{- end -}}
{{- if and (not (include "agent.telemetryAccApplyScope" $)) (eq $e.name "DCGM_EXPORTER_KUBERNETES") -}}{{- fail "telemetry.accelerators.metrics.extraEnv: DCGM_EXPORTER_KUBERNETES maps GPUs to pods through the kubelet socket, which this chart mounts only when the accelerators follow the scope (telemetry.accelerators.metrics.applyScope=true, or auto with a scope set): set that instead" -}}{{- end -}}
{{- end -}}
{{- end -}}
{{- if and .Values.telemetry.energy.metrics.enabled (eq .Values.telemetry.energy.metrics.source "existing") -}}
{{- $_ := include "agent.telemetryScrapeEndpoint" (dict "endpoint" .Values.telemetry.energy.metrics.existing.prometheusEndpoint "what" "telemetry.energy.metrics.existing.prometheusEndpoint") -}}
{{- end -}}
{{- if and .Values.telemetry.accelerators.metrics.enabled (eq .Values.telemetry.accelerators.metrics.source "existing") .Values.telemetry.accelerators.metrics.existing.prometheusEndpoint -}}
{{- $_ := include "agent.telemetryScrapeEndpoint" (dict "endpoint" .Values.telemetry.accelerators.metrics.existing.prometheusEndpoint "what" "telemetry.accelerators.metrics.existing.prometheusEndpoint") -}}
{{- end -}}
{{- if .Values.telemetry.scope.selector -}}
{{- fail "telemetry.scope.selector is not implemented: filtering already-received telemetry by an arbitrary Kubernetes label selector isn't something the collector can do cleanly after the fact. Use telemetry.scope.namespaces / telemetry.scope.exclude instead." -}}
{{- end -}}
{{- if and .Values.telemetry.opamp.enabled (not .Values.telemetry.opamp.server.endpoint) -}}
{{- fail "telemetry.opamp.enabled requires telemetry.opamp.server.endpoint" -}}
{{- end -}}
{{- if gt (len .Values.telemetry.resource.attributes) 10 -}}
{{- fail "telemetry.resource.attributes takes at most 10 tags: every one is stamped on every record, so each costs storage and cardinality downstream" -}}
{{- end -}}
{{- $seenTags := dict -}}
{{- range .Values.telemetry.resource.attributes -}}
{{- if hasPrefix "continuum." (lower .key) -}}
{{- fail (printf "telemetry.resource.attributes: %q uses the continuum. prefix, which is reserved for the provenance this chart stamps itself (org, cluster, intent, scope)" .key) -}}
{{- end -}}
{{- if hasKey $seenTags .key -}}
{{- fail (printf "telemetry.resource.attributes: %q is listed twice" .key) -}}
{{- end -}}
{{- $_ := set $seenTags .key true -}}
{{- end -}}
{{- if and .Values.telemetry.debug.verbosity (not (has .Values.telemetry.debug.verbosity (list "basic" "detailed"))) -}}
{{- fail "telemetry.debug.verbosity must be empty (off), basic or detailed" -}}
{{- end -}}
{{- if and .Values.telemetry.receiver.auth.enabled (not .Values.telemetry.receiver.auth.secretName) -}}
{{- fail "telemetry.receiver.auth.enabled requires telemetry.receiver.auth.secretName" -}}
{{- end -}}
{{- if and .Values.telemetry.receiver.tls.clientCAKey (not .Values.telemetry.receiver.tls.secretName) -}}
{{- fail "telemetry.receiver.tls.clientCAKey requires telemetry.receiver.tls.secretName (the Secret that holds the CA bundle)" -}}
{{- end -}}
{{- /* The scrape jobs are keyed by jobName: the collector refuses to start on two jobs of one name, which would take every
       other signal of the cluster collector down with it. A namespace name is a DNS label; an empty one would make the
       discovery watch every namespace. The selector goes to the API server as written, which answers a malformed one with an
       error the collector only logs, leaving the job without targets. */ -}}
{{- $seenJobs := dict -}}
{{- range $t := .Values.telemetry.applicationMetrics.metrics.scrapeTargets -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" (toString $t.namespace)) -}}
{{- fail (printf "telemetry.applicationMetrics.metrics.scrapeTargets: job %q has namespace %q, which is not a namespace name" (toString $t.jobName) (toString $t.namespace)) -}}
{{- end -}}
{{- if hasKey $seenJobs (toString $t.jobName) -}}
{{- fail (printf "telemetry.applicationMetrics.metrics.scrapeTargets: jobName %q is used twice; each job needs its own (it becomes service.name and the collector refuses to start on duplicates)" (toString $t.jobName)) -}}
{{- end -}}
{{- $_ := set $seenJobs (toString $t.jobName) true -}}
{{- if regexMatch "[^A-Za-z0-9_.,=!()/ -]" (toString (default "" $t.podLabelSelector)) -}}
{{- fail (printf "telemetry.applicationMetrics.metrics.scrapeTargets: job %q has podLabelSelector %q, which is not a Kubernetes label selector (letters, digits, - _ . / = ! , ( ) and spaces: \"app=web,tier in (a,b)\")" (toString $t.jobName) (toString $t.podLabelSelector)) -}}
{{- end -}}
{{- if regexMatch "[?#\\s\"'\\\\]" (toString (default "" $t.path)) -}}
{{- fail (printf "telemetry.applicationMetrics.metrics.scrapeTargets: the path of job %q may not contain a query string, a fragment, whitespace, quotes or backslashes" (toString $t.jobName)) -}}
{{- end -}}
{{- end -}}
{{- if and .Values.telemetry.export.otlp.tls.mtls.enabled (not .Values.telemetry.export.otlp.tls.mtls.secretName) -}}
{{- fail "telemetry.export.otlp.tls.mtls.enabled requires telemetry.export.otlp.tls.mtls.secretName (a Secret holding tls.crt, tls.key, and ca.crt)" -}}
{{- end -}}
{{- include "agent.telemetryExportValidate" . -}}
{{- end -}}

{{/* An "existing exporter" endpoint (telemetry.energy|accelerators.metrics.existing.prometheusEndpoint) as a Prometheus
     scrape target. People write it the way the UI asks for it - host:port, host:port/path or a full http(s):// URL -
     but a Prometheus static target is a bare host[:port] and a "/" anywhere in it makes the whole collector config
     invalid ("... is not a valid hostname"), which takes every cluster-collector signal down, not just this one. So
     it is split here into what the scrape job really takes: scheme (http unless https:// was given), the host[:port]
     target, and metrics_path (/metrics unless a path was given). Returns JSON: {scheme, target, path}. Takes a dict:
     endpoint (the value) and what (the values key, for the error). A query string or fragment is refused rather than
     silently dropped. */}}
{{- define "agent.telemetryScrapeEndpoint" -}}
{{- $raw := trim (toString .endpoint) -}}
{{- $scheme := "http" -}}
{{- if hasPrefix "https://" $raw -}}{{- $scheme = "https" -}}{{- $raw = trimPrefix "https://" $raw -}}
{{- else if hasPrefix "http://" $raw -}}{{- $raw = trimPrefix "http://" $raw -}}{{- end -}}
{{- $target := regexFind "^[^/]*" $raw -}}
{{- $path := default "/metrics" (trimPrefix $target $raw) -}}
{{- if not (regexMatch "^(\\[[0-9A-Fa-f:.]+\\]|[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?)(:[0-9]{1,5})?$" $target) -}}
{{- fail (printf "%s: %q is not a host[:port] with an optional /path (for example kepler.monitoring.svc:9102/metrics, or http(s)://host:port/path)" .what (toString .endpoint)) -}}
{{- end -}}
{{- if regexMatch "[?#\\s\"'\\\\]" $path -}}
{{- fail (printf "%s: the path of %q may not contain a query string, a fragment, whitespace, quotes or backslashes" .what (toString .endpoint)) -}}
{{- end -}}
{{- toJson (dict "scheme" $scheme "target" $target "path" $path) -}}
{{- end -}}

{{/* Turns a scope (namespaces to keep, namespaces to drop, workloads to keep inside a namespace) into the
     OTTL conditions a filterprocessor DROPS on, as a JSON list - callers `| fromJsonArray` it. Takes a dict:
       namespaces, exclude  lists of namespace names (Kubernetes namespace names are DNS-1123 labels, so a regex
                            alternation built from them needs no escaping)
       workloads            list of {namespace, names}. A namespace listed here keeps only those
                            workloads (matched against the deployment, statefulset, daemonset, job or cronjob
                            name k8sattributes put on the record); listed with no names keeps nothing of it.
                            Workload names are restricted to [a-z0-9-] by values.schema.json for the same
                            no-escaping reason.
       exempt               appended to every condition (networkLatency's standing exemption)
       guard                true for infrastructure signals: a record carrying no namespace at all (a node, the
                            host's own CPU) is never dropped, only one whose namespace is out of scope.
                            Application signals: with a namespaces allow-list a record with no namespace is dropped (it
                            is not in the list); with only exclude / workloads it passes, since nothing says it is in
                            the namespace being excluded or narrowed - which is also what happens to a pod
                            k8sattributes cannot identify (a hostNetwork pod, a record whose pod is not yet known).
       nsExprs              optional: where to read the namespace from. Defaults to the resource attribute.
       nsAttr               optional: where the workload half reads the namespace from (defaults to the resource attribute);
                            set it together with nsExprs when the namespace lives on the data point, not on the resource. */}}
{{- define "agent.telemetryScopeConditions" -}}
{{- $out := list -}}
{{- $exempt := .exempt | default "" -}}
{{- $nsExprs := .nsExprs | default (list "resource.attributes[\"k8s.namespace.name\"]") -}}
{{- range $nsExpr := $nsExprs -}}
{{- if $.namespaces -}}
{{- $m := printf "not IsMatch(%s, \"^(%s)$\")" $nsExpr (join "|" $.namespaces) -}}
{{- if $.guard }}{{ $m = printf "%s != nil and %s" $nsExpr $m }}{{ end -}}
{{- $out = append $out (printf "(%s)%s" $m $exempt) -}}
{{- end -}}
{{- if $.exclude -}}
{{- $out = append $out (printf "(IsMatch(%s, \"^(%s)$\"))%s" $nsExpr (join "|" $.exclude) $exempt) -}}
{{- end -}}
{{- end -}}
{{- $nsAttr := .nsAttr | default "resource.attributes[\"k8s.namespace.name\"]" -}}
{{- range $w := (.workloads | default list) -}}
{{- $ns := $w.namespace -}}
{{- $names := $w.names -}}
{{- if $names -}}
{{- $any := list -}}
{{- range $a := list "k8s.deployment.name" "k8s.statefulset.name" "k8s.daemonset.name" "k8s.job.name" "k8s.cronjob.name" -}}
{{- $any = append $any (printf "IsMatch(resource.attributes[\"%s\"], \"^(%s)$\")" $a (join "|" $names)) -}}
{{- end -}}
{{- $out = append $out (printf "(%s == \"%s\" and not (%s))%s" $nsAttr $ns (join " or " $any) $exempt) -}}
{{- else -}}
{{- $out = append $out (printf "(%s == \"%s\")%s" $nsAttr $ns $exempt) -}}
{{- end -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{/* Whether any scope in this release narrows by workload - the one case where k8sattributes has to extract
     the Job and CronJob names too (a few more attributes on every record, so only when something filters on them). */}}
{{- define "agent.telemetryWorkloadScopeEnabled" -}}
{{- $t := .Values.telemetry -}}
{{- if or $t.scope.workloads $t.scope.infra.workloads $t.applicationMetrics.metrics.scope.workloads $t.applicationLogs.logs.scope.workloads $t.traces.traces.scope.workloads -}}true{{- end -}}
{{- end -}}

{{/* The metadata list k8sattributes extracts, in both collector configs. The Deployment, StatefulSet and DaemonSet names are
     always there: the FUSION dashboards match an application's pods on them, and a pod metric carries no workload name of its own. */}}
{{- define "agent.telemetryK8sAttrsMetadata" -}}
{{- $m := list "k8s.namespace.name" "k8s.pod.name" "k8s.pod.uid" "k8s.node.name" "k8s.deployment.name" "k8s.statefulset.name" "k8s.daemonset.name" -}}
{{- if include "agent.telemetryWorkloadScopeEnabled" . }}{{ $m = concat $m (list "k8s.job.name" "k8s.cronjob.name") }}{{ end -}}
{{- toJson $m -}}
{{- end -}}

{{/* The scope infrastructure signals follow (telemetry.scope.infra): only what carries a namespace is
     narrowed; nodes and the host's own metrics pass untouched. */}}
{{- define "agent.telemetryInfraScopeConditions" -}}
{{- $i := .Values.telemetry.scope.infra -}}
{{- include "agent.telemetryScopeConditions" (dict "namespaces" $i.namespaces "exclude" $i.exclude "workloads" $i.workloads "guard" true) -}}
{{- end -}}

{{/* Per-workload resource requests/limits: the override if the operator set one, else the shared default.
     Sprig has no clean "is this map non-empty" beyond truthiness, which works fine here since an empty map
     ({}) and an unset field both render as Go's nil/zero value and are falsy in a template {{- if }}. */}}
{{- define "agent.telemetryHostCollectorResources" -}}
{{- if .Values.telemetry.hostCollector.resources -}}{{- toYaml .Values.telemetry.hostCollector.resources -}}
{{- else -}}{{- toYaml .Values.telemetry.resources -}}{{- end -}}
{{- end -}}
{{/* The cluster collector's resources: what is configured (clusterCollector.resources, else the shared default), with the
     memory limit - and the request, to half of it - raised to the floor for the cluster's size when that is higher.
     Measured with the real collector against a 500-node / 15,200-pod cluster: the 1Gi default sat at memory_limiter's refusal
     point (60% of the limit), so the collector threw data away in a loop while its pod looked healthy. The limit is only ever
     raised, never lowered, and telemetry.clusterCollector.autoSize.enabled=false pins exactly what is configured. A limit the
     chart cannot read as a quantity is left alone. */}}
{{- define "agent.telemetryClusterCollectorResources" -}}
{{- $r := deepCopy (ternary .Values.telemetry.clusterCollector.resources .Values.telemetry.resources (not (empty .Values.telemetry.clusterCollector.resources))) -}}
{{- $floorMi := int (include "agent.telemetryClusterMemoryFloorMi" .) -}}
{{- $limit := dig "limits" "memory" "" $r -}}
{{- if and $floorMi $limit -}}
{{- $have := include "agent.quantityBytes" $limit -}}
{{- if and $have (lt (float64 $have) (mulf (float64 $floorMi) 1048576.0)) -}}
{{- $_ := set (get $r "limits") "memory" (include "agent.miQuantity" $floorMi) -}}
{{- $req := dig "requests" "memory" "" $r -}}
{{- $reqBytes := include "agent.quantityBytes" $req -}}
{{- if or (not $reqBytes) (lt (float64 $reqBytes) (mulf (float64 $floorMi) 524288.0)) -}}
{{- if not (kindIs "map" (get $r "requests")) }}{{ $_ := set $r "requests" (dict) }}{{ end -}}
{{- $_ := set (get $r "requests") "memory" (include "agent.miQuantity" (div $floorMi 2)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- toYaml $r -}}
{{- end -}}

{{/* A whole number of mebibytes as a Kubernetes quantity: 2048 -> 2Gi, 1536 -> 1536Mi. */}}
{{- define "agent.miQuantity" -}}
{{- if eq (mod (int .) 1024) 0 -}}{{- printf "%dGi" (div (int .) 1024) -}}{{- else -}}{{- printf "%dMi" (int .) -}}{{- end -}}
{{- end -}}

{{/* Memory (Mi) the cluster collector needs at least, by the cluster's node count (~30 pods per node, every signal on, the
     k8sattributes caches shared): 0 up to 300 nodes (the configured default stands), then 2Gi, 3Gi, 5Gi, 8Gi. See
     telemetry.clusterCollector in values.yaml for the measurements. */}}
{{- define "agent.telemetryClusterMemoryFloorMi" -}}
{{- if .Values.telemetry.clusterCollector.autoSize.enabled -}}
{{- $n := int (include "agent.telemetryClusterNodeCount" .) -}}
{{- if gt $n 2000 -}}8192{{- else if gt $n 1000 -}}5120{{- else if gt $n 600 -}}3072{{- else if gt $n 300 -}}2048{{- else -}}0{{- end -}}
{{- else -}}0{{- end -}}
{{- end -}}

{{/* The number of nodes of the cluster: telemetry.clusterCollector.autoSize.nodes when set, else what the API server says
     while Helm installs or upgrades (`lookup`; empty - so 0 - under `helm template`, Argo CD and Flux, and for an account that
     may not list nodes). Looked up once per render. */}}
{{- define "agent.telemetryClusterNodeCount" -}}
{{- if not (hasKey .Values "agentClusterNodes") -}}
{{- $n := int .Values.telemetry.clusterCollector.autoSize.nodes -}}
{{- if and (le $n 0) .Values.telemetry.clusterCollector.autoSize.enabled -}}
{{- $n = len (dig "items" (list) (lookup "v1" "Node" "" "")) -}}
{{- end -}}
{{- $_ := set .Values "agentClusterNodes" $n -}}
{{- end -}}
{{- .Values.agentClusterNodes -}}
{{- end -}}

{{/* A Kubernetes quantity (a string such as "512Mi", "1Gi", "2G", or a bare number of bytes) as a whole number of bytes;
     empty when it is neither. */}}
{{- define "agent.quantityBytes" -}}
{{- $raw := . -}}
{{- if kindIs "string" $raw -}}
{{- if regexMatch "^[0-9]+(\\.[0-9]+)?(Ki|Mi|Gi|Ti|k|M|G|T)?$" $raw -}}
{{- $scale := dict "Ki" 1024.0 "Mi" 1048576.0 "Gi" 1073741824.0 "Ti" 1099511627776.0 "k" 1000.0 "M" 1000000.0 "G" 1000000000.0 "T" 1000000000000.0 -}}
{{- $suf := regexFind "(Ki|Mi|Gi|Ti|k|M|G|T)$" $raw -}}
{{- printf "%.0f" (mulf (float64 (regexFind "^[0-9]+(\\.[0-9]+)?" $raw)) (ternary (get $scale $suf) 1.0 (ne $suf ""))) -}}
{{- end -}}
{{- else if $raw -}}{{- printf "%.0f" (float64 $raw) -}}
{{- end -}}
{{- end -}}

{{/* true when the cluster collector runs one shared k8sattributes processor instead of one per pipeline
     (telemetry.clusterCollector.shareWatches), which needs the collector's feature gate, present from 0.150.0. */}}
{{- define "agent.telemetryShareWatches" -}}
{{- if .Values.telemetry.clusterCollector.shareWatches -}}
{{- $tag := toString .Values.telemetry.collectorImage.tag -}}
{{- if regexMatch "^v?0\\.[0-9]+\\.[0-9]+" $tag -}}
{{- if ge (int (regexReplaceAll "^v?0\\.([0-9]+)\\..*$" $tag "${1}")) 150 -}}true{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- define "agent.telemetryKeplerResources" -}}
{{- if .Values.telemetry.energy.metrics.resources -}}{{- toYaml .Values.telemetry.energy.metrics.resources -}}
{{- else -}}{{- toYaml .Values.telemetry.resources -}}{{- end -}}
{{- end -}}
{{- define "agent.telemetryAcceleratorsResources" -}}
{{- if .Values.telemetry.accelerators.metrics.resources -}}{{- toYaml .Values.telemetry.accelerators.metrics.resources -}}
{{- else -}}{{- toYaml .Values.telemetry.resources -}}{{- end -}}
{{- end -}}

{{/* Baseline collector hygiene processor: always rendered, first in every pipeline. Protects the
     collector itself from being OOM-killed under unexpected load - not a user on/off toggle, only its
     thresholds are configurable (telemetry.processors.memoryLimiter). Paired with agent.telemetryGomemlimit
     below, which computes a GOMEMLIMIT env var for the same containers so Go's own GC backs off in step
     with this. */}}
{{- define "agent.telemetryMemoryLimiterYAML" -}}
memory_limiter:
  check_interval: {{ .Values.telemetry.processors.memoryLimiter.checkInterval | quote }}
  limit_percentage: {{ .Values.telemetry.processors.memoryLimiter.limitPercentage }}
  spike_limit_percentage: {{ .Values.telemetry.processors.memoryLimiter.spikeLimitPercentage }}
{{- end -}}

{{/* Off by default (telemetry.processors.resourceDetection.enabled) - emits nothing when disabled. The
     pipeline processor lists in telemetry-host-config.yaml/telemetry-cluster-config.yaml only reference
     this by name when it's actually enabled, so a disabled call site never needs to guard this itself.
     timeout guards against an unreachable cloud metadata endpoint blocking the pipeline indefinitely - see
     the detectors list in values.yaml for why cloud detectors aren't in the default set. */}}
{{- define "agent.telemetryResourceDetectionYAML" -}}
{{- if .Values.telemetry.processors.resourceDetection.enabled }}
resourcedetection:
  detectors: {{ .Values.telemetry.processors.resourceDetection.detectors | toJson }}
  timeout: {{ .Values.telemetry.processors.resourceDetection.timeout | quote }}
  override: true
{{- end }}
{{- end -}}

{{/* On by default (telemetry.processors.redaction.enabled) - see the field's own comment in values.yaml
     for exactly what allow_all_keys: true + blocked_key_patterns does and does not do (masks matching
     values, never drops anything else). */}}
{{- define "agent.telemetryRedactionYAML" -}}
{{- if .Values.telemetry.processors.redaction.enabled }}
redaction:
  allow_all_keys: true
  blocked_key_patterns: {{ .Values.telemetry.processors.redaction.blockedKeyPatterns | toJson }}
  summary: info
{{- end }}
{{- end -}}

{{/* Ikhnos provenance: stamps this release's org/cluster/telemetry-grant identity onto every signal
     this agent emits, as plain resource attributes - continuum.org.id, continuum.cluster.id,
     continuum.intent.id. Unlike every other processor above, this one is never a user on/off toggle and
     is not meant to be hand-edited in values.yaml: telemetry.resource.* is filled in by the server's own
     generated `helm upgrade` command when a telemetry grant is created or changed, the same way
     access.tier and server.caPin are server-composed rather than user-edited. It must win over anything a
     user's own telemetry.processors.extraProcessors sets on these same keys, so the host/cluster config
     templates append "resource/continuum" to every pipeline's processor list after extraProcessorNames
     (and, for traces, extraTracesProcessorNames) and right before the final "batch" - action: upsert plus
     running last beats whatever an earlier, user-supplied processor set first. */}}
{{- define "agent.telemetryContinuumProvenanceYAML" -}}
resource/continuum:
  attributes:
    - {key: continuum.org.id, value: {{ .Values.telemetry.resource.orgId | quote }}, action: upsert}
    - {key: continuum.cluster.id, value: {{ .Values.telemetry.resource.clusterId | quote }}, action: upsert}
    {{- if .Values.telemetry.resource.intentId }}
    - {key: continuum.intent.id, value: {{ .Values.telemetry.resource.intentId | quote }}, action: upsert}
    {{- end }}
    {{- if .Values.telemetry.resource.scope }}
    {{- /* What this telemetry covers ("shop; payments: api+worker"), so a backend can tell narrowed from whole-cluster data. */}}
    - {key: continuum.scope, value: {{ .Values.telemetry.resource.scope | quote }}, action: upsert}
    {{- end }}
{{- end -}}

{{/* Tags the person chose to put on everything this release emits (telemetry.resource.attributes, a list of
     {key, value} - a list rather than a map so that setting it in a `helm upgrade --reuse-values` replaces the
     whole set, where a map would keep every key an earlier command put there), as plain resource attributes. Two rules keep them from ever misleading a reader: they are INSERTED, never
     upserted, so a key an application already set on its own telemetry keeps the application's value; and
     the continuum.* names belong to resource/continuum above - validation (agent.telemetryValidate) refuses
     a tag using that prefix, and resource/continuum runs after this anyway. Only rendered, and only listed in
     the pipelines, when at least one tag is set. */}}
{{- define "agent.telemetryTagsYAML" -}}
{{- if .Values.telemetry.resource.attributes }}
resource/tags:
  attributes:
    {{- range .Values.telemetry.resource.attributes }}
    - {key: {{ .key | quote }}, value: {{ .value | quote }}, action: insert}
    {{- end }}
{{- end }}
{{- end -}}

{{/* The processors every pipeline ends with, in order: the tags (when any), provenance, then batch. */}}
{{- define "agent.telemetryTailProcessors" -}}
{{- $tail := list -}}
{{- if .Values.telemetry.resource.attributes }}{{ $tail = append $tail "resource/tags" }}{{ end -}}
{{- $tail = concat $tail (list "resource/continuum" "batch") -}}
{{- toJson $tail -}}
{{- end -}}

{{/* Off unless telemetry.debug.verbosity is "basic" (a count of what passed through, per batch, in the
     collector's own log - no content) or "detailed" (every record's content in that log: it can include
     whatever the telemetry carries, so it is for short, deliberate troubleshooting). */}}
{{- define "agent.telemetryDebugExporterYAML" -}}
{{- if .Values.telemetry.debug.verbosity }}
debug:
  verbosity: {{ .Values.telemetry.debug.verbosity }}
{{- end }}
{{- end -}}

{{/* The exporters list a pipeline of signal type .m (with .root the chart context) names: that type's
     destination, plus the debug exporter when it is on. */}}
{{- define "agent.telemetryExporterList" -}}
{{- $l := list (include "agent.telemetryExporterFor" .) -}}
{{- if .root.Values.telemetry.debug.verbosity }}{{ $l = append $l "debug" }}{{ end -}}
{{- toJson $l -}}
{{- end -}}

{{/* Traces only, referenced only from the traces pipeline in telemetry-cluster-config.yaml. 100 (default)
     means no sampling; the pipeline only references this by name when the percentage is below 100, so a
     default install renders no probabilistic_sampler block or pipeline entry at all. */}}
{{- define "agent.telemetryProbabilisticSamplerYAML" -}}
{{- if lt (int .Values.telemetry.processors.tracesSampling.percentage) 100 }}
probabilistic_sampler:
  sampling_percentage: {{ .Values.telemetry.processors.tracesSampling.percentage }}
{{- end }}
{{- end -}}

{{/* Computes a GOMEMLIMIT value (~80% of a workload's resolved memory *limit*) from its resolved
     resources dict - pass the YAML text of agent.telemetryHostCollectorResources / ClusterCollectorResources
     / KeplerResources through `fromYaml` first, e.g.
     {{ include "agent.telemetryGomemlimit" (include "agent.telemetryHostCollectorResources" . | fromYaml) }}.
     Returns empty when there's no limits.memory to read, or it isn't in a Kubernetes quantity format this
     recognizes - GOMEMLIMIT is a Go-runtime nicety paired with memory_limiter above, not something worth
     failing a release over. Kubernetes' binary suffixes (Ki/Mi/Gi/Ti) map directly to GOMEMLIMIT's own IEC
     suffixes (KiB/MiB/GiB/TiB, same magnitude); Kubernetes' decimal SI suffixes (k/M/G/T) and a bare
     number both convert to a plain byte count instead, since GOMEMLIMIT itself has no decimal-suffix
     form. */}}
{{- define "agent.telemetryGomemlimit" -}}
{{- $raw := "" -}}
{{- if and .limits .limits.memory -}}{{- $raw = .limits.memory -}}{{- end -}}
{{- if $raw -}}
{{- if kindIs "string" $raw -}}
{{/* A Kubernetes quantity with a unit suffix always arrives as a YAML/JSON string (the suffix letters
     make it non-numeric) - e.g. "256Mi", "500M". Parsed here rather than trusted as pre-normalized. */}}
{{- if regexMatch "^[0-9]+(\\.[0-9]+)?(Ki|Mi|Gi|Ti|k|M|G|T)?$" $raw -}}
{{- $n := regexFind "^[0-9]+(\\.[0-9]+)?" $raw -}}
{{- $suf := regexFind "(Ki|Mi|Gi|Ti|k|M|G|T)$" $raw -}}
{{- $scale := dict "Ki" 1024.0 "Mi" 1048576.0 "Gi" 1073741824.0 "Ti" 1099511627776.0 "k" 1000.0 "M" 1000000.0 "G" 1000000000.0 "T" 1000000000000.0 -}}
{{- $factor := 1.0 -}}
{{- if $suf -}}{{- $factor = get $scale $suf -}}{{- end -}}
{{/* Always emitted as a bare byte count, never with the original unit suffix rescaled: GOMEMLIMIT
     supports a bare number of bytes directly, and re-expressing "80% of 1Gi" in Gi/Ti loses precision at
     small integer magnitudes (0.8 rounds right back to 1) - bytes has none of that problem at any size. */}}
{{- printf "%.0f" (mulf (mulf (float64 $n) $factor) 0.8) -}}
{{- end -}}
{{- else -}}
{{/* A bare byte count with no suffix at all (e.g. `memory: 1000000` written directly in a values file)
     decodes as an actual YAML/JSON number, not a string - use it directly rather than routing it through
     the string/regex path above, which a Go %v-formatted float (e.g. "1e+06") would silently fail to
     match. */}}
{{- printf "%.0f" (mulf (float64 $raw) 0.8) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* The bearertokenauth extension (server-side use - confighttp/configgrpc's per-protocol "auth:
     authenticator: bearertokenauth" field references this by name), and the one extra env entry its token
     needs, only when telemetry.receiver.auth.enabled. Both no-ops otherwise, so callers can always include
     them unconditionally. Cluster collector only - see telemetry-cluster-config.yaml. */}}
{{- define "agent.telemetryReceiverAuthExtensionYAML" -}}
{{- if .Values.telemetry.receiver.auth.enabled }}
bearertokenauth:
  token: "${env:CONTINUUM_TELEMETRY_RECEIVER_AUTH}"
{{- end }}
{{- end -}}
{{/* The "tls" block of both OTLP receiver protocols (telemetry.receiver.tls): the Secret is mounted at /receiver-tls. The
     certificate is re-read every hour, so a renewal needs no restart. A CA key makes every client present a certificate. */}}
{{- define "agent.telemetryReceiverTLSYAML" -}}
cert_file: /receiver-tls/tls.crt
key_file: /receiver-tls/tls.key
reload_interval: 1h
{{- with .Values.telemetry.receiver.tls.clientCAKey }}
client_ca_file: /receiver-tls/{{ . }}
{{- end }}
{{- end -}}
{{- define "agent.telemetryReceiverTLSMounts" -}}
{{- if .Values.telemetry.receiver.tls.secretName -}}
- {name: receiver-tls, mountPath: /receiver-tls, readOnly: true}
{{- end -}}
{{- end -}}
{{- define "agent.telemetryReceiverTLSVolumes" -}}
{{- if .Values.telemetry.receiver.tls.secretName -}}
- name: receiver-tls
  secret: {secretName: {{ .Values.telemetry.receiver.tls.secretName | quote }}}
{{- end -}}
{{- end -}}

{{- define "agent.telemetryReceiverAuthEnv" -}}
{{- if .Values.telemetry.receiver.auth.enabled }}
- name: CONTINUUM_TELEMETRY_RECEIVER_AUTH
  valueFrom:
    secretKeyRef:
      name: {{ .Values.telemetry.receiver.auth.secretName }}
      key: {{ .Values.telemetry.receiver.auth.secretKey }}
{{- end }}
{{- end -}}

{{/* Comma-separated list of enabled telemetry signal names, for the agent container's own
     CONTINUUM_TELEMETRY_SIGNALS env var (see deployment.yaml): the agent self-reports this in its
     Diagnostics, the same way it already self-reports its installed RBAC tier, so the UI can show what
     telemetry is actually installed without needing a live control-plane channel for it. Purely
     informational - the agent never interprets or enforces these names itself. */}}
{{- define "agent.telemetrySignalNames" -}}
{{- include "agent.telemetryDefaults" . -}}
{{- $t := .Values.telemetry -}}
{{- $n := list -}}
{{- if $t.resourceUsage.metrics.enabled }}{{ $n = append $n "resourceUsage" }}{{ end -}}
{{- if $t.energy.metrics.enabled }}{{ $n = append $n "energy" }}{{ end -}}
{{- if $t.accelerators.metrics.enabled }}{{ $n = append $n "accelerators" }}{{ end -}}
{{- if $t.kubernetesState.metrics.enabled }}{{ $n = append $n "kubernetesState" }}{{ end -}}
{{- if $t.nodeRuntime.metrics.enabled }}{{ $n = append $n "nodeRuntime" }}{{ end -}}
{{- if $t.networkLatency.metrics.enabled }}{{ $n = append $n "networkLatency" }}{{ end -}}
{{- if $t.applicationMetrics.metrics.enabled }}{{ $n = append $n "applicationMetrics" }}{{ end -}}
{{- if $t.systemLogs.logs.enabled }}{{ $n = append $n "systemLogs" }}{{ end -}}
{{- if $t.kubernetesEvents.logs.enabled }}{{ $n = append $n "kubernetesEvents" }}{{ end -}}
{{- if $t.applicationLogs.logs.enabled }}{{ $n = append $n "applicationLogs" }}{{ end -}}
{{- if $t.traces.traces.enabled }}{{ $n = append $n "traces" }}{{ end -}}
{{- join "," $n -}}
{{- end -}}

{{/* The effective-configuration counterparts to agent.telemetrySignalNames above, for the agent container's
     own CONTINUUM_TELEMETRY_* env vars (see deployment.yaml): not just which signals are on, but how each is
     actually configured - the destination, the processor settings telemetry-intent.md names as mattering
     most for "safe by default" (redaction, resourcedetection, traces sampling), and which source backs
     energy/accelerators when either is on. Same self-report mechanism installed_tier already uses, extended
     from a flat ceiling to a small config shape - see Diagnostics.installed_telemetry_config's own doc
     comment in agent.proto. Every one of these is only ever read, during `helm template`/`helm upgrade`, by
     deployment.yaml below gating the whole block on agent.telemetryEnabled, so none of them needs its own
     "is telemetry even on" guard. */}}
{{- define "agent.telemetryEffectiveDestination" -}}
{{- $root := . -}}
{{- $parts := list -}}
{{- $routed := false -}}
{{- range (include "agent.telemetryModalities" (dict "root" . "scope" "all") | fromJsonArray) -}}
{{- if include "agent.telemetryHasRoute" (dict "root" $root "m" .) -}}{{- $routed = true -}}{{- $parts = append $parts (printf "%s=%s" . (get $root.Values.telemetry.export.routes .).endpoint) -}}{{- end -}}
{{- end -}}
{{- if not $routed -}}{{- .Values.telemetry.export.otlp.endpoint -}}
{{- else -}}
{{- if include "agent.telemetryDefaultUsed" (dict "root" . "scope" "all") -}}{{- $parts = append $parts (printf "default=%s" .Values.telemetry.export.otlp.endpoint) -}}{{- end -}}
{{- join "," $parts -}}
{{- end -}}
{{- end -}}
{{- define "agent.telemetryEffectiveRedactionEnabled" -}}{{- .Values.telemetry.processors.redaction.enabled -}}{{- end -}}
{{- define "agent.telemetryEffectiveResourceDetectionEnabled" -}}{{- .Values.telemetry.processors.resourceDetection.enabled -}}{{- end -}}

{{/* The OpAMP extension block (ALPHA status upstream), and its entry in service.extensions - both emit
     nothing when telemetry.opamp.enabled is false, so callers can always include them unconditionally.
     Report-only: reports_effective_config/reports_health/reports_available_components default to true in
     the extension itself and are left at their defaults here rather than restated. */}}
{{- define "agent.telemetryOpampExtensionYAML" -}}
{{- if .Values.telemetry.opamp.enabled -}}
opamp:
  server:
    ws:
      endpoint: {{ .Values.telemetry.opamp.server.endpoint | quote }}
      {{- if or .Values.telemetry.opamp.server.tls.insecure .Values.telemetry.opamp.server.tls.caFile }}
      tls:
        insecure: {{ .Values.telemetry.opamp.server.tls.insecure }}
        {{- if .Values.telemetry.opamp.server.tls.caFile }}
        ca_file: {{ .Values.telemetry.opamp.server.tls.caFile | quote }}
        {{- end }}
      {{- end }}
      {{- if .Values.telemetry.opamp.server.headers }}
      headers:
        {{- range $k, $v := .Values.telemetry.opamp.server.headers }}
        {{ $k }}: {{ $v | quote }}
        {{- end }}
      {{- end }}
{{- end -}}
{{- end -}}

{{- define "agent.telemetryName" -}}continuum-telemetry{{- end -}}

{{/* Which telemetry workloads mount host paths, as a JSON list of names, for NOTES.txt: the host collector when
     resourceUsage or systemLogs is on (nodeRuntime alone reads the kubelet over the network and mounts nothing from the
     host), the bundled Kepler and dcgm-exporter. */}}
{{- define "agent.telemetryHostAccessWho" -}}
{{- $l := list -}}
{{- if or .Values.telemetry.resourceUsage.metrics.enabled .Values.telemetry.systemLogs.logs.enabled }}{{ $l = append $l "the host collector" }}{{ end -}}
{{- if and .Values.telemetry.energy.metrics.enabled (eq .Values.telemetry.energy.metrics.source "bundle-kepler") }}{{ $l = append $l "Kepler" }}{{ end -}}
{{- if and .Values.telemetry.accelerators.metrics.enabled (eq .Values.telemetry.accelerators.metrics.source "bundle-dcgm") }}{{ $l = append $l "dcgm-exporter" }}{{ end -}}
{{- toJson $l -}}
{{- end -}}

{{/* What the kubelet asks of a collector pod. The health_check extension answers 200 only once the collector has
     started every pipeline (receivers listening, exporters built) and 503 before that and while it shuts down, so a
     pod whose collector is not serving is not Ready (and an OTLP client is not sent to it through the Service), where
     before the pod was Ready the moment its container started. It listens on every address of the pod's own network
     namespace (the kubelet probes the pod IP), never on the node: no pod here uses hostNetwork. The port is
     fixed; agent.telemetryValidate refuses telemetry.health.port when it would collide with it or with OTLP. */}}
{{- define "agent.telemetryProbePort" -}}13133{{- end -}}
{{- define "agent.telemetryHealthCheckExtensionYAML" -}}
health_check:
  endpoint: ":{{ include "agent.telemetryProbePort" . }}"
{{- end -}}
{{- /* startupProbe holds the other two off for up to 5 minutes (the cluster collector waits for its informers to sync, which
       takes longest on a large cluster) so a slow start is not a restart loop; after that a collector that stops answering for
       a minute is restarted, and one that is not ready is taken out of the Service. */ -}}
{{- define "agent.telemetryProbesYAML" -}}
startupProbe:
  httpGet: {path: /, port: health}
  periodSeconds: 5
  timeoutSeconds: 3
  failureThreshold: 60
readinessProbe:
  httpGet: {path: /, port: health}
  periodSeconds: 10
  timeoutSeconds: 3
  failureThreshold: 3
livenessProbe:
  httpGet: {path: /, port: health}
  periodSeconds: 20
  timeoutSeconds: 5
  failureThreshold: 3
{{- end -}}

{{/* Which signal types have at least one signal turned on, as a JSON list of "metrics", "logs", "traces",
     for one collector: .scope is "host" (the per-node DaemonSet: resource usage, node runtime, system logs),
     "cluster" (the Deployment: everything else) or "all". Routes, exporters, credentials and certificates
     are only ever rendered for the signal types a collector actually carries - the host collector, running
     on every node, never gets the credential of a route only the cluster collector uses. */}}
{{- define "agent.telemetryModalities" -}}
{{- $t := .root.Values.telemetry -}}
{{- $host := or (eq .scope "host") (eq .scope "all") -}}
{{- $cluster := or (eq .scope "cluster") (eq .scope "all") -}}
{{- $l := list -}}
{{- if or (and $host (or $t.resourceUsage.metrics.enabled $t.nodeRuntime.metrics.enabled)) (and $cluster (or $t.energy.metrics.enabled $t.accelerators.metrics.enabled $t.kubernetesState.metrics.enabled $t.applicationMetrics.metrics.enabled $t.networkLatency.metrics.enabled)) -}}{{- $l = append $l "metrics" -}}{{- end -}}
{{- if or (and $host $t.systemLogs.logs.enabled) (and $cluster (or $t.kubernetesEvents.logs.enabled $t.applicationLogs.logs.enabled)) -}}{{- $l = append $l "logs" -}}{{- end -}}
{{- if and $cluster $t.traces.traces.enabled -}}{{- $l = append $l "traces" -}}{{- end -}}
{{- toJson $l -}}
{{- end -}}

{{/* Whether signal type .m (with .root the chart context) has a route of its own. */}}
{{- define "agent.telemetryHasRoute" -}}
{{- $r := get .root.Values.telemetry.export.routes .m -}}
{{- if and (kindIs "map" $r) $r.endpoint -}}true{{- end -}}
{{- end -}}

{{/* Whether the default destination (telemetry.export.otlp) is used by any enabled signal type of a
     collector (.root the chart context, .scope as above). */}}
{{- define "agent.telemetryDefaultUsed" -}}
{{- $root := .root -}}
{{- range (include "agent.telemetryModalities" . | fromJsonArray) -}}
{{- if not (include "agent.telemetryHasRoute" (dict "root" $root "m" .)) -}}true{{- end -}}
{{- end -}}
{{- end -}}

{{/* Export health (telemetry.health): whether the collectors serve their own export counters for the agent
     to read. True only while telemetry itself is on. */}}
{{- define "agent.telemetryHealthEnabled" -}}
{{- include "agent.telemetryDefaults" . -}}
{{- if and .Values.telemetry.health.enabled (include "agent.telemetryEnabled" .) -}}true{{- end -}}
{{- end -}}

{{/* The collector's own metrics, as a service.telemetry stanza (emits at column 0; the caller nindents it):
     a pull reader serving Prometheus text on the pod's own address - not 0.0.0.0, and not the default
     localhost the agent could not reach. POD_IP is the container's downward-API env var. */}}
{{- define "agent.telemetryHealthReaderYAML" -}}
telemetry:
  metrics:
    readers:
      - pull:
          exporter:
            prometheus:
              host: ${env:POD_IP}
              port: {{ .Values.telemetry.health.port }}
{{- end -}}

{{/* The names the agent resolves to find every collector pod (a headless Service each), comma separated,
     for CONTINUUM_TELEMETRY_HEALTH_TARGETS. Only collectors this install deploys. */}}
{{- define "agent.telemetryHealthTargets" -}}
{{- $t := list -}}
{{- if include "agent.telemetryHostEnabled" . -}}{{- $t = append $t (printf "%s-host-metrics.%s.svc:%d" (include "agent.telemetryName" .) .Release.Namespace (.Values.telemetry.health.port | int)) -}}{{- end -}}
{{- if include "agent.telemetryClusterEnabled" . -}}{{- $t = append $t (printf "%s-cluster-metrics.%s.svc:%d" (include "agent.telemetryName" .) .Release.Namespace (.Values.telemetry.health.port | int)) -}}{{- end -}}
{{- join "," $t -}}
{{- end -}}

{{/* "otlp" (configgrpc), "otlphttp" (confighttp) or "zipkin" for a protocol value. */}}
{{- define "agent.telemetryExporterType" -}}
{{- if eq . "http" -}}otlphttp{{- else if eq . "zipkin" -}}zipkin{{- else -}}otlp{{- end -}}
{{- end -}}

{{/* The default destination's exporter name - whichever telemetry.export.otlp.protocol asks for. Every
     exporters:/pipelines: reference uses the name from here or from agent.telemetryExporterFor instead of a
     literal "otlp", so the two stay in sync - see the bug this fixed: protocol=http rendered an httpOnly
     destination (Grafana Cloud, Datadog) under the gRPC-only exporter, which those backends simply do not
     speak. */}}
{{- define "agent.telemetryExporterName" -}}
{{- include "agent.telemetryExporterType" .Values.telemetry.export.otlp.protocol -}}
{{- end -}}

{{/* The exporter signal type .m (with .root the chart context) is sent through: its own route's, named
     "<type>/<signal>" (otlphttp/metrics), or the default destination's. */}}
{{- define "agent.telemetryExporterFor" -}}
{{- if include "agent.telemetryHasRoute" . -}}
{{- $r := get .root.Values.telemetry.export.routes .m -}}
{{- printf "%s/%s" (include "agent.telemetryExporterType" $r.protocol) .m -}}
{{- else -}}
{{- include "agent.telemetryExporterName" .root -}}
{{- end -}}
{{- end -}}

{{/* The URL the zipkin exporter posts spans to, from a destination (.endpoint, .tls.insecure).
     telemetry.export.otlp.endpoint is host:port everywhere else; here a full URL is accepted too, and a bare
     host[:port] gets the scheme .tls.insecure picks and Zipkin's own v2 path, which is where every Zipkin
     server (and the receivers that speak its API) listens. A URL that already has a path is used exactly as
     written. */}}
{{- define "agent.telemetryZipkinEndpoint" -}}
{{- $e := .endpoint -}}
{{- if not (regexMatch "^https?://" $e) -}}{{- $e = printf "%s://%s" (ternary "http" "https" .tls.insecure) $e -}}{{- end -}}
{{- if regexMatch "^https?://[^/]+/?$" $e -}}{{- $e = printf "%s/api/v2/spans" (trimSuffix "/" $e) -}}{{- end -}}
{{- $e -}}
{{- end -}}

{{/* The trust and identity lines of an exporter's tls block, at column 0, empty when it has none: the client
     certificate (and the CA that verifies the destination, from the same Secret) when ...tls.mtls is on; otherwise
     the CA bundle of caSecretName, mounted at .ca; otherwise the caFile path as written (nothing mounts it). */}}
{{- define "agent.telemetryExporterTrust" -}}
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

{{/* One exporter block for a destination: .key is its name in the collector config, .c the destination
     (the telemetry.export.otlp shape), .env the variable its credential header is read from, .mtls and .ca the
     directories its client certificate and CA bundle Secrets are mounted at, .root the chart context. Emits at
     column 0. The auth header's value is never written here - only a reference to the environment variable the
     container injects it into from a Secret at start (see agent.telemetryExporterEnv). A client certificate
     carries reload_interval: 1h, so a renewed certificate in the mounted Secret is picked up without a restart
     (the CA in ca.crt is not re-read; see telemetry.rolloutOnSecretChange in values.yaml). An http endpoint takes
     the scheme tls.insecure picks unless it already carries one. */}}
{{- define "agent.telemetryExporterBlock" -}}
{{- $c := .c -}}
{{- $trust := include "agent.telemetryExporterTrust" . -}}
{{ .key }}:
{{- if eq $c.protocol "zipkin" }}
  endpoint: {{ include "agent.telemetryZipkinEndpoint" $c | quote }}
  format: json
{{- else if eq $c.protocol "http" }}
  {{- $e := $c.endpoint }}
  {{- if not (regexMatch "^https?://" $e) }}{{ $e = printf "%s://%s" (ternary "http" "https" $c.tls.insecure) $e }}{{ end }}
  {{- if and .m (or $c.fullUrl (regexMatch (printf "/v1/%s/?$" .m) $e)) }}
  {{- /* A route whose URL is already the whole address of its signal: posted to as written, the collector appends nothing. */}}
  {{ .m }}_endpoint: {{ $e | quote }}
  {{- else }}
  endpoint: {{ $e | quote }}
  {{- end }}
{{- else }}
  endpoint: {{ $c.endpoint | quote }}
{{- end }}
{{- with .root.Values.telemetry.export.timeout }}
  timeout: {{ . | quote }}
{{- end }}
{{- if and (eq $c.protocol "grpc") .root.Values.telemetry.export.keepalive.time }}
  keepalive:
    time: {{ .root.Values.telemetry.export.keepalive.time | quote }}
    timeout: {{ .root.Values.telemetry.export.keepalive.timeout | quote }}
    permit_without_stream: true
{{- end }}
{{- if eq $c.protocol "grpc" }}
  tls:
    insecure: {{ $c.tls.insecure }}
    {{- if $trust }}
    {{- $trust | nindent 4 }}
    {{- end }}
    {{- if $c.tls.serverName }}
    server_name_override: {{ $c.tls.serverName | quote }}
    {{- end }}
{{- else if or $trust $c.tls.serverName }}
  {{/* The http and zipkin exporters have no insecure toggle: the endpoint's scheme IS that choice, and the
       collector appends /v1/<signal> to an otlphttp endpoint itself. */}}
  tls:
    {{- if $trust }}
    {{- $trust | nindent 4 }}
    {{- end }}
    {{- if $c.tls.serverName }}
    server_name_override: {{ $c.tls.serverName | quote }}
    {{- end }}
{{- end }}
{{- if $c.auth.secretName }}
  headers:
    {{ $c.auth.headerName }}: {{ printf "${env:%s}" .env | quote }}
{{- end }}
{{- include "agent.telemetryExporterResilienceYAML" .root | nindent 2 }}
{{- end -}}

{{/* What every exporter does while its destination is unreachable, spelled out instead of left to the collector's
     defaults (which retry for 5 minutes and then drop): retry for telemetry.export.queue.retryMaxElapsedTime and
     hold at most telemetry.export.queue.size requests meanwhile.
     block_on_overflow is what makes a full queue push back. Without it (the collector's default) a full queue REJECTS
     the batch and the batch processor in front of the exporter only logs "sending queue is full": the receiver has
     already answered its sender 200, so the data is lost and nobody upstream knows. With it, the batch processor
     waits for room and the receivers wait with it (an application's own SDK, a file being read, a scrape, keeps what
     it could not hand over), instead of the collector discarding it.
     sending_queue.batch cuts a request into pieces of at most telemetry.export.queue.maxRequestBytes serialized bytes
     (min_size 1: nothing is held back to fill a batch, that is the batch processor's job). processors.batch caps a
     batch in ITEMS, and 4096 log records of 2 KiB are 8 MiB: past the 4 MiB the next hop accepts, so the whole batch
     was refused with a permanent error and dropped. 0 turns the cut off. With
     telemetry.export.queue.persistent.enabled the queue is also written to an emptyDir (file_storage/queue) and
     survives a container restart. Emits at column 0; callers nindent it. */}}
{{- define "agent.telemetryExporterResilienceYAML" -}}
{{- $q := .Values.telemetry.export.queue -}}
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

{{/* The file_storage extension the exporter queues share when telemetry.export.queue.persistent.enabled: the
     root filesystem is read-only, so it lives on the emptyDir telemetry.yaml mounts at /queue. */}}
{{- define "agent.telemetryQueueExtensionYAML" -}}
{{- if .Values.telemetry.export.queue.persistent.enabled }}
file_storage/queue:
  directory: /queue
{{- end }}
{{- end -}}

{{/* The service.extensions list of one collector: health_check (always: it is what the pod's probes ask), then opamp
     and the queue's file_storage, whichever are on, as a JSON list. The cluster collector adds bearertokenauth to it
     itself. */}}
{{- define "agent.telemetryExtensionNames" -}}
{{- $l := list "health_check" -}}
{{- if .Values.telemetry.opamp.enabled }}{{ $l = append $l "opamp" }}{{ end -}}
{{- if .Values.telemetry.export.queue.persistent.enabled }}{{ $l = append $l "file_storage/queue" }}{{ end -}}
{{- toJson $l -}}
{{- end -}}

{{/* Explicit batch sizes, the last processor of every pipeline. The collector's default leaves a batch's maximum
     unbounded, and a batch past the next hop's receive limit (4 MiB on a collector's OTLP/gRPC receiver) is
     rejected whole and retried until it is dropped. */}}
{{- define "agent.telemetryBatchYAML" -}}
batch:
  timeout: {{ .Values.telemetry.processors.batch.timeout | quote }}
  send_batch_size: {{ .Values.telemetry.processors.batch.sendBatchSize }}
  send_batch_max_size: {{ .Values.telemetry.processors.batch.sendBatchMaxSize }}
{{- end -}}

{{/* The emptyDir behind the persistent sending queue (the root filesystem is read-only). It survives a container
     restart but not a rescheduled pod, and counts against the node's ephemeral storage up to sizeLimit. */}}
{{- define "agent.telemetryQueueVolume" -}}
- name: queue
  emptyDir: {sizeLimit: {{ .Values.telemetry.export.queue.persistent.sizeLimit | quote }}}
{{- end -}}

{{/* The Secrets one collector mounts for a client certificate or a CA bundle (.scope is "host" or "cluster"), as a JSON list - on
     the cluster collector also the OTLP receiver's own server certificate. */}}
{{- define "agent.telemetryMtlsSecretNames" -}}
{{- $root := .root -}}
{{- $l := list -}}
{{- if and (include "agent.telemetryDefaultUsed" .) $root.Values.telemetry.export.otlp.tls.mtls.enabled }}{{ $l = append $l $root.Values.telemetry.export.otlp.tls.mtls.secretName }}{{ end -}}
{{- range (include "agent.telemetryModalities" . | fromJsonArray) -}}
{{- if include "agent.telemetryHasRoute" (dict "root" $root "m" .) -}}
{{- $r := get $root.Values.telemetry.export.routes . -}}
{{- if $r.tls.mtls.enabled }}{{ $l = append $l $r.tls.mtls.secretName }}{{ else if $r.tls.caSecretName }}{{ $l = append $l $r.tls.caSecretName }}{{ end -}}
{{- end -}}
{{- end -}}
{{- $d := $root.Values.telemetry.export.otlp.tls -}}
{{- if and (include "agent.telemetryDefaultUsed" .) $d.caSecretName (not $d.mtls.enabled) }}{{ $l = append $l $d.caSecretName }}{{ end -}}
{{- if and (eq .scope "cluster") $root.Values.telemetry.receiver.tls.secretName }}{{ $l = append $l $root.Values.telemetry.receiver.tls.secretName }}{{ end -}}
{{- toJson (uniq $l) -}}
{{- end -}}

{{/* A digest of what those Secrets hold right now, for a pod annotation: renewing a client certificate and running
     the install command again then restarts the collector instead of leaving it on the old certificate until
     reload_interval comes round. Read with `lookup`, which is empty under `helm template` (so also under Argo CD and
     Flux) and for a Secret that does not exist; the annotation is then left out and nothing can fail the render.
     An account that may not `get` Secrets in this namespace makes Helm itself fail the lookup: turn
     telemetry.rolloutOnSecretChange off there. */}}
{{- define "agent.telemetryMtlsChecksum" -}}
{{- if .root.Values.telemetry.rolloutOnSecretChange -}}
{{- $ns := .root.Release.Namespace -}}
{{- $parts := list -}}
{{- range (include "agent.telemetryMtlsSecretNames" . | fromJsonArray) -}}
{{- $s := lookup "v1" "Secret" $ns . -}}
{{- if and $s $s.data }}{{ $parts = append $parts (printf "%s=%s" . (toJson $s.data | sha256sum)) }}{{ end -}}
{{- end -}}
{{- if $parts }}{{ join "," $parts | sha256sum }}{{ end -}}
{{- end -}}
{{- end -}}

{{/* The Secrets one collector reads into an environment variable (.scope as above): the credential header of the default
     destination and of each route it uses, and - cluster collector only - the receiver's bearer token. A container reads
     its environment once, at start, so changing the value inside one of these Secrets does nothing until the pod restarts. */}}
{{- define "agent.telemetryAuthSecretNames" -}}
{{- $root := .root -}}
{{- $l := list -}}
{{- if and (include "agent.telemetryDefaultUsed" .) $root.Values.telemetry.export.otlp.auth.secretName }}{{ $l = append $l $root.Values.telemetry.export.otlp.auth.secretName }}{{ end -}}
{{- range (include "agent.telemetryModalities" . | fromJsonArray) -}}
{{- if include "agent.telemetryHasRoute" (dict "root" $root "m" .) -}}
{{- $a := (get $root.Values.telemetry.export.routes .).auth -}}
{{- if $a.secretName }}{{ $l = append $l $a.secretName }}{{ end -}}
{{- end -}}
{{- end -}}
{{- if and (eq .scope "cluster") $root.Values.telemetry.receiver.auth.enabled }}{{ $l = append $l $root.Values.telemetry.receiver.auth.secretName }}{{ end -}}
{{- if $root.Values.telemetry.export.proxy.secretName }}{{ $l = append $l $root.Values.telemetry.export.proxy.secretName }}{{ end -}}
{{- toJson (uniq $l) -}}
{{- end -}}

{{/* A digest of what those Secrets hold right now, for a pod annotation (checksum/auth): rotating a credential and running
     the install command again then restarts the collector, which is the only way it reads the new value. Same rules as
     agent.telemetryMtlsChecksum: empty under `helm template` (Argo CD, Flux), for a Secret that does not exist, and when
     telemetry.rolloutOnSecretChange is off. */}}
{{- define "agent.telemetryAuthChecksum" -}}
{{- if .root.Values.telemetry.rolloutOnSecretChange -}}
{{- $ns := .root.Release.Namespace -}}
{{- $parts := list -}}
{{- range (include "agent.telemetryAuthSecretNames" . | fromJsonArray) -}}
{{- $s := lookup "v1" "Secret" $ns . -}}
{{- if and $s $s.data }}{{ $parts = append $parts (printf "%s=%s" . (toJson $s.data | sha256sum)) }}{{ end -}}
{{- end -}}
{{- if $parts }}{{ join "," $parts | sha256sum }}{{ end -}}
{{- end -}}
{{- end -}}

{{/* The "exporters" stanza shared by both collector ConfigMaps: the default destination when any enabled
     signal type uses it, then one exporter per route of an enabled signal type. Emits at column 0; the caller
     nindents it into place. */}}
{{- define "agent.telemetryExporterYAML" -}}
{{- $root := .root -}}
{{- $blocks := list -}}
{{- if include "agent.telemetryDefaultUsed" . -}}
{{- $blocks = append $blocks (include "agent.telemetryExporterBlock" (dict "root" $root "key" (include "agent.telemetryExporterName" $root) "c" $root.Values.telemetry.export.otlp "env" "CONTINUUM_TELEMETRY_AUTH" "mtls" "/export-mtls" "ca" "/export-ca")) -}}
{{- end -}}
{{- range (include "agent.telemetryModalities" . | fromJsonArray) -}}
{{- if include "agent.telemetryHasRoute" (dict "root" $root "m" .) -}}
{{- $blocks = append $blocks (include "agent.telemetryExporterBlock" (dict "root" $root "key" (include "agent.telemetryExporterFor" (dict "root" $root "m" .)) "c" (get $root.Values.telemetry.export.routes .) "m" . "env" (printf "CONTINUUM_TELEMETRY_AUTH_%s" (upper .)) "mtls" (printf "/export-mtls-%s" .) "ca" (printf "/export-ca-%s" .))) -}}
{{- end -}}
{{- end -}}
{{- join "\n" $blocks -}}
{{- end -}}

{{/* The extra env entries a telemetry collector container needs beyond NODE_NAME: one per destination in use
     that has an auth header configured - CONTINUUM_TELEMETRY_AUTH for the default, and
     CONTINUUM_TELEMETRY_AUTH_<SIGNAL> for each route. A no-op (empty) when none has one, so callers can always
     include it unconditionally. */}}
{{- define "agent.telemetryExporterEnv" -}}
{{- $root := .root -}}
{{- $entries := list -}}
{{- $a := $root.Values.telemetry.export.otlp.auth -}}
{{- if and (include "agent.telemetryDefaultUsed" .) $a.secretName -}}
{{- $entries = append $entries (printf "- name: CONTINUUM_TELEMETRY_AUTH\n  valueFrom:\n    secretKeyRef:\n      name: %s\n      key: %s" $a.secretName $a.secretKey) -}}
{{- end -}}
{{- range (include "agent.telemetryModalities" . | fromJsonArray) -}}
{{- if include "agent.telemetryHasRoute" (dict "root" $root "m" .) -}}
{{- $ra := (get $root.Values.telemetry.export.routes .).auth -}}
{{- if $ra.secretName -}}
{{- $entries = append $entries (printf "- name: CONTINUUM_TELEMETRY_AUTH_%s\n  valueFrom:\n    secretKeyRef:\n      name: %s\n      key: %s" (upper .) $ra.secretName $ra.secretKey) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- join "\n" $entries -}}
{{- end -}}

{{/* Client-certificate Secrets for destinations in use that have telemetry.export...tls.mtls.enabled: the
     default at /export-mtls (as ever), each route at /export-mtls-<signal>. Two lists of entries, one for a
     container's volumeMounts and one for the pod's volumes; both empty when none is configured. */}}
{{- define "agent.telemetryExportMtlsMounts" -}}
{{- $root := .root -}}
{{- $l := list -}}
{{- if and (include "agent.telemetryDefaultUsed" .) $root.Values.telemetry.export.otlp.tls.mtls.enabled -}}
{{- $l = append $l "- {name: export-mtls, mountPath: /export-mtls, readOnly: true}" -}}
{{- end -}}
{{- range (include "agent.telemetryModalities" . | fromJsonArray) -}}
{{- if include "agent.telemetryHasRoute" (dict "root" $root "m" .) -}}
{{- if (get $root.Values.telemetry.export.routes .).tls.mtls.enabled -}}
{{- $l = append $l (printf "- {name: export-mtls-%s, mountPath: /export-mtls-%s, readOnly: true}" . .) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- join "\n" $l -}}
{{- end -}}

{{- define "agent.telemetryExportMtlsVolumes" -}}
{{- $root := .root -}}
{{- $l := list -}}
{{- if and (include "agent.telemetryDefaultUsed" .) $root.Values.telemetry.export.otlp.tls.mtls.enabled -}}
{{- $l = append $l (printf "- name: export-mtls\n  secret: {secretName: %s}" $root.Values.telemetry.export.otlp.tls.mtls.secretName) -}}
{{- end -}}
{{- range (include "agent.telemetryModalities" . | fromJsonArray) -}}
{{- if include "agent.telemetryHasRoute" (dict "root" $root "m" .) -}}
{{- $r := get $root.Values.telemetry.export.routes . -}}
{{- if $r.tls.mtls.enabled -}}
{{- $l = append $l (printf "- name: export-mtls-%s\n  secret: {secretName: %s}" . $r.tls.mtls.secretName) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- join "\n" $l -}}
{{- end -}}

{{/* CA-bundle Secrets (...tls.caSecretName, key ca.crt) for destinations in use: the default at /export-ca, each
     route at /export-ca-<signal>. Not mounted for a destination whose mtls is on, because its own Secret's ca.crt is
     what is trusted then. Same shape as the client-certificate lists above. */}}
{{- define "agent.telemetryExportCAMounts" -}}
{{- $root := .root -}}
{{- $l := list -}}
{{- $d := $root.Values.telemetry.export.otlp.tls -}}
{{- if and (include "agent.telemetryDefaultUsed" .) $d.caSecretName (not $d.mtls.enabled) -}}
{{- $l = append $l "- {name: export-ca, mountPath: /export-ca, readOnly: true}" -}}
{{- end -}}
{{- range (include "agent.telemetryModalities" . | fromJsonArray) -}}
{{- if include "agent.telemetryHasRoute" (dict "root" $root "m" .) -}}
{{- $r := get $root.Values.telemetry.export.routes . -}}
{{- if and $r.tls.caSecretName (not $r.tls.mtls.enabled) -}}
{{- $l = append $l (printf "- {name: export-ca-%s, mountPath: /export-ca-%s, readOnly: true}" . .) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- join "\n" $l -}}
{{- end -}}

{{- define "agent.telemetryExportCAVolumes" -}}
{{- $root := .root -}}
{{- $l := list -}}
{{- $d := $root.Values.telemetry.export.otlp.tls -}}
{{- if and (include "agent.telemetryDefaultUsed" .) $d.caSecretName (not $d.mtls.enabled) -}}
{{- $l = append $l (printf "- name: export-ca\n  secret: {secretName: %s}" $d.caSecretName) -}}
{{- end -}}
{{- range (include "agent.telemetryModalities" . | fromJsonArray) -}}
{{- if include "agent.telemetryHasRoute" (dict "root" $root "m" .) -}}
{{- $r := get $root.Values.telemetry.export.routes . -}}
{{- if and $r.tls.caSecretName (not $r.tls.mtls.enabled) -}}
{{- $l = append $l (printf "- name: export-ca-%s\n  secret: {secretName: %s}" . $r.tls.caSecretName) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- join "\n" $l -}}
{{- end -}}

{{/* Telemetry's own images: the OTel Collector Contrib distribution, and (only when bundled) Kepler.
     Digest-vs-tag resolution mirrors agent.image/agent.imagePullPolicy above exactly. */}}
{{- define "agent.telemetryCollectorImage" -}}
{{- $i := .Values.telemetry.collectorImage -}}
{{- if $i.digest -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" (toString $i.digest)) -}}{{- fail (printf "telemetry.collectorImage.digest must look like sha256:<64 hex characters>, got %q" (toString $i.digest)) -}}{{- end -}}
{{- printf "%s@%s" (include "agent.imageRepo" (dict "root" . "repo" $i.repository)) $i.digest -}}
{{- else -}}
{{- printf "%s:%s" (include "agent.imageRepo" (dict "root" . "repo" $i.repository)) (toString $i.tag) -}}
{{- end -}}
{{- end -}}
{{- define "agent.telemetryCollectorImagePullPolicy" -}}{{- .Values.telemetry.collectorImage.pullPolicy | default "IfNotPresent" -}}{{- end -}}

{{/* Which Kepler the energy signal runs (telemetry.energy.metrics.engine): "ebpf" - release-0.7.x, the default, or
     "powercap" - v0.10 and later. Anything else fails here, so a typo cannot silently pick the other one. */}}
{{- define "agent.telemetryKeplerEngine" -}}
{{- $e := toString (.Values.telemetry.energy.metrics.engine | default "ebpf") -}}
{{- if not (has $e (list "ebpf" "powercap")) -}}{{- fail (printf "telemetry.energy.metrics.engine must be ebpf or powercap, got %q" $e) -}}{{- end -}}
{{- $e -}}
{{- end -}}

{{/* The image of the selected engine: keplerImage for ebpf, powercapImage for powercap. A digest wins over the tag. */}}
{{- define "agent.telemetryKeplerImage" -}}
{{- $m := .Values.telemetry.energy.metrics -}}
{{- $i := ternary $m.powercapImage $m.keplerImage (eq (include "agent.telemetryKeplerEngine" .) "powercap") -}}
{{- if $i.digest -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" (toString $i.digest)) -}}{{- fail (printf "telemetry.energy.metrics.%s.digest must look like sha256:<64 hex characters>, got %q" (ternary "powercapImage" "keplerImage" (eq (include "agent.telemetryKeplerEngine" .) "powercap")) (toString $i.digest)) -}}{{- end -}}
{{- printf "%s@%s" (include "agent.imageRepo" (dict "root" . "repo" $i.repository)) $i.digest -}}
{{- else -}}
{{- printf "%s:%s" (include "agent.imageRepo" (dict "root" . "repo" $i.repository)) (toString $i.tag) -}}
{{- end -}}
{{- end -}}
{{- define "agent.telemetryKeplerImagePullPolicy" -}}
{{- $m := .Values.telemetry.energy.metrics -}}
{{- $i := ternary $m.powercapImage $m.keplerImage (eq (include "agent.telemetryKeplerEngine" .) "powercap") -}}
{{- $i.pullPolicy | default "IfNotPresent" -}}
{{- end -}}

{{/* Render-time checks of the bundled Kepler's settings (a no-op unless it is bundled). The powercap engine's startup check and
     its whole signal are the node level (kepler_node_cpu_watts): without it nothing could tell a Kepler that works from one
     that does not. */}}
{{- define "agent.telemetryKeplerValidate" -}}
{{- $m := .Values.telemetry.energy.metrics -}}
{{- if and $m.enabled (eq $m.source "bundle-kepler") (eq (include "agent.telemetryKeplerEngine" .) "powercap") -}}
{{- if not (has "node" ($m.powercap.metricLevels | default (list "node" "pod"))) -}}{{- fail "telemetry.energy.metrics.powercap.metricLevels must include node: the node-level series (kepler_node_cpu_watts) is what shows Kepler works, and what the pod-level energy adds up to" -}}{{- end -}}
{{- end -}}
{{- end -}}

{{/* The Kepler DaemonSet's nodeSelector as YAML: the operator's, plus kubernetes.io/arch: amd64 for the powercap engine
     unless the operator names that key themselves. Upstream publishes the v0.10+ image for amd64 only (a single-platform
     manifest), so on an arm64 node the pod would sit in ImagePullBackOff / "exec format error" - which looks like a broken
     install rather than a node Kepler has no build for. */}}
{{- define "agent.telemetryKeplerNodeSelector" -}}
{{- $ns := deepCopy (.Values.telemetry.energy.metrics.nodeSelector | default dict) -}}
{{- if and (eq (include "agent.telemetryKeplerEngine" .) "powercap") (not (hasKey $ns "kubernetes.io/arch")) -}}{{- $_ := set $ns "kubernetes.io/arch" "amd64" -}}{{- end -}}
{{- toYaml $ns -}}
{{- end -}}

{{/* engine=powercap: Kepler's config file (read with --config.file; the pod-informer mode and the CPU meter order have no
     flag). Why each setting:
       kube.podInformer.mode apiserver  the default, "kubelet", asks the node's kubelet for /pods, which the kubelet authorizes
                                        as nodes/proxy - and Kubernetes documents get on nodes/proxy as NOT read-only
                                        (it authorizes running commands in any container of the node). apiserver mode needs
                                        only get/list/watch on pods, the one rule this chart's Kepler ServiceAccount has.
       web.listenAddresses ":9103"      the port the Service, the scrape job and the NetworkPolicy use (upstream's own is
                                        28282), on every address of the pod.
       cpu.preferredMeters              rapl, then hwmon: upstream's order. "fake" (synthetic readings, "do not enable in
                                        production") is deliberately not offered.
       exporter.prometheus.debugCollectors []  no go_* / process_* series of Kepler itself.
       metricsLevel                     telemetry.energy.metrics.powercap.metricLevels; the default includes the process
                                        level, one series per host process. */}}
{{- define "agent.telemetryKeplerPowercapConfig" -}}
log:
  level: info
  format: text
host:
  sysfs: /sys
  procfs: /proc
monitor:
  interval: 5s
  staleness: 500ms
  maxTerminated: 100
  minTerminatedEnergyThreshold: 10
cpu:
  preferredMeters: [rapl, hwmon]
exporter:
  stdout:
    enabled: false
  prometheus:
    enabled: true
    debugCollectors: []
    metricsLevel: {{ .Values.telemetry.energy.metrics.powercap.metricLevels | default (list "node" "pod") | toJson }}
web:
  configFile: ""
  listenAddresses: [":9103"]
kube:
  enabled: true
  config: ""
  nodeName: ""
  podInformer:
    mode: apiserver
{{- end -}}

{{/* The preflight init container's script (telemetry.energy.metrics.nodeChecks), per engine: a node Kepler cannot work on
     is announced, once an hour, in the log and the pod is held in Init:0/1 (a SIGTERM ends it at once) - not started, so no
     crash loop and no privileged process running for nothing. Only a definite "no" holds: a check that cannot run (no
     bpftool in a custom image, an unreadable sysfs) lets Kepler start, which then fails or works on its own terms. */}}
{{- define "agent.telemetryKeplerPreflightScript" -}}
hold() {
  trap 'exit 0' TERM INT
  while :; do
    printf '%s\n' "$1"
    sleep 3600 &
    wait $!
  done
}
{{- if eq (include "agent.telemetryKeplerEngine" .) "powercap" }}
# Kepler v0.10+ reads RAPL zones (/sys/class/powercap/*/energy_uj) or hwmon power sensors; with neither it exits with
# "failed to create CPU power meter".
found=
for f in /sys/class/powercap/*/energy_uj; do
  [ -r "$f" ] && found=1 && break
done
if [ -z "$found" ]; then
  for f in /sys/class/hwmon/hwmon*/power*_input /sys/class/hwmon/hwmon*/power*_average /sys/class/hwmon/hwmon*/energy*_input /sys/class/hwmon/hwmon*/curr*_input; do
    [ -e "$f" ] && found=1 && break
  done
fi
if [ -z "$found" ]; then
  hold "Kepler (engine=powercap) is NOT SUPPORTED ON THIS NODE: it found no RAPL energy counter (/sys/class/powercap/*/energy_uj) and no hwmon power sensor, which is all Kepler v0.10+ can read (it has no estimation mode). This is the normal case on cloud VMs and on most Arm servers. Kepler is not started here, so the pod waits in Init:0/1 instead of crash-looping. If this machine does have RAPL, load the driver (modprobe intel_rapl_common intel_rapl_msr; on Talos via machine.kernel.modules) and restart the pod. Otherwise: keep Kepler off this node pool (telemetry.energy.metrics.nodeSelector), use telemetry.energy.metrics.engine=ebpf (estimates power on VMs), or point telemetry.energy.metrics.source=existing at a Kepler you run elsewhere. telemetry.energy.metrics.nodeChecks=false starts Kepler anyway."
fi
echo "kepler preflight: a RAPL or hwmon power source is present"
{{- else }}
# Kepler release-0.7 loads eBPF tracing programs (fentry/fexit and raw tracepoints) when it starts. When the kernel refuses
# them it dies with "panic: runtime error: invalid memory address or nil pointer dereference" in pkg/bpf (NewExporter
# calls Detach on a half-built exporter) and the load error is never printed. bpftool, which ships in this image, probes the
# same thing.
probe=`timeout 60 /usr/bin/bpftool feature probe kernel 2>&1`
case "$probe" in
  *"eBPF program_type tracing is NOT available"*)
    hold "Kepler (engine=ebpf, release-0.7) is NOT SUPPORTED ON THIS NODE: the kernel refuses eBPF tracing programs (bpftool: 'eBPF program_type tracing is NOT available'), which Kepler loads at start-up; without this check it would crash-loop with a nil-pointer panic that hides the cause. Typical causes: a kernel built without BTF (CONFIG_DEBUG_INFO_BTF) or BPF trampolines, a lockdown, seccomp or LSM policy that blocks BPF tracing, or a sandboxed guest kernel (gVisor, a micro-VM). Kepler is not started here, so the pod waits in Init:0/1 instead of crash-looping. Options: keep Kepler off this node pool (telemetry.energy.metrics.nodeSelector), run it on bare metal with telemetry.energy.metrics.engine=powercap, or point telemetry.energy.metrics.source=existing at a Kepler you run elsewhere. telemetry.energy.metrics.nodeChecks=false starts Kepler anyway."
    ;;
esac
[ -e /sys/kernel/btf/vmlinux ] || echo "kepler preflight: warning: /sys/kernel/btf/vmlinux is missing (no kernel BTF); Kepler may fail to load its eBPF programs"
echo "kepler preflight: the kernel accepts eBPF tracing programs (or the probe could not run)"
{{- end }}
{{- end -}}

{{- define "agent.telemetryAcceleratorsImage" -}}
{{- $i := .Values.telemetry.accelerators.metrics.dcgmImage -}}
{{- if $i.digest -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" (toString $i.digest)) -}}{{- fail (printf "telemetry.accelerators.metrics.dcgmImage.digest must look like sha256:<64 hex characters>, got %q" (toString $i.digest)) -}}{{- end -}}
{{- printf "%s@%s" (include "agent.imageRepo" (dict "root" . "repo" $i.repository)) $i.digest -}}
{{- else -}}
{{- printf "%s:%s" (include "agent.imageRepo" (dict "root" . "repo" $i.repository)) (toString $i.tag) -}}
{{- end -}}
{{- end -}}
{{- define "agent.telemetryAcceleratorsImagePullPolicy" -}}{{- .Values.telemetry.accelerators.metrics.dcgmImage.pullPolicy | default "IfNotPresent" -}}{{- end -}}


{{/* ============================== Host collector (DaemonSet): platform adaptation ==============================
     Everything below only decides how the host collector meets the node it lands on: how it addresses the kubelet,
     which host paths it mounts. */}}

{{/* Whether the host collector pod mounts anything from the node (resourceUsage reads the node's /proc and mounts,
     systemLogs the pod log directory): the cases that Pod Security "baseline" refuses. */}}
{{- define "agent.telemetryHostMountsHost" -}}
{{- if or .Values.telemetry.resourceUsage.metrics.enabled .Values.telemetry.systemLogs.logs.enabled -}}true{{- end -}}
{{- end -}}

{{/* The node's whole root filesystem is mounted at /hostfs only for what needs it: hostmetrics' filesystem scraper
     (telemetry.resourceUsage.metrics.hostFilesystem, off by default - it must stat every mount point of the node) and
     journaling: host, which runs the node's own journalctl in a chroot of it. */}}
{{- define "agent.telemetryHostRootfsMounted" -}}
{{- if or (and .Values.telemetry.resourceUsage.metrics.enabled .Values.telemetry.resourceUsage.metrics.hostFilesystem) (and .Values.telemetry.systemLogs.logs.enabled (eq .Values.telemetry.systemLogs.logs.journaling "host")) -}}true{{- end -}}
{{- end -}}

{{/* Only the node's /proc, at /hostfs/proc: all hostmetrics' cpu and memory scrapers read (verified with the real collector:
     the same metrics, no errors, with nothing else under root_path). Used when resourceUsage is on and the whole root is not
     mounted anyway. */}}
{{- define "agent.telemetryHostProcMounted" -}}
{{- if and .Values.telemetry.resourceUsage.metrics.enabled (not (include "agent.telemetryHostRootfsMounted" .)) -}}true{{- end -}}
{{- end -}}

{{/* kubelet_stats' endpoint. NODE_IP is status.hostIP and NODE_NAME spec.nodeName (both set by telemetry.yaml). With
     viaAPIServer the receiver wants the bare node name: it builds /api/v1/nodes/<name>/proxy/ from it. */}}
{{- define "agent.telemetryKubeletEndpoint" -}}
{{- $k := .Values.telemetry.kubelet -}}
{{- if $k.viaAPIServer -}}${env:NODE_NAME}
{{- else if eq $k.address "nodeName" -}}https://${env:NODE_NAME}:10250
{{- else -}}https://${env:NODE_IP}:10250
{{- end -}}
{{- end -}}

{{/* Whether the pod needs NODE_IP at all. */}}
{{- define "agent.telemetryKubeletNeedsNodeIP" -}}
{{- $k := .Values.telemetry.kubelet -}}
{{- if and (or .Values.telemetry.resourceUsage.metrics.enabled .Values.telemetry.nodeRuntime.metrics.enabled) (not $k.viaAPIServer) (ne $k.address "nodeName") -}}true{{- end -}}
{{- end -}}

{{/* "configMap" or "secret" when telemetry.kubelet.ca names a bundle to trust for the kubelet, else empty. */}}
{{- define "agent.telemetryKubeletCA" -}}
{{- $ca := .Values.telemetry.kubelet.ca -}}
{{- if $ca.configMap -}}configMap{{- else if $ca.secret -}}secret{{- end -}}
{{- end -}}

{{/* Settings the schema cannot cross-check. All of them are things that would otherwise render a collector that starts, is
     Ready, and reads nothing - or that is refused at admission with an error that does not name the setting. */}}
{{- define "agent.telemetryHostValidate" -}}
{{- $t := .Values.telemetry -}}
{{- $k := $t.kubelet -}}
{{- if and $k.ca.configMap $k.ca.secret -}}{{- fail "telemetry.kubelet.ca: set either configMap or secret, not both" -}}{{- end -}}
{{- if and $k.viaAPIServer (or $k.insecureSkipVerify $k.ca.configMap $k.ca.secret (ne $k.address "ip")) -}}
{{- fail "telemetry.kubelet.viaAPIServer makes the API server call the kubelet, so telemetry.kubelet.insecureSkipVerify, telemetry.kubelet.ca and telemetry.kubelet.address would be ignored (insecureSkipVerify would even switch off the check of the API server itself): unset them" -}}
{{- end -}}
{{- $l := $t.systemLogs.logs -}}
{{- if not (regexMatch "^/[A-Za-z0-9._-][A-Za-z0-9._/-]*$" $l.podLogsDir) -}}
{{- fail (printf "telemetry.systemLogs.logs.podLogsDir is %q: it must be an absolute path (the kubelet's podLogsDir, /var/log/pods by default)" $l.podLogsDir) -}}
{{- end -}}
{{- range $l.extraHostPaths -}}
{{- if or (not (regexMatch "^/[A-Za-z0-9._-][A-Za-z0-9._/-]*$" .)) (contains ".." .) -}}
{{- fail (printf "telemetry.systemLogs.logs.extraHostPaths: %q is not an absolute path without \"..\" (and not /)" .) -}}
{{- end -}}
{{- end -}}
{{- if not (regexMatch "^/[A-Za-z0-9._-][A-Za-z0-9._/-]*$" $l.journalctlPath) -}}
{{- fail (printf "telemetry.systemLogs.logs.journalctlPath is %q: it must be an absolute path inside the node's root filesystem" $l.journalctlPath) -}}
{{- end -}}
{{- end -}}

{{/* The nodeAffinity of the bundled dcgm-exporter DaemonSet, as YAML (empty when it is not to be set). A DaemonSet whose
     selector matches no node is not an error to Kubernetes: it has 0 desired pods and says nothing, so a cluster where
     nobody ever set the one label the chart looked for got "enabled, healthy, empty" - the failure mode this exists to avoid.
     Left to its defaults (no nodeSelector, no affinity) the pods go to any node that announces an NVIDIA GPU by one of the
     labels the common installers leave; nodeSelectorTerms are ORed, so one match is enough:
       nvidia.com/gpu.present=true                               NVIDIA GPU Operator (controllers/state_manager.go sets it)
       nvidia.com/gpu.count                                      GPU Feature Discovery / the device plugin's gfd.enabled
       feature.node.kubernetes.io/pci-10de.present               Node Feature Discovery, NVIDIA PCI vendor (vendor-only labels)
       feature.node.kubernetes.io/pci-0302_10de.present          ... 3D controller class + vendor (NFD's default label format)
       feature.node.kubernetes.io/pci-0300_10de.present          ... VGA controller class + vendor
       cloud.google.com/gke-accelerator                          GKE GPU node pools (cloud.google.com/kubernetes-engine/docs/how-to/gpus)
     The three NFD labels are the set the GPU Operator itself treats as "this node has a GPU". An affinity or a nodeSelector
     of the release replaces all of this (the nodeSelector is ANDed with a given affinity by the scheduler). */}}
{{- define "agent.telemetryAcceleratorsAffinity" -}}
{{- $a := .Values.telemetry.accelerators.metrics -}}
{{- if $a.affinity -}}
{{- toYaml $a.affinity -}}
{{- else if not $a.nodeSelector -}}
{{- /* matchExpressions inside one term are ANDed, so each label is a term of its own. */ -}}
{{- $terms := list -}}
{{- range $k := list "nvidia.com/gpu.present" "feature.node.kubernetes.io/pci-10de.present" "feature.node.kubernetes.io/pci-0302_10de.present" "feature.node.kubernetes.io/pci-0300_10de.present" -}}
{{- $terms = append $terms (dict "matchExpressions" (list (dict "key" $k "operator" "In" "values" (list "true")))) -}}
{{- end -}}
{{- range $k := list "nvidia.com/gpu.count" "cloud.google.com/gke-accelerator" -}}
{{- $terms = append $terms (dict "matchExpressions" (list (dict "key" $k "operator" "Exists"))) -}}
{{- end -}}
{{- toYaml (dict "nodeAffinity" (dict "requiredDuringSchedulingIgnoredDuringExecution" (dict "nodeSelectorTerms" $terms))) -}}
{{- end -}}
{{- end -}}

{{/* ====================== Export path: connectivity checks, proxy ====================== */}}

{{/* The HTTP(S) proxy variables of a collector container, as env entries at column 0 (callers nindent them; empty when
     no proxy is configured). HTTPS_PROXY is what the OTLP/gRPC exporter always uses (a CONNECT tunnel, TLS or not) and the
     OTLP/HTTP exporter uses for an https:// URL; HTTP_PROXY is for an http:// URL. NO_PROXY always names what must not be
     proxied, because the Kubernetes API client (k8sattributes, k8s_cluster, k8sobjects, the Prometheus receiver's pod
     discovery) and kubelet_stats read the same variables: loopback, the in-cluster names (.svc, .cluster.local), the API
     server's ClusterIP - an ADDRESS, which no domain suffix matches, and which the kubelet puts into every container as
     KUBERNETES_SERVICE_HOST - this pod's IP and, where the collector scrapes the kubelet, the node's IP. $(NAME) is
     expanded by the kubelet from the container's own earlier variables and from those service variables, so the env list
     this is placed in must define POD_IP (and NODE_IP when .kubelet) before it. The secret form: HTTPS_PROXY is required
     from the Secret (a missing Secret or key keeps the container in CreateContainerConfigError, which names it, rather than
     starting without a proxy and failing to connect), HTTP_PROXY is optional. */}}
{{- define "agent.telemetryProxyEnv" -}}
{{- $p := .root.Values.telemetry.export.proxy -}}
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
{{- $no := list "localhost" "127.0.0.1" "::1" ".svc" ".cluster.local" "$(KUBERNETES_SERVICE_HOST)" "$(POD_IP)" }}
{{- if .kubelet }}{{ $no = append $no "$(NODE_IP)" }}{{ end }}
{{- if $p.noProxy }}{{ $no = append $no $p.noProxy }}{{ end }}
- {name: NO_PROXY, value: {{ join "," $no | quote }}}
{{- end }}
{{- end -}}

{{/* Fails the render for a destination that cannot work, with the values key to change. .c is the destination (the
     telemetry.export.otlp shape), .what its values path, .m the signal type of a route ("" for the default destination),
     .root the chart context. Everything here is something the collector either refuses to start with (a gRPC endpoint with a
     path or without a port: "invalid configuration ... missing port in address", a crash loop) or accepts and then cannot
     deliver through (https:// with insecure: true is TLS, with a verification error for every batch; http:// with
     insecure: false is a TLS handshake against a plaintext port), measured with the collector this chart pins. Only the
     port-number guess is optional (telemetry.export.checkPorts). */}}
{{- define "agent.telemetryDestinationCheck" -}}
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
{{- if and .root.Values.telemetry.export.checkPorts (eq $port "4318") -}}{{- fail (printf "%s.endpoint %q is the conventional OTLP/HTTP port but %s.protocol is grpc: a gRPC client on an HTTP port is refused for good and everything sent is dropped. Set %s.protocol=http, or use port 4317. (A destination that really serves gRPC on 4318: set telemetry.export.checkPorts=false.)" $what $e $what $what) -}}{{- end -}}
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
{{- if and .root.Values.telemetry.export.checkPorts (eq (regexFind ":[0-9]+$" $hostpart) ":4317") -}}{{- fail (printf "%s.endpoint %q is the conventional OTLP/gRPC port but %s.protocol is http: an HTTP client on a gRPC port is refused for good and everything sent is dropped. Set %s.protocol=grpc, or use port 4318. (A destination that really serves HTTP on 4317: set telemetry.export.checkPorts=false.)" $what $e $what $what) -}}{{- end -}}
{{- end -}}
{{- if and $c.fullUrl (ne $c.protocol "http") -}}{{- fail (printf "%s.fullUrl only applies to protocol=http" $what) -}}{{- end -}}
{{- if and $plain $c.tls.mtls.enabled -}}
{{- fail (printf "%s: TLS is off (%s) but a client certificate (tls.mtls) is configured; it would never be used, the destination would never see who is sending, and the data would go in plaintext. Either turn TLS on (tls.insecure=false and, for HTTP, an https:// endpoint) or turn tls.mtls off" $what (ternary "tls.insecure is true or the endpoint is http://" "the endpoint is http://, or has no scheme while tls.insecure is true" (eq $c.protocol "grpc"))) -}}
{{- end -}}
{{- end -}}

{{/* Runs the check above for every destination in use (see agent.telemetryDestinationsInUse), then the proxy settings.
     Called from agent.telemetryValidate. */}}
{{- define "agent.telemetryExportValidate" -}}
{{- $t := .Values.telemetry -}}
{{- $root := . -}}
{{- /* Only destinations that something is sent to: a default destination every signal type has a route around is never
       rendered, and a stale value in it (the install command Ikhnos prints leaves one) must not fail the install. */ -}}
{{- range (include "agent.telemetryDestinationsInUse" . | fromJsonArray) -}}
{{- if and .c.endpoint (ne .c.protocol "zipkin") -}}
{{- include "agent.telemetryDestinationCheck" (dict "root" $root "c" .c "what" .name "m" .m) -}}
{{- end -}}
{{- end -}}
{{- range $k, $v := $t.export.proxy -}}
{{- if and (has $k (list "httpProxy" "httpsProxy")) $v (not (regexMatch "^((https?|socks5h?)://)?[^/@\\s]+@?[^/\\s]*/?$" $v)) -}}
{{- fail (printf "telemetry.export.proxy.%s %q is not a proxy URL (http://host:3128, https://host:3129 or socks5://host:1080)" $k $v) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Whether a destination (.c, the telemetry.export.otlp shape) is reached without TLS: a gRPC endpoint with tls.insecure
     (or http://), an HTTP endpoint that says http:// or says nothing while tls.insecure is true. "true" or empty. */}}
{{- define "agent.telemetryDestinationPlain" -}}
{{- $c := .c -}}
{{- $e := toString $c.endpoint -}}
{{- if eq $c.protocol "zipkin" -}}{{- if not (regexMatch "^https://" (include "agent.telemetryZipkinEndpoint" $c)) -}}true{{- end -}}
{{- else if eq $c.protocol "http" -}}{{- if or (hasPrefix "http://" $e) (and (not (hasPrefix "https://" $e)) $c.tls.insecure) -}}true{{- end -}}
{{- else -}}{{- if or (hasPrefix "http://" $e) (and (not (hasPrefix "https://" $e)) $c.tls.insecure) -}}true{{- end -}}
{{- end -}}
{{- end -}}

{{/* The destinations in use (the default one when any signal type goes there, each route of a signal type in use), as a JSON
     list of {name, c}: .name is the values path for messages. */}}
{{- define "agent.telemetryDestinationsInUse" -}}
{{- $root := . -}}
{{- $l := list -}}
{{- if include "agent.telemetryDefaultUsed" (dict "root" . "scope" "all") -}}{{- $l = append $l (dict "name" "telemetry.export.otlp" "m" "" "c" .Values.telemetry.export.otlp) -}}{{- end -}}
{{- range (include "agent.telemetryModalities" (dict "root" . "scope" "all") | fromJsonArray) -}}
{{- if include "agent.telemetryHasRoute" (dict "root" $root "m" .) -}}{{- $l = append $l (dict "name" (printf "telemetry.export.routes.%s" .) "m" . "c" (get $root.Values.telemetry.export.routes .)) -}}{{- end -}}
{{- end -}}
{{- toJson $l -}}
{{- end -}}

{{/* One line per destination in use for NOTES: where, how, and whether TLS and a credential are involved. */}}
{{- define "agent.telemetryDestinationLines" -}}
{{- $root := . -}}
{{- $out := list -}}
{{- range (include "agent.telemetryDestinationsInUse" . | fromJsonArray) -}}
{{- $c := .c -}}
{{- $tls := "TLS" -}}
{{- if include "agent.telemetryDestinationPlain" (dict "c" $c) }}{{ $tls = "PLAINTEXT" }}{{ else if $c.tls.mtls.enabled }}{{ $tls = "mutual TLS" }}{{ end -}}
{{- $out = append $out (printf "%s -> %s (%s, %s%s)" (trimPrefix "telemetry.export." .name) $c.endpoint $c.protocol $tls (ternary (printf ", header %s from Secret %s" $c.auth.headerName $c.auth.secretName) "" (ne (toString $c.auth.secretName) ""))) -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{/* The Secrets (with the keys read from each) that the collectors cannot start without, as a JSON list of strings. A
     missing Secret leaves the pod in ContainerCreating ("FailedMount": a volume) or CreateContainerConfigError (an
     environment variable) - visible in `kubectl describe pod`, but only if someone looks, so NOTES names them. */}}
{{- define "agent.telemetryExportSecrets" -}}
{{- $root := . -}}
{{- $l := list -}}
{{- range (include "agent.telemetryDestinationsInUse" . | fromJsonArray) -}}
{{- $c := .c -}}
{{- if $c.tls.mtls.enabled }}{{ $l = append $l (printf "%s (tls.crt, tls.key, ca.crt)" $c.tls.mtls.secretName) }}{{ else if $c.tls.caSecretName }}{{ $l = append $l (printf "%s (ca.crt)" $c.tls.caSecretName) }}{{ end -}}
{{- if $c.auth.secretName }}{{ $l = append $l (printf "%s (%s)" $c.auth.secretName $c.auth.secretKey) }}{{ end -}}
{{- end -}}
{{- if $root.Values.telemetry.receiver.auth.enabled }}{{ $l = append $l (printf "%s (%s)" $root.Values.telemetry.receiver.auth.secretName $root.Values.telemetry.receiver.auth.secretKey) }}{{ end -}}
{{- if $root.Values.telemetry.export.proxy.secretName }}{{ $l = append $l (printf "%s (HTTPS_PROXY%s)" $root.Values.telemetry.export.proxy.secretName (ternary "" ", HTTP_PROXY optional" (ne (toString $root.Values.telemetry.export.proxy.httpProxy) ""))) }}{{ end -}}
{{- toJson (uniq $l) -}}
{{- end -}}

{{/* What NOTES warns about on the export path, as a JSON list of strings: each is a setting that is accepted but will not
     do what the person most likely meant, or a limit that will be hit later and then fail quietly. Kept out of the render
     failures above because each has a legitimate use. */}}
{{- define "agent.telemetryExportWarnings" -}}
{{- $root := . -}}
{{- $t := .Values.telemetry -}}
{{- $w := list -}}
{{- $dests := include "agent.telemetryDestinationsInUse" . | fromJsonArray -}}
{{- $p := $t.export.proxy -}}
{{- $proxied := or $p.httpProxy $p.httpsProxy $p.secretName -}}
{{- $anyGrpc := false -}}
{{- $external := false -}}
{{- range $dests -}}
{{- if eq .c.protocol "grpc" }}{{ $anyGrpc = true }}{{ end -}}
{{- $host := regexReplaceAll "^(https?://|dns:///|dns:|passthrough:///)?(\\[[^]]*\\]|[^/:]*).*$" (toString .c.endpoint) "${2}" -}}
{{- if not (regexMatch "(^localhost$|^127\\.|^\\[::1\\]$|\\.svc(\\.|$)|\\.cluster\\.local\\.?$)" $host) }}{{ $external = true }}{{ end -}}
{{- if and .c.auth.secretName (include "agent.telemetryDestinationPlain" (dict "c" .c)) -}}
{{- $w = append $w (printf "%s sends its credential header (%s) over a connection without TLS: anyone on the path can read it. Use TLS (tls.insecure=false and, for HTTP, an https:// endpoint) or send it only inside a trusted network." .name .c.auth.headerName) -}}
{{- end -}}
{{- if and (or .c.tls.caSecretName .c.tls.caFile) (include "agent.telemetryDestinationPlain" (dict "c" .c)) -}}
{{- $w = append $w (printf "%s names a CA (tls.caSecretName / tls.caFile) but TLS is off (tls.insecure=true or an http:// endpoint): the CA is never used and the data goes in plaintext. If you meant TLS, set tls.insecure=false (and use an https:// endpoint for HTTP)." .name) -}}
{{- end -}}
{{- end -}}
{{- if and ($root.Values.proxy.httpsProxy | default $root.Values.proxy.httpProxy) (not $proxied) $external -}}
{{- $w = append $w "The agent goes through a proxy (proxy.*) but the telemetry collectors do not - telemetry.export.proxy is not inherited - and a destination is outside the cluster. If the cluster reaches it only through the proxy, set telemetry.export.proxy.httpsProxy (a gRPC destination uses only that one) and httpProxy." -}}
{{- end -}}
{{- if $proxied -}}
{{- if and $anyGrpc (not $p.httpsProxy) (not $p.secretName) -}}
{{- $w = append $w "telemetry.export.proxy.httpProxy is set but a destination is gRPC, which only ever uses httpsProxy (a CONNECT tunnel): it goes direct, not through the proxy. Set telemetry.export.proxy.httpsProxy." -}}
{{- end -}}
{{- if or (contains "@" (toString $p.httpProxy)) (contains "@" (toString $p.httpsProxy)) -}}
{{- $w = append $w "telemetry.export.proxy carries credentials (user:password@) in clear text in the pod spec and in `helm get values`. Put the URL in a Secret under the key HTTPS_PROXY (and HTTP_PROXY) and name it in telemetry.export.proxy.secretName." -}}
{{- end -}}
{{- if $root.Values.networkPolicy.telemetryEgress.enabled -}}
{{- $w = append $w "networkPolicy.telemetryEgress is on and a proxy is configured: the collectors connect only to the proxy, so networkPolicy.telemetryEgress.allowedEgress must allow the proxy's address and port (the destinations behind it need no rule)." -}}
{{- end -}}
{{- end -}}
{{- $q := $t.export.queue -}}
{{- $mrb := int $q.maxRequestBytes -}}
{{- if and $anyGrpc (gt $mrb 4194304) -}}
{{- $w = append $w (printf "telemetry.export.queue.maxRequestBytes is %d, above the 4194304 (4 MiB) a gRPC receiver accepts by default: a larger request is refused for good (\"received message larger than max\") and dropped. Keep it at 3145728 unless every gRPC destination was raised." $mrb) -}}
{{- end -}}
{{- if and $q.persistent.enabled $mrb -}}
{{- $limit := int64 (include "agent.quantityBytes" $q.persistent.sizeLimit | default "0") -}}
{{- $worst := mul (int64 $q.size) (int64 $mrb) (int64 (len (include "agent.telemetryModalities" (dict "root" $root "scope" "all") | fromJsonArray))) -}}
{{- if and $limit (gt $worst $limit) -}}
{{- $w = append $w (printf "telemetry.export.queue.persistent: a full queue is up to %d MiB (size %d x maxRequestBytes x each signal type) but its emptyDir is limited to %s. The kubelet evicts a pod whose emptyDir passes its sizeLimit, which also deletes the queue. In practice requests are far smaller than maxRequestBytes; raise persistent.sizeLimit, or lower queue.size, if the destination can be down for long under heavy load." (div $worst 1048576) (int $q.size) $q.persistent.sizeLimit) -}}
{{- end -}}
{{- end -}}
{{- toJson $w -}}
{{- end -}}

{{/* Whether the energy / GPU data follows telemetry.scope (telemetry.energy.metrics.applyScope, telemetry.accelerators.metrics.applyScope).
     "auto" (the default) means: yes exactly when the intent defines a scope (namespaces, exclude or workloads) - a user who
     scoped the telemetry to some namespaces must not receive another namespace's pods' energy or GPU data; with no scope
     there is nothing to follow, so nothing is filtered and, for dcgm-exporter, the kubelet's pod-resources socket is not mounted.
     true / false force it either way. "true" or empty. */}}
{{- define "agent.telemetryScopeDefined" -}}
{{- $s := .Values.telemetry.scope -}}
{{- if or $s.namespaces $s.exclude $s.workloads -}}true{{- end -}}
{{- end -}}
{{- define "agent.telemetryEnergyApplyScope" -}}
{{- $v := .Values.telemetry.energy.metrics.applyScope -}}
{{- if kindIs "string" $v -}}{{- if eq $v "auto" -}}{{- include "agent.telemetryScopeDefined" . -}}{{- end -}}{{- else if $v -}}true{{- end -}}
{{- end -}}
{{- define "agent.telemetryAccApplyScope" -}}
{{- $v := .Values.telemetry.accelerators.metrics.applyScope -}}
{{- if kindIs "string" $v -}}{{- if eq $v "auto" -}}{{- include "agent.telemetryScopeDefined" . -}}{{- end -}}{{- else if $v -}}true{{- end -}}
{{- end -}}
