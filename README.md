# Carrot AI Infra Platform（kk-infra）

基于 Kubernetes 的分布式 AI 推理基础设施平台（Go 多模块微服务）。面向企业研发与平台运维团队，将底层 GPU 资源转化为可直接消费的模型服务：选择模型 → 配置规格 → 部署服务 → 获得 OpenAI 兼容 API → 查看性能指标 → 扩缩容/下线。

> 项目文档：`docs/PRD-V2.md`、`docs/ROADMAP-V2.md`、`docs/ARCHITECTURE-V2.md`、`docs/INTERACTION-PROTOTYPE-V2.md`

---

## 1. 总体架构

```text
用户 / Web Console / OpenAI API
                │
                ▼
┌─────────────────────────────┐
│ controlplane 控制面 :8080    │  REST API / 部署 UseCase / 状态机 / Reconciler
├─────────────────────────────┤
│ modelregistry :8081         │  模型 & 版本元数据 / 可部署性校验
├─────────────────────────────┤
│ k8sadapter :8082            │  K8s Client / vLLM Renderer / Discovery / Fake
├─────────────────────────────┤
│ gateway :8083               │  OpenAI 兼容 API / 路由 / 鉴权 / 流式
├─────────────────────────────┤
│ observability :8084         │  指标聚合 / 查询 API（MVP 内存，预留 Prometheus）
├─────────────────────────────┤
│ inference :8085             │  Mock vLLM 推理后端（本地闭环用；生产为真实 vLLM）
└─────────────────────────────┘
```

- **控制面与执行面解耦**：controlplane 负责业务编排，k8sadapter 屏蔽 Kubernetes 细节（Fake / 真实集群可切换）
- **状态机驱动**：部署生命周期 NEW→VALIDATING→SUBMITTING→STARTING→RUNNING / FAILED，事件全程可审计
- **指标链路**：gateway 异步上报请求指标 → observability 聚合；observability 定期从 controlplane 采集 GPU 利用率
- **多模块 workspace**：`go.work` 统一编排 `lib` + 8 个模块（6 服务 + pipeline 骨架 + test/contract）
- **发布流程**：模型版本 `REGISTERED → VALIDATED → RELEASED`，仅 RELEASED 可部署；支持升级/回滚
- **租户与安全**：配额校验（创建/扩容/删除释放）、API Key 模型授权、审计日志、RBAC/NetworkPolicy 清单
- **可观测性**：Prometheus adapter（DCGM 指标）+ 告警规则清单

## 2. 服务清单

| 服务 | 端口 | 职责 | 关键依赖 |
|---|---|---|---|
| `lib` | - | 领域模型、状态机、错误码、API 类型、中间件（共享库） | 无 |
| `controlplane` | 8080 | 部署/模型/资源 UseCase、状态机、Reconciler、REST API | modelregistry、k8sadapter、observability |
| `modelregistry` | 8081 | 模型与版本 CRUD、可部署性校验 | lib |
| `k8sadapter` | 8082 | K8s 资源渲染、Node/GPU 发现、状态同步、Fake 实现 | lib |
| `gateway` | 8083 | `/v1/models`、`/v1/chat/completions`、API Key、路由、限流、流式 | lib、observability（可选） |
| `observability` | 8084 | 请求/GPU 指标内存聚合与查询 | lib、controlplane（GPU 采集） |
| `inference` | 8085 | Mock OpenAI 兼容后端（流式 + TTFT 模拟），本地 E2E 用 | lib |

## 3. 快速开始

### 环境要求

- Go 1.26.5（仓库声明 go 1.26；本机 PATH 默认 1.20.4，**必须用 `/opt/homebrew/bin/go`**）
- 前端：Node.js ≥ 20 + npm

### 3.1 一键启动全部后端服务（Fake K8s 环境）

```bash
./hack/dev-up.sh                  # 内存存储（默认）
./hack/dev-up.sh --storage=postgres   # 本机 docker Postgres 持久化
```

脚本自动编译 6 个服务二进制到 `${TMPDIR:-/tmp}/kk-infra-bin/` 并启动：

| 服务 | 地址 |
|---|---|
| controlplane | http://localhost:8080 |
| modelregistry | http://localhost:8081 |
| k8sadapter | http://localhost:8082 |
| gateway | http://localhost:8083 |
| observability | http://localhost:8084 |
| inference | http://localhost:8085 |

Fake 环境内置 2 节点 × 8 卡 A100 GPU 池（共 16 卡），无需 Docker/K8s 即可跑通完整闭环。

**Postgres 模式**（R1 新增）：
- 使用本机 docker Postgres：`/Users/flyhy/workspace/docker/postgresql`（`localhost:5432`，库 `carrot`）
- 依赖：`database/sql + lib/pq`（零外部下载）；migration 自动执行（`lib/store/migrations/001_init.sql`）
- 重启服务后部署状态/事件/API Key 从 PG 恢复（已验证）

### 3.2 启动前端管理台

```bash
cd frontend
npm install
npm run dev        # http://localhost:5173
```

控制台使用邮箱、密码和租户 ID 调用真实登录 API，使用短期 access token、刷新和退出接口；会话支持平台管理员、租户管理员、开发者和只读者。需为 controlplane 配置签名密钥并启用认证，提前创建账号和租户成员关系。无认证 Fake 开发模式不能直接登录控制台。modelregistry、pipeline、k8sadapter、gateway 与 observability 的已实施鉴权、模型归属迁移及发布门禁见 [服务身份指南](docs/SERVICE-AUTH.md)。生产控制台需配置 `/pipeline` 代理。历史归属恢复、存量推理 Key 撤权及整体网络隔离仍待收口，不能据控制台登录宣称整个系统已安全隔离。管理端口不得直接暴露给非可信网络。

Notebook 管理、Hub 身份和部署配置见 [Notebook 指南](docs/NOTEBOOK.md)。

### 3.3 一键验证 MVP 闭环（Fake 环境）

```bash
./hack/e2e-test.sh
```

自动断言：GPU 发现 → 注册模型 → 创建 vLLM 服务 → Running → OpenAI 调用（非流式 + 流式）→ 指标查询 → 扩容 → 删除释放 GPU。

### 3.3.1 契约测试（R1 新增）

```bash
./hack/contract-test.sh
```

断言统一 API 契约：错误码（404/400/409/401）、部署状态机、幂等语义、响应格式（code/message/requestId + X-Request-Id）。

### 3.4 真实 Kubernetes 环境

```bash
# 1. 构建 Mock 推理镜像（Docker Desktop 集群可直接使用本地镜像）
docker build -f services/inference/Dockerfile -t kk-infra/inference:local .

# 2. 预编译控制面二进制
./hack/build-binaries.sh

# 3. 运行真实 K8s 验证脚本（需 Docker Desktop K8s 已启用）
./hack/e2e-local-k8s.sh
```

## 4. API 一览

### 控制面（controlplane :8080）

```text
POST   /api/v1/deployments                      创建模型服务（幂等）
GET    /api/v1/deployments                      服务列表
GET    /api/v1/deployments/{id}                 服务详情（状态/事件/诊断）
POST   /api/v1/deployments/{id}/scale           扩缩容
POST   /api/v1/deployments/{id}/restart         重启
DELETE /api/v1/deployments/{id}                 删除（释放 GPU）
GET    /api/v1/deployments/{id}/metrics?range=1h  指标（转发 observability）
GET    /api/v1/resources/gpus?gpuType=A100      GPU 资源汇总与节点列表
```

### 模型注册中心（modelregistry :8081）

```text
POST   /api/v1/models                           注册模型
GET    /api/v1/models                           模型列表
GET    /api/v1/models/{id}                      模型详情
DELETE /api/v1/models/{id}                      删除模型（连带版本）
POST   /api/v1/models/{id}/versions             注册版本
GET    /api/v1/models/{id}/versions             版本列表
POST   /api/v1/versions/{versionId}/validate    校验版本可部署性
DELETE /api/v1/models/{id}/versions/{version}   删除版本
```

### 推理网关（gateway :8083，OpenAI 兼容）

```text
GET    /v1/models                               模型列表（需 Bearer Token）
POST   /v1/chat/completions                     对话补全（支持 stream 流式）
POST   /api/v1/keys                             创建 API Key（明文仅返回一次）
POST   /internal/routes                         注册模型路由（内部）
```

调用示例：

```bash
curl http://localhost:8083/v1/chat/completions \
  -H "Authorization: Bearer <API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen-demo","messages":[{"role":"user","content":"你好"}]}'
```

### 可观测性（observability :8084）

```text
POST   /api/v1/metrics/requests                 上报请求指标（gateway 调用）
POST   /api/v1/metrics/gpu                      上报 GPU 利用率（采集器调用）
GET    /api/v1/deployments/{id}/metrics?range=1h  请求指标序列（requests/errorRate/ttftMs/tokensPerSec）
GET    /api/v1/gpus/metrics?range=1h            GPU 利用率序列
```

`range` 支持：`5m` / `15m` / `30m` / `1h`（默认）/ `6h` / `24h`。

## 5. 演示流程（MVP 验收场景）

```text
发现 GPU → 注册模型 → 创建 vLLM 服务 → 获取 Endpoint
→ OpenAI API 调用 → 查看 TTFT/GPU 指标 → 扩容 → 删除并释放资源
```

端到端验收步骤由 `hack/e2e-test.sh` 自动执行。

## 6. 目录结构

```text
kk-infra/
├── go.work                    # 多模块编排
├── docs/                      # PRD / 架构 / 路线图 / 交互原型
├── lib/                       # 共享库（domain / errcode / apitypes / middleware）
├── services/
│   ├── controlplane/          # 控制面（8080）
│   ├── modelregistry/         # 模型注册中心（8081）
│   ├── k8sadapter/            # K8s 适配器（8082）
│   ├── gateway/               # 推理网关（8083）
│   ├── observability/         # 可观测性（8084）
│   ├── inference/             # Mock vLLM（8085）
│   └── pipeline/              # 发布流水线（后续轮骨架）
├── frontend/                  # Vue 3 + Vite + TS 管理台
└── hack/                      # dev-up / e2e / 构建脚本
```

## 7. 技术选型

| 项 | 选择 | 理由 |
|---|---|---|
| 语言 | Go 1.26.5 | 仓库已声明 go 1.26 |
| HTTP | 标准库 `net/http`（Go 1.22+ 方法路由） | 零依赖、够用 |
| 存储（MVP） | 内存 repository（`sync.RWMutex`）+ 接口 | 接口预留 Postgres 后续轮 |
| K8s 客户端 | 自定义 `KubeClient` 接口 + Fake 实现 | 接口隔离，真实/模拟可切换 |
| 前端 | Vue 3 + Vite + TS（vue-router + fetch，不引 UI 框架） | 轻量管理台 |
| 日志 | `log/slog` | 零依赖；RequestID 中间件注入 |

## 8. 当前能力边界

- 适配器已提供 vLLM、Triton、TensorRT-LLM 的基础运行时渲染；后两者的真实模型仓库与集群启动尚未验收。
- Volcano Queue/PodGroup 集成和 JupyterHub 部署清单已提供；需要安装对应组件的真实集群验收。
- `unified` 部署可用；`disaggregated` 的 NIXL 代理已有原型，但 Prefill/Decode Kubernetes 生命周期仍未完成，适配器会拒绝该模式。
- 多集群注册、基础放置、informer 容量采集、Volcano/Prometheus/DCGM 上报、独立集群告警和受审计人工故障重建已有首版。新建、扩容、升级和重建提交前已接入持久化并发容量预留；升级保留旧模板，重建保留原集群预留。缩容及旧模板/孤儿预留回收、租户配额一致性与失败/重启完整修复尚未闭环，凭据自动续期和跨集群入口发现仍待完成。旧部署缺少对应模型版本预留时扩容暂时拒绝，需迁移或对账恢复。详细状态见 [路线计划](docs/ROADMAP-V2.md) 与 [六个里程碑收口清单](docs/MILESTONE-COMPLETION.md)。AIOps 仍属后续范围。
- 旧集群清理首版：管理员通过部署 `orphan-reservations` 查询核对，再显式调用 `POST /api/v1/deployments/{id}/orphan-cleanup` 并确认删除。清理认领阻止跨集群重建，身份/UID 校验删除确认后只释放旧集群预留；执行中崩溃保留锁定，不自动解锁。需要应用 migration 022 并先升级 adapter，不能回退到不校验身份的删除接口。

## 9. 多集群容量心跳

Controlplane 启用身份服务并设置 `CLUSTER_ENCRYPTION_KEY`（32 字节随机密钥的标准 Base64 编码）后，平台管理员可通过 `POST /api/v1/clusters` 注册集群。空身份库的 `/api/v1/auth/bootstrap` 现在创建首个平台管理员；已有账号角色不会自动改变。初始化应由安装者在开放服务入口前完成。请求字段为 `id`、`name`、`endpoint`（Kubernetes HTTPS API 地址）、`adapterUrl`、`kubeconfig`、`labels`、`supportedRuntimes`、可选 `allowedTenants` 和 `servingUrlTemplate`。型号和 runtime 必须与模型版本匹配。

跨集群使用 Gateway 时，`servingUrlTemplate` 必须能把 `{service}` 和 `{namespace}` 映射到 Gateway 可达的 HTTPS 推理入口，例如 `https://{service}.{namespace}.west.example.com`。管理员需要在目标集群的入口控制器及 DNS 中配置对应的路由与 TLS；控制面只解析并持久化地址，不创建入口资源。已注册集群可通过 `PUT /api/v1/clusters/<id>/serving-route` 提交 `{"servingUrlTemplate":"https://{service}.{namespace}.west.example.com"}`。缺少模板的集群不会承接需要 Gateway 的新部署；更换模板后，现有部署下一轮对账会更新持久化路由。单集群部署仍使用 adapter 返回的集群内地址。

通过 `POST /api/v1/clusters/<id>/agent-token` 使用管理员 access token 签发心跳凭据，将响应的 `data.token` 安全保存为 agent 的只读文件。响应 `data.expiresAt` 为到期时间（24 小时）；到期前重新签发并替换文件。Agent 不需要控制面的 JWT 签名密钥。使用 Kubernetes Secret volume 时挂载整个目录，避免 `subPath` 阻止 token 文件更新。

```bash
go run ./services/k8sadapter/cmd --fake=false \
  --kubeconfig=/run/secrets/cluster/kubeconfig \
  --cluster-id=gpu-west \
  --controlplane-url=https://controlplane.example.com \
  --cluster-agent-token-file=/run/secrets/agent/token
```

Agent 启动后立即上报，以后每 30 秒更新一次。真实 adapter 使用 client-go Node/全命名空间 Pod informer；Kubeconfig 身份须有集群范围的 `list/watch nodes`、`list/watch pods` 权限，并允许 `GET /version` 健康探测。初次缓存同步未完成、watch 错误或 API 探测失败时上报 unhealthy，不使用 REST 全量查询绕过缓存错误。GPU 型号优先使用节点标签 `carrot.ai/gpu-type`，其次 `nvidia.com/gpu.product`；缺少标签或节点非 Ready/不可调度时上报 unhealthy。占用统计按已绑定且未终止的 Pod 请求量计算，包括正在删除的 Pod；GPU 限额默认请求与 init/sidecar 峰值遵循 [Kubernetes GPU](https://kubernetes.io/docs/tasks/manage-gpus/scheduling-gpus/) 和 [sidecar 资源规则](https://kubernetes.io/docs/concepts/workloads/pods/sidecar-containers/#resource-sharing-within-containers)。这不是 DCGM 利用率采集。

本地可用 `--fake=true` 演示心跳，该数据是模拟容量；`--virtual-gpus` 也仅用于开发。超过 90 秒没有新心跳的集群会被放置筛选排除。当前采用保守策略：任一 GPU 节点异常即停止将新部署放入该集群。Agent 通过 HTTPS 连接控制面；跨集群 adapter 和推理入口的实际连通性仍须在部署环境验证。

真实 adapter 增加 `--volcano-enabled=true` 后，每轮心跳查询 `scheduling.volcano.sh/v1beta1/queues`；kubeconfig 身份还须有集群范围 `list queues` 权限。心跳及管理员集群列表的 `volcanoQueues` 保存状态、capability/deserved/allocated 原始资源量和排队任务数；这些值不等同于可立即调度 GPU，最终准入仍由 Volcano 决定。CRD 缺失、权限拒绝或查询失败时报告 unhealthy 并清除旧快照。未启用 Volcano 时不查询该 API。已有 PostgreSQL 安装启动控制面时需执行 migration 017。

可选 `--agent-prometheus-url=http://prometheus.monitoring.svc.cluster.local:9090` 启用本集群遥测，要求 Prometheus 的 `job="dcgm"` 仅包含该集群 exporter。上报 GPU 利用率（%）、显存使用/剩余（MiB）、最后 XID 错误码及 scrape up，保留 labels、求值时间和最旧样本年龄；求值时间不是原始 scrape 时间。缺失、过期（90 秒）、非有限值和无效大值都报告为遥测 unhealthy，而非伪零值。遥测不可用不会单独导致部署 FAILED，历史 XID 也不是当前硬件健康判定。未启用时 `telemetry` 缺省，重新禁用后清除旧快照；PostgreSQL 使用 migration 018 持久化此字段。

管理员 `GET /api/v1/clusters/alerts` 查询当前集群告警：注册后/最后心跳超过 90 秒为 `offline`，新鲜 unhealthy 心跳为 `unhealthy`，启用的遥测失败或过期为 `telemetry`。无需先创建部署；离线时不再重复报警旧遥测，恢复后条件解除。返回稳定 `fingerprint` 和最近观测时间，不是持久化告警历史。

Prometheus 抓取使用管理员 `POST /api/v1/clusters/monitor-token` 签发的专用 token（24 小时），只允许 `GET /internal/clusters/metrics`，不允许管理 API 或心跳写入。将 token 作为只读 Secret 文件挂载到 Prometheus，再启用 `deployments/k8s/11-prometheus.yaml` 的 `carrot-cluster-monitor` 示例 job，使用 `authorization.credentials_file` 读取文件；不要使用管理员 access token 或在 ConfigMap 中写明文凭据。到期前重新签发并替换文件。跨网络抓取使用 HTTPS，并限制入口来源为监控服务。`13-alert-rules.yaml` 包含独立集群规则；生产通知还需配置 Prometheus 到 Alertmanager 及其接收器，当前清单不自动安装通知渠道。

运行 `bash hack/cluster-agent-e2e.sh` 可验证管理员初始化、登录、集群注册、token 签发、agent 上报与容量保存。脚本需要 Go、curl、jq，使用独立内存服务和 Fake K8s，默认端口为 18080/18082/18083；遇到占用会退出，完成后停止自身进程并保留临时日志。它不替代真实集群验收。
