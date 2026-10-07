{{/* Platform layer: what the cluster's ADMISSION (Pod Security, mesh injection) and its image
     registry policy do to this chart's pods, as opposed to what the pods do. Everything here reads its values with
     `dig` and a default instead of `.Values.x.y`: `helm upgrade --reuse-values` swaps this chart's defaults for the
     old release's, so a block added after the install is simply absent, and a bare `.Values.podSecurity.runAsUser`
     would fail the whole render with "nil pointer evaluating interface {}". The defaults below are the ones in
     values.yaml (a test keeps them in step). */}}

{{/* The pod-level securityContext of a pod that runs as an unprivileged user (podSecurity.runAsUser, 65532 by default, as
     user, group and - when asked - fsGroup). Takes a dict: root (the chart root) and fsGroup (true when the pod mounts a
     Secret/volume that must be group-readable). Indent with nindent. */}}
{{- define "agent.nonRootPodSecurityContext" -}}
runAsNonRoot: true
{{- $uid := int (dig "podSecurity" "runAsUser" 65532 .root.Values.AsMap) }}
runAsUser: {{ $uid }}
runAsGroup: {{ $uid }}
{{- if .fsGroup }}
fsGroup: {{ $uid }}
{{- end }}
seccompProfile: {type: RuntimeDefault}
{{- end -}}

{{/* Keeps a service mesh's sidecar out of the telemetry pods. Istio injects when its webhook sees the namespace label
     (istio-injection=enabled) unless the POD carries the label sidecar.istio.io/inject="false"
     (istio.io/latest/docs/setup/additional-setup/sidecar-injection/); Linkerd skips a pod annotated
     linkerd.io/inject: disabled (linkerd.io/2/features/proxy-injection/). In an injected namespace these pods would
     otherwise (a) fail to be created where the namespace enforces Pod Security restricted/baseline, because Istio's
     init container needs NET_ADMIN/NET_RAW, (b) export before the proxy is up and (c) have their export traffic held
     to the mesh's outbound policy (REGISTRY_ONLY refuses an OTLP destination that has no ServiceEntry). Kepler and the
     host collector are node agents that reach the kubelet and the node, which a sidecar adds nothing to.
     mesh.telemetryInjection: disabled (default) | inherit (change nothing; use podLabels/podAnnotations). A key the
     operator already sets in podLabels/podAnnotations wins and is not repeated (a duplicate key is a render error). */}}
{{- define "agent.meshOptOutLabels" -}}
{{- if and (ne (toString (dig "mesh" "telemetryInjection" "disabled" .Values.AsMap)) "inherit") (not (hasKey (.Values.podLabels | default dict) "sidecar.istio.io/inject")) -}}
sidecar.istio.io/inject: "false"
{{- end -}}
{{- end -}}
{{- define "agent.meshOptOutAnnotations" -}}
{{- if and (ne (toString (dig "mesh" "telemetryInjection" "disabled" .Values.AsMap)) "inherit") (not (hasKey (.Values.podAnnotations | default dict) "linkerd.io/inject")) -}}
linkerd.io/inject: disabled
{{- end -}}
{{- end -}}

{{/* One registry for every image. global.imageRegistry replaces the REGISTRY HOST of an image repository and keeps the
     rest of its path, so  quay.io/sustainable_computing_io/kepler  becomes  <registry>/sustainable_computing_io/kepler,
     otel/opentelemetry-collector-contrib (Docker Hub, no host written) becomes <registry>/otel/opentelemetry-
     collector-contrib, and nvcr.io/nvidia/k8s/dcgm-exporter becomes <registry>/nvidia/k8s/dcgm-exporter - the layout an
     air-gapped mirror or a pull-through cache project normally has. A first path segment counts as a host when it has
     a "." or ":" in it or is "localhost" (the rule the container runtimes use). The registry value may carry a path
     (registry.corp/mirror). Takes a dict: root and repo. Digests and tags are untouched. */}}
{{- define "agent.imageRepo" -}}
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

{{/* Pods of this release that need host access (hostPath, hostNetwork, hostPID, privileged or SYS_ADMIN), as a JSON list
     of {name, sa}: the ServiceAccount each runs under. Pod Security baseline and restricted both refuse every one of them. The cluster collector, nodeRuntime
     alone on the host collector, and the agent itself are not in it: they are restricted-compliant. sa is empty
     for the node probe and the traffic observer, which run under the namespace's default ServiceAccount. */}}
{{- define "agent.hostAccessPods" -}}
{{- include "agent.telemetryDefaults" . -}}
{{- $l := list -}}
{{- if or .Values.telemetry.resourceUsage.metrics.enabled .Values.telemetry.systemLogs.logs.enabled -}}
{{- $l = append $l (dict "name" "the host collector" "sa" (printf "%s-telemetry" (include "agent.name" .))) -}}
{{- end -}}
{{- if and .Values.telemetry.energy.metrics.enabled (eq .Values.telemetry.energy.metrics.source "bundle-kepler") -}}
{{- $l = append $l (dict "name" "Kepler" "sa" (printf "%s-kepler" (include "agent.name" .))) -}}
{{- end -}}
{{- if and .Values.telemetry.accelerators.metrics.enabled (eq .Values.telemetry.accelerators.metrics.source "bundle-dcgm") -}}
{{- $l = append $l (dict "name" "dcgm-exporter" "sa" (printf "%s-dcgm" (include "agent.name" .))) -}}
{{- end -}}
{{- if and .Values.nodeProbe.enabled (or .Values.nodeProbe.hostSys .Values.nodeProbe.hostNetwork) -}}
{{- $l = append $l (dict "name" "the node probe" "sa" "") -}}
{{- end -}}
{{- if .Values.flowObserver.enabled -}}
{{- $l = append $l (dict "name" "the traffic observer" "sa" "") -}}
{{- end -}}
{{- toJson $l -}}
{{- end -}}

{{/* The Pod Security level this release's namespace enforces ("" = none known). Read from the namespace's
     pod-security.kubernetes.io/enforce label with Helm's `lookup`, or declared with preflight.assumeEnforce (which wins:
     for `helm template`, Argo CD and Flux, where lookup sees no cluster and answers an empty map). Three ways this is
     blind, all harmless: the namespace does not exist yet (helm install --create-namespace), a render with no cluster,
     and a cluster-wide default set in the API server's admission configuration instead of a label (Talos enforces
     baseline, RKE2's CIS profile restricted: there the label is absent; label the namespace privileged first, which
     overrides the default). One way it can hurt: Helm turns an API error other than "not found" into a render error, so an
     account that may not `get` the Namespace fails the install with "namespaces ... is forbidden" - set
     preflight.namespaceLookup=false then. */}}
{{- define "agent.namespaceEnforceLevel" -}}
{{- $assume := toString (dig "preflight" "assumeEnforce" "" .Values.AsMap) -}}
{{- if $assume -}}{{- $assume -}}
{{- else if dig "preflight" "namespaceLookup" true .Values.AsMap -}}
{{- $ns := lookup "v1" "Namespace" "" .Release.Namespace -}}
{{- if and $ns (hasKey $ns "metadata") (kindIs "map" $ns.metadata) (hasKey $ns.metadata "labels") (kindIs "map" $ns.metadata.labels) -}}
{{- index $ns.metadata.labels "pod-security.kubernetes.io/enforce" | default "" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* The pre-flight message, or "" when there is nothing to say: this release needs host access in a namespace that
     enforces Pod Security baseline or restricted. Without it the install succeeds and the DaemonSet sits at 0 pods with
     a FailedCreate event nobody is looking at (Pod Security enforces on the pods, not on the workload object:
     kubernetes.io/docs/concepts/security/pod-security-admission/, "Workload resources and Pod templates"). */}}
{{- define "agent.preflightPodSecurityMessage" -}}
{{- $level := include "agent.namespaceEnforceLevel" . -}}
{{- if or (eq $level "baseline") (eq $level "restricted") -}}
{{- $who := list -}}
{{- range (include "agent.hostAccessPods" . | fromJsonArray) -}}{{- $who = append $who .name -}}{{- end -}}
{{- if $who -}}
namespace {{ .Release.Namespace }} enforces the Pod Security "{{ $level }}" profile, which refuses {{ join ", " $who }} (host paths, host namespaces, privileged mode or SYS_ADMIN): the DaemonSet would be created and then sit at 0 pods. Label the namespace before installing (a namespace of its own for these pods keeps the rest of the cluster as strict as it is): kubectl label namespace {{ .Release.Namespace }} pod-security.kubernetes.io/enforce=privileged --overwrite - or turn those signals off. To install anyway set preflight.podSecurity=warn (message in NOTES) or off.
{{- end -}}
{{- end -}}
{{- end -}}
{{- define "agent.preflightPodSecurity" -}}
{{- $mode := toString (dig "preflight" "podSecurity" "fail" .Values.AsMap) -}}
{{- if eq $mode "fail" -}}
{{- with include "agent.preflightPodSecurityMessage" . -}}{{- fail (printf "Pod Security pre-flight: %s" .) -}}{{- end -}}
{{- end -}}
{{- end -}}

{{/* The DNS egress rule every NetworkPolicy of this chart starts with (a policy that selects a pod denies whatever it does not
     list, so a pod that cannot resolve a name cannot do anything else either). Takes a dict: dnsCIDRs (extra addresses).
     Two kinds of resolver have to be reachable, and the usual rules do not cover both:
       - kube-dns/CoreDNS pods in kube-system (k8s-app=kube-dns): kubeadm, k3s, RKE2, EKS, AKS, GKE, Talos, kind.
       - NodeLocal DNSCache, which answers on the node (kubernetes.io/docs/tasks/administer-cluster/nodelocaldns/): its pods
         are hostNetwork pods in kube-system that no podSelector in a NetworkPolicy matches, so with it on every lookup
         was refused and, say, the OTLP destination's name never resolved. It listens on a link-local address; 169.254.20.10 is
         the one the upstream manifest and GKE use, listed here because nothing else in the cluster can be at a
         link-local address the pod can reach. A different address (or, with kube-proxy in iptables mode, the kube-dns
         ClusterIP, which the cache answers for) goes in dnsCIDRs. */}}
{{- define "agent.dnsEgressRule" -}}
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
