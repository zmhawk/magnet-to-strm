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

`direct` 模式将 `/redirect` 指向当前真实 115 路径；`stable_dav` 模式将其指向 rclone 上的稳定 SHA1 对象路径。`/redirect` 与 `/dav` 共享物化缓存。

WebDAV 只读支持：

- `PROPFIND` 支持 `Depth: 0` 和 `Depth: 1`，从 SQLite 返回目录和对象属性。
- `GET`、`HEAD` 通过 SHA1 物化器取得 `pick_code`，获取 115 临时下载地址并流式转发；支持 Range 请求。
- `PUT`、`DELETE`、`MOVE`、`COPY` 等写操作返回 405。

## aria2 JSON-RPC

地址：

```text
http://127.0.0.1:8080/jsonrpc
```

支持 `aria2.addUri`、任务查询、全局状态、批量请求和 `system.multicall`。同一批请求中的多个 `aria2.addUri` 会先统一查重，再通过一次 115 批量添加请求提交尚不存在的离线任务。

设置 `MTS_ARIA2_RPC_SECRET` 后，客户端必须按 aria2 约定传入 `token:<secret>`。
