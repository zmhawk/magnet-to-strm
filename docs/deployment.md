# 部署指南

## Docker Compose

```bash
mkdir -p config
# 可选：复制示例；省略时服务会自动生成默认配置
cp config.example.toml config/config.toml
cp .env.example .env
# 编辑 config/config.toml 和 .env，或启动后通过 Web UI 修改配置
docker compose up -d
docker compose logs -f magnet-to-strm
```

配置目录 `./config` 可写挂载到 `/etc/magnet-to-strm`，Web UI 保存会更新
`./config/config.toml`。从旧版本升级时，将原来的 `./config.toml` 移到
`./config/config.toml` 后再启动，继续使用原配置。目录挂载支持原子保存，不能换成只读或单文件挂载。

Compose 将 `/data` 保存到 `magnet-data` 命名卷。示例配置中的相对 `strms` 对应容器内 `/data/strms`。如果媒体服务器需要直接读取宿主机目录，可把 `library.strm_dir` 改为 `/strms`，并在 `compose.yaml` 中额外挂载：

```yaml
volumes:
  - /你的媒体目录/strms:/strms
```

容器默认使用基础镜像的运行身份（root）。挂载宿主目录时，可在 `.env` 中同时设置
`PUID` 和 `PGID`，使容器改用指定的宿主机用户和组：

```dotenv
PUID=1000
PGID=1000
```

两项均可留空；如果设置，则必须同时设置。容器不会修改挂载目录的文件所有权，
宿主机目录需要提前允许指定的 UID/GID 读写。

## rclone

需要稳定路径和本地播放缓存时，使用 [`examples/rclone`](../examples/rclone/README.md) 中的独立 Compose 示例。调用链为：

```text
播放器 → /redirect → rclone VFS → /dav → 115
```

## 网络与安全

不建议把 `/jsonrpc`、`/redirect`、`/dav` 或管理端口直接暴露到公网。远程访问时应使用 HTTPS、反向代理、身份认证、访问控制和限流。详细安全要求见 [安全政策](../SECURITY.md)。

升级前备份 `database.path` 指向的数据库文件和 STRM 目录。由新版本创建的数据库不能用旧版本程序打开。
