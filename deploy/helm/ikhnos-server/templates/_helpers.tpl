{{/* Names. "ikhnos-server" is the chart name; a release called ikhnos-server (or containing it) is not repeated. */}}
{{- define "ikhnos.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ikhnos.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "ikhnos.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Labels shared by everything; the component is added per object. */}}
{{- define "ikhnos.labels" -}}
helm.sh/chart: {{ include "ikhnos.chart" . }}
app.kubernetes.io/name: {{ include "ikhnos.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/part-of: ikhnos
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/* What a Service, a NetworkPolicy or a Deployment selects: the server pod. Never changes after install (selectors are immutable). */}}
{{- define "ikhnos.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ikhnos.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: server
{{- end -}}

{{- define "ikhnos.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "ikhnos.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Image reference: repository@digest when a digest is set (the tag is then ignored), else repository:tag.
     Call with (dict "image" .Values.image "defaultTag" .Chart.AppVersion). */}}
{{- define "ikhnos.imageRef" -}}
{{- if .image.digest -}}
{{- printf "%s@%s" .image.repository .image.digest -}}
{{- else -}}
{{- printf "%s:%s" .image.repository (.image.tag | default .defaultTag | toString) -}}
{{- end -}}
{{- end -}}

{{/* Effective imagePullPolicy for a component's image block: an explicit value always wins; otherwise Always
     when the resolved tag is the moving `edge` tag (a node that already cached anything named edge won't
     re-pull under IfNotPresent even after the registry moves on), else IfNotPresent.
     Call with (dict "image" .Values.image "defaultTag" .Chart.AppVersion). */}}
{{- define "ikhnos.imagePullPolicy" -}}
{{- if .image.pullPolicy -}}
{{- .image.pullPolicy -}}
{{- else if eq (.image.tag | default .defaultTag | toString) "edge" -}}
Always
{{- else -}}
IfNotPresent
{{- end -}}
{{- end -}}

{{/* PVC that holds /data (SQLite, the CA and its key). */}}
{{- define "ikhnos.dataClaim" -}}
{{- default (printf "%s-data" (include "ikhnos.fullname" .)) .Values.persistence.existingClaim -}}
{{- end -}}

{{/* --admin-behind-tls-proxy: explicit value wins; otherwise on when this chart creates an HTTPRoute
     (a TLS-terminating Gateway is then in front by construction). Renders "true" or nothing. */}}
{{- define "ikhnos.behindProxy" -}}
{{- $v := .Values.admin.behindTlsProxy -}}
{{- if kindIs "bool" $v -}}
{{- if $v -}}true{{- end -}}
{{- else if .Values.httproute.enabled -}}true{{- end -}}
{{- end -}}

{{/* How the agent port is exposed, in the same words the UI uses: "gateway", "loadbalancer", "nodeport" or
     "clusterip". Passed to the binary as --agent-exposure so Settings → Installation can explain the current
     address instead of just showing it - the server has no other way to know this about itself. */}}
{{- define "ikhnos.agentExposure" -}}
{{- if .Values.agent.tlsRoute.enabled -}}gateway
{{- else -}}{{- lower .Values.agent.service.type -}}
{{- end -}}
{{- end -}}

{{/* Whether the chart should generate (and, once created, keep using) a self-signed certificate for the admin port:
     no explicit Secret was given, nothing else is confirmed to already protect the port, and the escape hatch
     (admin.tls.selfSigned=false) wasn't used. Renders "true" or nothing. */}}
{{- define "ikhnos.adminSelfSigned" -}}
{{- if .Values.admin.tls.secretName -}}
{{- else if include "ikhnos.behindProxy" . -}}
{{- else if not .Values.admin.tls.selfSigned -}}
{{- else -}}true{{- end -}}
{{- end -}}

{{/* Name of the chart-generated self-signed admin TLS secret (only created when ikhnos.adminSelfSigned is true). */}}
{{- define "ikhnos.adminSelfSignedSecretName" -}}
{{- printf "%s-admin-tls-selfsigned" (include "ikhnos.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Secret that actually holds the admin TLS certificate: the user-provided one, or this chart's generated one, or
     nothing at all (plain HTTP, behind a trusted proxy/tunnel). */}}
{{- define "ikhnos.adminTLSSecretName" -}}
{{- if .Values.admin.tls.secretName -}}
{{- .Values.admin.tls.secretName -}}
{{- else if include "ikhnos.adminSelfSigned" . -}}
{{- include "ikhnos.adminSelfSignedSecretName" . -}}
{{- end -}}
{{- end -}}

{{/* The admin listener is plain HTTP unless a certificate (user-provided or self-signed) is mounted. */}}
{{- define "ikhnos.adminTLS" -}}
{{- if .Values.admin.tls.secretName -}}true{{- else if include "ikhnos.adminSelfSigned" . -}}true{{- end -}}
{{- end -}}

{{/* ---- Neo4j ---- */}}
{{- define "ikhnos.neo4jName" -}}{{- printf "%s-neo4j" (include "ikhnos.fullname" .) | trunc 63 | trimSuffix "-" -}}{{- end -}}

{{- define "ikhnos.neo4jSelectorLabels" -}}
app.kubernetes.io/name: {{ include "ikhnos.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: neo4j
{{- end -}}

{{/* Neo4j is mandatory (mode is always bundled or external; validated below), so this is always true. Kept as its
own helper because several templates read it rather than repeating the mode check. */}}
{{- define "ikhnos.neo4jEnabled" -}}true{{- end -}}

{{/* Where the server finds Neo4j. */}}
{{- define "ikhnos.neo4jURL" -}}
{{- if eq .Values.neo4j.mode "bundled" -}}
{{- printf "http://%s.%s.svc:7474" (include "ikhnos.neo4jName" .) .Release.Namespace -}}
{{- else if eq .Values.neo4j.mode "external" -}}
{{- .Values.neo4j.external.url -}}
{{- end -}}
{{- end -}}

{{/* Secret and key that hold the Neo4j password (the server reads it as a file; bundled Neo4j reads it as NEO4J_AUTH). */}}
{{- define "ikhnos.neo4jSecretName" -}}
{{- if eq .Values.neo4j.mode "external" -}}
{{- .Values.neo4j.external.existingSecret -}}
{{- else if .Values.neo4j.auth.existingSecret -}}
{{- .Values.neo4j.auth.existingSecret -}}
{{- else -}}
{{- printf "%s-neo4j-auth" (include "ikhnos.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "ikhnos.neo4jSecretKey" -}}
{{- if eq .Values.neo4j.mode "external" -}}
{{- .Values.neo4j.external.passwordKey -}}
{{- else if .Values.neo4j.auth.existingSecret -}}
{{- .Values.neo4j.auth.passwordKey -}}
{{- else -}}password
{{- end -}}
{{- end -}}

{{/* Name and key of an existing Secret the operator brought their own CA key passphrase in (e.g. from an
external secret manager) - empty when none is named, which is also what pki.encryptAtRest's default (no
pki.caKeyPassphraseSecret.name) looks like: the server then mints and persists its own passphrase on first
boot (--ca-key-auto-passphrase, see cmd/server and internal/pki.AutoPassphrase) rather than expecting one
in a Secret, so the deployment template mounts nothing and only passes --ca-key-passphrase-file when this
is non-empty. */}}
{{- define "ikhnos.caPassphraseSecretName" -}}
{{- .Values.pki.caKeyPassphraseSecret.name -}}
{{- end -}}

{{/* The Secret that holds the sealing key (empty: secrets are stored as they are), and the file name its key has inside it. */}}
{{- define "ikhnos.sealingSecretName" -}}
{{- (.Values.sealing | default dict).keySecret | default dict | dig "name" "" -}}
{{- end -}}

{{- define "ikhnos.sealingSecretKey" -}}
{{- if include "ikhnos.sealingSecretName" . -}}
{{- (.Values.sealing.keySecret).key | default "sealing.key" -}}
{{- end -}}
{{- end -}}

{{- define "ikhnos.caPassphraseSecretKey" -}}
{{- if .Values.pki.caKeyPassphraseSecret.name -}}
{{- .Values.pki.caKeyPassphraseSecret.key | default "passphrase" -}}
{{- end -}}
{{- end -}}

{{/* Port of the external Neo4j URL (explicit, or the scheme's default). */}}
{{- define "ikhnos.neo4jExternalPort" -}}
{{- $u := urlParse .Values.neo4j.external.url -}}
{{- $p := regexFind ":[0-9]+$" (get $u "host") | trimPrefix ":" -}}
{{- if $p -}}{{- $p -}}{{- else if eq (get $u "scheme") "https" -}}443{{- else -}}80{{- end -}}
{{- end -}}

{{/* ---- Validation. Every failure says what to set. Rendered from templates/validate.yaml (which produces no object). ---- */}}
{{- define "ikhnos.validate" -}}
{{- /* agent.publicAddress */ -}}
{{- $addr := .Values.agent.publicAddress -}}
{{- if not $addr -}}
{{- fail "agent.publicAddress is required: the host:port your agents dial to reach this server, for example ikhnos.example.com:8443 (or NODE_IP:30443 with agent.service.type=NodePort). It also becomes a name in the server certificate, so it must be exactly what agents use." -}}
{{- end -}}
{{- if not (regexMatch "^(\\[[0-9a-fA-F:.]+\\]|[^:/\\s\\[\\]]+):[0-9]{1,5}$" $addr) -}}
{{- fail (printf "agent.publicAddress must look like host:port (no scheme, no path), got %q" $addr) -}}
{{- end -}}
{{- /* registration */ -}}
{{- if not (has .Values.registration (list "invite" "open" "closed")) -}}
{{- fail (printf "registration must be one of invite, open, closed; got %q" (toString .Values.registration)) -}}
{{- end -}}
{{- /* agent service */ -}}
{{- if not (has .Values.agent.service.type (list "LoadBalancer" "NodePort" "ClusterIP")) -}}
{{- fail (printf "agent.service.type must be LoadBalancer, NodePort or ClusterIP; got %q" (toString .Values.agent.service.type)) -}}
{{- end -}}
{{- /* admin exposure: the server refuses cleartext on a non-loopback address unless told a TLS proxy is in front */ -}}
{{- if and (not (include "ikhnos.adminTLS" .)) (not (include "ikhnos.behindProxy" .)) -}}
{{- fail "the admin listener (UI and API) carries sign-in passwords and the session cookie, and the server refuses to serve it in clear text without TLS. You've set admin.tls.selfSigned=false, which turns off this chart's own self-signed-certificate fallback, so choose one explicitly: enable httproute so a Gateway terminates TLS (and leave admin.behindTlsProxy unset); set admin.tls.secretName to a kubernetes.io/tls Secret; or, if you put your own TLS in front (or only use kubectl port-forward to localhost), set admin.behindTlsProxy=true." -}}
{{- end -}}
{{- if and .Values.admin.tls.secretName (not .Values.admin.tls.certKey) -}}{{- fail "admin.tls.certKey must not be empty when admin.tls.secretName is set" -}}{{- end -}}
{{- /* admin.sso.headerName: only safe when the admin port is reachable exclusively through a proxy that
     overwrites the header itself - the same condition --admin-behind-tls-proxy already names, and the server
     also enforces this at startup, so a stray --set can't skip it by rendering without this chart. */ -}}
{{- if and .Values.admin.sso.headerName (not (include "ikhnos.behindProxy" .)) -}}
{{- fail "admin.sso.headerName requires admin.behindTlsProxy=true (or httproute.enabled, which implies it): trusting a proxy-asserted identity header is only safe when that proxy is the sole way to reach this port and always overwrites the header itself." -}}
{{- end -}}
{{- /* exposure objects */ -}}
{{- if and .Values.httproute.enabled (not .Values.httproute.parentRefs) -}}
{{- fail "httproute.enabled needs httproute.parentRefs (the Gateway to attach to)" -}}
{{- end -}}
{{- if and .Values.agent.tlsRoute.enabled (or (not .Values.agent.tlsRoute.parentRefs) (not .Values.agent.tlsRoute.hostnames)) -}}
{{- fail "agent.tlsRoute.enabled needs agent.tlsRoute.parentRefs and agent.tlsRoute.hostnames" -}}
{{- end -}}
{{- /* the agent Service always renders (there is no toggle for it): with tlsRoute also on and the Service still a
       LoadBalancer or NodePort, agents get TWO working front doors to the same backend - the Gateway's TLSRoute
       AND the Service's own public address - which is confusing at best and, on a cloud LoadBalancer, a second
       bill for a listener nobody meant to keep. Route through the Gateway alone by setting the Service ClusterIP. */ -}}
{{- if and .Values.agent.tlsRoute.enabled (ne .Values.agent.service.type "ClusterIP") -}}
{{- fail (printf "agent.tlsRoute.enabled routes agents through a Gateway; agent.service.type is %q, which also stands up its own %s for the same port. Set agent.service.type=ClusterIP so the Gateway is the only way in." (toString .Values.agent.service.type) .Values.agent.service.type) -}}
{{- end -}}
{{- /* pki */ -}}
{{- if and .Values.pki.caKeyPassphraseSecret.name (not .Values.pki.caKeyPassphraseSecret.key) -}}
{{- fail "pki.caKeyPassphraseSecret.key must be set together with .name" -}}
{{- end -}}
{{- /* sealing */ -}}
{{- if and (include "ikhnos.sealingSecretName" .) (not ((.Values.sealing).keySecret).key) -}}
{{- fail "sealing.keySecret.key must be set together with .name" -}}
{{- end -}}
{{- /* neo4j: mandatory, so only the two real deployment choices are accepted */ -}}
{{- if not (has .Values.neo4j.mode (list "bundled" "external")) -}}
{{- fail (printf "neo4j.mode must be bundled or external (Neo4j is required by this chart; there is no \"none\"); got %q" (toString .Values.neo4j.mode)) -}}
{{- end -}}
{{- if eq .Values.neo4j.mode "external" -}}
{{- $url := .Values.neo4j.external.url -}}
{{- if not $url -}}
{{- fail "neo4j.mode=external needs neo4j.external.url (for example https://neo4j.example.com:7473 or http://neo4j.databases.svc:7474)" -}}
{{- end -}}
{{- if not (regexMatch "^https?://[^/]+" $url) -}}
{{- fail (printf "neo4j.external.url must start with http:// or https://, got %q" $url) -}}
{{- end -}}
{{- if not .Values.neo4j.external.existingSecret -}}
{{- fail "neo4j.mode=external needs neo4j.external.existingSecret (a Secret in this namespace holding the password under neo4j.external.passwordKey); the password is never accepted as a chart value" -}}
{{- end -}}
{{- /* plain http is accepted only inside the cluster or on loopback, unless the operator opts in explicitly */ -}}
{{- if and (hasPrefix "http://" $url) (not .Values.neo4j.allowInsecureHttp) (not (regexMatch "^http://(localhost|127\\.[0-9.]+|\\[::1\\]|[^./:]+|[^/:]+\\.svc(\\.[^/:]+)?|[^/:]+\\.cluster\\.local)(:[0-9]+)?(/.*)?$" $url)) -}}
{{- fail (printf "neo4j.external.url %q is plain http to a host outside the cluster: the password and the topology would cross the network in clear text. Use https://, an in-cluster address (svc / cluster.local), or set neo4j.allowInsecureHttp=true if you accept that." $url) -}}
{{- end -}}
{{- end -}}
{{- if and (eq .Values.neo4j.mode "bundled") .Values.neo4j.auth.existingSecret (not .Values.neo4j.auth.passwordKey) -}}
{{- fail "neo4j.auth.passwordKey must be set when neo4j.auth.existingSecret is" -}}
{{- end -}}
{{- if and .Values.backup.volumeSnapshot.enabled (not (regexMatch "^[^ ]+( [^ ]+){4}$" .Values.backup.volumeSnapshot.schedule)) -}}
{{- fail "backup.volumeSnapshot.schedule must be a 5-field cron expression" -}}
{{- end -}}
{{- end -}}

{{/* FUSION. These names mirror the ikhnos-fusion subchart's own (its "fusion.name" and "fusion.central"
     helpers) and the server's Go fusionName: the Role below grants exactly these objects, and a test renders both
     charts together to catch drift. */}}
{{- define "ikhnos.fusionName" -}}
{{- if contains "fusion" .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-fusion" .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* Non-empty when the server is allowed to switch FUSION on and off, i.e. it needs a Kubernetes API token. */}}
{{- define "ikhnos.fusionControl" -}}
{{- if and .Values.fusion.enabled .Values.fusionControl.enabled -}}true{{- end -}}
{{- end -}}

{{/* ---- Node probes (see values.yaml nodeProbe) ---- */}}
{{- define "ikhnos.probeName" -}}{{- printf "%s-probe" (include "ikhnos.fullname" .) | trunc 63 | trimSuffix "-" -}}{{- end -}}

{{/* The shared secret: the one given, else the one already in the cluster, else a new random one - so an upgrade never
     changes it (the pods read it only at start) and nobody types it. */}}
{{- define "ikhnos.probeSecret" -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace (include "ikhnos.probeName" .) -}}
{{- if .Values.nodeProbe.secret -}}{{- .Values.nodeProbe.secret -}}
{{- else if and $existing $existing.data (hasKey $existing.data "secret") -}}{{- index $existing.data "secret" | b64dec -}}
{{- else -}}{{- randAlphaNum 48 -}}
{{- end -}}
{{- end -}}
{{- define "ikhnos.probeSecretRevision" -}}{{ .Values.nodeProbe.secret | default "generated" | sha256sum }}{{- end -}}

{{/* The probes' image, as an image block for ikhnos.imageRef / ikhnos.imagePullPolicy. */}}
{{- define "ikhnos.probeImage" -}}
{{- $i := .Values.nodeProbe.image -}}
{{- $repo := $i.repository -}}
{{- if not $repo -}}
{{- $repo = ternary (printf "%s/ikhnos" .Values.agentInstall.imageRegistry) "ikhnos/ikhnos" (not (empty .Values.agentInstall.imageRegistry)) -}}
{{- end -}}
{{- $tag := $i.tag | default .Values.agentInstall.imageTag | default .Chart.AppVersion | toString -}}
{{- dict "repository" $repo "tag" $tag "digest" $i.digest "pullPolicy" $i.pullPolicy | toJson -}}
{{- end -}}
