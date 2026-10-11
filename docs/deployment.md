# 部署指南

## Docker Compose

```bash
mkdir -p config data cache
docker compose up -d
docker compose logs -f magnet-to-strm
```

配置目录 `./config` 可写挂载到 `/etc/magnet-to-strm`，Web UI 保存会更新 `./config/config.toml`。从旧版本升级时，将原来的 `./config.toml` 移到 `./config/config.toml` 后再启动，继续使用原配置。目录挂载支持原子保存，不能换成只读或单文件挂载。

首次打开 `http://<服务器地址>:8080` 完成表单配置，保存后自动启动完整服务。后续配置通过 Web UI 修改；115 TOKEN 修改立即生效，其他配置修改后重启容器。

Compose 将宿主机 `./data` 挂载到 `/data`，将 `./cache` 挂载到 `/cache`。初始化表单默认数据库路径为 `/data/db/magnet.db`，STRM 目录为 `/data/strms`，对应宿主机 `./data/db/magnet.db` 和 `./data/strms`。如果媒体服务器需要直接读取宿主机目录，可在 `compose.yaml` 中挂载 STRM 目录：

```yaml
volumes:
  - /你的媒体目录/strms:/data/strms
```

容器默认使用基础镜像的运行身份（root）。挂载宿主目录时，可在 `.env` 中同时设置 `PUID` 和 `PGID`，使容器改用指定的宿主机用户和组：

```dotenv
PUID=1000
PGID=1000
```

两项均可留空；如果设置，则必须同时设置。容器不会修改挂载目录的文件所有权，宿主机的 `config`、`data` 和 `cache` 目录需要提前允许指定的 UID/GID 读写。

监听地址由 Compose 的 `MTS_HTTP_LISTEN_ADDR` 设置，默认 `:8080`。修改容器内监听端口时，应同步修改端口映射和健康检查；只需改变宿主机访问端口时，修改 `ports` 左侧即可，例如 `8088:8080`。aria2 密钥通过 Web UI 配置，默认 Compose 不注入密钥环境变量。

## rclone

镜像默认集成并启用 rclone rcd，本地播放缓存保存在宿主机 `./cache`（容器内 `/cache`），无需额外 rclone 容器或端口。可在 Web UI 关闭内置 rclone，保存并重启生效。

需要单独部署外部 rclone 时，使用 [`examples/rclone`](../examples/rclone/README.md) 中的独立 Compose 示例。调用链为：

```text
播放器 → /redirect → rclone VFS → /dav → 115
```

## 网络与安全

不建议把 `/jsonrpc`、`/redirect`、`/dav` 或管理端口直接暴露到公网。远程访问时应使用 HTTPS、反向代理、身份认证、访问控制和限流。详细安全要求见 [安全政策](../SECURITY.md)。

升级前备份 `database.path` 指向的数据库文件和 STRM 目录。由新版本创建的数据库不能用旧版本程序打开。
