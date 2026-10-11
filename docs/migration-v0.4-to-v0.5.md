# v0.4 升级到 v0.5

保留原数据库、STRM 和配置文件即可升级，不需要重新解析磁链。当前两个版本的 SQLite schema 都是 11，无需手工迁移数据库结构。

## 必要修改

1. 停止旧容器，备份数据库、STRM、配置和 `.env`。选择需要升级的镜像版本标签。
2. 删除 `MTS_P115_REFRESH_TOKEN` 和 `MTS_P115_AUTH_TOKEN` 环境变量。原数据库中的 TOKEN 会继续使用；缺失或失效时，在 Web UI 配置。原 aria2 密钥和 qBittorrent 凭据可移到 Web UI；默认 Compose 不再注入 aria2 密钥，切换时保持客户端和服务端的凭据一致。
3. 使用内置 rclone 时，将原外置 rclone 的宿主机缓存目录以可写方式挂载到 `/cache`。内置 rclone 默认启用；继续使用外置 rclone 时，在 Web UI 关闭内置 rclone，并保留原代理目标地址和缓存挂载。

挂载示例（宿主机目录按实际情况替换）：

```yaml
environment:
  MTS_HTTP_LISTEN_ADDR: ":8080"
ports:
  - "7000:8080"
volumes:
  - ./data:/data
  - ./db:/db
  - /原rclone缓存目录:/cache
  - ./config:/etc/magnet-to-strm
```

`/db` 仅用于原数据库就在该路径的部署，默认 Compose 使用 `/data` 存储数据库。使用 `PUID` / `PGID` 时，确保指定用户可以读写挂载目录。监听地址不再通过 Web UI 修改；只改宿主机访问端口时，修改端口映射左侧即可。

## 启动检查

```bash
docker compose pull
docker compose up -d
docker compose logs --tail 100 magnet-to-strm
```

启动正常后，打开 Web UI 的配置页面，确认数据库仍指向原文件，将 STRM 输出目录填写为原目录的容器内绝对路径（例如 `/data/downloads`），使用内置 rclone 时，将 `public_base_url`（对外访问地址）和 `redirect_base_url`（代理目标地址）都设为播放器可访问的 magnet-to-strm 地址和宿主机端口，例如 `http://10.10.0.12:7000`；不要保留原外置 rclone 的端口。继续使用外置 rclone 时，代理目标地址仍指向外置 rclone。检查新增配置的默认值，然后点击一次“保存”。相对路径也会在保存时自动转换为绝对路径；保存会将新增配置的默认值写入配置文件，已有配置值保留，115 TOKEN 仍只保存在数据库中。

保存后重启容器，确认原任务记录仍在，并测试已有 STRM 播放。若 `public_base_url` 与旧 STRM 中的地址不同，还需使用 [`rewrite-strm`](operations.md) 更新旧链接；修改配置不会自动改写已有 STRM。已有完整配置会直接进入任务管理；若意外进入初始化页面，先检查配置挂载和数据库路径。后续仅修改 115 TOKEN 无需重启。

升级不正常时，停止新容器，恢复备份的旧配置、镜像和数据后再启动。不要让两个版本同时写同一个数据库。
