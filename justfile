# Install the Python app and browser automation dependencies.
setup:
    cd python && uv sync --extra browser

# Start Chromium, the report API, and the price scheduler.
run:
    #!/usr/bin/env bash
    set -euo pipefail
    cd python
    chromium=${CHROMIUM:-chromium}
    profile=${SOLAX_BROWSER_PROFILE:-"$PWD/state/chromium"}
    cdp_port=${SOLAX_CDP_PORT:-9222}
    port=${PORT:-8001}
    export SOLAX_CONTROL_ENABLED=1
    export SOLAX_SERIAL=H34A15J7S18051
    export UV_CACHE_DIR="${UV_CACHE_DIR:-$PWD/state/uv-cache}"
    command -v uv >/dev/null || { echo "uv is required; run just setup" >&2; exit 1; }
    command -v "$chromium" >/dev/null || { echo "Chromium not found: $chromium" >&2; exit 1; }
    mkdir -p "$profile" state
    browser_pid=
    api_pid=
    scheduler_pid=
    cleanup() {
      for pid in "$scheduler_pid" "$api_pid" "$browser_pid"; do
        [ -z "$pid" ] || kill "$pid" 2>/dev/null || true
      done
      wait 2>/dev/null || true
    }
    trap cleanup EXIT INT TERM
    "$chromium" --no-first-run --no-default-browser-check \
      --remote-debugging-address=127.0.0.1 --remote-debugging-port="$cdp_port" \
      --user-data-dir="$profile" https://solaxcloud.com/user-center/ &
    browser_pid=$!
    uv run --extra browser uvicorn solax_optimizer.api:app --host 127.0.0.1 --port "$port" &
    api_pid=$!
    SOLAX_CDP_URL="${SOLAX_CDP_URL:-http://127.0.0.1:$cdp_port}" \
      uv run --extra browser python -m solax_optimizer.scheduler &
    scheduler_pid=$!
    echo "Report: http://127.0.0.1:$port/"
    echo "SolaX login: Chromium; CDP: http://127.0.0.1:$cdp_port"
    wait -n "$browser_pid" "$api_pid" "$scheduler_pid"
