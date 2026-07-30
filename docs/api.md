# HTTP 与 API

服务默认监听 `:8080`。

## WebUI 与任务 API

- `/`：WebUI，提供任务记录、进度、状态筛选、取消、删除已取消记录、重建 STRM 和解析结果详情。
- `/api/v1/status`：返回完整模式或本地数据库模式。
- `GET /api/v1/jobs`、`GET /api/v1/jobs/<gid>`：读取本地任务列表、115 下载进度和详情。
- `POST /api/v1/jobs/<gid>/cancel`：取消排队或运行中的任务，并尽力删除对应的 115 离线任务。
- `DELETE /api/v1/jobs/<gid>`：删除失败或已取消的任务记录，已完成任务不能删除。
- `POST /api/v1/jobs/<gid>/rebuild-strm`：只使用本地文件映射重建已完成任务的全部视频 STRM，不访问 115。

## 播放与检查

- `/redirect/<sha1>`：确认或物化 115 文件后返回 302。
- `/redirect/<sha1>?info_hash=<info_hash>`：在恢复文件时优先使用指定来源磁链。
- `/dav/objects/<sha1前2位>/<sha1第3-4位>/<sha1>.<ext>`：稳定的只读 WebDAV 对象地址。
- `/healthz`：进程存活检查。
- `/readyz`：SQLite 就绪检查。

`direct` 模式将 `/redirect` 指向与请求 User-Agent 匹配的 115 临时下载链接；`proxy` 模式将其指向稳定 SHA1 对象路径，目标可以是本服务的 `/dav`，也可以是 rclone。旧名称 `stable_dav` 仍兼容。`/redirect` 与 `/dav` 共享物化缓存。

WebDAV 只读支持：

- `PROPFIND` 支持 `Depth: 0` 和 `Depth: 1`，从 SQLite 返回目录和对象属性。
- `GET`、`HEAD` 通过 SHA1 物化器取得 `pick_code`，获取 115 临时下载地址并流式转发；支持 Range 请求。
- `PUT`、`DELETE`、`MOVE`、`COPY` 等写操作返回 405。

## qBittorrent Web API

服务在 `/api/v2/` 提供 qBittorrent Web API 兼容子集，可作为 Radarr、Sonarr 等应用的
qBittorrent 下载客户端。它支持登录、分类，以及添加、查询、删除任务和读取文件信息；
任务实际由 115 离线下载处理，完成后 `content_path` 指向生成的 STRM 目录。

在 Radarr/Sonarr 中新增 qBittorrent 下载客户端时，填写本服务的主机和端口，用户名及
密码使用 `MTS_QBITTORRENT_USERNAME` 和 `MTS_QBITTORRENT_PASSWORD`，URL Base
保持为空。分类可按应用分别设置为 `radarr`、`sonarr`。如果应用与本服务看到的
`library.strm_dir` 路径不同，还需要配置 Remote Path Mapping。

这是面向自动化媒体应用常用流程的兼容接口，并非完整的 qBittorrent 实现。

## aria2 JSON-RPC

地址：

```text
http://127.0.0.1:8080/jsonrpc
```

支持 `aria2.addUri`、任务查询、全局状态、批量请求和 `system.multicall`。同一批请求中的多个 `aria2.addUri` 会先统一查重，再通过一次 115 批量添加请求提交尚不存在的离线任务。

设置 `MTS_ARIA2_RPC_SECRET` 后，客户端必须按 aria2 约定传入 `token:<secret>`。

浏览器下载插件、手机 App 或其他 aria2 客户端可将 RPC 地址设为上述 `/jsonrpc`，
RPC 协议选择 HTTP/JSON-RPC，密钥填写 `MTS_ARIA2_RPC_SECRET` 的值。通过此接口添加
磁力链接后，可以继续在客户端查询进度或取消任务。
