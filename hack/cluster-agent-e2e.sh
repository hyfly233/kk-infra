#!/usr/bin/env bash
# Isolated memory/Fake test: real binaries, login, token issuance and agent heartbeat.
# Requires go, curl and jq. Does not start or modify a real cluster.
set -euo pipefail
cd "$(dirname "$0")/.."
command -v jq >/dev/null

cp_port=${CLUSTER_E2E_CP_PORT:-18080}
bootstrap_port=${CLUSTER_E2E_BOOTSTRAP_PORT:-18082}
agent_port=${CLUSTER_E2E_AGENT_PORT:-18083}
cp_url="http://127.0.0.1:$cp_port"
bootstrap_url="http://127.0.0.1:$bootstrap_port"
agent_url="http://127.0.0.1:$agent_port"
for endpoint in "$cp_url" "$bootstrap_url" "$agent_url"; do
  if curl --max-time 1 -s -o /dev/null "$endpoint/"; then
    echo "Refusing to use an occupied test endpoint: $endpoint" >&2
    exit 1
  fi
done

test_dir=$(mktemp -d "${TMPDIR:-/tmp}/kk-cluster-agent.XXXXXX")
pids=()
cleanup() {
  if (( ${#pids[@]} )); then
    for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
    for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
  fi
  echo "Test logs and temporary binaries: $test_dir"
}
trap cleanup EXIT
umask 077
go build -o "$test_dir/controlplane" ./services/controlplane/cmd
go build -o "$test_dir/adapter" ./services/k8sadapter/cmd

wait_ready() {
  local endpoint=$1
  for _ in {1..50}; do
    if curl --max-time 1 -s -o /dev/null "$endpoint/"; then return; fi
    sleep 0.1
  done
  echo "Service did not start: $endpoint" >&2
  exit 1
}

"$test_dir/adapter" --fake=true --addr="127.0.0.1:$bootstrap_port" >"$test_dir/bootstrap.log" 2>&1 &
pids+=("$!")
wait_ready "$bootstrap_url"
CARROT_AUTH_SECRET=local-cluster-agent-e2e-secret \
CLUSTER_ENCRYPTION_KEY=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY= \
  "$test_dir/controlplane" --addr="127.0.0.1:$cp_port" --storage=memory \
  --k8s-adapter="$bootstrap_url" --gateway-url= --observability-url= \
  >"$test_dir/controlplane.log" 2>&1 &
pids+=("$!")
wait_ready "$cp_url"

curl --fail --silent --show-error "$cp_url/api/v1/auth/bootstrap" -H 'Content-Type: application/json' \
  -d '{"id":"admin","email":"admin@example.test","password":"Local-only-pass123!","tenantId":"test"}' | jq -e '.code == 0' >/dev/null
admin_token=$(curl --fail --silent --show-error "$cp_url/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.test","password":"Local-only-pass123!","tenantId":"test"}' | jq -er '.data.accessToken')
curl --fail --silent --show-error "$cp_url/api/v1/clusters" -H "Authorization: Bearer $admin_token" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"gpu-test\",\"name\":\"Fake E2E\",\"endpoint\":\"https://fake.example.test\",\"adapterUrl\":\"$agent_url\",\"kubeconfig\":\"fake-test-only\",\"supportedRuntimes\":[\"vLLM\"]}" | jq -e '.code == 0' >/dev/null
curl --fail --silent --show-error -X POST "$cp_url/api/v1/clusters/gpu-test/agent-token" \
  -H "Authorization: Bearer $admin_token" | jq -er '.data.token' >"$test_dir/token"

"$test_dir/adapter" --fake=true --addr="127.0.0.1:$agent_port" \
  --cluster-id=gpu-test --controlplane-url="$cp_url" --cluster-agent-token-file="$test_dir/token" \
  --agent-prometheus-url=http://127.0.0.1:1 \
  >"$test_dir/agent.log" 2>&1 &
pids+=("$!")
wait_ready "$agent_url"
initial_report=false
for _ in {1..50}; do
  if curl --fail --silent --show-error "$cp_url/api/v1/clusters" -H "Authorization: Bearer $admin_token" |
    jq -e '.data[] | select(.id == "gpu-test") | .healthStatus == "healthy" and .lastHeartbeat != null and .gpuCapacity[0].gpuType == "A100" and .gpuCapacity[0].allocatable == 16 and .gpuCapacity[0].used == 0 and .telemetry.status == "unhealthy"' >/dev/null; then
    echo "PASS: administrator login -> agent token -> running Fake agent -> persisted capacity"
    echo "PASS: unavailable Prometheus -> unhealthy telemetry without failing Kubernetes capacity"
    initial_report=true
    break
  fi
  sleep 0.1
done
[ "$initial_report" = true ] || { echo "Agent did not report expected capacity" >&2; exit 1; }

monitor_token=$(curl --fail --silent --show-error -X POST "$cp_url/api/v1/clusters/monitor-token" \
  -H "Authorization: Bearer $admin_token" | jq -er '.data.token')
curl --fail --silent --show-error "$cp_url/internal/clusters/metrics" -H "Authorization: Bearer $monitor_token" |
  grep -Fq 'carrot_cluster_alert{cluster_id="gpu-test",kind="telemetry"} 1'
curl --fail --silent --show-error "$cp_url/api/v1/clusters/alerts" -H "Authorization: Bearer $admin_token" |
  jq -e '.data[] | select(.fingerprint == "gpu-test/telemetry")' >/dev/null
echo "PASS: telemetry alert API and scoped Prometheus metrics"

# Synthetic Queue snapshot tests the HTTP/storage contract, not real Volcano collection.
agent_token=$(<"$test_dir/token")
curl --fail --silent --show-error "$cp_url/api/v1/clusters/gpu-test/heartbeat" \
  -H "Authorization: Bearer $agent_token" -H 'Content-Type: application/json' \
  -d '{"healthStatus":"healthy","gpuCapacity":[{"gpuType":"A100","total":16,"allocatable":16,"used":0}],"volcanoQueues":[{"name":"tenant-test","state":"Open","capability":{"nvidia.com/gpu":"8"},"allocated":{"nvidia.com/gpu":"2"},"pending":3}]}' |
  jq -e '.code == 0 and .data.accepted' >/dev/null
curl --fail --silent --show-error "$cp_url/api/v1/clusters" -H "Authorization: Bearer $admin_token" |
  jq -e '.data[] | select(.id == "gpu-test") | .volcanoQueues[0].pending == 3 and .volcanoQueues[0].allocated["nvidia.com/gpu"] == "2"' >/dev/null
echo "PASS: synthetic Volcano queue heartbeat -> persisted administrator query"
curl --fail --silent --show-error "$cp_url/api/v1/clusters/alerts" -H "Authorization: Bearer $admin_token" |
  jq -e '.data | length == 0' >/dev/null
echo "PASS: cleared telemetry snapshot resolves active alert"

curl --fail --silent --show-error "$agent_url/v1/deployments" -H 'Content-Type: application/json' \
  -d '{"deploymentId":"cleanup-probe","name":"cleanup-probe","namespace":"tenant-test","replicas":1,"runtime":"vLLM","resource":{"gpuType":"A100","gpuCount":1}}' |
  jq -e '.code == 0' >/dev/null
curl --silent --show-error -X DELETE "$agent_url/v1/deployments/cleanup-probe/managed?namespace=tenant-test&deploymentId=wrong-id" |
  jq -e '.code != 0' >/dev/null
curl --fail --silent --show-error "$agent_url/v1/deployments/cleanup-probe?namespace=tenant-test" |
  jq -e '.data.deploymentId == "cleanup-probe"' >/dev/null
for _ in {1..2}; do
  curl --fail --silent --show-error -X DELETE "$agent_url/v1/deployments/cleanup-probe/managed?namespace=tenant-test&deploymentId=cleanup-probe" |
    jq -e '.code == 0 and .data.deleted' >/dev/null
done
echo "PASS: managed Fake deletion rejects wrong identity and retries idempotently"

curl --fail --silent --show-error "$agent_url/v1/deployments" -H 'Content-Type: application/json' \
  -d '{"deploymentId":"heartbeat-load","name":"heartbeat-load","namespace":"tenant-test","replicas":1,"runtime":"vLLM","resource":{"gpuType":"A100","gpuCount":2}}' |
  jq -e '.code == 0' >/dev/null
for _ in {1..70}; do
  if curl --fail --silent --show-error "$cp_url/api/v1/clusters" -H "Authorization: Bearer $admin_token" |
    jq -e '.data[] | select(.id == "gpu-test") | .healthStatus == "healthy" and .gpuCapacity[0].used == 2 and ((.volcanoQueues // []) | length) == 0 and .telemetry.status == "unhealthy"' >/dev/null; then
    echo "PASS: periodic heartbeat replaces capacity after Fake workload allocation"
    exit 0
  fi
  sleep 0.5
done
echo "Periodic heartbeat did not update occupied capacity" >&2
exit 1
