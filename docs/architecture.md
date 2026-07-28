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

`serve` 在数据库已有凭证或环境变量配置 Refresh Token 时运行完整模式；没有凭证时仍可启动本地数据库模式，WebUI、健康检查和本地任务查询可用，但 115 离线下载、文件物化、WebDAV 文件流和 aria2 添加任务会停用。

CLI 和 aria2 接口共用 SQLite 中的持久化任务状态。服务重启时，未完成的运行中任务会恢复到等待队列；任务只有在 SQLite 保存和真实 STRM 写入都成功后才会标记成功。本地数据库模式不会恢复或执行排队任务。

## 当前限制

- 项目依赖 115 接口和下载地址的可用性，上游行为变化可能导致功能失效。
- 单个 SQLite 数据库适合个人或小规模部署；高并发和多节点部署尚未作为目标验证。
- `/healthz` 只表示进程存活，`/readyz` 只检查 SQLite 就绪，不代表 115 或下游服务可用。
