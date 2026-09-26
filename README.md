# Blogroll Monitor

[![Docker](https://img.shields.io/badge/Docker-29.x%20recommended-2496ED?logo=docker&logoColor=white)](https://docs.docker.com/engine/install/)
[![Go](https://img.shields.io/badge/Go-1.26.4-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![SQLite](https://img.shields.io/badge/SQLite-WAL-003B57?logo=sqlite&logoColor=white)](https://www.sqlite.org/wal.html)
[![HMAC](https://img.shields.io/badge/HMAC-SHA--256-8B5CF6)](https://pkg.go.dev/crypto/hmac)

独立部署的友链可用性探针。博客后端是友链资料与审核状态的唯一事实来源；本服务只保存 catalog 镜像、调度状态、检测历史、聚合数据和待投递状态事件。浏览器不会连接本服务。

## 数据流

1. 使用 HTTPS + HMAC 从博客 `GET /internal/blogroll-monitor/catalog` 分页拉取完整 catalog。
2. 按持久化的 `next_check_at` 错峰调度，默认全局 32 并发、同一可注册主域 1 并发。
3. 每次解析和重定向都复检全部 A/AAAA；socket 固定连接到已验证 IP，同时保留 HTTP Host 与 TLS SNI。
4. 状态机默认连续 3 次硬失败进入 `DOWN`、连续 2 次成功恢复 `UP`。
5. 检查结果先与 outbox 同事务写入 SQLite，再批量回传 `POST /internal/blogroll-monitor/status-batch`。

`401/403/405/429` 映射为 `DEGRADED`；SSRF 拒绝和配置错误映射为 `UNKNOWN`，不会把链接标红。公开状态的 20 分钟过期映射由博客后端统一处理。

长期架构、接口契约与跨博客适配约定见 [项目上下文](docs/PROJECT_CONTEXT.md)。

## <img src="https://raw.githubusercontent.com/garrett/Tux/main/tux.svg" alt="Linux" width="24" height="24"> 首次安装

首次安装前需要 Git、Docker Engine 和 Docker Compose 插件；尚未安装时，请先按 [Linux 环境准备](docs/linux-prerequisites.md)完成环境准备。本项目的安装脚本只负责配置并启动监控器，不会安装这些依赖，也不会自动登录或修改博客后端。

克隆仓库并进入目录：

```bash
git clone https://github.com/heartyuui/blogroll-monitor.git
```

```bash
cd blogroll-monitor
```

运行安装脚本：

```bash
chmod +x scripts/install.sh
```

```bash
./scripts/install.sh
```

首次运行时，脚本会询问博客后端的 HTTPS 根地址、节点 ID、HMAC key ID 和本机健康检查端口。`BLOG_BASE_URL` 要填写监控器所在服务器能够访问的完整地址，不是本地文件路径、单独的 `/api`，也不需要写到具体接口：

- 如果 catalog 接口位于 `https://blog.example.com/internal/blogroll-monitor/catalog`，填写 `https://blog.example.com`。
- 如果接口位于 `https://api.example.com/internal/blogroll-monitor/catalog`，填写 `https://api.example.com`。

HMAC key ID 和 `MONITOR_HMAC_SECRET` 是不同的值：key ID 是密钥的标识，不是密钥本身，例如 `monitor-2026-09`，可以接受脚本给出的默认值；secret 才是用于签名的保密密钥。首次安装时如果没有预先设置 secret，脚本会生成至少 32 个随机字节的密钥，把 key ID 和 secret 写入监控器的 `.env`，并在 `.backend-env` 中生成博客后端需要的 `FRIEND_LINK_MONITOR_HMAC_KEYS`（格式为 `key ID:secret`）。两端必须使用同一对值。脚本还会：

- 以 `0600` 权限写入监控服务的 `.env`。
- 以 `0600` 权限写入博客后端需要的 `.backend-env`，但不会在终端显示密钥。
- 校验 Compose 配置，构建并启动容器，等待容器完成首次 catalog 同步并进入 `healthy`。

首次交互式生成配置时，脚本会暂停，等待你把 `.backend-env` 中的变量安全地加入博客后端并重启后端。不要提交、公开或粘贴这两个文件的内容。若希望分两步操作，先生成配置：

```bash
./scripts/install.sh --prepare-only
```

配置并重启博客后端后，再运行：

```bash
./scripts/install.sh
```

已有 `.env` 时会直接复用，不会覆盖。只有显式执行 `./scripts/install.sh --reconfigure` 才会先创建带 UTC 时间戳的权限受限备份，再重建配置。自动化环境可以使用：

```bash
BLOG_BASE_URL=https://example.com ./scripts/install.sh --prepare-only --non-interactive
```

生产安装要求 `BLOG_BASE_URL` 使用 HTTPS。当前版本固定访问该地址根路径下的两个受 HMAC 保护的接口：

- `GET /internal/blogroll-monitor/catalog`
- `POST /internal/blogroll-monitor/status-batch`

反向代理必须原样转发这两个路径；HMAC 签名包含请求路径，代理改写路径会导致验签失败。

如果博客后端只在 `https://blog.example.com/api/internal/...` 提供接口，当前版本不会保留填写在 `BLOG_BASE_URL` 中的 `/api`。需要先通过反向代理在根路径暴露上述接口，或修正监控器的路径拼接；仅填写 `https://blog.example.com/api` 无法解决。

因此监控器可以部署到不同服务器，也能接入其他博客，但其他博客后端需要实现相同的 HMAC 签名、分页 catalog 和批量状态回传协议；仅更换域名并不能自动适配任意博客程序。

轮换密钥时，先让博客后端同时接受新旧 key，再切换监控器使用的 key ID 和 secret；确认同步正常后移除旧 key。

## 手动部署（不使用安装脚本）

```bash
cp .env.example .env
```

编辑 `.env`，至少填写 `BLOG_BASE_URL`、`MONITOR_HMAC_KEY_ID` 和 `MONITOR_HMAC_SECRET`，再依次执行：

```bash
docker compose build
```

```bash
docker compose up -d
```

```bash
docker compose ps
```

```bash
curl --fail http://127.0.0.1:8080/health/ready
```

Compose 默认只把健康接口绑定到主机回环地址 `127.0.0.1:8080`；可通过 `.env` 中的 `MONITOR_HOST_PORT` 修改主机端口。监控器主动访问博客后端，浏览器和公网无需访问该端口。

SQLite 位于命名卷 `/data`，启用 WAL、foreign keys、busy timeout 和单写连接。容器以非 root、只读根文件系统、无 Linux capabilities 运行。建议在主机或云防火墙中额外限制出站：拒绝 RFC1918、loopback、link-local、metadata 和其他非公网网段，只开放必要的 80/443 与 DNS；应用层 SSRF 校验仍必须保留。

升级前备份 volume 中的 `friend-link-monitor.db` 及 WAL 文件；应在停容器或使用 SQLite 在线备份工具时复制，不能只复制正在写入的主库文件。迁移在启动时自动、按版本事务执行。

## 健康检查

- `GET /health/live`：进程存活。
- `GET /health/ready`：SQLite 可用且至少完成过一次博客 catalog 全量对账。

健康接口不返回目标、数据库路径、密钥或内部错误。

## 保留策略

- 原始检查：7 天。
- 小时聚合：180 天。
- 每日聚合与状态转换：730 天。
- 已成功投递的 outbox：24 小时后清理；未投递事件不会因原始结果清理而丢失。

全部周期都可通过环境变量覆盖。1000 条链接、5 分钟周期平均约 3.3 次检查/秒；上线后应根据实际 SQLite 体积和 I/O 调整保留期。

## 安全边界与限制

- 只接受 `http`/`https`，生产仅允许默认 80/443；拒绝 userinfo、非法 IDN、过长 URL 和非公网地址。
- 不读取系统 HTTP 代理，不发送 Cookie/Authorization，不记录签名、密钥或响应正文。
- 单节点结果只代表探针所在地的可访问性。
- 第一版不提供公开状态或友链 CRUD API，也没有远程手动探测入口。
- ICP 与 IP 归属地仅保留 `internal/provider` 接口和未配置实现。接入真实 ICP 服务时必须使用合法稳定的第三方 API；GeoIP 可接 MaxMind GeoLite2 或 IP2Location，并把香港、澳门、台湾归入 `overseas`。DNS/CDN 结果必须称为访问节点或解析 IP，不能称为源站位置。

图标致谢：Tux 原作 Larry Ewing（使用 GIMP 创作），[矢量版](https://github.com/garrett/Tux)由 Garrett LeSage 重绘、IFo Hancroft 整理。
