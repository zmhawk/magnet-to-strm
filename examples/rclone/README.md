# 搭配 rclone 内存缓冲示例

rclone 从稳定的 SHA1 WebDAV 路径读取文件，并为每个打开的文件提供
1 GiB 内存缓冲。

## 调用链

```text
播放器 → magnet-to-strm /redirect
       → rclone /objects/<sha1 路径>
       → magnet-to-strm /dav
       → 115
```

## 使用

```bash
cd examples/rclone
cp .env.example .env
docker compose up -d
```

启动前只需：

- 在 `.env` 中填写 115 Refresh Token；
- 在 `config.toml` 中填写 115 工作目录 ID；
- 将两个 `nas.local` 地址改成播放器能够访问的宿主机地址。

数据保存在 `examples/rclone/data`。此示例未配置认证，请勿直接暴露到公网。
