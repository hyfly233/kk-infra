# Notebook 工作空间

## API 与身份边界

`GET/POST/DELETE /api/v1/notebooks/workspace` 查询、启动、停止并移除当前用户的唯一 `workspace` named server。租户和所有者仅取自 controlplane JWT；不接受客户端指定所有者、镜像或 namespace。只读者不能启动/删除。每次请求及 Hub 启动均复核成员角色、用户和租户启用状态；已启动的服务器不会因停用自动停止，目前需 Hub 运维清理。

状态为 `ABSENT/STOPPED/STARTING/RUNNING/STOPPING`，Hub 返回异步接受不等于启动或删除已完成。失败需重新查询；不自动重试启动。删除保留 Notebook PVC 和 Hub 用户记录，不擦除文件。页面每五秒查询，打开地址使用固定公开 Hub URL，不信任 Hub 返回的任意 URL。

Hub 校验调用 `POST /internal/notebooks/introspect`，必须携带 `aud=notebook-hub`、固定 subject 的服务 JWT。启动授权为两分钟 `aud=notebook-spawn` JWT，不可调用管理 API。Hub 用户名由 tenant/user 哈希生成，不跨租户复用。Hub 登录用户名为 `邮箱|租户ID`，密码为平台密码；刷新令牌立即撤销，仅短期访问令牌保存在加密 auth_state 中，过期后需重新登录。

## 部署与凭据

1. controlplane 注入 `CARROT_AUTH_SECRET` 启用认证；其他服务的管理接口鉴权尚未收口，必须保持在可信网络内。初始化账号及租户成员后，平台管理员调用 `POST /api/v1/notebooks/hub-token` 获取 24 小时服务 JWT；过期前重新签发、更新 Hub Secret 并重启 Hub。不得放入 Git、URL 或日志。
2. 在 `jupyterhub` namespace 安全创建 `jupyterhub-secrets`：`platform-hub-token` 是上述 JWT；`api-token` 是独立随机 Hub 管理令牌；`crypt-key` 是 32 字节十六进制加密密钥。Hub 配置授予该服务用户/服务器生命周期权限，不授予读取 Notebook 内容权限。
3. controlplane 环境注入 `NOTEBOOK_HUB_URL=http://jupyterhub.jupyterhub.svc.cluster.local:8081`、`NOTEBOOK_PUBLIC_URL=https://<Hub公开域名>`、`NOTEBOOK_HUB_API_TOKEN=<同一api-token>`。内部 URL 必须指向 Hub API 而非代理端口。部署环境需配置 HTTPS 入口和 WebSocket 转发。
4. 启用 adapter `--volcano-enabled=true --volcano-queue-prefix=tenant-`，安装 Volcano；应用 `deployments/k8s/20-jupyterhub.yaml`。租户初始化创建 Queue，Hub 校验归属后创建单成员 PodGroup。自定义 Queue 前缀尚不支持此 Hub 配置。
5. Hub SQLite、cookie secret 使用 PVC，单副本 Recreate；Notebook 使用 10Gi PVC。配置可用默认 StorageClass，并验证所选 Notebook 镜像包含与 Hub 兼容的 single-user 服务。当前镜像组合尚未做真实 spawning 验收。

Notebook Pod 使用 `tenant-notebook` SA、不挂载 Kubernetes token；网络仅允许 DNS、Hub API 出口和 Hub namespace 入口。Hub API 入口限 controlplane 和租户 Notebook Pod。用户不能直接访问模型/互联网；额外业务出口必须由管理员审查。CNI 必须实际执行 NetworkPolicy。Hub SA 跨 namespace 权限属于可信控制组件，不下发给 Notebook。

## 验证与尚未验收

```sh
./hack/check.sh ./services/controlplane ./services/k8sadapter
python3 hack/test-notebook-config.py
bash hack/notebook-e2e.sh
cd frontend
node --test tests/auth.test.mjs
npm run build
```

本地覆盖 owner 隔离、服务 audience、角色变更/停用拒绝、异步 Hub 状态、错误脱敏、启动配置和会话竞争。HTTP 测试使用 Stub Hub，不证明镜像、Hub RBAC、PVC、调度、NetworkPolicy 或真实 PostgreSQL可用。真实集群验收按用户决定暂缓；Hub 服务 JWT 自动轮换、PodGroup 崩溃清理和已运行服务器停用收敛仍需后续完善。

协议参考：[JupyterHub named-server API](https://jupyterhub.readthedocs.io/en/5.2.1/reference/rest-api.html)、[RBAC scopes](https://jupyterhub.readthedocs.io/en/4.0.2/rbac/scopes.html)、[KubeSpawner 配置](https://jupyterhub-kubespawner.readthedocs.io/en/stable/spawner.html)。生产部署仍需以实际镜像版本核对。
