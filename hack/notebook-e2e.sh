#!/usr/bin/env bash
# Isolated real controlplane/adapter binaries, memory identity and HTTP Stub Hub.
# Does not modify an existing dev environment or prove JupyterHub spawning.
set -euo pipefail
cd "$(dirname "$0")/.."
command -v jq >/dev/null
cp_url=http://127.0.0.1:19080
adapter_url=http://127.0.0.1:19082
hub_url=http://127.0.0.1:19081
for endpoint in "$cp_url" "$adapter_url" "$hub_url"; do
  if curl --max-time 1 -s -o /dev/null "$endpoint/"; then
    echo "Refusing occupied test endpoint: $endpoint" >&2
    exit 1
  fi
done
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/kk-notebook.XXXXXX")
umask 077
pids=()
cleanup() {
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
  echo "Local test logs: $test_dir"
}
trap cleanup EXIT
go build -o "$test_dir/controlplane" ./services/controlplane/cmd
go build -o "$test_dir/adapter" ./services/k8sadapter/cmd
python3 hack/notebook-hub-stub.py --port 19081 >"$test_dir/hub.log" 2>&1 &
pids+=("$!")
"$test_dir/adapter" --fake=true --addr=127.0.0.1:19082 >"$test_dir/adapter.log" 2>&1 &
pids+=("$!")
CARROT_AUTH_SECRET=local-notebook-test-secret NOTEBOOK_HUB_URL="$hub_url" \
NOTEBOOK_PUBLIC_URL=https://notebooks.example.test NOTEBOOK_HUB_API_TOKEN=local-hub-test-token \
  "$test_dir/controlplane" --addr=127.0.0.1:19080 --storage=memory --k8s-adapter="$adapter_url" \
  --gateway-url= --observability-url= >"$test_dir/controlplane.log" 2>&1 &
pids+=("$!")
for endpoint in "$cp_url" "$adapter_url" "$hub_url"; do
  ready=false
  for _ in {1..50}; do
    if curl --max-time 1 -s -o /dev/null "$endpoint/"; then ready=true; break; fi
    sleep 0.1
  done
  "$ready" || { echo "Service did not start: $endpoint" >&2; exit 1; }
done
curl --fail -sS "$cp_url/api/v1/auth/bootstrap" -H 'Content-Type: application/json' \
  -d '{"id":"admin","email":"admin@example.test","password":"Local-only-pass123!","tenantId":"test"}' | jq -e '.code == 0' >/dev/null
token=$(curl --fail -sS "$cp_url/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.test","password":"Local-only-pass123!","tenantId":"test"}' | jq -er '.data.accessToken')
workspace="$cp_url/api/v1/notebooks/workspace"
hub_identity=$(curl --fail -sS -X POST "$cp_url/api/v1/notebooks/hub-token" -H "Authorization: Bearer $token" | jq -er '.data.token')
curl --fail -sS "$cp_url/internal/notebooks/introspect" -H "Authorization: Bearer $hub_identity" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$token\"}" | jq -e '.active == true and .tenantId == "test"' >/dev/null
service_as_user=$(curl -s -o /dev/null -w '%{http_code}' "$workspace" -H "Authorization: Bearer $hub_identity")
[[ "$service_as_user" == 401 ]]
curl --fail -sS "$workspace" -H "Authorization: Bearer $token" | jq -e '.data.status == "ABSENT"' >/dev/null
curl --fail -sS -X POST "$workspace" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d '{"tenantId":"other","ownerId":"other"}' | jq -e '.data.status == "STARTING" and .data.tenantId == "test" and .data.ownerId == "admin"' >/dev/null
curl --fail -sS "$workspace" -H "Authorization: Bearer $token" | jq -e '.data.status == "STARTING"' >/dev/null
curl --fail -sS "$workspace" -H "Authorization: Bearer $token" | jq -e '.data.status == "RUNNING" and (.data.url | startswith("https://notebooks.example.test/user/nb-"))' >/dev/null
curl --fail -sS -X DELETE "$workspace" -H "Authorization: Bearer $token" | jq -e '.data.status == "STOPPING"' >/dev/null
curl --fail -sS "$workspace" -H "Authorization: Bearer $token" | jq -e '.data.status == "STOPPING"' >/dev/null
curl --fail -sS "$workspace" -H "Authorization: Bearer $token" | jq -e '.data.status == "ABSENT"' >/dev/null
unauthorized=$(curl -s -o /dev/null -w '%{http_code}' "$workspace")
[[ "$unauthorized" == 401 ]]
echo 'Notebook lifecycle Fake E2E passed (not real Hub/Kubernetes).'
