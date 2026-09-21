{{/* Names. "continuum-server" is the chart name; a release called continuum-server (or containing it) is not repeated. */}}
{{- define "continuum.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "continuum.fullname" -}}
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

{{- define "continuum.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Labels shared by everything; the component is added per object. */}}
{{- define "continuum.labels" -}}
helm.sh/chart: {{ include "continuum.chart" . }}
app.kubernetes.io/name: {{ include "continuum.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/part-of: continuum
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/* What a Service, a NetworkPolicy or a Deployment selects: the server pod. Never changes after install (selectors are immutable). */}}
{{- define "continuum.selectorLabels" -}}
app.kubernetes.io/name: {{ include "continuum.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: server
{{- end -}}

{{- define "continuum.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "continuum.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Image reference: repository@digest when a digest is set (the tag is then ignored), else repository:tag.
     Call with (dict "image" .Values.image "defaultTag" .Chart.AppVersion). */}}
{{- define "continuum.imageRef" -}}
{{- if .image.digest -}}
{{- printf "%s@%s" .image.repository .image.digest -}}
{{- else -}}
{{- printf "%s:%s" .image.repository (.image.tag | default .defaultTag | toString) -}}
{{- end -}}
{{- end -}}

{{/* PVC that holds /data (SQLite, the CA and its key). */}}
{{- define "continuum.dataClaim" -}}
{{- default (printf "%s-data" (include "continuum.fullname" .)) .Values.persistence.existingClaim -}}
{{- end -}}

{{/* The admin listener is plain HTTP unless a certificate is mounted. */}}
{{- define "continuum.adminTLS" -}}{{- if .Values.admin.tls.secretName -}}true{{- end -}}{{- end -}}

{{/* --admin-behind-tls-proxy: explicit value wins; otherwise on when this chart creates an Ingress or HTTPRoute
     (a TLS-terminating proxy is then in front by construction). Renders "true" or nothing. */}}
{{- define "continuum.behindProxy" -}}
{{- $v := .Values.admin.behindTlsProxy -}}
{{- if kindIs "bool" $v -}}
{{- if $v -}}true{{- end -}}
{{- else if or .Values.ui.ingress.enabled .Values.httproute.enabled -}}true{{- end -}}
{{- end -}}

{{/* ---- Neo4j ---- */}}
{{- define "continuum.neo4jName" -}}{{- printf "%s-neo4j" (include "continuum.fullname" .) | trunc 63 | trimSuffix "-" -}}{{- end -}}

{{- define "continuum.neo4jSelectorLabels" -}}
app.kubernetes.io/name: {{ include "continuum.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: neo4j
{{- end -}}

{{/* Neo4j is mandatory (mode is always bundled or external; validated below), so this is always true. Kept as its
own helper because several templates read it rather than repeating the mode check. */}}
{{- define "continuum.neo4jEnabled" -}}true{{- end -}}

{{/* Where the server finds Neo4j. */}}
{{- define "continuum.neo4jURL" -}}
{{- if eq .Values.neo4j.mode "bundled" -}}
{{- printf "http://%s.%s.svc:7474" (include "continuum.neo4jName" .) .Release.Namespace -}}
{{- else if eq .Values.neo4j.mode "external" -}}
{{- .Values.neo4j.external.url -}}
{{- end -}}
{{- end -}}

{{/* Secret and key that hold the Neo4j password (the server reads it as a file; bundled Neo4j reads it as NEO4J_AUTH). */}}
{{- define "continuum.neo4jSecretName" -}}
{{- if eq .Values.neo4j.mode "external" -}}
{{- .Values.neo4j.external.existingSecret -}}
{{- else if .Values.neo4j.auth.existingSecret -}}
{{- .Values.neo4j.auth.existingSecret -}}
{{- else -}}
{{- printf "%s-neo4j-auth" (include "continuum.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "continuum.neo4jSecretKey" -}}
{{- if eq .Values.neo4j.mode "external" -}}
{{- .Values.neo4j.external.passwordKey -}}
{{- else if .Values.neo4j.auth.existingSecret -}}
{{- .Values.neo4j.auth.passwordKey -}}
{{- else -}}password
{{- end -}}
{{- end -}}

{{/* Secret and key that hold the CA key passphrase: an explicit existing Secret (BYO, e.g. from an external secret
manager) if one is named, this chart's own auto-generated one otherwise (only when pki.encryptAtRest is true), or
nothing at all when the passphrase is turned off outright. Empty means "no passphrase": the deployment template
checks for that and never mounts anything or passes --ca-key-passphrase-file in that case. */}}
{{- define "continuum.caPassphraseSecretName" -}}
{{- if .Values.pki.caKeyPassphraseSecret.name -}}
{{- .Values.pki.caKeyPassphraseSecret.name -}}
{{- else if .Values.pki.encryptAtRest -}}
{{- printf "%s-ca-passphrase" (include "continuum.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "continuum.caPassphraseSecretKey" -}}
{{- if .Values.pki.caKeyPassphraseSecret.name -}}
{{- .Values.pki.caKeyPassphraseSecret.key -}}
{{- else -}}passphrase
{{- end -}}
{{- end -}}

{{/* Port of the external Neo4j URL (explicit, or the scheme's default). */}}
{{- define "continuum.neo4jExternalPort" -}}
{{- $u := urlParse .Values.neo4j.external.url -}}
{{- $p := regexFind ":[0-9]+$" (get $u "host") | trimPrefix ":" -}}
{{- if $p -}}{{- $p -}}{{- else if eq (get $u "scheme") "https" -}}443{{- else -}}80{{- end -}}
{{- end -}}

{{/* ---- Validation. Every failure says what to set. Rendered from templates/validate.yaml (which produces no object). ---- */}}
{{- define "continuum.validate" -}}
{{- /* agent.publicAddress */ -}}
{{- $addr := .Values.agent.publicAddress -}}
{{- if not $addr -}}
{{- fail "agent.publicAddress is required: the host:port your agents dial to reach this server, for example continuum.example.com:8443 (or NODE_IP:30443 with agent.service.type=NodePort). It also becomes a name in the server certificate, so it must be exactly what agents use." -}}
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
{{- if and (not (include "continuum.adminTLS" .)) (not (include "continuum.behindProxy" .)) -}}
{{- fail "the admin listener (UI and API) carries sign-in passwords and the session cookie, and the server refuses to serve it in clear text without TLS. Choose one: enable ui.ingress (or httproute) so a proxy terminates TLS (and leave admin.behindTlsProxy unset); or set admin.tls.secretName to a kubernetes.io/tls Secret; or, if you put your own TLS in front (or only use kubectl port-forward to localhost), set admin.behindTlsProxy=true." -}}
{{- end -}}
{{- if and .Values.admin.tls.secretName (not .Values.admin.tls.certKey) -}}{{- fail "admin.tls.certKey must not be empty when admin.tls.secretName is set" -}}{{- end -}}
{{- /* exposure objects */ -}}
{{- if and .Values.ui.ingress.enabled (not .Values.ui.ingress.hosts) -}}
{{- fail "ui.ingress.enabled needs at least one entry in ui.ingress.hosts" -}}
{{- end -}}
{{- if and .Values.httproute.enabled (not .Values.httproute.parentRefs) -}}
{{- fail "httproute.enabled needs httproute.parentRefs (the Gateway to attach to)" -}}
{{- end -}}
{{- if and .Values.agent.ingress.enabled (not .Values.agent.ingress.host) -}}
{{- fail "agent.ingress.enabled needs agent.ingress.host (the name agents dial; ssl-passthrough routes on it)" -}}
{{- end -}}
{{- if and .Values.agent.tlsRoute.enabled (or (not .Values.agent.tlsRoute.parentRefs) (not .Values.agent.tlsRoute.hostnames)) -}}
{{- fail "agent.tlsRoute.enabled needs agent.tlsRoute.parentRefs and agent.tlsRoute.hostnames" -}}
{{- end -}}
{{- /* pki */ -}}
{{- if and .Values.pki.caKeyPassphraseSecret.name (not .Values.pki.caKeyPassphraseSecret.key) -}}
{{- fail "pki.caKeyPassphraseSecret.key must be set together with .name" -}}
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
