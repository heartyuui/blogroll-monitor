# 项目上下文

本文记录 Blogroll Monitor 的长期边界与跨系统契约。安装步骤见 [README](../README.md)，Linux 依赖准备见 [Linux 环境准备](linux-prerequisites.md)。具体博客的域名、密钥和部署目录不属于本仓库文档。

## 项目边界

- 本项目是独立运行的 Go 友链可用性探针，不提供前端，也不管理友链资料或审核状态。博客后端是这些数据的唯一事实来源；浏览器不直接访问探针。
- 探针从博客后端同步启用监测的友链，在本地 SQLite 保存 catalog 镜像、调度与检测历史、聚合数据和待投递事件，再向博客后端回传状态。
- 对接其他博客需要其后端实现同一协议。更换 `BLOG_BASE_URL` 不能让任意博客程序自动兼容。

## 数据流与持久化

1. 按 `MONITOR_SYNC_INTERVAL` 分页拉取 catalog。各页必须属于同一个 `syncId`；只有收到完整分页并完成对账后，才清理本轮不再存在的链接。
2. 根据持久化的 `next_check_at` 调度检测，默认全局最多 32 个并发任务、同一可注册主域最多 1 个。每次 DNS 解析和重定向都执行非公网地址检查，并将连接固定到已验证的 IP。
3. 状态机默认连续 2 次成功进入 `UP`、连续 3 次硬失败进入 `DOWN`；`DEGRADED` 与 `UNKNOWN` 会重置连续计数。状态和检测结果写入 SQLite，并在同一事务中写入待发送的 outbox 事件。
4. Outbox 批量回传到博客后端。投递失败会保留事件并重试；清理历史检测数据不会删除未投递事件。

SQLite 使用 WAL，数据库位于 Compose 命名卷的 `/data`。默认保留原始检查 7 天、小时聚合 180 天、每日聚合与状态转换 730 天；已投递 outbox 在 24 小时后清理。数据库迁移在启动时自动执行。备份时应停止容器，或使用 SQLite 在线备份方式，不要只复制运行中的主库文件而遗漏 WAL。

## 博客后端接口契约

`BLOG_BASE_URL` 在生产环境填写可访问的 HTTPS 根地址，例如 `https://blog.example.com`。当前客户端固定请求域名根路径；填写 `https://blog.example.com/api` 不会给请求加上 `/api` 前缀。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/internal/blogroll-monitor/catalog` | 分页获取友链 catalog；客户端发送 `limit=200`，后续页附带 `cursor`。 |
| `POST` | `/internal/blogroll-monitor/status-batch` | 以 `nodeId` 和 `events` 批量回传状态；每个结果通过 `eventId` 对应事件。 |

当前协议版本为 `1`。Catalog 响应包含 `schemaVersion`、`syncId`、`items`、`nextCursor`、`complete`；每个 item 至少包含 `id`、`url`、`monitorRevision`、`monitorEnabled`。状态批次响应包含 `schemaVersion` 与 `results`，每个结果包含 `eventId` 和 `accepted`。字段的代码定义分别见 [`internal/model/model.go`](../internal/model/model.go) 与 [`internal/syncclient/client.go`](../internal/syncclient/client.go)。对接后端应按 `eventId` 幂等处理重试，并使用 `monitorRevision` 避免旧结果覆盖新配置。

两个接口都需要 HMAC-SHA-256 鉴权。`MONITOR_HMAC_KEY_ID` 是密钥标识，`MONITOR_HMAC_SECRET` 是至少 32 随机字节的无填充 base64url 密钥，两端必须配置同一对值。签名版本为 `monitor-hmac-v1`；签名内容依次包含版本、key ID、HTTP 方法、实际请求路径及规范化查询参数、Unix 时间戳、nonce、请求体 SHA-256，每项以换行分隔。请求携带 `X-Monitor-Protocol-Version: 1` 及 `X-Monitor-Key-Id`、`X-Monitor-Timestamp`、`X-Monitor-Nonce`、`X-Monitor-Content-SHA256`、`X-Monitor-Signature`。精确定义见 [`internal/signing/signing.go`](../internal/signing/signing.go)。

反向代理必须将上述路径原样送达后端，不能改写路径或查询参数。签名覆盖请求目标；路径不一致会导致验签失败。只有部署博客后端新接口并完成反代后，公开域名上的路径才能用于监控器。

## 部署与运维约束

- `scripts/install.sh` 生成权限为 `0600` 的 `.env` 与 `.backend-env`，并等待后端配置完成；它不会安装 Docker 或修改博客后端。两个文件及备份均不得提交或公开。
- Compose 只把健康端口映射到主机 `127.0.0.1`。容器内监听地址必须为 `0.0.0.0:8080`，安装脚本和 `.env.example` 已设置；无需向公网开放探针端口。
- `/health/live` 表示进程存活；`/health/ready` 表示 SQLite 可用且本进程至少完成过一次 catalog 全量对账。后续同步失败不会自动清除该就绪标志，因此排障仍需检查日志和后端接口。
- 生产环境应使用 HTTPS，禁止把测试用的非安全本地连接或 loopback 检测选项带入生产。探针不使用系统 HTTP 代理，也不向目标透传 Cookie 或 Authorization；网络出口仍建议在主机或云防火墙做限制。

## 修改与发布约定

修改接口路径、签名规范、字段或协议版本时，要同步检查探针客户端、对接后端、反代规则、安装提示、README 和测试。跨系统发布应先保证后端接口与 HMAC 配置可用，再配置不改写路径的反代，最后启动或更新探针；推送 GitHub 只更新源码，不会自动部署服务器。提交前运行 `go test ./...`、`go vet ./...` 并检查变更和敏感文件。
