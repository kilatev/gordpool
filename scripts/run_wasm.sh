#!/usr/bin/env bash
set -euo pipefail

# Build the WASM bundle and serve the web/ folder.
# Usage: scripts/run_wasm.sh [port]

PORT="${1:-8080}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="${ROOT}/web"

echo "Building wasm → ${WEB_DIR}/app.wasm"
GOOS=js GOARCH=wasm go build -o "${WEB_DIR}/app.wasm" "${ROOT}/cmd/web"

# Locate wasm_exec.js (snap Go puts it under lib/wasm).
GOROOT="$(go env GOROOT)"
if [[ -f "${GOROOT}/misc/wasm/wasm_exec.js" ]]; then
  WASM_EXEC_SRC="${GOROOT}/misc/wasm/wasm_exec.js"
elif [[ -f "${GOROOT}/lib/wasm/wasm_exec.js" ]]; then
  WASM_EXEC_SRC="${GOROOT}/lib/wasm/wasm_exec.js"
else
  echo "wasm_exec.js not found under ${GOROOT}/misc/wasm or ${GOROOT}/lib/wasm" >&2
  exit 1
fi

echo "Copying wasm_exec.js from ${WASM_EXEC_SRC}"
cp "${WASM_EXEC_SRC}" "${WEB_DIR}/wasm_exec.js"

echo "Serving UI + proxy at http://localhost:${PORT}"
exec go run "${ROOT}/cmd/serve" -web "${WEB_DIR}" -listen ":${PORT}"
