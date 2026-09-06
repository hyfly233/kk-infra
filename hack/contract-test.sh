#!/usr/bin/env bash
# Carrot AI Infra —— 跨服务契约测试
# 验证统一 API 错误码、状态语义、幂等语义（R1-1）。
# 前提：先执行 hack/dev-up.sh（内存或 PG 均可）
set -euo pipefail

CP=http://127.0.0.1:8080
MR=http://127.0.0.1:8081
GW=http://127.0.0.1:8083

log() { echo -e "\n\033[1;36m=== $* ===\033[0m"; }
fail() { echo -e "\033[1;31mFAIL: $*\033[0m"; exit 1; }
pass() { echo -e "\033[1;32m✓ $*\033[0m"; }

# json_field '["data"]["id"]' 从 stdin JSON 提取
json_field() {
  python3 -c "
import sys,json
d=json.load(sys.stdin)
for p in '$1'.strip('[]').split(']['):
    d=d[p.strip('\"')]
print(d)
"
}

# ---------- 1. 服务就绪 ----------
log "1. 服务就绪检查"
code=$(curl -s -o /dev/null -w "%{http_code}" "$CP/api/v1/deployments" 2>/dev/null || true)
[ "$code" = "200" ] || fail "controlplane 未就绪 ($code)"
code=$(curl -s -o /dev/null -w "%{http_code}" "$MR/api/v1/models" 2>/dev/null || true)
[ "$code" = "200" ] || fail "modelregistry 未就绪 ($code)"
pass "controlplane + modelregistry 就绪"

# ---------- 2. 统一错误码契约 ----------
log "2. 错误码契约"

# 2.1 404：不存在模型
code=$(curl -s -o /dev/null -w "%{http_code}" "$MR/api/v1/models/nonexistent" 2>/dev/null || true)
[ "$code" = "404" ] || fail "不存在模型应 404，实际 $code"
pass "不存在模型 → 404"

# 2.2 400：缺必填字段
code=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$MR/api/v1/models" -H 'Content-Type: application/json' -d '{}' 2>/dev/null || true)
[ "$code" = "400" ] || fail "缺名称应 400，实际 $code"
pass "缺模型名称 → 400"

# 2.3 409：重名模型
curl -s -X POST "$MR/api/v1/models" -H 'Content-Type: application/json' -d '{"name":"contract-dup"}' >/dev/null 2>&1
code=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$MR/api/v1/models" -H 'Content-Type: application/json' -d '{"name":"contract-dup"}' 2>/dev/null || true)
[ "$code" = "409" ] || fail "重名模型应 409，实际 $code"
pass "重名模型 → 409"

# 2.4 401：网关无效 Key
code=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$GW/v1/chat/completions" -H 'Authorization: Bearer sk-carrot-invalid' -H 'Content-Type: application/json' -d '{"model":"x","messages":[]}' 2>/dev/null || true)
[ "$code" = "401" ] || fail "无效 Key 应 401，实际 $code"
pass "网关无效 Key → 401"

# ---------- 3. 部署状态机契约 ----------
log "3. 部署状态机契约"

# 3.1 准备模型版本（幂等：模型已存在则复用；版本冲突则复用已有版本）
MID=$(curl -s "$MR/api/v1/models" | python3 -c "
import sys,json
d=json.load(sys.stdin)
for m in d.get('data',[]):
    if m['name']=='contract-model':
        print(m['id']); break
" 2>/dev/null)
if [ -z "$MID" ]; then
  MID=$(curl -s -X POST "$MR/api/v1/models" -H 'Content-Type: application/json' -d '{"name":"contract-model"}' | json_field '["data"]["id"]')
fi
# 版本：尝试创建，冲突则查已有
VID=$(curl -s -X POST "$MR/api/v1/models/$MID/versions" -H 'Content-Type: application/json' -d '{"version":"1.0","artifactUri":"s3://contract/model.bin","runtime":"vLLM","gpuType":"A100","gpuCount":1,"memoryMB":32768}' | python3 -c "
import sys,json
d=json.load(sys.stdin)
if d.get('code')==0: print(d['data']['id'])
" 2>/dev/null)
if [ -z "$VID" ]; then
  VID=$(curl -s "$MR/api/v1/models/$MID/versions" | python3 -c "
import sys,json
d=json.load(sys.stdin)
for v in d.get('data',[]):
    if v['version']=='1.0': print(v['id']); break
" 2>/dev/null)
fi
curl -s -X POST "$MR/api/v1/versions/$VID/validate" >/dev/null 2>&1
curl -s -X POST "$MR/api/v1/versions/$VID/release" >/dev/null 2>&1
pass "模型版本已就绪（VALIDATED → RELEASED）"

# 3.2 创建部署 → 非终态
DEP=$(curl -s -X POST "$CP/api/v1/deployments" -H 'Content-Type: application/json' -d "{\"idempotencyKey\":\"contract-deploy-1\",\"name\":\"contract-demo\",\"modelVersionId\":\"$VID\",\"replicas\":1}" | json_field '["data"]["id"]')
ST=$(curl -s "$CP/api/v1/deployments/$DEP" | json_field '["data"]["status"]')
case "$ST" in
  NEW|VALIDATING|SUBMITTING|STARTING|RUNNING) pass "部署初始状态合法: $ST" ;;
  *) fail "部署初始状态非法: $ST" ;;
esac

# 3.3 幂等：同 IdempotencyKey 返回同一部署
DEP2=$(curl -s -X POST "$CP/api/v1/deployments" -H 'Content-Type: application/json' -d "{\"idempotencyKey\":\"contract-deploy-1\",\"name\":\"contract-demo\",\"modelVersionId\":\"$VID\",\"replicas\":1}" | json_field '["data"]["id"]')
[ "$DEP" = "$DEP2" ] || fail "幂等键应返回同一部署: $DEP != $DEP2"
pass "幂等创建返回同一部署"

# 3.4 等待 RUNNING
for i in $(seq 1 20); do
  ST=$(curl -s "$CP/api/v1/deployments/$DEP" | json_field '["data"]["status"]')
  [ "$ST" = "RUNNING" ] && break
  sleep 1
done
[ "$ST" = "RUNNING" ] || fail "部署未达 RUNNING: $ST"
pass "部署进入 RUNNING"

# 3.5 404：不存在部署
code=$(curl -s -o /dev/null -w "%{http_code}" "$CP/api/v1/deployments/nonexistent" 2>/dev/null || true)
[ "$code" = "404" ] || fail "不存在部署应 404，实际 $code"
pass "不存在部署 → 404"

# 3.6 幂等删除：删两次都成功
curl -s -X DELETE "$CP/api/v1/deployments/$DEP" >/dev/null 2>&1
code=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$CP/api/v1/deployments/$DEP" 2>/dev/null || true)
[ "$code" = "200" ] || fail "幂等删除应 200，实际 $code"
pass "幂等删除成功"

# ---------- 4. 响应格式契约 ----------
log "4. 响应格式契约"
# 4.1 统一响应包装：code/message/requestId
RESP=$(curl -s "$MR/api/v1/models")
echo "$RESP" | python3 -c "
import sys,json
d=json.load(sys.stdin)
assert 'code' in d and 'message' in d and 'requestId' in d, f'缺少统一字段: {d}'
assert d['code'] == 0, f'code 应为 0: {d}'
print('  统一响应包装 ✓')
"
# 4.2 Request ID 头
RID=$(curl -s -D - -o /dev/null "$MR/api/v1/models" | grep -i '^x-request-id:' | tr -d '\r' | awk '{print $2}')
[ -n "$RID" ] || fail "缺少 X-Request-Id 响应头"
pass "Request ID 响应头存在: $RID"

log ""
log "🎉 契约测试全部通过：错误码（404/400/409/401）、状态机、幂等、响应格式"
