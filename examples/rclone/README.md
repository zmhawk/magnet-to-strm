# 外置 rclone 示例

默认镜像已内置 rclone，此示例用于需要单独运行 rclone 容器的部署。示例启用 VFS 磁盘缓存和内存缓冲，可在 Compose 中调整容量。

## 调用链

```text
播放器 → magnet-to-strm /redirect → 外置 rclone /objects → magnet-to-strm /dav → 115
```

## 使用

先将 `config.toml` 和 `rclone.conf` 中的 `nas.local` 替换为可访问的服务器地址，然后启动：

```bash
cd examples/rclone
mkdir -p config
cp config.toml config/config.toml
docker compose up -d
```

打开 `http://<服务器地址>:7000`，通过 Web UI 完成初始化，填写真实的 115 工作目录 ID 和 Refresh TOKEN。首次表单中的两个地址会使用当前页面 origin，请将代理目标地址改为 `http://<服务器地址>:7001`，并关闭内置 rclone，然后保存。Auth TOKEN 可选，两个 TOKEN 仅保存在数据库中。

应用数据保存在 `./data`，配置保存在 `./config/config.toml`，外置 rclone 缓存保存在 `./.rclone-cache`。此示例未配置认证，请勿直接暴露到公网。
