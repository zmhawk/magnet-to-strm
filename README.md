# magnet-to-strm

通过 115 离线下载解析磁力链接，生成真实的 `.strm` 文件。播放器访问 STRM 时，服务会确认 115 中的文件并提供稳定的只读 WebDAV 视频流；文件缺失时，会按来源磁链自动恢复。

> 项目仍处于早期阶段。升级前请备份数据库和 STRM 目录，并先在非关键环境中验证。

## 核心能力

- 通过 115 离线任务解析磁链，并把文件、SHA1 和远端位置保存到 SQLite。
- 只为视频文件生成 STRM，保留磁链内的目录结构。
- 提供稳定的 `/redirect/<sha1>` 和基于 SHA1 的只读 WebDAV 路径。
- 提供持久化任务队列、适用于 Radarr/Sonarr 的 qBittorrent 兼容 API，以及适用于浏览器插件和手机 App 的 aria2 兼容 API。

## 快速开始

```bash
mkdir -p config data cache
docker compose up -d
docker compose logs -f magnet-to-strm
```

打开 `http://<服务器地址>:8080`，在初始配置页面填写 115 工作目录 ID、Refresh TOKEN、数据库和 STRM 路径等设置，然后保存。无需提前创建配置文件；保存后服务自动进入任务管理。两个播放地址默认使用当前页面的 origin（协议、主机和端口），请使用播放器能访问的地址打开页面。

Compose 将应用数据保存到宿主机 `./data`，内置 rclone 播放缓存保存到 `./cache`，配置保存到 `./config/config.toml`。内置 rclone 默认启用，可通过 Web UI 关闭。部署细节见 [部署指南](docs/deployment.md)，开发运行方式见 [贡献指南](CONTRIBUTING.md)。

## 工作方式

```text
磁力链接 → 115 离线任务 → SQLite + STRM
播放器   → /redirect → 内置 rclone 缓存 → /dav → 115
```

STRM 内容为：

```text
${http.public_base_url}/redirect/<sha1>?info_hash=<info_hash>
```

`info_hash` 只用于文件缺失时选择首选恢复来源；不带该参数的旧格式仍然兼容。

## 文档

- [配置参考](docs/configuration.md)：Web UI 配置、TOKEN 管理和配置文件。
- [部署指南](docs/deployment.md)：Docker Compose、目录挂载和升级。
- [rclone 示例](examples/rclone/README.md)：可选的外置 rclone 部署。
- [HTTP 与 API](docs/api.md) · [使用与恢复](docs/operations.md)
- [设计](docs/design.md) · [架构与限制](docs/architecture.md)
- [贡献指南](CONTRIBUTING.md) · [安全政策](SECURITY.md) · [行为准则](CODE_OF_CONDUCT.md)

## 许可证

项目采用 [Apache License 2.0](LICENSE) 发布。

请仅处理你有权使用的内容。本项目不存储、分发内容，也不判断内容的权利归属。
