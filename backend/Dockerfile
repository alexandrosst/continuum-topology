# Two images come out of this file. Build from the repository root:
#   docker build -f backend/Dockerfile --target continuum -t continuum/continuum:0.1.0-dev .   # everything that runs in a cluster
#   docker build -f backend/Dockerfile --target server    -t continuum/server:0.1.0-dev .      # the control plane and the UI
# Multi-arch (one tag that runs on x86 servers and ARM boards; scripts/publish.sh does this and prints the digest):
#   docker buildx build -f backend/Dockerfile --platform linux/amd64,linux/arm64 --target continuum -t REGISTRY/continuum:0.1.0-dev --push .
#
# The `continuum` image holds ONE binary, /continuum, whose first argument picks the role: `agent` (the in-cluster agent),
# `probe` (the optional node probe) or `flow` (the optional node flow collector). The ENTRYPOINT is just the binary and there is
# no default role: the Helm chart chooses it with the container's `args:` (["agent", ...], ["probe", ...], ["flow", ...]), and
# `docker run IMAGE flow --print 30s` (or `agent --help`) works the same way. The image's default user is the distroless
# nonroot user (65532), which is what the agent and the probe run as. The flow collector loads an eBPF program, which needs
# root with CAP_BPF/CAP_PERFMON: the chart's flow DaemonSet says so explicitly (runAsUser: 0 plus exactly those capabilities),
# because that decision belongs in the pod spec the cluster owner reads, not hidden in the image. The former per-role image targets
# (agent, probe, flow) are gone: use --target continuum and pick the role by argument.
#
# The build context is the repository root. The root .dockerignore keeps node_modules, dist, .git, screenshots (shots*) and logs
# out of it; the UI stage below needs package.json, package-lock.json, index.html, vite.config.ts, the tsconfig files, public/ and
# src/, and the Go stage needs backend/, so do not add those to .dockerignore.
#
# The server image (--target server) holds the control plane and the built UI, runs as uid/gid 65532 and keeps all state in
# /data (SQLite, and the enrollment CA whose key every agent trusts: mount a persistent volume there). Its ports are 8443 (agents,
# mutual TLS with the server's own CA: never terminate that at an L7 proxy) and 8080 (admin API and UI, plain HTTP inside the
# container). Kubernetes: deploy/helm/continuum-server. Plain Docker, for a trial on one host:
#   docker run -d --name continuum -v continuum-data:/data -p 8443:8443 -p 8080:8080 continuum/server:0.1.0-dev \
#     --agent-address=HOST_OR_IP:8443 --admin-behind-tls-proxy --registration invite
# (only put port 8080 on a network you trust, or behind a TLS proxy: --admin-behind-tls-proxy is your statement that it is.)
#
# It also ships a small offline GeoIP database by default (see the `geoip` stage below), so location
# suggestions for a public connecting address work with no setup; --geoip-db / the chart's geoip.path
# override it with a different or newer one, same as before this existed.

FROM --platform=$BUILDPLATFORM node:22-alpine AS ui
WORKDIR /ui
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

# A small, free, CC BY 4.0 city database baked into the server image so location suggestions (see --geoip-db)
# work the moment the cluster's connecting address is a public one - nobody has to find, download and mount
# a file first. Pulled at build time from the stable CDN URL that DB-IP-City-Lite's own pipeline
# (github.com/wp-statistics/DbIP-City-lite) refreshes on the 1st of every month, so a rebuild always picks up
# a recent snapshot without this file pinning a version. It is architecture-independent data, not a binary,
# so this runs once on the build host (--platform=$BUILDPLATFORM) rather than once per target arch. A build
# host with no route to jsdelivr.net fails here; either give it one or drop this stage and the COPY/ENV below
# that reference it, then set --geoip-db / geoip.path yourself as before. --geoip-db (and the chart's
# geoip.path) always override this default with a different or newer file - MaxMind GeoLite2, a paid tier,
# whatever the deployment wants instead.
FROM --platform=$BUILDPLATFORM alpine:3.20 AS geoip
RUN apk add --no-cache curl && mkdir -p /geoip \
 && curl -fSL https://cdn.jsdelivr.net/npm/dbip-city-lite/dbip-city-lite.mmdb.gz | gunzip > /geoip/dbip-city-lite.mmdb

# The Go build always runs natively and cross-compiles, so a multi-arch build (amd64 for servers, arm64 for
# boards) does not go through emulation.
FROM --platform=$BUILDPLATFORM golang:1.25 AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
ARG VERSION=0.1.0-dev
# CHART_VERSION is the continuum-agent chart version this same pipeline run publishes to the registry (edge:
# 0.0.0-edge.<sha>; a tagged release: the tag) - baked only into the server binary, which is the only one that ever
# prints a `--version` flag for that chart. Left unset (the default, e.g. a local `docker build`), the server falls
# back to the chart's own Chart.yaml literal instead - see Admin.agentChartVersion's doc comment for why the two
# can disagree for a CI-published image.
ARG CHART_VERSION=
ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/continuum ./cmd/continuum \
 && go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.agentChartVersion=${CHART_VERSION}" -o /out/server ./cmd/server

# The default user of this base image is 65532 (nonroot); nothing here overrides it, so a pod that needs root (the flow
# collector) asks for it in its own securityContext.
FROM gcr.io/distroless/static-debian12:nonroot AS continuum
COPY --from=build /out/continuum /continuum
ENTRYPOINT ["/continuum"]

# An empty /data owned by the runtime user: the distroless image has no shell to create it, and the server needs to write there
# (it does MkdirAll on the data directory and creates pki/ and continuum.db inside). When a volume is mounted over it, Kubernetes
# fsGroup (the chart sets 65532) or the volume's own ownership decides who can write instead; an empty named Docker volume
# inherits this ownership.
FROM build AS serverdata
RUN mkdir -p /out/data && chmod 0700 /out/data

FROM gcr.io/distroless/static-debian12:nonroot AS server
COPY --from=build /out/server /server
COPY --from=ui /ui/dist /ui
COPY --from=serverdata --chown=65532:65532 /out/data /data
COPY --from=geoip --chown=65532:65532 /geoip /geoip
USER 65532:65532
EXPOSE 8443 8080
# CONTINUUM_GEOIP_DB is only a default: an explicit --geoip-db flag (Kubernetes args:, or docker run IMAGE
# --geoip-db=...) always wins over it, same as any env-defaulted flag - see main.go. That is how the chart's
# geoip.path, when someone sets it, replaces this bundled database with their own rather than needing to know
# this path exists at all.
ENV CONTINUUM_GEOIP_DB=/geoip/dbip-city-lite.mmdb
# ENTRYPOINT fixes where things live. Its --admin-listen=0.0.0.0:8080 makes the admin port reachable from outside the container, and
# the server then REFUSES to start unless it is given --admin-tls-cert/--admin-tls-key, or --admin-behind-tls-proxy to state that a
# TLS-terminating proxy protects the port (see the docker run example above; the Helm chart does this for you).
ENTRYPOINT ["/server", "--data-dir=/data", "--ui-dir=/ui", "--admin-listen=0.0.0.0:8080"]
# Default arguments, used only when the container is started without any (Kubernetes `args:` and `docker run IMAGE --flag` replace
# them). --agent-address is required and there is no correct default for it, so this starts a server that is safe but not useful:
# it answers on loopback inside the container only and hands agents an address they cannot reach. Give real arguments.
CMD ["--agent-address=localhost:8443", "--admin-listen=127.0.0.1:8080"]
