#!/usr/bin/env bash
# 构建 + 静态检查 + 测试 kk-infra 全部 Go 模块。
# 注意：go.work 根目录不支持 ./...，必须逐个模块进入执行。
# 用法: hack/check.sh [模块...]  （默认全部模块）
set -euo pipefail

export PATH="/opt/homebrew/bin:$PATH"   # Go 1.26.5

cd "$(dirname "$0")/.."

MODULES=("$@")
if [ ${#MODULES[@]} -eq 0 ]; then
  MODULES=(
    ./lib
    ./services/modelregistry
    ./services/k8sadapter
    ./services/controlplane
    ./services/gateway
    ./services/inference
    ./services/observability
    ./services/pipeline
    ./services/disaggproxy
  )
fi

for m in "${MODULES[@]}"; do
  echo "=== $m ==="
  (cd "$m" && go build ./... && go vet ./... && go test ./... -count=1)
done

echo "全部模块检查通过 ✔"
