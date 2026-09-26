#!/usr/bin/env bash
# Regenerates backend/gen/continuumv1/*.pb.go from backend/proto/continuum/v1/agent.proto.
#
# Run from anywhere; this script locates the backend module root itself:
#   backend/proto/generate.sh
#
# Requires on PATH:
#   protoc            (tested against v29.3; any recent 3.x/4.x release works)
#   protoc-gen-go      v1.36.11  (go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11)
#   protoc-gen-go-grpc v1.5.1    (go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1)
#
# The versions above match what is recorded in the header comments of the currently checked-in
# generated files (gen/continuumv1/*.pb.go). Using different versions is not an error, but it will
# show up as unrelated churn in `git diff` beyond the schema change you actually meant to make -
# match them when you can.
#
# protoc needs to resolve "google/protobuf/timestamp.proto" (a well-known type agent.proto imports).
# Most protoc installs (Homebrew, apt, the GitHub release zips) ship it under <prefix>/include next
# to the protoc binary itself; this script finds it there automatically. If your install doesn't
# have it in that layout, set PROTOC_INCLUDE to the directory that contains the top-level "google/"
# folder before running this script.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
backend_dir="$(cd "$script_dir/.." && pwd)"

for tool in protoc protoc-gen-go protoc-gen-go-grpc; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "generate.sh: $tool not found on PATH" >&2
    exit 1
  fi
done

if [ -n "${PROTOC_INCLUDE:-}" ]; then
  include_dir="$PROTOC_INCLUDE"
else
  protoc_prefix="$(dirname "$(dirname "$(command -v protoc)")")"
  include_dir="$protoc_prefix/include"
fi

if [ ! -f "$include_dir/google/protobuf/timestamp.proto" ]; then
  echo "generate.sh: could not find google/protobuf/timestamp.proto under $include_dir" >&2
  echo "generate.sh: set PROTOC_INCLUDE to the directory holding the top-level google/ folder" >&2
  exit 1
fi

echo "generate.sh: using protoc include dir: $include_dir"
protoc --version
protoc-gen-go --version
protoc-gen-go-grpc --version

cd "$backend_dir"
protoc \
  --proto_path=proto \
  --proto_path="$include_dir" \
  --go_out=. --go_opt=module=continuum \
  --go-grpc_out=. --go-grpc_opt=module=continuum \
  proto/continuum/v1/agent.proto

echo "generate.sh: wrote gen/continuumv1/agent.pb.go and gen/continuumv1/agent_grpc.pb.go"
