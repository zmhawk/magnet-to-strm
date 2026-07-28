# magnet-to-strm

通过 115 离线下载解析磁力链接，生成真实的 `.strm` 文件。播放器访问 STRM 时，服务会确认 115 中的文件并提供稳定的只读 WebDAV 视频流；文件缺失时，会按来源磁链自动恢复。

> 项目仍处于早期阶段。升级前请备份数据库和 STRM 目录，并先在非关键环境中验证。

## 核心能力

- 通过 115 离线任务解析磁链，并把文件、SHA1 和远端位置保存到 SQLite。
- 只为视频文件生成 STRM，保留磁链内的目录结构。
- 提供稳定的 `/redirect/<sha1>` 和基于 SHA1 的只读 WebDAV 路径。
- 提供持久化任务队列和 aria2 兼容的 `/jsonrpc` 接口。
- 可选使用 rclone VFS 缓存；无 115 凭据时也可查看本地记录和 WebUI。

## 快速开始

### 本地运行

需要 Go 1.26 或更高版本。执行 115 相关操作还需要 115 账号、Refresh Token 和工作目录 ID。

```bash
cp config.example.toml config.toml
cp .env.example .env
# 编辑 config.toml 和 .env

go build -o magnet-to-strm ./cmd/magnet-to-strm
./magnet-to-strm add 'magnet:?xt=urn:btih:...'
./magnet-to-strm serve
```

默认 STRM 输出目录为 `strms`，服务监听 `:8080`。完整配置说明见 [配置参考](docs/configuration.md)，命令说明见 [使用与恢复](docs/operations.md)。

### Docker Compose

```bash
cp config.example.toml config.toml
cp .env.example .env
# 编辑 config.toml 和 .env
docker compose up -d
docker compose logs -f magnet-to-strm
```

Compose 会把应用数据保存到 `magnet-data` 命名卷。部署细节、目录挂载和升级建议见 [部署指南](docs/deployment.md)。

## 工作方式

```text
磁力链接 → 115 离线任务 → SQLite + STRM
播放器   → /redirect → 下游文件服务
播放器   → /redirect → rclone VFS（可选）→ /dav → 115
```

STRM 内容为：

```text
${http.public_base_url}/redirect/<sha1>?info_hash=<info_hash>
```

`info_hash` 只用于文件缺失时选择首选恢复来源；不带该参数的旧格式仍然兼容。

## 文档

- [配置参考](docs/configuration.md)：`config.toml` 和环境变量。
- [部署指南](docs/deployment.md)：Docker Compose、目录挂载和升级。
- [rclone 示例](examples/rclone/README.md)：稳定 WebDAV 与 VFS 缓存。
- [HTTP 与 API](docs/api.md) · [使用与恢复](docs/operations.md)
- [设计](docs/design.md) · [架构与限制](docs/architecture.md)
- [贡献指南](CONTRIBUTING.md) · [安全政策](SECURITY.md) · [行为准则](CODE_OF_CONDUCT.md)

## 许可证

项目采用 [Apache License 2.0](LICENSE) 发布。

请仅处理你有权使用的内容。本项目不存储、分发内容，也不判断内容的权利归属。
