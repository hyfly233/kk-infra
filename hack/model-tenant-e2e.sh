#!/usr/bin/env bash
# Isolated memory/Fake binaries; DevelopmentVerifier does not check real S3 bytes.
set -euo pipefail
cd "$(dirname "$0")/.."
for port in 19280 19281 19282 19285 19286; do
  if curl --max-time 1 -s -o /dev/null "http://127.0.0.1:$port/"; then
    echo "Refusing occupied port: $port" >&2
    exit 1
  fi
done
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/kk-model-tenant.XXXXXX")
umask 077
pids=()
cleanup() {
  for pid in ${pids[@]+"${pids[@]}"}; do kill "$pid" 2>/dev/null || true; done
  for pid in ${pids[@]+"${pids[@]}"}; do wait "$pid" 2>/dev/null || true; done
  echo "Local test logs: $test_dir"
}
trap cleanup EXIT
for service in controlplane modelregistry k8sadapter inference pipeline; do
  go build -o "$test_dir/$service" "./services/$service/cmd"
done
CARROT_AUTH_SECRET=local-model-tenant-test-secret "$test_dir/modelregistry" --addr=127.0.0.1:19281 --storage=memory --controlplane-url=http://127.0.0.1:19280 >"$test_dir/registry.log" 2>&1 &
pids+=("$!")
CARROT_AUTH_SECRET=local-model-tenant-test-secret "$test_dir/k8sadapter" --fake=true --addr=127.0.0.1:19282 >"$test_dir/adapter.log" 2>&1 &
pids+=("$!")
"$test_dir/inference" --addr=127.0.0.1:19285 >"$test_dir/inference.log" 2>&1 &
pids+=("$!")
CARROT_AUTH_SECRET=local-model-tenant-test-secret "$test_dir/pipeline" --addr=127.0.0.1:19286 --storage=memory \
  --controlplane-url=http://127.0.0.1:19280 --model-registry=http://127.0.0.1:19281 --probe-url=http://127.0.0.1:19285 >"$test_dir/pipeline.log" 2>&1 &
pids+=("$!")
CARROT_AUTH_SECRET=local-model-tenant-test-secret "$test_dir/controlplane" --addr=127.0.0.1:19280 --storage=memory \
  --model-registry=http://127.0.0.1:19281 --k8s-adapter=http://127.0.0.1:19282 --gateway-url= --observability-url= >"$test_dir/controlplane.log" 2>&1 &
pids+=("$!")
for port in 19280 19281 19282 19285 19286; do
  ready=false
  for _ in {1..50}; do
    if curl --max-time 1 -s -o /dev/null "http://127.0.0.1:$port/"; then ready=true; break; fi
    sleep 0.1
  done
  "$ready" || { echo "Service did not start on $port" >&2; exit 1; }
done
adapter_status=$(curl --max-time 2 -s -o /dev/null -w '%{http_code}' http://127.0.0.1:19282/v1/resources/gpus)
[ "$adapter_status" = 401 ] || { echo "Anonymous adapter request returned $adapter_status, expected 401" >&2; exit 1; }
MODEL_E2E_REGISTRY=http://127.0.0.1:19281 MODEL_E2E_CP=http://127.0.0.1:19280 MODEL_E2E_PIPELINE=http://127.0.0.1:19286 \
  go test ./services/controlplane/internal/clients -run '^TestModelTenantBinaryLifecycle$' -count=1
echo 'Model tenant / pipeline / deployment Fake E2E passed (not real S3/Kubernetes).'
