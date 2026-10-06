import type { QuickStartBackend, QuickStartKind } from './history'
import { hasQuickStartSpec } from './quickStartBackends'

/**
 * Part C of the quick-start gateway: a plain Kubernetes manifest (not a new Ikhnos-maintained Helm
 * chart - just YAML, the same "mechanism only" shape quickStartBackends.ts already uses for the `helm
 * install` commands) for a minimal nginx:alpine reverse-proxy Deployment/Service/ConfigMap, in the same
 * namespace as the quick-start install, that gates access to the already-installed tool: it checks
 * `Authorization: Bearer <token>` against the token minted server-side (see the gateway-token endpoint in
 * admin_quickstart.go) entirely with a local nginx `if` block - no callback to this server, ever, the same
 * one-way trust model as everything else quick-start.
 *
 * Only jaeger/zipkin/prometheus/loki have a manifest here (see hasGatewayManifest): "custom" names no known
 * upstream chart, so there is no Service/port this app can safely assume - a custom backend's own access
 * control is the admin's to set up, same as everything else about it.
 *
 * The proxy target for each kind is exactly the Service name/port its own `helm install` in
 * quickStartBackends.ts already creates (see that file's own exportEndpoint/portForward comments, which
 * this mirrors rather than re-deriving):
 *   - jaeger: the jaeger-quickstart Service's http-query port (16686) - the query UI/API.
 *   - zipkin: the zipkin-quickstart Service's port 9411 - the Zipkin UI and API.
 *   - prometheus: the prometheus-quickstart-server Service's port 80 (proxying to the container's 9090) -
 *     the web UI/API.
 *   - loki: the loki-quickstart Service's port 3100 - its HTTP API (Loki itself has no UI).
 * None of the three terminates TLS or does anything else that would conflict with a plain HTTP reverse
 * proxy in front of them, so one template covers all three without per-kind adaptation.
 */
interface GatewayTarget {
  service: string
  port: number
}

const GATEWAY_TARGET: Partial<Record<QuickStartKind, GatewayTarget>> = {
  jaeger: { service: 'jaeger-quickstart', port: 16686 },
  zipkin: { service: 'zipkin-quickstart', port: 9411 },
  prometheus: { service: 'prometheus-quickstart-server', port: 80 },
  loki: { service: 'loki-quickstart', port: 3100 },
}

/** Whether `kind` has a gateway manifest at all - true for jaeger/zipkin/prometheus/loki, false for "custom"
 * (see this file's own doc comment) and false for any future catalog kind this file has not been taught
 * a proxy target for yet (hasQuickStartSpec alone is not enough - a new catalog entry needs its own
 * GATEWAY_TARGET row before a manifest can be generated for it). */
export function hasGatewayManifest(kind: QuickStartKind): boolean {
  return hasQuickStartSpec(kind) && kind in GATEWAY_TARGET
}

/** The gateway's own resource name - stable per kind (there is at most one saved backend per built-in
 * kind, see QuickStartBackends.tsx), so re-generating the manifest after a token is re-minted updates the
 * same ConfigMap/Deployment/Service instead of creating new ones. */
export function gatewayName(kind: QuickStartKind): string {
  return `${kind}-gateway`
}

const indent = (text: string, spaces: number) => {
  const pad = ' '.repeat(spaces)
  return text
    .split('\n')
    .map((l) => (l ? pad + l : l))
    .join('\n')
}

/**
 * The `kubectl apply -f -` heredoc for this backend's gateway, gating it with `token` (the plaintext from
 * the gateway-token endpoint, templated into the ConfigMap right after minting - the same trust level as
 * the regional-operator receiver secret, which is also shown once and pasted into a chart's own values).
 * Throws if `hasGatewayManifest(backend.kind)` is false - callers check that first (see
 * QuickStartBackends.tsx), the same convention quickStartSpec() uses for an unknown kind.
 */
export function gatewayManifest(backend: QuickStartBackend, token: string): string {
  const target = GATEWAY_TARGET[backend.kind]
  if (!target) throw new Error(`no gateway manifest for quick-start kind ${backend.kind}`)
  const name = gatewayName(backend.kind)
  const ns = backend.namespace
  const upstream = `${target.service}.${ns}.svc.cluster.local:${target.port}`

  // A static (non-variable) proxy_pass host is resolved once, at startup, via the cluster's own DNS - the
  // Service this points at only needs to exist (which `helm install`/`helm upgrade --install` creates
  // immediately, before any of its pods are ready), not already be serving traffic, so applying this right
  // after the install command above is fine.
  const nginxConf = `server {
  listen 8080;
  location / {
    set $gateway_auth 0;
    if ($http_authorization = "Bearer ${token}") {
      set $gateway_auth 1;
    }
    if ($gateway_auth = 0) {
      return 401;
    }
    proxy_pass http://${upstream}/;
    proxy_set_header Host $host;
    proxy_http_version 1.1;
    proxy_read_timeout 300s;
  }
}
`

  const yaml = `# Gates ${target.service}.${ns} behind a bearer token this nginx checks itself - Ikhnos never sees
# this traffic and never dials ${target.service} directly. Run the install command above first (it
# creates the Service this proxies to), then this one.
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${name}-config
  namespace: ${ns}
data:
  default.conf: |
${indent(nginxConf, 4)}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${name}
  namespace: ${ns}
  labels:
    app: ${name}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ${name}
  template:
    metadata:
      labels:
        app: ${name}
    spec:
      containers:
        - name: nginx
          image: nginx:alpine
          ports:
            - containerPort: 8080
          volumeMounts:
            - name: config
              mountPath: /etc/nginx/conf.d/default.conf
              subPath: default.conf
          resources:
            requests: { cpu: 10m, memory: 16Mi }
            limits: { cpu: 200m, memory: 64Mi }
      volumes:
        - name: config
          configMap:
            name: ${name}-config
---
apiVersion: v1
kind: Service
metadata:
  name: ${name}
  namespace: ${ns}
spec:
  selector:
    app: ${name}
  ports:
    - port: 8080
      targetPort: 8080
`
  return `kubectl apply -f - <<'EOF'\n${yaml}EOF`
}

/** The `kubectl port-forward` equivalent for the gateway itself, in place of the raw tool's own (see
 * quickStartSpec(kind).portForward) - once a token is set, this is what "reach it locally" means. */
export function gatewayPortForward(backend: QuickStartBackend, localPort = 8080): string {
  const name = gatewayName(backend.kind)
  return `kubectl -n ${backend.namespace} port-forward svc/${name} ${localPort}:8080`
}
