# Linux 环境准备

以下以 root shell 和 systemd 为前提，列出 Debian、Ubuntu、Fedora 安装 Git、Docker Engine、Buildx 和 Docker Compose 插件的命令。其他发行版请参考 [Git](https://git-scm.com/install/linux) 与 [Docker](https://docs.docker.com/engine/install/) 官方文档。已有 Docker 环境应先核对冲突包及现有配置；环境准备完成后，返回 [README](../README.md) 部署 Blogroll Monitor。

## 安装 Git

Debian、Ubuntu：

```bash
apt update
```

```bash
apt install git
```

Fedora：

```bash
dnf install git
```

其他发行版可参考 [Git 官方 Linux 安装说明](https://git-scm.com/install/linux)。安装后运行 `git --version` 确认命令可用。

## 安装 Docker Engine 和 Docker Compose 插件

下面使用 Docker 官方软件源，分别安装 Docker Engine 与 `docker-buildx-plugin`、`docker-compose-plugin`。后两个是软件包名，不是要单独执行的命令。安装后使用 `docker compose`，而不是旧版的 `docker-compose`。

### Debian

先核对 [Docker 官方 Debian 指南](https://docs.docker.com/engine/install/debian/)支持的版本与冲突包。全新主机可执行：

```bash
apt update
```

```bash
apt install ca-certificates curl
```

```bash
install -m 0755 -d /etc/apt/keyrings
```

```bash
curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
```

```bash
chmod a+r /etc/apt/keyrings/docker.asc
```

```bash
tee /etc/apt/sources.list.d/docker.sources >/dev/null <<EOF
Types: deb
URIs: https://download.docker.com/linux/debian
Suites: $(. /etc/os-release && echo "$VERSION_CODENAME")
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
```

```bash
apt update
```

```bash
apt install docker-ce docker-ce-cli containerd.io docker-buildx-plugin
```

```bash
apt install docker-compose-plugin
```

### Ubuntu

先核对 [Docker 官方 Ubuntu 指南](https://docs.docker.com/engine/install/ubuntu/)支持的版本与冲突包。全新主机可执行：

```bash
apt update
```

```bash
apt install ca-certificates curl
```

```bash
install -m 0755 -d /etc/apt/keyrings
```

```bash
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
```

```bash
chmod a+r /etc/apt/keyrings/docker.asc
```

```bash
tee /etc/apt/sources.list.d/docker.sources >/dev/null <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
```

```bash
apt update
```

```bash
apt install docker-ce docker-ce-cli containerd.io docker-buildx-plugin
```

```bash
apt install docker-compose-plugin
```

### Fedora

先核对 [Docker 官方 Fedora 指南](https://docs.docker.com/engine/install/fedora/)支持的版本与冲突包。全新主机可执行：

```bash
dnf config-manager addrepo --from-repofile https://download.docker.com/linux/fedora/docker-ce.repo
```

```bash
dnf install docker-ce docker-ce-cli containerd.io docker-buildx-plugin
```

```bash
dnf install docker-compose-plugin
```

```bash
systemctl enable --now docker
```

如果安装时提示导入 GPG 密钥，请先按照 Docker 官方 Fedora 指南核对指纹。

### 已有 Docker Engine，只缺插件

仅在 Docker Engine 已通过 Docker 官方软件源安装、且 `docker version` 能显示 `Client` 和 `Server` 时，才按发行版补装：

Debian、Ubuntu：

```bash
apt update
```

```bash
apt install docker-buildx-plugin docker-compose-plugin
```

Fedora：

```bash
dnf install docker-buildx-plugin docker-compose-plugin
```

也可参考 [Docker Compose 插件官方安装说明](https://docs.docker.com/compose/install/linux/)。
