#!/usr/bin/env bash
# Every fuzz target in the Go backend, as a JSON array for a workflow matrix: [{"pkg":"./internal/install","name":"FuzzX"}, ...].
# Found by looking, not listed by hand, so a target added tomorrow is run next Sunday without anyone editing a workflow.
# With an argument, only the target of that name (the workflow's manual run); it is an error if there is none, because a typo that
# fuzzes nothing and reports green is the failure this exists to avoid.
set -euo pipefail
cd "$(dirname "$0")/../backend"
all=$(grep -rn --include='*_test.go' -E '^func Fuzz[A-Za-z0-9_]*\(' . |
  sed -E 's|^\./(.*)/[^/:]+:[0-9]+:func (Fuzz[A-Za-z0-9_]*)\(.*|{"pkg":"./\1","name":"\2"}|' |
  sort | jq -cs '.')
if [ -n "${1:-}" ]; then
  all=$(jq -c --arg n "$1" '[.[] | select(.name == $n)]' <<<"$all")
fi
if [ "$(jq 'length' <<<"$all")" -eq 0 ]; then
  echo "no fuzz target${1:+ named $1} found" >&2
  exit 1
fi
echo "$all"
