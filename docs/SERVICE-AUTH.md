# 管理接口与内部调用鉴权

## 当前已实施边界

controlplane、modelregistry、pipeline、gateway、observability 注入同一 `CARROT_AUTH_SECRET`；使用环境注入，不把密钥放进命令行或 Git。空密钥仅保留不安全的本地 Fake 开发兼容模式；不能用于生产。

| 接口 | 启用认证后的身份 |
| --- | --- |
| gateway `/api/v1/keys` 及子路径 | `aud=controlplane` 的平台/租户管理员用户 JWT |
| gateway `/internal/routes` | `aud=service:gateway`，subject 必须为 controlplane |
| observability 请求指标写入 | `aud=service:observability`，subject 必须为 gateway |
| observability GPU 指标写入 | 同 audience，subject 必须为 k8sadapter |
| observability 账单、费率和指标查询 | 同 audience，subject 必须为 controlplane |
| controlplane `/internal/resources/gpus` | `aud=service:controlplane`，subject 必须为 observability |
| modelregistry 模型/版本管理 | `aud=controlplane` 用户 JWT；对象按模型租户隔离，只读者不可修改 |
| modelregistry 内部读取 | `aud=service:modelregistry`，subject 为 controlplane 或 pipeline |
| modelregistry 版本 validate/release 内部写入 | 同 audience，仅 pipeline；用户不能直接 release |
| pipeline 发布/审批/查询 | 用户 JWT 与租户授权；启动和审批重新查询模型版本归属 |

内部 HTTP 客户端每次生成一分钟服务 JWT，使用专用 audience、固定只读 role、空 tenant；服务身份不可用于用户管理 API。服务客户端拒绝重定向，不向跳转地址传递凭据。所有 JWT 必须带有效期限和四级角色之一。

租户管理员只能列出/修改本租户 Key，越租户 Key ID 返回 404；平台管理员可管理所有租户。轮换只能继承旧 Key 的租户，不能重新归属。响应不含哈希；新明文只在创建/轮换结果返回，响应禁止缓存。控制面部署指标查询先检查所属租户，再向 observability 转发。用户账单仍经控制面租户授权接口访问，不直接开放观测服务聚合查询。

OpenAI `/v1/models`、`/v1/chat/completions` 继续使用 API Key，不改为用户 JWT。

## 模型归属与发布

migration 023 持久化模型 `tenant_id`，版本继承模型归属。普通用户创建模型时以 JWT 租户为准；平台管理员可明确指定租户。模型名称仍全局唯一。部署新建、升级、重建和历史版本恢复检查版本归属；同名部署也不能跨租户返回。

历史模型不自动分配给 `default`：空归属记录仅平台管理员可读，禁止修改、发布或部署。运维须先依据历史部署和业务记录确认所有者，再进行受控数据库归属迁移；当前没有自动归属恢复 API。只读者的版本响应隐藏 artifact URI，S3 URI 拒绝内嵌凭据、查询参数和片段。

控制台通过 `/pipeline` 启动流水线及人工审批，不再直接调用 release；生产反向代理须将此前缀转发到 pipeline 并重写为 `/api`，开发 Vite 已配置。流水线对已 VALIDATED 版本仍重新校验 artifact；发布写入只接受 pipeline 服务 JWT。此权限门禁不代表临时模型探针、基准或真实 S3 校验已验收。

## 网络与运维

`deployments/k8s/14-observability-access.yaml` 限制 observability 8084 入口为同 namespace 的 controlplane、gateway、k8sadapter 和 prometheus Pod。清单默认 `carrot-ai`，应用前必须与实际部署 namespace/`app` 标签对齐；若另有 NetworkPolicy 放行，Kubernetes 策略的并集可能扩大入口。

`GET /metrics` 当前仍为无 HTTP 鉴权的 Prometheus exporter；必须依赖上述 CNI 网络隔离，不能直接公开该端口。此策略未验证真实网络执行，不能据 YAML 存在宣称隔离验收通过。跨集群通信还需 TLS、网络来源限制和凭据运维。

## 测试与未完成项

```sh
./hack/check.sh
bash hack/gateway-auth-e2e.sh
bash hack/model-tenant-e2e.sh
cd frontend
node --test tests/auth.test.mjs tests/keys.test.mjs tests/releases.test.mjs
npm run build
```

独立进程 E2E 使用真实 gateway/observability 二进制、内存账本和 Mock 推理，验证服务路由写入、Key 管理隔离、OpenAI 调用、鉴权指标上报与账单查询。不代替真实 PostgreSQL/Kubernetes/NetworkPolicy 验收。

模型租户 E2E 使用独立 controlplane/modelregistry/pipeline 二进制、内存存储、开发 artifact verifier、Mock 推理和 Fake adapter，覆盖越租户读取/发布/部署、人工审批及控制面服务身份读取。不验证 artifact 字节、真实模型探针或 PostgreSQL migration。

adapter 内部授权及整套 NetworkPolicy 仍需收口。Key 管理目前验证短期用户 JWT，不主动查询成员撤权；已停用租户的存量推理 Key 尚需在线撤权。历史模型/部署归属恢复、JWT 密钥轮换和全部越租户测试矩阵未完成，不标记安全里程碑完成。
