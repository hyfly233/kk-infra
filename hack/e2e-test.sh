#!/usr/bin/env bash
# Carrot AI Infra —— Fake K8s 环境 MVP 全链路验证脚本
# 前提：先执行 hack/dev-up.sh 或自行启动 6 个服务
# 流程：发现 GPU → 注册模型 → 创建服务 → Running → OpenAI 调用（含流式）
#       → 查询指标（新增）→ 扩容 → 删除释放
set -euo pipefail

CP=http://127.0.0.1:8080
MR=http://127.0.0.1:8081
GW=http://127.0.0.1:8083
OBS=http://127.0.0.1:8084
INF=http://127.0.0.1:8085

log() { echo -e "\n\033[1;36m=== $* ===\033[0m"; }
fail() { echo -e "\033[1;31mFAIL: $*\033[0m"; exit 1; }
json_field() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)"; }

# ---------- 1. 服务就绪 ----------
log "1. 检查服务就绪"
check_ready() { # url expected_code
  code=$(curl -s -o /dev/null -w "%{http_code}" "$1" 2>/dev/null || true)
  [ "$code" = "$2" ] || return 1
}
check_ready "$CP/api/v1/deployments" 200 || fail "服务未就绪: $CP"
check_ready "$MR/api/v1/models" 200 || fail "服务未就绪: $MR"
check_ready "$GW/v1/models" 401 || fail "服务未就绪: $GW（gateway 需 401 鉴权）"
check_ready "$OBS/api/v1/gpus/metrics" 200 || fail "服务未就绪: $OBS"
check_ready "$INF/v1/models" 200 || fail "服务未就绪: $INF"
echo "全部服务就绪"

# ---------- 1.5 清理上次残留（幂等可重跑） ----------
log "1.5 清理上次运行残留"
# 删除已有部署
for did in $(curl -s "$CP/api/v1/deployments" | python3 -c "
import sys,json
d=json.load(sys.stdin)
for x in d.get('data',[]):
    print(x.get('id',''))
" 2>/dev/null); do
  [ -n "$did" ] && curl -s -X DELETE "$CP/api/v1/deployments/$did" >/dev/null 2>&1 || true
done
# 删除已有模型（连带版本）
for mid in $(curl -s "$MR/api/v1/models" | python3 -c "
import sys,json
d=json.load(sys.stdin)
for x in d.get('data',[]):
    print(x.get('id',''))
" 2>/dev/null); do
  [ -n "$mid" ] && curl -s -X DELETE "$MR/api/v1/models/$mid" >/dev/null 2>&1 || true
done
# 删除已有 gateway 路由
curl -s -X DELETE "$GW/internal/routes/qwen-demo" >/dev/null 2>&1 || true
echo "清理完成"

# ---------- 2. GPU 发现 ----------
log "2. GPU 资源发现"
GPU_JSON=$(curl -s "$CP/api/v1/resources/gpus")
echo "$GPU_JSON" | head -c 300; echo
echo "$GPU_JSON" | grep -q "A100" || fail "GPU 发现缺少 A100"

# ---------- 3. 注册模型与版本 ----------
log "3. 注册模型 qwen / 版本 7b"
curl -s -X POST "$MR/api/v1/models" -H 'Content-Type: application/json' \
  -d '{"name":"qwen","description":"Qwen 测试模型"}' >/dev/null
MODEL_ID=$(curl -s "$MR/api/v1/models" | json_field "['data'][0]['id']")
VER_JSON=$(curl -s -X POST "$MR/api/v1/models/$MODEL_ID/versions" -H 'Content-Type: application/json' \
  -d '{"version":"7b","artifactUri":"s3://models/qwen-7b","runtime":"vLLM","gpuType":"A100","gpuCount":1,"memoryMB":2048,"contextLength":8192}')
VERSION_ID=$(echo "$VER_JSON" | json_field "['data']['id']")
curl -s -X POST "$MR/api/v1/versions/$VERSION_ID/validate" >/dev/null
curl -s -X POST "$MR/api/v1/versions/$VERSION_ID/release" >/dev/null
echo "版本已校验并发布 (RELEASED)"

# ---------- 4. 创建部署 ----------
log "4. 创建模型服务 qwen-demo（1 GPU × 1 副本）"
DEPLOY_JSON=$(curl -s -X POST "$CP/api/v1/deployments" -H 'Content-Type: application/json' \
  -d "{\"idempotencyKey\":\"e2e-qwen-$(date +%s)\",\"name\":\"qwen-demo\",\"modelVersionId\":\"$VERSION_ID\",\"tenantId\":\"default\",\"replicas\":1}")
DEPLOY_ID=$(echo "$DEPLOY_JSON" | json_field "['data']['id']")
echo "部署 ID: $DEPLOY_ID"

# ---------- 5. 等待 Running ----------
log "5. 等待部署进入 RUNNING"
READY=""
for i in $(seq 1 30); do
  STATUS=$(curl -s "$CP/api/v1/deployments/$DEPLOY_ID" | json_field "['data']['status']")
  [ "$STATUS" = "RUNNING" ] && { READY=1; break; }
  sleep 1
done
[ -n "$READY" ] || fail "部署未进入 RUNNING，最终状态=$STATUS"
echo "部署 RUNNING ✅"

# ---------- 6. 配置 gateway 路由 + API Key ----------
log "6. 配置 gateway 路由与 API Key"
curl -s -X POST "$GW/internal/routes" -H 'Content-Type: application/json' \
  -d "{\"model\":\"qwen-demo\",\"endpoint\":\"$INF\",\"tenantId\":\"default\",\"deploymentId\":\"$DEPLOY_ID\"}" >/dev/null
KEY_JSON=$(curl -s -X POST "$GW/api/v1/keys" -H 'Content-Type: application/json' -d '{"tenantId":"default"}')
API_KEY=$(echo "$KEY_JSON" | json_field "['data']['key']")
echo "API Key 已创建"

# ---------- 7. OpenAI 调用 ----------
log "7.0 配置测试费率"
curl -s -X PUT "$OBS/internal/rate-cards/default" -H 'Content-Type: application/json' \
  -d '{"gpuType":"default","inputTokenPerMillion":1,"outputTokenPerMillion":2,"gpuHour":3}' \
  | grep -q '"code":0' || fail "配置测试费率失败"

log "7.1 非流式调用 /v1/chat/completions"
RESP=$(curl -s -X POST "$GW/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"qwen-demo","messages":[{"role":"user","content":"你好，介绍一下你自己"}]}')
echo "$RESP" | head -c 300; echo
echo "$RESP" | grep -q "回复:" || fail "非流式调用失败"

log "7.2 流式调用"
# 输出到临时文件再检查（避免 pipefail 下 curl|head 的 SIGPIPE）
STREAM_FILE=$(mktemp)
curl -s -N --max-time 15 -X POST "$GW/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"qwen-demo","messages":[{"role":"user","content":"讲个故事"}],"stream":true}' \
  > "$STREAM_FILE" 2>&1 || true
grep -q "data:" "$STREAM_FILE" || { cat "$STREAM_FILE" | head -c 200; fail "流式调用失败（无 data: 事件）"; }
head -c 200 "$STREAM_FILE"; echo
grep -q '"content"' "$STREAM_FILE" || fail "流式响应缺少 content chunk"
rm -f "$STREAM_FILE"
echo "流式调用 ✅"

# ---------- 8. 指标查询（新增） ----------
log "8.1 直接查询 observability 请求指标"
sleep 2  # 等待异步上报
OBS_JSON=$(curl -s "$OBS/api/v1/deployments/$DEPLOY_ID/metrics?range=5m")
echo "$OBS_JSON" | head -c 500; echo
echo "$OBS_JSON" | grep -q '"requests"' || fail "observability 无 requests 序列"
REQ_COUNT=$(echo "$OBS_JSON" | python3 -c "
import sys,json
d=json.load(sys.stdin)
for s in d['data']['series']:
    if s['name']=='requests':
        print(int(sum(p['val'] for p in s['points'])))
")
echo "观测到请求数: $REQ_COUNT"
[ "$REQ_COUNT" -ge 2 ] || fail "请求指标不足（期望 ≥2，实际 $REQ_COUNT）——gateway 埋点或上报链路异常"

log "8.2 通过 controlplane 转发查询指标"
CP_JSON=$(curl -s "$CP/api/v1/deployments/$DEPLOY_ID/metrics?range=5m")
echo "$CP_JSON" | grep -q '"requests"' || fail "controlplane 指标转发失败"
echo "controlplane 指标转发 OK ✅"

log "8.3 查询租户用量账单"
BILLING_JSON=$(curl -s "$CP/api/v1/billing?tenantId=default")
echo "$BILLING_JSON" | grep -q '"deploymentId"' || fail "账单缺少部署用量"
OUTPUT_TOKENS=$(echo "$BILLING_JSON" | python3 -c "import sys,json; print(sum(x['outputTokens'] for x in json.load(sys.stdin)['data']))")
[ "$OUTPUT_TOKENS" -gt 0 ] || fail "账单输出 Token 未累计"
echo "账单累计输出 Token: $OUTPUT_TOKENS ✅"

log "8.4 GPU 利用率指标"
sleep 17  # 等待采集器下一轮（15s 周期）
GPU_METRIC=$(curl -s "$OBS/api/v1/gpus/metrics?range=5m")
echo "$GPU_METRIC" | head -c 400; echo
echo "$GPU_METRIC" | grep -q '"gpuUtil"' || fail "GPU 利用率指标缺失"

# ---------- 9. 扩容 ----------
log "9. 扩容到 2 副本"
curl -s -X POST "$CP/api/v1/deployments/$DEPLOY_ID/scale" \
  -H 'Content-Type: application/json' -d '{"replicas":2}' >/dev/null
sleep 2
REPS=$(curl -s "$CP/api/v1/deployments/$DEPLOY_ID" | json_field "['data']['replicas']")
[ "$REPS" = "2" ] || fail "扩容未生效，replicas=$REPS"
echo "扩容到 2 副本 ✅"

# ---------- 10. 删除释放 ----------
log "10. 删除部署（释放 GPU）"
curl -s -X DELETE "$CP/api/v1/deployments/$DEPLOY_ID" >/dev/null
sleep 2
GPU_AFTER=$(curl -s "$CP/api/v1/resources/gpus" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(d['data']['summary']['availableGpu'])
")
echo "删除后可用 GPU: $GPU_AFTER"
[ "$GPU_AFTER" = "16" ] || fail "GPU 未释放，availableGpu=$GPU_AFTER（期望 16）"
echo "GPU 已释放 ✅"

log ""
log "🎉 Fake 环境 MVP 闭环全部通过：GPU 发现 → 模型注册 → 部署 → 调用（含流式）→ 指标 → 扩容 → 删除释放"
