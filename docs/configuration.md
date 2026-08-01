# 配置参考

从示例创建本地配置：

```bash
cp config.example.toml config.toml
cp .env.example .env
```

完整字段和建议起始值见 [`config.example.toml`](../config.example.toml)。相对路径以程序工作目录为基准；未知 TOML 字段会导致启动失败。

## 数据库与 HTTP

- `database.path` 是 SQLite 数据库路径。
- `http.listen_addr` 是监听地址；`http.public_base_url` 是播放器能够访问、并写入 STRM 的公开地址。
- `http.redirect_base_url` 是 `proxy` 重定向目标的基础地址；省略时默认等于 `http.public_base_url`，由本服务的 `/dav` 代理，也可配置为 rclone 等外部服务。
- `http.redirect_type` 可设为 `direct` 或 `proxy`。前者返回与客户端 User-Agent 匹配的 115 临时下载链接，后者返回稳定的 SHA1 对象路径。旧名称 `stable_dav` 仍兼容，加载时会规范化为 `proxy`。
- `/dav` 和根级 `/objects` 路径不需要配置上游 WebDAV；它们使用已保存的 `pick_code` 获取 115 临时下载地址并代理文件流。rclone 的上游应配置为 `${http.public_base_url}/dav`。
- `http.materialize_cache_ttl` 是 `/redirect` 和 `/dav` 共享的 SHA1 物化缓存滑动过期时间。

## 115 请求与任务

- `p115.work_dir_id` 是离线任务工作目录 ID，`add` 和完整运行模式必填。
- `p115.request_rate`、`request_burst`、`request_concurrency` 共同限制 115 API 请求。
- `p115.offline_quota_min_remaining` 设置 aria2/qB 新增任务的 115 离线额度保护值；剩余额度低于该值时拒绝新增，`0` 表示关闭。播放触发的物化任务不受影响。
- `p115.offline_poll_interval` 控制非等待场景下的任务列表缓存和重试间隔。
- `p115.offline_poll_min_interval` 与 `offline_poll_max_interval` 控制等待任务完成时动态退避的上下限。
- `ingest.job_timeout` 是单个整理任务的总超时时间。超时后会标记失败，并删除对应的 115 离线任务及其源文件。

## 日志

- `logging.level` 可设为 `debug`、`info`、`warn` 或 `error`，默认是 `info`。
- 115 API 请求和客户端取消等待属于 `debug`；正常任务生命周期属于 `info`；警告和可恢复失败属于 `warn`。

## STRM 与缓存

- `library.strm_dir` 是真实 STRM 输出目录，可以使用绝对路径。
- `library.cache_retention` 控制托管缓存的最长保留时间。
- `library.cache_max_size` 是 115 临时目录的空间上限，支持 `TB`、`GB`、`MB`、`KB` 及 `T`、`G`、`M`、`K`；`0` 表示不限制。超限时按 `last_accessed_at` 从旧到新清理。
- `library.sweep_interval` 控制后台清理周期。

## 环境变量

`.env` 只支持以下秘密：

```dotenv
MTS_P115_REFRESH_TOKEN=...
MTS_ARIA2_RPC_SECRET=...
MTS_QBITTORRENT_USERNAME=...
MTS_QBITTORRENT_PASSWORD=...
```

`MTS_ARIA2_RPC_SECRET` 用于 aria2 JSON-RPC 的 `token:<secret>` 认证。Refresh Token 更新后，服务会换取新凭据并保存到 SQLite。

`MTS_QBITTORRENT_USERNAME` 和 `MTS_QBITTORRENT_PASSWORD` 用于 qBittorrent Web API
（`/api/v2/`）认证，二者均为空时不启用认证；未配置它们不会阻止服务启动。建议在服务
暴露给非本机客户端时同时设置，并在 Radarr/Sonarr 中填写相同凭据。

未配置凭证时，`serve` 仍可启动本地数据库模式；115 离线下载、文件物化、WebDAV 文件流和 aria2 添加任务会被停用。`add` 命令仍要求完整的 115 配置。

从旧版本升级时，应删除 `http.dav_upstream_base_url`、`http.dav_upstream_username` 和 `http.dav_upstream_password`；未知配置字段会阻止服务启动。
