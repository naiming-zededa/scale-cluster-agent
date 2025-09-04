#!/usr/bin/env bash
# create_clusters.sh
# Usage:
#   ./create_clusters.sh 7
#   ./create_clusters.sh 1 12
# Environment:
#   API_URL (default http://localhost:9090)
#   PREFIX  (default test-cluster)

set -euo pipefail

API_URL="${API_URL:-http://localhost:9090}"
PREFIX="${PREFIX:-test-cluster}"

usage() {
  echo "Usage: $0 <n> [m]"
  echo "  Create one cluster (n) or a range n..m (inclusive)."
  exit 1
}

# Validate args
if [[ $# -lt 1 || $# -gt 2 ]]; then
  usage
fi
re='^[0-9]+$'
if ! [[ $1 =~ $re ]]; then
  echo "First argument must be a number" >&2; exit 2
fi
START="$1"
END="$1"
if [[ $# -eq 2 ]]; then
  if ! [[ $2 =~ $re ]]; then
    echo "Second argument must be a number" >&2; exit 2
  fi
  END="$2"
fi

if (( START > END )); then
  echo "Start > End" >&2; exit 2
fi

for (( i=START; i<=END; i++ )); do
  num=$(printf '%03d' "$i")
  name="${PREFIX}-${num}"
  echo "Creating cluster ${name}..."
  resp=$(curl -sS -X POST "${API_URL}/clusters" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"${name}\"}" ) || {
      echo "Request failed for ${name}" >&2
      continue
    }
  echo "Response: $resp"
done

