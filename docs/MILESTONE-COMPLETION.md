# 六个里程碑收口清单

本清单对齐用户指定的六个里程碑。代码存在、单测通过、Fake E2E、真实集群验收分别记录，不互相替代。真实集群验收按此前决定暂缓；未勾选项不宣称完成。

## 1. MVP 运行链路

- [ ] 逐项复核 restart、upgrade、Gateway PostgreSQL 恢复、详情指标/事件/诊断/日志及设置页，补齐对应测试证据。
- [ ] 修复完整契约/E2E 的 artifact fixture，让开发链路能够通过实际 artifact 校验与发布门禁，而非绕过它们。

## 2. 安全、多租户与单集群

- [ ] 复核全部管理 API 的用户/租户/服务 audience 授权和凭据脱敏，形成越租户、伪造 JWT、refresh token 失效测试矩阵。
- [x] gateway Key 管理支持管理员 JWT/租户隔离和不变租户轮换，控制台创建 Key 使用当前租户，内部路由限定 controlplane 服务 JWT；observability 聚合接口限定调用服务，控制面/网关客户端及 GPU collector 已接服务 JWT。控制面部署指标先检查租户；JWT 强制期限/固定角色，Key 元数据隐藏哈希。全量 Go、前端构建/专项测试、HTTP 与独立进程 Fake E2E 通过，记录见 `SERVICE-AUTH.md`；不替代真实存储/网络验收。
- [ ] 已确认缺口：modelregistry 持久化租户归属及鉴权、adapter/pipeline 内部授权、停用租户的存量推理 Key 撤权和整体 NetworkPolicy 尚未收口。短期 JWT 校验不代表在线成员撤权；不标记安全里程碑完成。
- [ ] 对齐租户启停、Namespace/RBAC/Quota/NetworkPolicy 的幂等生命周期和失败恢复。
- [x] 孤儿扫描覆盖所有命名空间，按集群/namespace/名称匹配，检测事件写审计并在同一进程周期内去重；不自动删除。已补 Fake/模拟 API 与审计回归，真实 API 权限验收未运行。
- [ ] 验证副本漂移修复、租户配额与部署状态的原子认领、失败/删除/重启释放一致性。

## 3. 观测、弹性与账本

- [ ] 按原始计划复核指标维度、显存/TPOT/队列/KV cache/错误率和 KEDA 保护条件。
- [ ] 复核 Token、失败请求、GPU 副本时长账本，费率、日聚合、费用、CSV 及前端页面，并补计算/契约测试证据。

## 4. Artifact、门禁与渐进交付

- [ ] 复核 S3 artifact checksum/下载、发布阶段门禁/审批/审计、引用删除保护及凭据脱敏。
- [ ] 复核 Argo/Istio stable/canary、分析失败回滚、显式 rollback、控制面/网关重启恢复，补齐本地模拟契约。

## 5. 高级工作负载

- [ ] 完成 Prefill/Decode/Proxy 资源渲染、创建/升级/删除、状态与日志汇总，以及 Volcano/Argo 生命周期。
- [ ] 固定并验证 vLLM/NIXL 版本与配置，完成实例配对和请求亲和性；缺失能力继续拒绝创建。
- [ ] 完成 Notebook workspace 创建/查询/删除 API、租户授权、Hub 服务身份、受限 SA/Queue/网络策略及 UI。
- [x] Notebook 本地首版：个人 workspace API/UI、真实控制台登录/刷新/退出、每次成员状态复核、Hub 服务/启动 audience、Queue/PodGroup、受限 SA、网络策略更新和 Hub PVC 配置。Go HTTP、客户端/身份测试、前端会话竞争、配置 Python 测试和独立进程 Stub Hub/Fake adapter E2E 通过；真实 Hub/GPU/网络/PVC 验收未运行。部署与剩余运维边界见 `NOTEBOOK.md`，不以此勾选整项完成。
- [ ] 完成 Triton/TensorRT-LLM artifact 布局与协议适配，不能把基础容器渲染算作可用运行时。

## 6. 多集群

- [x] 集群身份/加密凭据、健康心跳、基础放置、跨集群入口模板、故障标记及受审计人工重建首版。
- [x] informer GPU 请求、Volcano Queue、Prometheus/DCGM、独立告警、占用归属和模板代际报告首版。
- [x] 新建部署的 GPU 预留账本：migration 020、内存锁/PG 行锁、同模板占用抵扣、删除释放和并发回归测试。
- [x] 扩容的状态/generation 条件认领及原模板预留增长；同部署并发扩容拒绝，adapter 不确定失败保留配额/预留，对账不提前结束 SCALING。内存仓库读写返回独立快照，状态转换使用条件更新。
- [x] 升级提交前预留完整新模板容量，重建在状态/generation 原子认领后预留目标集群；旧模板及原集群预留保留。扩容按模型版本选择模板，不能借用失败版本预留；已有容量拒绝、失败保留和旧/新集群账本回归。
- [x] 重建提交前容量预留失败时条件恢复原集群归属，保留递增 generation、旧预留及认领/拒绝审计；连续重试和过期恢复拒绝有回归测试。adapter 提交后的失败及不确定数据库提交仍需恢复/对账。
- [x] 真实 adapter 删除确认：前台级联删除控制器，直接查询确认控制器、关联资源及同 namespace/app 的 ReplicaSet/Pod 均退出后才返回成功。终止中资源、查询或关联删除错误保留预留与租户配额；HTTP mock 回归覆盖 Deployment/Rollout 及重试，不代表真实集群验证。
- [x] 未完成删除保持 DELETING 并由 Reconciler 周期重试；migration 021 的幂等配额释放凭证防止中断/重试重复扣减。路由撤销和终态写入失败也保留删除意图。已有并发释放、中断恢复及后台重试回归；历史漏释放、多控制面互斥和真实 PostgreSQL 事务验收仍待完成。
- [x] 平台管理员可查询部署在非当前集群的残留预留及集群健康状态；接口只读且不泄露连接凭据，鉴权/筛选/空结果有 HTTP 契约回归。该查询不替代安全清理或资源退出确认。
- [x] 管理员显式旧集群清理首版：migration 022 持久化清理认领，拒绝并发清理并阻止跨集群重建；身份校验删除确认后只释放旧集群预留，不释放当前租户配额。失败保留放置阻止，可重试同一旧集群；执行中崩溃保留运行锁。已有认领竞争、HTTP 权限/失败重试、归属/UID 防护及旧 adapter 不安全回退拒绝回归。
- [ ] 缩容及旧模板安全回收、清理执行中崩溃的安全解锁/恢复、无账本孤儿资源处置，租户配额与容量跨表一致性。无对应 GPU 型号及模型版本账本的旧部署扩容暂时拒绝，需补迁移/对账恢复。显式旧集群清理首版不代表这些任务已经完成。
- [ ] 处理部署记录、配额和容量跨表事务窗口及控制面重启修复；失败状态不等于资源已经清理。
- [ ] Volcano 队列/硬件健康准入预检查及集群凭据/告警部署运维闭环。

## 验证门槛

每个切片运行聚焦回归及 `./hack/check.sh`，前端修改运行 `npm run build`；HTTP/API、生命周期、路由或渲染修改运行相应契约/Fake E2E。真实 PostgreSQL、MinIO、Kind、Volcano/KEDA/Istio/Argo/DCGM、多 GPU runtime 和多集群流量验证单独列出，当前不标记通过。
