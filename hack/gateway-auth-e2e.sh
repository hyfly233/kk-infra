#!/usr/bin/env bash
# Real gateway/inference binaries, controlplane HTTP client; memory/Fake only.
set -euo pipefail
cd "$(dirname "$0")/.."
for endpoint in http://127.0.0.1:19180 http://127.0.0.1:19182 http://127.0.0.1:19183 http://127.0.0.1:19184 http://127.0.0.1:19185; do
  if curl --max-time 1 -s -o /dev/null "$endpoint/"; then
    echo "Refusing occupied endpoint: $endpoint" >&2
    exit 1
  fi
done
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/kk-gateway-auth.XXXXXX")
umask 077
pids=()
cleanup() {
  for pid in ${pids[@]+"${pids[@]}"}; do kill "$pid" 2>/dev/null || true; done
  for pid in ${pids[@]+"${pids[@]}"}; do wait "$pid" 2>/dev/null || true; done
  echo "Local test logs: $test_dir"
}
trap cleanup EXIT
go build -o "$test_dir/gateway" ./services/gateway/cmd
go build -o "$test_dir/inference" ./services/inference/cmd
go build -o "$test_dir/observability" ./services/observability/cmd
go build -o "$test_dir/controlplane" ./services/controlplane/cmd
go build -o "$test_dir/adapter" ./services/k8sadapter/cmd
CARROT_AUTH_SECRET=local-gateway-e2e-secret "$test_dir/adapter" --fake=true --addr=127.0.0.1:19182 >"$test_dir/adapter.log" 2>&1 &
pids+=("$!")
CARROT_AUTH_SECRET=local-gateway-e2e-secret "$test_dir/controlplane" --addr=127.0.0.1:19180 --storage=memory --k8s-adapter=http://127.0.0.1:19182 --gateway-url= --observability-url= >"$test_dir/controlplane.log" 2>&1 &
pids+=("$!")
CARROT_AUTH_SECRET=local-gateway-e2e-secret "$test_dir/observability" --addr=127.0.0.1:19184 --storage=memory >"$test_dir/observability.log" 2>&1 &
pids+=("$!")
CARROT_AUTH_SECRET=local-gateway-e2e-secret "$test_dir/gateway" --addr=127.0.0.1:19183 --storage=memory --controlplane-url=http://127.0.0.1:19180 --observability-url=http://127.0.0.1:19184 >"$test_dir/gateway.log" 2>&1 &
pids+=("$!")
"$test_dir/inference" --addr=127.0.0.1:19185 >"$test_dir/inference.log" 2>&1 &
pids+=("$!")
for endpoint in http://127.0.0.1:19180 http://127.0.0.1:19182 http://127.0.0.1:19183 http://127.0.0.1:19184 http://127.0.0.1:19185; do
  ready=false
  for _ in {1..50}; do
    if curl --max-time 1 -s -o /dev/null "$endpoint/"; then ready=true; break; fi
    sleep 0.1
  done
  "$ready" || { echo "Service did not start: $endpoint" >&2; exit 1; }
done
GATEWAY_E2E_URL=http://127.0.0.1:19183 GATEWAY_E2E_CP=http://127.0.0.1:19180 INFERENCE_E2E_URL=http://127.0.0.1:19185 OBSERVABILITY_E2E_URL=http://127.0.0.1:19184 \
  go test ./services/controlplane/internal/clients -run '^TestGatewayBinaryAuthenticatedLifecycle$' -count=1
echo 'Gateway/observability authenticated binary E2E passed (memory/mock inference only).'
