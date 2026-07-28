# 使用与恢复

## CLI

```bash
./magnet-to-strm add 'magnet:?xt=urn:btih:...'
./magnet-to-strm add -config config.toml -json 'magnet:?xt=urn:btih:...'
./magnet-to-strm serve
./magnet-to-strm serve -config config.toml
./magnet-to-strm rewrite-strm -config config.toml '<旧 URL 前缀>' '<新 URL 前缀>'
```

`add` 和 `serve` 启动时不会批量修改已有 STRM。需要更换旧链接前缀时，显式运行 `rewrite-strm`。该命令递归扫描 `library.strm_dir`，只修改普通 `.strm` 文件中以旧前缀加 `/` 开头的内容，并使用同目录临时文件原子替换。符号链接、其他 STRM 和普通文件不会被修改，也不会自动修改 `config.toml`。

## 任务恢复

当 115 任务显示完成但结果目录不存在（错误码 `430004`），或结果中缺少请求的 SHA1 时，程序会删除旧任务、重新提交磁链并继续轮询。恢复时优先使用 STRM 中标注的来源磁链，失败后按最近成功顺序尝试其他关联磁链。

每次任务执行最多自动重建一次。只有任务历史中的 `wp_path_id` 与 `p115.work_dir_id` 一致时，重建或删除任务才会同时删除云端源文件（夹），否则只删除任务记录。

同一个 SHA1 的并发 `/redirect` 和 `/dav` 请求共享一个后台物化任务。客户端提前断开时只停止当前请求的等待，不会取消 115 查询和恢复；后续请求会继续等待同一任务或命中缓存。

115 临时下载地址失效时，日志会记录 `pick_code`、HTTP 状态、`cached_at`、`age` 和 User-Agent，但不会记录实际下载 URL。可以按 `age` 统计实际观察到的地址有效期。

## 缓存清理

后台仅在整条磁链的所有有效内容都超过 `library.cache_retention` 后，清理位于 `p115.work_dir_id` 下且标记为托管缓存的文件。共享给仍活跃磁链的内容会保留。

当缓存超过 `library.cache_max_size` 时，即使尚未达到保留期，也会按 `last_accessed_at` 从旧到新清理。清理优先使用已记录的 `delete_file_id` 删除任务源文件（夹）；该字段为空时，才回退为查询历史任务或逐文件删除。
