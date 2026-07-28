# 搭配 rclone VFS 缓存示例

rclone 的缓存键依赖文件路径，而 115 文件路径可能变化。本示例让 rclone 从 magnet-to-strm 的 SHA1 稳定 WebDAV 路径读取文件，并使用 VFS `full` 模式缓存。

## 调用链

```text
播放器 → magnet-to-strm /redirect
       → rclone /objects/<sha1 路径>
       → magnet-to-strm /dav
       → 115
```

## 启动

1. 复制环境变量示例并填写 Refresh Token：

   ```bash
   cp examples/rclone/.env.example examples/rclone/.env
   ```

2. 按部署环境编辑三个文件：

   - `config.toml`：设置 115 工作目录和播放器可访问的公开 URL。
   - `rclone.conf`：设置 magnet-to-strm 的 `/dav` 地址。
   - `compose.yaml`：设置镜像、宿主机端口、数据目录和缓存目录。

3. 从仓库根目录启动：

   ```bash
   docker compose \
     --env-file examples/rclone/.env \
     -f examples/rclone/compose.yaml \
     up -d
   ```

4. 检查服务：

   ```bash
   curl http://127.0.0.1:7000/healthz
   docker compose \
     --env-file examples/rclone/.env \
     -f examples/rclone/compose.yaml \
     logs -f magnet-to-strm rclone
   ```

## 缓存与停止

- `--vfs-cache-max-size` 和 `--vfs-cache-max-age` 控制 rclone 本地缓存。
- 应用数据与缓存使用宿主机目录，`docker compose down` 不会删除。

示例未给 WebUI、`/redirect` 和 rclone WebDAV 增加认证。不要直接暴露到公网；远程访问时应增加 HTTPS、认证和访问控制。
