# 架构与限制

## 代码结构

```text
cmd/magnet-to-strm        命令行入口
internal/config           强类型配置
internal/ingest           磁链、离线任务、扫描和 STRM 发布用例
internal/strm             真实 STRM 写入与手动前缀替换
internal/materialize      远端确认、离线任务恢复、访问和清理
internal/provider/p115    115 SDK、TOKEN 与全局 API 限流
internal/storage/sqlite   业务仓储
internal/transport        HTTP、只读 WebDAV 与 aria2 适配器
internal/bootstrap        依赖组装与应用生命周期
```

## 运行模式

首次初始化未完成时，`serve` 仅提供配置页面、配置接口、服务端目录浏览和健康检查，不启动业务数据库、115、任务接口或 rclone。保存初始配置后自动重新加载并启动完整服务。115 TOKEN 从 SQLite 读取，不从环境变量读取；后续通过 Web UI 修改时在运行中验证或刷新。

已完成初始化但 115 凭证不可用时，服务仍提供本地任务查询；115 离线下载、文件物化和文件流等操作不可用。更新有效 TOKEN 后可恢复相关操作，无需重启。

CLI 和 aria2 接口共用 SQLite 中的持久化任务状态。服务重启时，未完成的运行中任务会恢复到等待队列；任务只有在 SQLite 保存和真实 STRM 写入都成功后才会标记成功。115 不可用时，排队任务等待凭证恢复后再执行。

## 当前限制

- 项目依赖 115 接口和下载地址的可用性，上游行为变化可能导致功能失效。
- 单个 SQLite 数据库适合个人或小规模部署；高并发和多节点部署尚未作为目标验证。
- `/healthz` 只表示进程存活，`/readyz` 只检查 SQLite 就绪，不代表 115 或下游服务可用。
