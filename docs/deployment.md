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

容器使用 UID `10001` 运行，宿主目录需要允许该 UID 写入。

## rclone

需要稳定路径和本地播放缓存时，使用 [`examples/rclone`](../examples/rclone/README.md) 中的独立 Compose 示例。调用链为：

```text
播放器 → /redirect → rclone VFS → /dav → OpenList/115 WebDAV
```

## 网络与安全

不建议把 `/jsonrpc`、`/redirect`、`/dav` 或管理端口直接暴露到公网。远程访问时应使用 HTTPS、反向代理、身份认证、访问控制和限流。详细安全要求见 [安全政策](../SECURITY.md)。

升级前备份 `database.path` 指向的数据库文件和 STRM 目录。由新版本创建的数据库不能用旧版本程序打开。
