# 管理接口与内部调用鉴权

## 当前已实施边界

controlplane、modelregistry、pipeline、k8sadapter、gateway、observability 注入同一 `CARROT_AUTH_SECRET`；注册集群的远端 adapter 也必须配置匹配的密钥。使用环境注入，不把密钥放进命令行或 Git。空密钥仅保留不安全的本地 Fake 开发兼容模式；不能用于生产。

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
| k8sadapter 全部 `/v1` 管理接口 | `aud=service:k8sadapter`，subject 仅 controlplane；用户及集群心跳 JWT 不可调用 |
| controlplane `/internal/tenants/{tenantId}/serving-status` | `aud=service:controlplane`，subject 仅 gateway |
| controlplane `/internal/auth/introspect` | 同 audience，subject 仅 gateway/modelregistry/pipeline；用户 token 放请求体 |

内部 HTTP 客户端每次生成一分钟服务 JWT，使用专用 audience、固定只读 role、空 tenant；服务身份不可用于用户管理 API。服务客户端拒绝重定向，不向跳转地址传递凭据。所有 JWT 必须带有效期限和四级角色之一。

控制面默认 adapter 和已注册集群 adapter 均发送上述身份，包括租户初始化、部署读写、重启和受管删除。租户授权由控制面完成，adapter 不直接接受用户 JWT。集群心跳使用另一专属身份，不能混用。启用鉴权时 `dev-up.sh` 以匿名 401 检查管理 HTTP 可达性；这不等同于依赖就绪。

租户管理员只能列出/修改本租户 Key，越租户 Key ID 返回 404；平台管理员可管理所有租户。轮换只能继承旧 Key 的租户，不能重新归属。响应不含哈希；新明文只在创建/轮换结果返回，响应禁止缓存。控制面部署指标查询先检查所属租户，再向 observability 转发。用户账单仍经控制面租户授权接口访问，不直接开放观测服务聚合查询。

OpenAI `/v1/models`、`/v1/chat/completions` 继续使用 API Key，不改为用户 JWT。

启用认证的 gateway 在每次 OpenAI 请求通过 Key 校验后，使用服务 JWT 在线查询控制面的租户状态。租户不存在或已停用返回 403；控制面不可达、响应无效或配置缺失返回 503，不转发推理。查询超时为三秒，拒绝重定向，不缓存启用状态。部署 gateway 时设置 `--controlplane-url`（默认 `http://127.0.0.1:8080`），容器环境应使用控制面 Service 地址。PostgreSQL 模式读取当前 `tenants` 记录，不依赖控制面启动时快照；历史仅有成员/Key、没有租户记录的租户不会放行，需先核实并补齐租户记录。

停用不会删除 Key 或强制中断已开始的流式请求；停用后的新请求被拒绝。该机制是租户级推理撤权，空密钥开发模式不执行在线查询。

控制面管理 API、JWT introspection 和 Notebook 复核当前用户、租户及成员角色；未知用户、停用用户/租户、移除成员或角色变化均拒绝旧 access token。登录和 refresh 同样复核，refresh 使用当前角色而不是会话旧角色。PostgreSQL 使用三秒超时的实时关联查询，查询异常不放行；内存模式以当前身份表为准。为成员加入新租户时登记租户，数据库冲突不覆盖现有停用状态。生产用户 JWT 必须对应实际账号和成员记录，手工签名不能替代注册。

gateway Key 管理、modelregistry 和 pipeline 的所有用户管理请求也调用内部 introspection 复核当前成员；角色变更后的旧 JWT 拒绝 401。三个服务设置 `--controlplane-url`（默认 `http://127.0.0.1:8080`，容器中替换为 Service 地址）。共用客户端先验签，再使用专属服务 JWT 提交用户 token，并核对返回的用户、租户、角色和有效期；不缓存放行结果，三秒超时且拒绝重定向。控制面不可达、返回异常或客户端未配置均拒绝用户请求。pipeline 用户会话仅支持 `controlplane` audience。内部服务 JWT 不走用户成员复核，仍按各服务固定 audience/caller 授权；OpenAI API Key 继续走租户启用校验，不改为用户 JWT。

## 模型归属与发布

migration 023 持久化模型 `tenant_id`，版本继承模型归属。普通用户创建模型时以 JWT 租户为准；平台管理员可明确指定租户。模型名称仍全局唯一。部署新建、升级、重建和历史版本恢复检查版本归属；同名部署也不能跨租户返回。

历史模型不自动分配给 `default`：空归属记录仅平台管理员可读，禁止修改、发布或部署。运维须先依据历史部署和业务记录确认所有者，再进行受控数据库归属迁移；当前没有自动归属恢复 API。只读者的版本响应隐藏 artifact URI，S3 URI 拒绝内嵌凭据、查询参数和片段。

控制台通过 `/pipeline` 启动流水线及人工审批，不再直接调用 release；生产反向代理须将此前缀转发到 pipeline 并重写为 `/api`，开发 Vite 已配置。流水线对已 VALIDATED 版本仍重新校验 artifact；发布写入只接受 pipeline 服务 JWT。此权限门禁不代表临时模型探针、基准或真实 S3 校验已验收。

## 网络与运维

`deployments/k8s/14-observability-access.yaml` 限制 observability 8084 入口为同 namespace 的 controlplane、gateway、k8sadapter 和 prometheus Pod。清单默认 `carrot-ai`，应用前必须与实际部署 namespace/`app` 标签对齐；若另有 NetworkPolicy 放行，Kubernetes 策略的并集可能扩大入口。

`15-management-access.yaml` 增加同集群管理端口入口基线：四个管理服务默认拒绝入站；controlplane 8080 允许 console、gateway、modelregistry、pipeline、adapter、observability；modelregistry 8081 允许 console、controlplane、pipeline；adapter 8082 仅 controlplane；pipeline 8086 仅 console。所有来源仅同命名空间 Pod，`app: console` 指受信任的控制台反向代理，不是浏览器。应用前确认这些实际标签；跨 namespace 的入口控制器、远端 agent、远端控制面和 Prometheus 监控需要针对真实来源单独添加最小策略。当前不自动放行外部地址。

这些清单不限制平台 egress，也不能按 HTTP 路径隔离同端口的用户/内部接口。gateway 的公开 OpenAI 入口与管理接口共用 8083，仍依赖 HTTP 鉴权和可信入口代理配置；远端连接必须配合 TLS。清单仅完成本地结构和允许/拒绝矩阵测试，未应用到用户集群；不能据此标记整体网络隔离完成。

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

独立进程 E2E 使用真实 controlplane/gateway/observability/adapter 二进制、内存账本、Fake Kubernetes 和 Mock 推理，验证服务路由写入、Key 管理隔离、OpenAI 调用、鉴权指标上报、账单查询及租户停用后的存量 Key 拒绝。不代替真实 PostgreSQL/Kubernetes/NetworkPolicy 验收。

模型租户 E2E 使用独立 controlplane/modelregistry/pipeline 二进制、内存存储、开发 artifact verifier、Mock 推理和 Fake adapter，覆盖越租户读取/发布/部署、人工审批及控制面服务身份读取。不验证 artifact 字节、真实模型探针或 PostgreSQL migration。

整套 NetworkPolicy 仍需收口，远端 adapter 必须使用 TLS 和受限网络入口；共享签名密钥不是集群级密钥隔离。已开始的管理操作不会因后续撤权自动中断。历史模型/部署归属恢复、JWT 密钥轮换和全部越租户测试矩阵未完成，真实 PostgreSQL 多实例撤权尚未验收，不标记安全里程碑完成。
