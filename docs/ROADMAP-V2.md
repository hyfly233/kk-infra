# Carrot AI Infra 路线计划（V2）

> 依据当前代码与“AI 推理连接 IaaS/PaaS”的产品方向。执行基线：单集群、vLLM、OpenAI 兼容 API；Fake K8s 只用于开发验收，不能作为生产能力。

## 1. 当前判断

仓库已经完成一个可演示的 MVP 闭环：`模型注册 → GPU 资源检查 → 部署生命周期 → OpenAI API → 指标 → 扩缩容/删除`。现有实现的关键边界是：内存存储、Fake K8s/Mock 推理、单租户、基础路由和指标聚合。

因此下一阶段不应继续横向增加组件，而应补齐生产闭环的四个缺口：持久化与恢复、真实 Kubernetes/Prometheus、租户隔离与配额、可诊断的发布流程。

## 2. 分阶段路线

### R1：MVP 收口（当前）

目标：让本地演示和代码契约稳定。

- 修复并固化 E2E 流式断言
- 统一 API 错误、状态、标签和幂等语义
- 补齐服务详情中的事件、诊断、API 示例
- 用契约测试覆盖 controlplane、gateway、observability

验收：一条脚本稳定通过“部署→调用（流式/非流式）→指标→扩容→删除释放”。

### R2：单集群生产化（优先级最高）

目标：真实集群上可持续运行一个租户或多个基础租户。

- Postgres 替换内存 repository；迁移、索引、审计事件持久化
- 真实 client-go/informer；控制面重启后从 Kubernetes 对账恢复
- Prometheus + DCGM Exporter；指标按 deployment/tenant/model 维度查询
- API Key、RBAC、Namespace、ResourceQuota、NetworkPolicy
- 部署超时、重试、失败诊断、删除清理和孤儿资源回收
- 使用 Kueue 或 Volcano 实现队列/配额，不自研调度器

门槛：控制面重启不丢状态；跨租户不可见/不可调用；GPU 与部署状态最终一致；故障能定位到 Pod 事件或运行时日志。

### R3：推理平台能力

目标：从“能部署”升级为“可发布、可扩缩、可运营”。

- 模型版本发布门禁：artifact 校验、启动探针、基准测试、人工确认
- 灰度/回滚、模型路由、限流和 fallback
- 基于 QPS、队列长度、TTFT、KV Cache 的推理扩缩容
- 成本估算、Token 用量、租户账单维度
- 生产 Dashboard、告警和 SLO（可用性、TTFT、错误率）

门槛：一次发布可回滚；扩缩容不会破坏正在服务的请求；每个租户能解释资源和 Token 消耗。

### R4：平台扩展

多运行时、Volcano、租户 Notebook 和 Prefill/Decode 分离已进入里程碑 5。当前实现状态与剩余工作见下文；多集群调度和 AIOps Agent 仍属后续范围。

## 3. 里程碑 5：运行时与高级工作负载状态

本节以代码实现状态为准。真实集群验收暂缓，不影响记录功能实现和 Fake/单测覆盖之间的区别。

### 已实现

- **RuntimeDriver 基础实现**：vLLM、Triton、TensorRT-LLM 可选择运行时镜像、启动参数、健康探针和指标映射；modelregistry 接受这三种 runtime。
- **Volcano 基础集成**：适配器可按开关渲染 Queue/PodGroup 与 `schedulerName: volcano`，并负责创建租户 Queue 和 PodGroup。默认关闭；真实 Volcano CRD/API 尚未验收。
- **JupyterHub 基础部署**：提供 Hub 清单、平台 JWT introspection authenticator、租户 namespace 与受限 Notebook ServiceAccount/NetworkPolicy 配置，并将 Notebook Pod 指向 Volcano 队列。
- **Disaggregated proxy 原型**：新增 NIXL Prefill→KV transfer metadata→Decode HTTP 代理，支持 OpenAI completions 路径、流式响应透传、会话键选址和缺少 KV 元数据时失败关闭；已有本地 HTTP 单测。
- **Serving mode 数据链路**：部署 API、领域对象和迁移已记录 `serving_mode`，默认 `unified`，仅允许 vLLM 选择 `disaggregated`。

### 未完成（阻止完整验收）

- **Disaggregated Kubernetes 编排**：尚未渲染/管理 Prefill、Decode、Proxy 三类 Deployment/Service；升级、删除、状态汇总、Volcano PodGroup 和 Argo Rollouts 生命周期没有接通。真实适配器因此拒绝 disaggregated 部署，即使配置了 proxy image。
- **NIXL runtime 配置与配对**：Prefill/Decode 启动参数、跨 Pod NIXL 网络/side-channel 配置尚未实现。代理当前对 Prefill 和 Decode 端点分别哈希选择，无法保证同一会话落到同一组配对实例。
- **Notebook 产品链路**：尚无面向用户的 workspace 创建/查询/删除 API 和 UI，也未完成 Hub 到平台的服务身份授权与网络策略端到端配置。现有清单是部署基础，不足以据此宣称 Notebook 功能已验收。
- **多运行时兼容性**：Triton/TensorRT-LLM 的模型仓库布局、artifact 加载约定、真实启动和请求协议尚未集成验收；当前 driver 覆盖的是基础容器渲染契约。

### 验收边界与后续顺序

单元测试/Fake 测试不能证明 Volcano 调度、JupyterHub spawning、GPU 运行时或 NIXL 跨 Pod 传输在真实集群可用。真实集群测试按用户决定暂缓；在进入集群验收前，先完成 disaggregated 资源生命周期、NIXL 实例配对和 Notebook 管理/API 授权，再补相应契约测试及 Fake K8s 测试。

## 4. 近期迭代顺序

1. E2E/契约测试与文档一致性
2. Postgres repository + migration
3. 真实 K8s adapter + informer/reconcile
4. Prometheus/DCGM 指标链路
5. 租户/RBAC/配额/安全基线
6. 发布门禁与灰度回滚

## 5. 暂不做的决定

不在当前阶段引入 Kubeflow 全家桶、Karmada、自研 GPU scheduler 或 Agent AIOps。原因是它们不能直接解决当前代码的可靠性和产品闭环问题。

## 6. 里程碑 6：多集群控制面进度

### 已实现切片

- PostgreSQL migration 015 新增集群注册表、部署 `cluster_id` 和 Gateway route `cluster_id` 字段；016 为已有集群增加推理入口模板。
- Controlplane 集群注册/列表 API 限平台管理员调用；kubeconfig 使用 `CLUSTER_ENCRYPTION_KEY`（32 字节随机密钥的标准 Base64 编码）通过 AES-256-GCM 加密保存，响应不返回凭据。
- 集群健康与 GPU 容量心跳 API 要求 `cluster-agent:<clusterID>` audience 且 subject 与集群 ID 一致的 JWT。
- 内存和 PostgreSQL 集群 repository 均已提供；注册、凭据加密、容量校验和身份边界有单测/HTTP 契约测试。
- 新建部署按新鲜健康心跳、GPU 余量、runtime 支持和租户 allow-list 选择集群；部署生命周期及状态对账按持久化的 `cluster_id` 调用对应 adapter。扩容和升级校验原集群容量，不会重新放置。
- Adapter 设置 `--cluster-id`、`--controlplane-url`、`--cluster-agent-token-file` 后立即上报，此后每 30 秒上报 GPU 容量。真实采集使用节点标签和全命名空间已绑定、未终止 Pod 的 GPU 请求；包含 init container 与原生 sidecar 峰值。采集失败或节点异常时上报 unhealthy 并清空容量。
- 平台管理员可通过 `POST /api/v1/clusters/{id}/agent-token` 签发有效期 24 小时的专属心跳凭据，签发动作写审计；agent 每轮重读 token 文件以支持轮换。该 token 不具备 controlplane 管理权限。
- 空身份库初始化创建首个平台管理员；租户管理员不能授予或修改平台管理员身份。`hack/cluster-agent-e2e.sh` 覆盖从初始化、登录到 agent 上报的独立进程 Fake 链路。
- 集群注册可指定 `servingUrlTemplate`，已有集群可由管理员调用 `PUT /api/v1/clusters/{id}/serving-route` 补配。Gateway 部署仅选择拥有入口模板的健康集群；部署、stable/canary 地址和后续对账解析为可持久化的 HTTPS 跨集群入口。
- 对账器现检查 RUNNING 部署；目标集群心跳失效或状态 unhealthy 时，部署标记 FAILED、写审计并撤销 Gateway 路由。入口模板变化时对账更新路由，健康且未变化时不重复注册。
- 集群查询会把超过 90 秒未更新的 healthy 心跳呈现为 `stale`；持久化的最后一次原始上报不被覆盖。
- 平台管理员可更新集群放置策略（`PUT /api/v1/clusters/{id}/placement-policy`）、轮换加密 kubeconfig（`PUT /api/v1/clusters/{id}/credentials`）、注销无未删除部署的集群（`DELETE /api/v1/clusters/{id}`）；操作有审计记录，响应不返回凭据。
- 集群故障导致部署 FAILED 后，平台管理员可调用 `POST /api/v1/deployments/{id}/rebuild`，传入 `targetClusterId` 和 `acknowledgeOrphanedResources: true`。仅允许重建到其他健康且具备 runtime、租户策略、GPU 容量及 Gateway 入口的集群；原子状态认领阻止重复操作，并记录旧集群可能残留资源的事件和审计。原集群恢复后仍须人工清理残留工作负载。

### 尚未完成

- Agent 仍使用 REST 周期采集，尚未改为 client-go informer/watch，也未上报 Volcano 队列容量或 Prometheus/DCGM 健康指标。GPU 并发预留尚未实现；上报的 GPU 占用是调度资源请求，并非实际利用率。
- 控制面现可用集群入口模板把内部 Service 名映射为 Gateway 可达地址；目标集群的 DNS、TLS、入口控制器和真实网络连通性由部署环境提供，尚无自动发现/创建入口资源和真实跨集群流量验收。
- 注册和 token 轮换由管理员操作，尚未提供自动续期；超过 90 秒未更新的容量不会参与放置并触发部署失败与路由撤销，但尚无独立集群离线告警。
- 已运行服务不在线迁移；人工重建不自动清理失联集群的旧资源。集群数据加密密钥的轮换策略待明确。

真实集群测试仍按计划暂缓。当前验证覆盖本地单测、HTTP 契约（含真实 REST 客户端的模拟 Kubernetes API、agent 汇总/失败/凭据轮换）和独立进程 Fake E2E（初次上报及下一周期 GPU 占用更新），不代表真实多集群调度或集群 agent 已可生产运行。
