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
- **Notebook 产品链路**：已补个人 workspace 创建/查询/删除 API、真实控制台会话与 UI、Hub audience 服务身份、启动授权复核、PodGroup、受限 SA 和网络配置；本地 HTTP/配置测试不替代真实 Hub spawning、持久卷和 Volcano 验收。配置和边界见 [Notebook 指南](NOTEBOOK.md)。
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

- 旧集群预留核对入口：`GET /api/v1/deployments/{id}/orphan-reservations` 仅平台管理员可用，返回当前集群和该部署在其他集群的预留及集群健康状态，按集群/代际排序；新增 `cleanupClusterId`、`cleanupRunning` 显示清理阻止状态。故障或过期集群的预留仍列出，空结果为数组；不返回凭据或 adapter 地址，不调用删除或释放。HTTP 契约回归覆盖鉴权、缺失部署、筛选、空结果及只读边界。该结果只证明账本残留，不证明实际工作负载已消失。
- 显式清理首版：平台管理员调用 `POST /api/v1/deployments/{id}/orphan-cleanup`，请求示例 `{"clusterId":"gpu-west","acknowledgeDelete":true}`。只处理非当前集群、同部署/租户/namespace 的账本预留；无预留且无清理认领时幂等返回，不删除无账本孤儿。migration 022 以部署行持久化旧集群及执行标志，状态/generation 条件认领只允许一个执行；认领存在期间禁止跨集群重建。失败保留旧集群阻止状态、结束本次执行，可重试同一集群；adapter 确认删除后释放该旧集群全部模板预留，再解除认领，记录认领/失败/完成审计。不改变当前部署状态、路由或租户配额。清理执行中崩溃保留 `cleanupRunning=true`，不提供自动超时解锁；需先确认旧执行已退出，再设计受审计恢复。此边界尚未闭环。
- 清理删除身份保护：使用独立 `DELETE /v1/deployments/{name}/managed?namespace=...&deploymentId=...`，旧 adapter 不支持时失败关闭，禁止退回普通删除接口。真实 adapter 在任何删除前核对全部存在对象的部署 ID、managed-by 标签与 UID，所有 DELETE 带 UID 前置条件；缺失标签/UID 或同名对象身份不符则拒绝，不降级猜测归属。继续直接确认控制器、关联资源、ReplicaSet/Pod 已退出；API 不通、仍在终止或 UID 已变化都保留旧预留。孤儿 ReplicaSet/Pod 若无法随控制器级联删除仍需人工核实，不直接按标签删 Pod。已有 HTTP mock、认领竞争、失败重试、权限及 UID 变化回归；Fake 二进制 E2E 覆盖身份拒绝与幂等删除，不替代真实 Kubernetes/PostgreSQL 验收。
- 删除确认前置：真实 adapter 对 Deployment/Rollout 发起 `Foreground` 删除，关联资源删除错误不再忽略；直接查 API server 确认各对象不存在，并确认同 namespace、`app=<name>` 的 ReplicaSet 和 Pod 列表为空（包括终止中对象），才返回成功。未退出或查询失败时保留集群预留与租户配额。普通未完成删除保持 DELETING，由现有 Reconciler 在三分钟超时后周期重试；重复显式删除不另起操作。adapter 凭据需具备 ReplicaSet/Pod list 权限，否则失败关闭。已有 HTTP mock 的 Deployment/Rollout、终止中 Pod、残留 ReplicaSet、关联资源失败及重试回归；没有做真实 API server 的 finalizer/GC 验收。
- 删除恢复与配额释放凭证：migration 021 新增 `deployment_quota_releases`，按部署 ID 保存租户、型号、数量。内存锁或 PostgreSQL 事务将凭证与配额扣减一起提交，重复释放幂等、参数冲突拒绝；凭证不随删除记录清理。普通删除与后台重试共用完成流程：确认 adapter 删除、释放集群预留、幂等释放租户配额、撤销路由，再条件更新 DELETED；任一步失败保留 DELETING。后台重试不会处理 RUNNING/DELETED 部署。已有并发释放、释放后终态写入中断/用例重建恢复、Reconciler 持续重试及不影响其他部署配额回归。此凭证只保障删除扣减不重复，不替代创建/升级/缩容的配额占用账本，也不自动修复历史 DELETED 记录漏释放；真实 PostgreSQL 事务、中断及多控制面资源操作互斥尚未验收。
- 重建容量拒绝恢复：目标容量预留返回错误时尚未提交工作负载，repository 按目标集群、SUBMITTING 状态及 generation 条件恢复原集群归属与 FAILED 状态，保留递增 generation 和原集群故障诊断，允许再次人工重建。过期恢复不能覆盖新操作；记录认领及拒绝审计。内存与 PostgreSQL 条件更新均已实现，回归覆盖连续重试、旧预留保留、不调用 adapter 及过期恢复拒绝。预留数据库提交结果不确定时不自动删除可能已写入的目标预留；该残留仍需对账。此恢复不适用于已经提交 adapter 后的失败。
- 升级/重建提交预留：升级在认领状态后、调用 adapter 前预留完整新模板容量，旧模板预留继续保留，因此需要重叠容量；容量拒绝不切换模型版本、不提交 adapter。adapter 不确定失败保留两份预留。人工重建按状态、原集群和 generation 原子认领，仅获胜请求预留目标集群，原集群预留不释放；目标预留失败记录 FAILED 并停止提交，认领审计仍保留。预留新增 `modelVersionId`，扩容只增长当前模型版本对应的最新模板，回滚不能借用失败版本预留。旧账本缺少版本绑定时扩容失败关闭，需要迁移/对账恢复。已有升级容量拒绝、失败保留、跨集群双账本和回滚容量隔离回归；租户旧/新模板配额、旧模板退休和重建失败恢复仍未闭环。
- 扩容预留：部署 repository 以状态/generation 条件认领 SCALING；集群 repository 在同一锁/行锁下单调提高该部署、对应 GPU 型号的最新模板预留，不创建新的模板代际。adapter 调用前容量拒绝会归还本次新增租户配额并恢复旧副本数；调用后不确定失败保留目标副本数、配额与预留。对账跳过正在执行的 SCALING，超时/中断标记 FAILED 而不假定资源释放；删除/重启/升级使用条件状态更新，拒绝覆盖已认领操作。内存部署读写深拷贝启动参数，避免共享指针绕过锁。已有阻塞 adapter、跨部署容量竞争、配额回滚与超时回归。缩容成功只释放租户配额，集群预留保持高水位直到删除；旧部署无相应型号账本时扩容失败关闭。该保守策略尚未完成缩容回收或旧账本迁移。
- 新建部署 GPU 原子预留首版：migration 020 在集群行持久化 `gpu_reservations`；内存实现用互斥锁，PostgreSQL 使用 `SELECT FOR UPDATE` 与心跳 UPDATE 串行化。余量扣除 Used 和每项预留中未被同部署/租户/namespace/模板代际 Pod 覆盖的部分，外部/旧模板占用不抵扣。新建部署在异步提交前预留，重复请求幂等；容量竞争失败记录 FAILED，保留租户配额直到显式删除。确认工作负载删除后释放该集群的预留，删除重试也执行释放；仍在心跳中出现的 Pod 占用不被清空。集群有预留时不能注销。单测覆盖 64 个请求竞争 8 张 GPU、内存服务重建保留账本和不同模板抵扣；PostgreSQL SQL 已实现但未做实例级验证。扩容及升级/重建提交接入见上项；安全回收、配额一致性及恢复尚未闭环，不能宣称整条容量生命周期已完成。
- GPU 占用归属报告：真实 adapter 从同一份 Pod 列表计算节点 Used 与 `deploymentGpu`（部署、租户、namespace、节点、GPU 型号及请求数量），保留已绑定的非终态/终止中 Pod；外部或缺少受管标签的 Pod 只计入总占用。心跳校验归属总和不超过各型号 Used，并通过 migration 019 与容量快照一起持久化。旧 agent 省略字段或失败上报时清除旧归属。虚拟 GPU/Fake 源暂不提供归属；节点与 Pod informer 缓存不是 API server 的原子快照。
- 模板代际：创建、升级及跨集群重建将控制面当前 generation 写入 `carrot.ai/template-generation` Pod 标签，agent 上报可选 `templateGeneration`，保留零值并拒绝负数/无效标签；旧 Pod 缺少标签时为未知。扩缩容只修改副本数，不更新模板标签或触发滚动替换；重启保持模板代际。标签不进入 Deployment 的不可变 selector。该字段是模板快照的代际，不是 Kubernetes resourceVersion，也不代表当前扩缩容操作已完成。已有渲染、HTTP、采集与持久化内存快照隔离测试，尚不用于自动释放预留。
- PostgreSQL migration 015 新增集群注册表、部署 `cluster_id` 和 Gateway route `cluster_id` 字段；016 为已有集群增加推理入口模板。
- Controlplane 集群注册/列表 API 限平台管理员调用；kubeconfig 使用 `CLUSTER_ENCRYPTION_KEY`（32 字节随机密钥的标准 Base64 编码）通过 AES-256-GCM 加密保存，响应不返回凭据。
- 集群健康与 GPU 容量心跳 API 要求 `cluster-agent:<clusterID>` audience 且 subject 与集群 ID 一致的 JWT。
- 内存和 PostgreSQL 集群 repository 均已提供；注册、凭据加密、容量校验和身份边界有单测/HTTP 契约测试。
- 新建部署按新鲜健康心跳、GPU 余量、runtime 支持和租户 allow-list 选择集群；部署生命周期及状态对账按持久化的 `cluster_id` 调用对应 adapter。扩容和升级校验原集群容量，不会重新放置。
- Adapter 设置 `--cluster-id`、`--controlplane-url`、`--cluster-agent-token-file` 后立即上报，此后每 30 秒上报 GPU 容量。真实 adapter 启动 client-go Node/全命名空间 Pod informer，通过 list/watch 缓存节点标签和已绑定、未终止 Pod 的 GPU 请求；包含 init container 与原生 sidecar 峰值。初次同步未完成、watch 错误、API 健康探测失败或节点异常时上报 unhealthy 并清空容量。退出时停止 watch。
- 平台管理员可通过 `POST /api/v1/clusters/{id}/agent-token` 签发有效期 24 小时的专属心跳凭据，签发动作写审计；agent 每轮重读 token 文件以支持轮换。该 token 不具备 controlplane 管理权限。
- 空身份库初始化创建首个平台管理员；租户管理员不能授予或修改平台管理员身份。`hack/cluster-agent-e2e.sh` 覆盖从初始化、登录到 agent 上报的独立进程 Fake 链路。
- 集群注册可指定 `servingUrlTemplate`，已有集群可由管理员调用 `PUT /api/v1/clusters/{id}/serving-route` 补配。Gateway 部署仅选择拥有入口模板的健康集群；部署、stable/canary 地址和后续对账解析为可持久化的 HTTPS 跨集群入口。
- 对账器现检查 RUNNING 部署；目标集群心跳失效或状态 unhealthy 时，部署标记 FAILED、写审计并撤销 Gateway 路由。入口模板变化时对账更新路由，健康且未变化时不重复注册。
- 集群查询会把超过 90 秒未更新的 healthy 心跳呈现为 `stale`；持久化的最后一次原始上报不被覆盖。
- 启用 `--volcano-enabled` 的真实 adapter 每轮心跳查询 Queue API，报告队列状态、capability/deserved/allocated 原始资源量及 pending/running/inqueue 数量。Migration 017 持久化 `volcano_queues`，管理员集群查询可读取；Volcano API 查询失败时整轮报告 unhealthy 并清空容量/队列快照。未启用时保持兼容，不查询 CRD；此报告不是 GPU 预留或调度承诺。
- Agent 可通过 `--agent-prometheus-url` 启用本集群 Prometheus/DCGM 遥测报告。保存 `job="dcgm"` 的 scrape up、GPU 利用率、显存使用/剩余（MiB）和最后 XID 错误码；含原始 labels、值、查询求值时间及最旧样本年龄。拒绝空/缺失、超过 90 秒、NaN/Inf 和 DCGM 无效大值。Migration 018 持久化 `telemetry`；未启用为 null，失败为 unhealthy 且清空样本。遥测状态与 Kubernetes 容量健康分开，不因监控故障撤销现有服务路由，也不把历史 XID 直接等同于当前 GPU 不可用。
- 独立集群告警首版：管理员 `GET /api/v1/clusters/alerts` 查询当前 offline/unhealthy/telemetry 条件，依据持久化心跳计算，不依赖部署存在；首次心跳宽限 90 秒，恢复后解除，稳定 fingerprint 避免重复。`POST /api/v1/clusters/monitor-token` 签发 24 小时、仅能抓取 `/internal/clusters/metrics` 的 audience 限定 JWT。Prometheus 告警规则覆盖离线、容量异常、遥测不可用与抓取失败；需由部署方挂载 token、启用示例 scrape job 并配置 Alertmanager 接收器。当前不保存告警历史，不代表通知渠道已经部署。
- 平台管理员可更新集群放置策略（`PUT /api/v1/clusters/{id}/placement-policy`）、轮换加密 kubeconfig（`PUT /api/v1/clusters/{id}/credentials`）、注销无未删除部署的集群（`DELETE /api/v1/clusters/{id}`）；操作有审计记录，响应不返回凭据。
- 集群故障导致部署 FAILED 后，平台管理员可调用 `POST /api/v1/deployments/{id}/rebuild`，传入 `targetClusterId` 和 `acknowledgeOrphanedResources: true`。仅允许重建到其他健康且具备 runtime、租户策略、GPU 容量及 Gateway 入口的集群；原子状态认领阻止重复操作，并记录旧集群可能残留资源的事件和审计。原集群恢复后仍须人工清理残留工作负载。

### 尚未完成

- 容量预留前置修复：租户配额增加现在以内存锁或 PostgreSQL 条件 UPDATE 原子校验上限，避免并发请求超出同一租户配额；内存查询返回快照。创建记录写入失败会归还本次预留，缩容失败保留旧副本配额，顺序重复删除不会再次释放。已有并发与生命周期回归测试。此修复不等同于集群 GPU 预留；并发删除、数据库状态与配额跨表事务、按部署占用对账和重启修复仍未闭环。集群预留不能直接叠加到心跳 Used，否则重复计算已调度 Pod；下一步需要关联模板代际、持久化预留与扩缩容操作，并在同一数据库事务内认领容量。
- Agent 已使用 client-go informer/watch，并提供 Volcano 与 Prometheus/DCGM 报告首版。队列数据使用每轮心跳的 REST 查询，不是 Queue informer；尚未纳入控制面队列准入预检查。尚未实现基于硬件健康状态的准入控制。GPU 并发预留已接入新建、扩容、升级和重建提交；缩容、旧模板与旧集群残留资源回收及配额/恢复仍需闭环。上报的 GPU 占用是调度资源请求，并非实际利用率。Informer 的 Fake 测试覆盖初次同步、Pod 新增/删除及错误状态，不替代真实 API server 的断线重连验收。
- 控制面现可用集群入口模板把内部 Service 名映射为 Gateway 可达地址；目标集群的 DNS、TLS、入口控制器和真实网络连通性由部署环境提供，尚无自动发现/创建入口资源和真实跨集群流量验收。
- 注册、agent/monitor token 轮换由管理员操作，尚未提供自动续期；集群独立告警已提供，但生产环境的 scrape 凭据挂载、Alertmanager 路由/接收器及通知验收仍需部署配置。
- 已运行服务不在线迁移；人工重建不自动清理失联集群的旧资源。集群数据加密密钥的轮换策略待明确。

真实集群测试仍按计划暂缓。当前验证覆盖本地单测、HTTP 契约（含真实 REST 客户端的模拟 Kubernetes API、agent 汇总/失败/凭据轮换）和独立进程 Fake E2E（初次上报及下一周期 GPU 占用更新），不代表真实多集群调度或集群 agent 已可生产运行。
