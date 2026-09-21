#!/usr/bin/env bash
# Publish what an install command needs, in one go: ONE container image (the `continuum` image: agent, node probe and flow
# collector are roles of the same binary; x86 and ARM in one tag) and the Helm chart, under one registry namespace. The
# server (once you set the same registry in Settings → Installation, or --image-registry) then prints a command that pulls both, with nothing built or downloaded on the cluster's side.
#
#   scripts/publish.sh myname              # registry namespace `myname` on Docker Hub, tag = the chart's appVersion
#   scripts/publish.sh ghcr.io/me 0.2.0    # another registry / tag (REGISTRY and TAG env work too)
#   DRY_RUN=1 scripts/publish.sh myname    # print what it would run
#   ONLY=image scripts/publish.sh myname   # just the image  (image | chart)
#   ONLY=chart scripts/publish.sh myname   # just the agent chart
#   ONLY=server scripts/publish.sh myname  # the server image (<registry>/server) and the server chart (oci://<registry>/continuum-server)
#
# There is no default registry on purpose: where you publish is your decision, and nothing in the source points at anyone's namespace.
#
# The image is <registry>/continuum:<tag>; the chart is oci://<registry>/continuum-agent. At the end the script prints the
# image's digest (sha256:...). A tag can be moved to a different image later; a digest cannot, so pin installs to it:
# `--set image.digest=sha256:...` (the chart then pulls repository@digest and ignores the tag).
#
# Needs: docker with buildx, helm 3.8+, and `docker login` (Docker Hub, or your registry) done already.
# For Docker Hub the repositories are created public on first push. Keep them public: a cluster pulls the
# image, and helm pulls the chart, without credentials. (Private? see imagePullSecrets in the README.)
#
# Signing (optional, on automatically when cosign is installed): every image and chart this script pushes is
# signed by digest with cosign, keylessly via Sigstore (Fulcio issues a short-lived certificate off an OIDC login -
# GitHub/Google/Microsoft interactively, or the CI provider's own token in Actions/GitLab/etc. - and Rekor logs the
# signature publicly; nothing to generate or store as a long-lived key). A cluster owner then verifies with
# `cosign verify` against the identity that signed it (see "Signing and verifying releases" in deploy/README.md)
# before an admission policy admits it, rather than trusting the registry alone. SIGN=0 skips signing entirely;
# SIGN=1 requires it (fails if cosign is missing) - useful in CI so a broken signer fails the release instead of
# silently shipping unsigned. COSIGN_KEY=cosign.key signs with a key pair instead of keyless, for an air-gapped
# registry with no route to Fulcio/Rekor (see COSIGN_EXPERIMENTAL / --tlog-upload=false in cosign's own docs for
# fully offline signing).
set -euo pipefail
cd "$(dirname "$0")/.."

REG="${1:-${REGISTRY:-}}"
if [[ -z "$REG" ]]; then echo "usage: scripts/publish.sh REGISTRY [TAG]   e.g. scripts/publish.sh myname   (a Docker Hub namespace, or ghcr.io/me, reg.example.com:8443/team)" >&2; exit 2; fi
CHART_DIR=backend/internal/chart/continuum-agent
TAG="${2:-${TAG:-$(sed -n 's/^appVersion:[[:space:]]*"\{0,1\}\([^"]*\)"\{0,1\}[[:space:]]*$/\1/p' "$CHART_DIR/Chart.yaml")}}"
ONLY="${ONLY:-}"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
IMAGE="${REG%/}/continuum"

case "$ONLY" in
  ""|image|chart|server) ;;
  agent|probe|flow) echo "ONLY=$ONLY is gone: agent, probe and flow are one image now. Use ONLY=image or ONLY=chart." >&2; exit 2 ;;
  *) echo "ONLY must be image, chart or server (or empty for everything), not '$ONLY'." >&2; exit 2 ;;
esac

# Where an OCI registry keeps things for this setting: a bare name is a Docker Hub namespace.
oci_base() {
  local r="${1%/}" first
  r="${r#docker.io/}"; r="${r#index.docker.io/}"; r="${r#registry-1.docker.io/}"
  first="${r%%/*}"
  if [[ "$1" == *docker.io/* ]] || { [[ "$first" != *.* && "$first" != *:* && "$first" != localhost ]]; }; then
    echo "registry-1.docker.io/$r"
  else
    echo "$r"
  fi
}

run() { echo "+ $*"; [[ -n "${DRY_RUN:-}" ]] || "$@"; }
want() { [[ -z "$ONLY" || "$ONLY" == "$1" ]]; }

# ---- signing (see the block comment at the top) ----
SIGN="${SIGN:-auto}"
COSIGN_BIN="$(command -v cosign || true)"
case "$SIGN" in
  0|false|no) COSIGN_BIN="" ;;
  1|true|yes) [[ -n "$COSIGN_BIN" ]] || { echo "SIGN=1 but cosign is not installed: https://docs.sigstore.dev/cosign/system_config/installation/" >&2; exit 2; } ;;
  auto) [[ -n "$COSIGN_BIN" || -n "${DRY_RUN:-}" ]] || echo "cosign not found: images and charts will be pushed UNSIGNED. Install cosign (see deploy/README.md, \"Signing and verifying releases\") or set SIGN=1 to require it, SIGN=0 to silence this." >&2 ;;
  *) echo "SIGN must be 0, 1 or auto, not '$SIGN'" >&2; exit 2 ;;
esac
# sign REF@DIGEST - no-ops quietly when cosign is unavailable and signing was not required.
sign() {
  [[ -n "$COSIGN_BIN" ]] || return 0
  local args=(sign --yes)
  [[ -n "${COSIGN_KEY:-}" ]] && args+=(--key "$COSIGN_KEY")
  run "$COSIGN_BIN" "${args[@]}" "$1"
}

echo "registry: $REG   image: $IMAGE:$TAG   chart: $(oci_base "$REG")/continuum-agent"

DIGEST=""
if want image; then
  meta="$(mktemp)"
  run docker buildx build -f backend/Dockerfile --platform "$PLATFORMS" --target continuum -t "$IMAGE:$TAG" --metadata-file "$meta" --push .
  if [[ -n "${DRY_RUN:-}" ]]; then
    rm -f "$meta"
    DIGEST="sha256:<the digest of the pushed image>"
  else
    # buildx writes the digest of what it pushed (for a multi-arch build: of the manifest list) into the metadata file.
    DIGEST="$(sed -n 's/.*"containerimage.digest"[[:space:]]*:[[:space:]]*"\(sha256:[0-9a-f]\{64\}\)".*/\1/p' "$meta" | head -n1)"
    rm -f "$meta"
    if [[ -z "$DIGEST" ]]; then
      echo "The image was pushed, but buildx did not report its digest. Read it with:" >&2
      echo "  docker buildx imagetools inspect $IMAGE:$TAG    (the line that starts with Digest:)" >&2
    fi
  fi
  [[ -n "$DIGEST" && "$DIGEST" != sha256:\<* ]] && sign "$IMAGE@$DIGEST"
fi

# chart_push CHART_DIR OCI_BASE CHART_NAME - packages and pushes a chart, then signs it by digest: `helm push`
# itself has no signing step, but the pushed chart is an ordinary OCI artifact, so cosign signs it exactly like a
# container image once we know what digest it landed at (parsed from helm push's own "Digest: sha256:..." line).
chart_push() {
  local dir="$1" base="$2" name="$3" out digest
  out="$(mktemp -d)"
  run helm package "$dir" -d "$out"
  if [[ -n "${DRY_RUN:-}" ]]; then
    run helm push "$out"/"$name"-*.tgz "oci://$base"
    return 0
  fi
  local log; log="$(mktemp)"
  echo "+ helm push $out/$name-*.tgz oci://$base"
  helm push "$out/$name"-*.tgz "oci://$base" 2>&1 | tee "$log"
  digest="$(sed -n 's/^Digest:[[:space:]]*\(sha256:[0-9a-f]\{64\}\).*$/\1/p' "$log" | head -n1)"
  rm -f "$log"
  if [[ -n "$digest" ]]; then
    sign "$base/$name@$digest"
  else
    echo "The chart was pushed, but its digest could not be parsed from helm's output, so it was not signed. Read it with:" >&2
    echo "  helm show chart oci://$base/$name --version <version>   (or: crane digest oci://$base/$name:<version>)" >&2
  fi
}

if want chart; then
  chart_push "$CHART_DIR" "$(oci_base "$REG")" continuum-agent
fi

# The server (control plane + UI) is a separate image and chart. It runs in ONE place, so amd64 only by default
# (SERVER_PLATFORMS=linux/amd64,linux/arm64 for a Raspberry Pi control plane).
SERVER_TAG="${SERVER_TAG:-$(sed -n 's/^appVersion:[[:space:]]*"\{0,1\}\([^"]*\)"\{0,1\}[[:space:]]*$/\1/p' deploy/helm/continuum-server/Chart.yaml)}"
SERVER_DIGEST=""
if want server; then
  meta="$(mktemp)"
  run docker buildx build -f backend/Dockerfile --platform "${SERVER_PLATFORMS:-linux/amd64}" --target server -t "${REG%/}/server:$SERVER_TAG" --metadata-file "$meta" --push .
  if [[ -n "${DRY_RUN:-}" ]]; then
    rm -f "$meta"; SERVER_DIGEST="sha256:<the digest of the pushed server image>"
  else
    SERVER_DIGEST="$(sed -n 's/.*"containerimage.digest"[[:space:]]*:[[:space:]]*"\(sha256:[0-9a-f]\{64\}\)".*/\1/p' "$meta" | head -n1)"
    rm -f "$meta"
  fi
  [[ -n "$SERVER_DIGEST" && "$SERVER_DIGEST" != sha256:\<* ]] && sign "${REG%/}/server@$SERVER_DIGEST"
  chart_push deploy/helm/continuum-server "$(oci_base "$REG")" continuum-server
fi

echo
echo "Done. The server prints commands that pull from here when started with:  --image-registry $REG"
echo "Check:  helm show chart oci://$(oci_base "$REG")/continuum-agent --version <chart version>"
if [[ -n "$DIGEST" ]]; then
  echo
  echo "=================================================================================="
  echo "  image   $IMAGE:$TAG"
  echo "  digest  $DIGEST"
  echo "=================================================================================="
  echo "Pin an install to exactly this image (the tag is then ignored):"
  echo "  --set image.repository=$IMAGE --set image.digest=$DIGEST"
fi
if [[ -n "$SERVER_DIGEST" ]]; then
  echo
  echo "Server:  helm install continuum-server oci://$(oci_base "$REG")/continuum-server --version <chart version> -n continuum --create-namespace \\"
  echo "           --set image.repository=${REG%/}/server --set image.digest=$SERVER_DIGEST ..."
  echo "         (the rest of the values: deploy/README.md)"
fi
if [[ -n "$COSIGN_BIN" ]]; then
  echo
  echo "Signed with cosign. A cluster owner verifies before trusting what was just pushed, e.g.:"
  echo "  cosign verify --certificate-identity-regexp '.*' --certificate-oidc-issuer-regexp '.*' $IMAGE@$DIGEST"
  echo "(see \"Signing and verifying releases\" in deploy/README.md for a real identity/issuer to pin to, and the admission-policy example)."
else
  echo
  echo "NOT signed (cosign not found or SIGN=0). See \"Signing and verifying releases\" in deploy/README.md."
fi
