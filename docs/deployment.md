# 部署指南

## Docker Compose

```bash
cp config.example.toml config.toml
cp .env.example .env
# 编辑 config.toml 和 .env
docker compose up -d
docker compose logs -f magnet-to-strm
```

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
