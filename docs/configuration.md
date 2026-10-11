# 配置参考

推荐启动后通过 Web UI 表单完成配置，无需提前编写配置文件。普通设置保存到 TOML，115 TOKEN 仅保存到 SQLite。完整文件字段见 [`config.example.toml`](../config.example.toml)；相对路径以程序工作目录为基准，未知 TOML 字段会导致启动失败。

## 数据库与 HTTP

首次初始化时，`http.public_base_url` 和 `http.redirect_base_url` 的表单默认值为当前页面的 `window.location.origin`，可以修改；已有配置的值不会被覆盖。

- `database.path` 是 SQLite 数据库路径。
- 监听地址通过 `MTS_HTTP_LISTEN_ADDR` 环境变量或启动参数 `-port` / `-listen` 设置，不在 Web UI 中修改。优先级为启动参数、非空环境变量、TOML 中的 `http.listen_addr`、默认 `:8080`。
- `http.public_base_url` 是播放器能够访问、并写入 STRM 的公开地址。
- `http.redirect_base_url` 是 `proxy` 重定向目标的基础地址；省略时默认等于 `http.public_base_url`，默认通过内置 rclone 缓存代理，也可配置为外部服务。
- `http.redirect_type` 可设为 `direct` 或 `proxy`。前者返回与客户端 User-Agent 匹配的 115 临时下载链接，后者返回稳定的 SHA1 对象路径。
- `/dav` 和根级 `/objects` 路径不需要配置上游 WebDAV；它们使用已保存的 `pick_code` 获取 115 临时下载地址并代理文件流。rclone 的上游应配置为 `${http.public_base_url}/dav`。
- `http.materialize_cache_ttl` 是 `/redirect` 和 `/dav` 共享的 SHA1 物化缓存滑动过期时间。

## 115 请求与任务

- `p115.work_dir_id` 是离线任务工作目录 ID，`add` 和完整运行模式必填。
- `p115.request_rate`、`request_burst`、`request_concurrency` 共同限制 115 API 请求。
- `p115.offline_quota_min_remaining` 设置 aria2/qB 新增任务的 115 离线额度保护值；剩余额度低于该值时拒绝新增，`0` 表示关闭。播放触发的物化任务不受影响。
- `p115.offline_poll_interval` 控制非等待场景下的任务列表缓存和重试间隔。
- `p115.offline_poll_min_interval` 与 `offline_poll_max_interval` 控制等待任务完成时动态退避的上下限。
- `ingest.job_timeout` 是单个整理任务的总超时时间。超时后会标记失败，并删除对应的 115 离线任务及其源文件。

## 内置 rclone

镜像内置固定版本的 rclone，`rclone.enabled` 默认为 `true`，`http.redirect_type` 默认为 `proxy`。播放链路为：

```text
播放器 → /redirect → /objects → 内置 rclone → /dav → 115
```

rclone 由主程序管理，以 `rcd` 启动，再通过仅监听回环地址、带随机凭据的 RC API 创建 HTTP/VFS 服务。无需额外开放端口或维护 rclone 配置文件。控制接口不会通过 Web UI 或播放入口暴露。

磁盘缓存默认保存在 `/cache`，Compose 默认挂载宿主机 `./cache`，使用 `full` 模式，默认上限 512 GiB、保留 72h、每 5m 清理一次，预读 4 GiB；内存缓冲 64 MiB、总上限 6 GiB，服务读写超时均为 24h。此缓存和 115 网盘缓存分别管理。

这些参数均可在 Web UI 的“HTTP 服务”组修改，缓存目录也可修改；容器中的自定义目录需挂载并可写。

点击“＋ 添加 rclone 参数”可增加一个完整行输入框，直接粘贴 `--vfs-read-chunk-size=128M` 或 `--low-level-retries 3`。支持带引号的参数值和不带值的布尔开关，空行忽略。程序自动将 VFS/服务参数应用于播放服务，其余参数用于 rcd 启动；不执行 shell 命令。内置监听地址、RC 认证由程序管理；常用参数使用专用字段，重复参数会在保存时提示。

可在此组关闭内置 rclone，保存并重启后生效；关闭后，默认 `proxy` 直接使用本服务的 WebDAV 代理，也可切换 `direct` 直连。

单独运行 Go 程序时会使用 PATH 中的 rclone；未安装时会记录提示并直接使用 WebDAV 代理，仍能进入初始配置页面。已安装的 rclone 启动失败或意外退出时，主程序会报错退出，避免保持不可用的播放代理。

## STRM 与缓存

- `library.strm_dir` 是真实 STRM 输出目录，可以使用绝对路径。
- `library.cache_retention` 控制托管缓存的最长保留时间。
- `library.cache_max_size` 是 115 临时目录的空间上限，支持 `TB`、`GB`、`MB`、`KB` 及 `T`、`G`、`M`、`K`；`0` 表示不限制。超限时按任务级 `managed_artifacts.last_accessed_at` 从旧到新删除整个结果文件夹；为空时使用 `created_at`。
- `library.sweep_interval` 控制后台清理周期。

## 环境变量

支持以下环境变量；本地 `.env` 也可使用这些变量：

```dotenv
MTS_HTTP_LISTEN_ADDR=:8080
MTS_ARIA2_RPC_SECRET=...
MTS_QBITTORRENT_USERNAME=...
MTS_QBITTORRENT_PASSWORD=...
```

`MTS_ARIA2_RPC_SECRET` 用于 aria2 JSON-RPC 的 `token:<secret>` 认证。115 Refresh TOKEN 和 Auth TOKEN 仅保存在 SQLite，通过 Web UI 配置，不读取环境变量，也不写入 TOML。Web UI 显示数据库当前 TOKEN。保存时仅在 Refresh TOKEN 改变或 Access TOKEN 无效时刷新，立即生效，无需重启服务；网络验证失败可原样再次保存重试。

`MTS_QBITTORRENT_USERNAME` 和 `MTS_QBITTORRENT_PASSWORD` 用于 qBittorrent Web API（`/api/v2/`）认证，二者均为空时不启用认证；未配置它们不会阻止服务启动。建议在服务暴露给非本机客户端时同时设置，并在 Radarr/Sonarr 中填写相同凭据。

初始化完成后，若数据库中的 115 凭证不可用，服务仍可读取本地任务记录，115 相关操作暂停；在 Web UI 更新有效 TOKEN 后恢复。`add` 命令仍要求完整的 115 配置。

从旧版本升级时，应删除 `http.dav_upstream_base_url`、`http.dav_upstream_username` 和 `http.dav_upstream_password`；未知配置字段会阻止服务启动。

## 在 Web UI 中修改配置

启动 `magnet-to-strm serve` 时，如果配置文件不存在，会在当前工作目录生成 `config.toml`，使用可直接启动配置页面的默认配置；如果指定 `-config /path/to/config.toml`，则在该路径创建或读取文件。已有文件不会被覆盖。

首次打开 Web UI 时自动进入初始配置页面，通过表单逐项设置并保存，无需编辑 TOML。完成后可进入任务管理；后续点击顶部“配置”修改各项设置。保存前检查配置值，校验失败时保留原文件；成功时将表单设置写入当前 TOML 配置文件。

首次初始化仅启动配置页面、配置接口和目录浏览，不创建数据库或 STRM 目录，不启动 115、任务接口或 rclone。首次保存成功后服务自动关闭配置模式、读取新配置并启动完整服务；页面等待启动完成后自动刷新。

115 Refresh TOKEN 和 Auth TOKEN 在“115 网盘”组设置，数据库中的当前值可查看、修改，立即验证或刷新，无需重启。

aria2 密钥和 qBittorrent 用户名、密码在接口认证组设置，其他配置修改需重启服务生效。接口认证凭据保存在当前配置文件的 `[credentials]` 中。接口凭据隐藏显示，留空保留原值，点击“清除”并保存会删除对应凭据。保存凭据时文件权限设为 `0600`。

aria2/qBittorrent 的非空环境变量或 `.env` 凭据优先于文件配置；115 TOKEN 仅通过 Web UI/数据库管理；已有 TOML TOKEN 会在正常启动时迁移到数据库并从文件中移除。

首次启动无需准备配置文件，可以直接指定端口：

```bash
./magnet-to-strm serve -port 8081
# 或指定监听地址和对外 URL：
./magnet-to-strm serve -listen 127.0.0.1:8081 -public-base-url https://media.example.com
```

`-port` 和 `-listen` 互斥。文件不存在时，生成的配置使用指定监听地址，默认对外 URL 也随端口生成；已有文件时，启动参数覆盖本次运行的对应配置，不会自动改写已有文件。`-public-base-url` 可单独覆盖对外 URL。通过 Web UI 保存的普通设置在重启后生效，115 TOKEN 修改立即生效；如果重启时仍传入启动参数，参数优先。

Web UI 中带星号的字段为必填，包含数据库文件路径、STRM 输出目录、115 工作目录 ID、Refresh TOKEN 和缓存设置等。已保存的 TOKEN 可保留，无需重复输入；Auth TOKEN 为可选项。文件解析缓存时间、TOKEN 提前刷新时间、离线任务轮询间隔、最短及最长轮询间隔、缓存清理间隔、单个任务超时时间和日志级别可留空，保存时使用默认值；表单会标明各项默认值。未完成配置时仍可启动服务进入初始配置页。

数据库文件路径和 STRM 输出目录通过“选择”按钮打开服务端 Tree 目录树。可浏览和选择的范围为 `/data`、`/cache`、运行服务的当前用户主目录、程序工作目录（启动时的当前目录）。目录按展开动作加载；不存在的根目录以及隐藏文件、隐藏目录不会显示。路径输入框也支持直接输入。数据库支持选择 `.db`、`.sqlite`、`.sqlite3` 文件，选择目录时使用该目录下的 `magnet.db`；STRM 只能选择目录。保存时再次检查范围并记录绝对路径，拒绝路径遍历和指向允许范围外的符号链接。

## 日志

- `logging.level` 可设为 `debug`、`info`、`warn` 或 `error`，默认是 `info`。
- 115 API 请求和客户端取消等待属于 `debug`；正常任务生命周期属于 `info`；警告和可恢复失败属于 `warn`。
