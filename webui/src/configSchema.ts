export type ConfigGroup =
  | "storage"
  | "http"
  | "p115"
  | "other"
  | "auth"
  | "logging";
type SettingSection =
  | "database"
  | "rclone"
  | "http"
  | "p115"
  | "library"
  | "ingest"
  | "logging";
export type CredentialKey =
  | "p115_auth_token"
  | "p115_refresh_token"
  | "aria2_rpc_secret"
  | "qbittorrent_username"
  | "qbittorrent_password";

interface FieldBase {
  group: ConfigGroup;
  label: string;
  required?: boolean;
  // Consecutive advanced fields share a collapsed panel.
  advanced?: boolean;
  // Defaults to required; explicit false also permits a normal required label.
  bold?: boolean;
  help?: string;
}

export type SettingField = FieldBase & {
  source: "settings";
  path: [SettingSection, string];
  // Used as the initial value during setup, or when a saved field is absent.
  defaultValue?: string | number | boolean;
  placeholder?: string;
} & (
    | { input: "text" }
    | { input: "switch" }
    | { input: "rcloneOptions" }
    | { input: "serverPath"; selection: "database" | "directory" }
    | { input: "number"; precision?: number }
    | { input: "select"; options: { value: string; label: string }[] }
  );

export interface CredentialField extends FieldBase {
  source: "credentials";
  key: CredentialKey;
  input: "password";
}

export type ConfigField = SettingField | CredentialField;

export const configGroups: { id: ConfigGroup; title: string }[] = [
  { id: "storage", title: "存储" },
  { id: "http", title: "服务" },
  { id: "p115", title: "115 网盘" },
  { id: "other", title: "其他" },
];

// Display groups are independent of the paths persisted in the configuration file.
// Required status, ordering, labels, controls and default hints all come from here.
export const configFields: ConfigField[] = [
  {
    group: "storage",
    source: "settings",
    path: ["database", "path"],
    label: "数据库文件路径",
    required: true,
    input: "serverPath",
    selection: "database",
    help: "输入或选择服务端数据库文件路径；填写已有目录时使用其中的 magnet.db",
    defaultValue: "/data/db/magnet.db",
  },
  {
    group: "storage",
    source: "settings",
    path: ["library", "strm_dir"],
    label: "STRM 输出目录",
    required: true,
    input: "serverPath",
    selection: "directory",
    help: "输入或选择服务端 STRM 输出目录；相对路径基于程序工作目录",
    defaultValue: "/data/strms",
  },
  {
    group: "http",
    source: "settings",
    path: ["http", "listen_addr"],
    label: "监听地址",
    required: true,
    input: "text",
    help: "例如 :8081 或 127.0.0.1:8081",
  },
  {
    group: "http",
    source: "settings",
    path: ["http", "public_base_url"],
    label: "对外访问地址",
    required: true,
    input: "text",
    help: "例如 http://127.0.0.1:8081，用于生成 STRM 链接",
  },
  {
    group: "http",
    source: "settings",
    path: ["http", "redirect_type"],
    label: "播放重定向方式",
    required: true,
    input: "select",
    options: [
      { value: "direct", label: "直连" },
      { value: "proxy", label: "代理" },
    ],
    help: "直连使用 115 直链播放；代理通过稳定路径播放，通常搭配 rclone",
  },
  {
    group: "http",
    source: "settings",
    path: ["http", "redirect_base_url"],
    label: "代理目标地址",
    input: "text",
    help: "默认填入内置 rclone 的对外播放入口（与对外访问地址相同）；也可填写外部代理地址",
  },

  {
    group: "http",
    source: "settings",
    path: ["rclone", "enabled"],
    label: "启用内置 rclone",
    input: "switch",
    help: "使用 Virtual File System (VFS) 提供播放磁盘缓存。",
  },
  {
    group: "http",
    source: "settings",
    path: ["rclone", "cache_dir"],
    label: "rclone 缓存目录",
    input: "text",
    defaultValue: "/cache",
    help: "VFS 缓存目录",
  },
  {
    group: "http",
    source: "settings",
    path: ["rclone", "vfs_cache_max_size"],
    label: "VFS 缓存空间上限 (--vfs-cache-max-size)",
    input: "text",
    defaultValue: "512G",
    bold: true,
    help: "至少大于你所观看的最大的单个视频资源大小",
  },
  {
    group: "http",
    source: "settings",
    path: ["rclone", "vfs_read_ahead"],
    label: "VFS 预读大小 (--vfs-read-ahead)",
    input: "text",
    defaultValue: "4G",
    bold: true,
    help: "例：对于 100 Mbps 码率的资源，4G 大概可预读 5 分钟的视频内容",
  },
  {
    group: "http",
    source: "settings",
    advanced: true,
    path: ["rclone", "server_read_timeout"],
    label: "rclone 读取超时 (--server-read-timeout)",
    input: "text",
    defaultValue: "24h",
  },
  {
    group: "http",
    source: "settings",
    advanced: true,
    path: ["rclone", "server_write_timeout"],
    label: "rclone 写入超时 (--server-write-timeout)",
    input: "text",
    defaultValue: "24h",
  },

  {
    group: "http",
    source: "settings",
    advanced: true,
    path: ["rclone", "vfs_cache_mode"],
    label: "VFS 缓存模式 (--vfs-cache-mode)",
    input: "select",
    options: ["off", "minimal", "writes", "full"].map((value) => ({
      value,
      label: value,
    })),
    defaultValue: "full",
  },

  {
    group: "http",
    source: "settings",
    advanced: true,
    path: ["rclone", "vfs_cache_max_age"],
    label: "VFS 缓存保留时间 (--vfs-cache-max-age)",
    input: "text",
    defaultValue: "72h",
  },
  {
    group: "http",
    source: "settings",
    advanced: true,
    path: ["rclone", "vfs_cache_poll_interval"],
    label: "VFS 缓存清理间隔 (--vfs-cache-poll-interval)",
    input: "text",
    defaultValue: "5m",
  },

  {
    group: "http",
    source: "settings",
    advanced: true,
    path: ["rclone", "custom"],
    label: "自定义 rclone 参数",
    input: "rcloneOptions",
    help: "每行一个参数，例如 --vfs-read-chunk-size=128M。保存后重启生效。",
  },

  {
    group: "p115",
    source: "credentials",
    key: "p115_refresh_token",
    label: "115 Refresh TOKEN",
    required: true,
    input: "password",
    help: "115 Refresh TOKEN（必填）；用于获取新的 Access TOKEN。",
  },
  {
    group: "p115",
    source: "credentials",
    key: "p115_auth_token",
    label: "115 Auth TOKEN",
    input: "password",
    help: "115 Access TOKEN（可选）；与 Refresh TOKEN 一起保存，凭据修改后立即刷新。",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "work_dir_id"],
    label: "115 工作目录 ID",
    required: true,
    input: "text",
    help: "用于离线下载和缓存的 115 目录 ID，浏览器打开 115 目标目录后，地址栏中的 cid 参数",
  },
  {
    group: "p115",
    source: "settings",
    path: ["library", "cache_retention"],
    label: "缓存保留时间",
    required: true,
    input: "text",
    help: "已离线到 115 的资源，多久后自动清理。例如 168h",
  },
  {
    group: "p115",
    source: "settings",
    path: ["library", "cache_max_size"],
    label: "缓存空间上限",
    required: true,
    input: "text",
    help: "115 缓存目录的最大占用空间，超出后会按最久未访问的文件清理。例如 4TB；0 表示不限制",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "offline_quota_min_remaining"],
    label: "离线额度保护阈值",
    input: "number",
    precision: 0,
    help: "115 离线额度剩余多少时，本服务不再进行离线下载；0 表示关闭保护",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "token_refresh_ahead"],
    advanced: true,
    label: "TOKEN 提前刷新时间",
    input: "text",
    defaultValue: "10m",
    help: "例如 10m",
  },
  {
    group: "p115",
    source: "settings",
    path: ["http", "materialize_cache_ttl"],
    advanced: true,
    label: "文件解析缓存时间",
    input: "text",
    defaultValue: "10m",
    help: "例如 30m",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "request_rate"],
    advanced: true,
    label: "每秒请求数",
    input: "number",
    help: "0 表示不限制",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "request_burst"],
    advanced: true,
    label: "允许的突发请求数",
    input: "number",
    precision: 0,
    help: "非负整数",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "request_concurrency"],
    advanced: true,
    label: "请求并发数",
    input: "number",
    precision: 0,
    help: "非负整数",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "offline_poll_interval"],
    advanced: true,
    label: "离线任务轮询间隔",
    input: "text",
    defaultValue: "5s",
    help: "例如 5m",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "offline_poll_min_interval"],
    advanced: true,
    label: "最短轮询间隔",
    input: "text",
    defaultValue: "2s",
    help: "例如 2s",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "offline_poll_max_interval"],
    advanced: true,
    label: "最长轮询间隔",
    input: "text",
    defaultValue: "5m",
    help: "例如 5m",
  },

  {
    group: "p115",
    source: "settings",
    path: ["library", "sweep_interval"],
    advanced: true,
    label: "缓存清理间隔",
    input: "text",
    defaultValue: "1h",
    help: "例如 1h",
  },
  {
    group: "other",
    source: "settings",
    path: ["ingest", "job_timeout"],
    label: "单个任务超时时间",
    input: "text",
    defaultValue: "6h",
    help: "例如 6h",
  },
  {
    group: "other",
    source: "credentials",
    key: "aria2_rpc_secret",
    label: "aria2 RPC 密钥",
    input: "password",
  },
  {
    group: "other",
    source: "credentials",
    key: "qbittorrent_username",
    label: "qBittorrent 用户名",
    input: "password",
  },
  {
    group: "other",
    source: "credentials",
    key: "qbittorrent_password",
    label: "qBittorrent 密码",
    input: "password",
  },
  {
    group: "other",
    source: "settings",
    path: ["logging", "level"],
    label: "日志级别",
    input: "select",
    defaultValue: "info",
    options: ["debug", "info", "warn", "error"].map((value) => ({
      value,
      label: value,
    })),
    help: "日常使用建议选择 info",
  },
];

export interface ConfigBlock {
  advanced: boolean;
  fields: ConfigField[];
}

// Only adjacent advanced fields share a panel, within the final display order.
export function buildFieldBlocks(fields: ConfigField[]): ConfigBlock[] {
  const blocks: ConfigBlock[] = [];
  for (const field of fields) {
    const advanced = Boolean(field.advanced);
    const previous = blocks[blocks.length - 1];
    if (advanced && previous?.advanced) previous.fields.push(field);
    else blocks.push({ advanced, fields: [field] });
  }
  return blocks;
}

export const groupedFields = configGroups.map((group) => {
  const fields = configFields.filter((field) => field.group === group.id);
  return { ...group, fields, blocks: buildFieldBlocks(fields) };
});

export const credentialFields = configFields.filter(
  (field): field is CredentialField => field.source === "credentials",
);

export function fieldID(field: ConfigField) {
  return field.source === "settings"
    ? field.path.join(".")
    : `credentials.${field.key}`;
}

export function initialSettings(
  settings: import("./api").Settings,
  setupRequired: boolean,
) {
  const values = Object.fromEntries(
    Object.entries(settings).map(([section, fields]) => [
      section,
      { ...fields },
    ]),
  );
  for (const field of configFields) {
    if (field.source !== "settings" || field.defaultValue === undefined)
      continue;
    const [section, key] = field.path;
    values[section] ??= {};
    if (
      setupRequired ||
      values[section][key] === undefined ||
      values[section][key] === null
    ) {
      values[section][key] = field.defaultValue;
    }
  }
  return values;
}
