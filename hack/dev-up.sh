#!/usr/bin/env bash
# Carrot AI Infra —— 本地一键启动（Fake K8s 环境，6 服务）
# 用法: hack/dev-up.sh [--no-build] [--storage=postgres|memory]
#   --storage=postgres 使用本机 docker Postgres（localhost:5432/carrot），默认 memory
# 启动后访问: 前端 http://localhost:5173（cd frontend && npm run dev）
#            控制面 http://localhost:8080  (REST API)
#            网关   http://localhost:8083  (OpenAI 兼容 API)
set -euo pipefail
export PATH="/opt/homebrew/bin:$PATH"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN_DIR="${TMPDIR:-/tmp}/kk-infra-bin"
PIDS=()
STORAGE="memory"
NO_BUILD=false

for arg in "$@"; do
  case "$arg" in
    --no-build) NO_BUILD=true ;;
    --storage=*) STORAGE="${arg#--storage=}" ;;
  esac
done

log() { echo -e "\n\033[1;36m=== $* ===\033[0m"; }

cleanup() {
  log "停止全部服务"
  for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# ---------- 1. 编译 ----------
if [ "$NO_BUILD" != "true" ]; then
  log "编译全部服务二进制 → $BIN_DIR"
  mkdir -p "$BIN_DIR"
  for svc in modelregistry k8sadapter controlplane gateway observability inference; do
    (cd "$ROOT/services/$svc" && go build -o "$BIN_DIR/$svc" ./cmd)
    echo "编译 $svc OK"
  done
fi

# ---------- 2. 启动 6 个服务 ----------
log "启动 modelregistry :8081(storage=$STORAGE)"
"$BIN_DIR/modelregistry" --addr :8081 --storage "$STORAGE" &
PIDS+=($!)

log "启动 k8sadapter :8082（Fake 集群，2 节点 × 8 卡 A100）"
"$BIN_DIR/k8sadapter" --addr :8082 --fake=true &
PIDS+=($!)

log "启动 controlplane :8080(storage=$STORAGE)"
"$BIN_DIR/controlplane" --addr :8080 \
  --model-registry http://127.0.0.1:8081 \
  --k8s-adapter http://127.0.0.1:8082 \
	--gateway-url http://127.0.0.1:8083 \
  --storage "$STORAGE" \
  --observability-url http://127.0.0.1:8084 &
PIDS+=($!)

log "启动 observability :8084（GPU 采集来自 controlplane）"
"$BIN_DIR/observability" --addr :8084 \
  --controlplane-url http://127.0.0.1:8080 &
PIDS+=($!)

log "启动 gateway :8083(指标上报到 observability, storage=$STORAGE)"
"$BIN_DIR/gateway" --addr :8083 \
  --storage "$STORAGE" \
  --observability-url http://127.0.0.1:8084 &
PIDS+=($!)

log "启动 inference :8085（Mock vLLM）"
"$BIN_DIR/inference" --addr :8085 --model qwen-demo &
PIDS+=($!)

# ---------- 3. 等待就绪 ----------
log "等待全部服务就绪"
for i in $(seq 1 30); do
  ok=0
  curl -sf http://127.0.0.1:8081/api/v1/models >/dev/null 2>&1 && ok=$((ok+1))
  curl -sf http://127.0.0.1:8082/v1/resources/gpus >/dev/null 2>&1 && ok=$((ok+1))
  curl -sf http://127.0.0.1:8080/api/v1/deployments >/dev/null 2>&1 && ok=$((ok+1))
  curl -sf http://127.0.0.1:8084/api/v1/deployments/x/metrics >/dev/null 2>&1 && ok=$((ok+1))
  # gateway /v1/models 需要鉴权，401 即服务已就绪
  code=$(curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:8083/v1/models 2>/dev/null || true)
  [ "$code" = "401" ] && ok=$((ok+1))
  curl -sf http://127.0.0.1:8085/v1/models >/dev/null 2>&1 && ok=$((ok+1))
  [ "$ok" -ge 6 ] && break
  sleep 1
done
[ "$ok" -ge 6 ] || { echo "服务未全部就绪 (ok=$ok/6)"; exit 1; }

log "全部就绪 🎉"
echo "  控制面   http://localhost:8080"
echo "  modelregistry http://localhost:8081"
echo "  k8sadapter     http://localhost:8082"
echo "  gateway        http://localhost:8083"
echo "  observability  http://localhost:8084"
echo "  inference      http://localhost:8085"
echo "  前端（需另起）: cd frontend && npm run dev  → http://localhost:5173"
echo ""
echo "按 Ctrl-C 停止全部服务"

# 阻塞等待（进程被 kill 时 cleanup 生效）
wait
